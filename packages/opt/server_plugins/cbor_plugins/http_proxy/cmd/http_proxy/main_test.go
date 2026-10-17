package main

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"gopkg.in/op/go-logging.v1"

	"github.com/katzenpost/katzenpost/server/cborplugin"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/http_proxy"
)

// roundTrip sends one HTTP request to path through the exit handler and
// returns the raw reply payload it writes back.
func roundTrip(t *testing.T, networks map[string]string, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://nowhere"+path, strings.NewReader(`{"id":1}`))
	req.Header.Set("Content-Type", "application/json")
	buf := new(bytes.Buffer)
	req.Write(buf)
	compressed, err := common.CompressData(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := cbor.Marshal(&http_proxy.Request{Payload: compressed})

	h := New(&http_proxy.Config{Networks: networks}, logging.MustGetLogger("test"), DefaultUserForwardPayloadLength-payloadMargin)
	replies := make(chan []byte, 1)
	h.write = func(cmd cborplugin.Command) { replies <- cmd.(*cborplugin.Response).Payload }
	h.OnCommand(&cborplugin.Request{ID: 1, SURB: []byte("surb"), Payload: payload})

	select {
	case p := <-replies:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
		return nil
	}
}

func decode(t *testing.T, b []byte) *http_proxy.Response {
	t.Helper()
	r := new(http_proxy.Response)
	if err := cbor.Unmarshal(b, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUpstreamStatusAndAllowlistedHeadersAreRelayed(t *testing.T) {
	const body = `{"error":"slow down 100%s"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Set-Cookie", "tracker=1")
		w.Header().Set(http_proxy.ErrorHeader, "mix-timeout")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(body))
	}))
	defer upstream.Close()

	r := decode(t, roundTrip(t, map[string]string{"net": upstream.URL}, "/net"))
	if r.Error != "" || r.Reason != "" {
		t.Fatalf("unexpected error %q (%q)", r.Error, r.Reason)
	}
	if r.Status != http.StatusTooManyRequests {
		t.Errorf("Status = %d, want 429", r.Status)
	}
	want := http.Header{"Content-Type": {"application/json"}, "Retry-After": {"7"}}
	if !reflect.DeepEqual(r.Header, want) {
		t.Errorf("Header = %v, want %v", r.Header, want)
	}
	got, err := common.DecompressData(r.Payload)
	if err != nil || string(got) != body {
		t.Errorf("body = %q (%v), want %q", got, err, body)
	}
}

func TestExitFailuresCarryReasons(t *testing.T) {
	big := make([]byte, 2*DefaultUserForwardPayloadLength)
	rand.Read(big) // incompressible
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	defer huge.Close()
	beyondCap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, http_proxy.MaxResponseBodyBytes+1))
	}))
	defer beyondCap.Close()

	// Closed after the others start, so none of them can take its port.
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	cases := []struct {
		name, path, reason string
	}{
		{"unconfigured network", "/missing", http_proxy.ReasonExitRefused},
		{"unreachable upstream", "/closed", http_proxy.ReasonUpstreamUnreachable},
		{"oversized reply", "/huge", http_proxy.ReasonTooLarge},
		{"reply past the read cap", "/beyond", http_proxy.ReasonTooLarge},
	}
	networks := map[string]string{"closed": closed.URL, "huge": huge.URL, "beyond": beyondCap.URL}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := decode(t, roundTrip(t, networks, c.path))
			if r.Reason != c.reason || r.Error == "" {
				t.Errorf("reply = {Reason: %q, Error: %q}, want reason %q", r.Reason, r.Error, c.reason)
			}
		})
	}
}

// A probe reply stays an empty payload, as before.
func TestProbeReplyIsUnchanged(t *testing.T) {
	if p := roundTrip(t, nil, "/_/probe"); p != nil {
		t.Errorf("probe reply = %x, want nil", p)
	}
}

// Plugin protocol 2: a parameters request gets a parameters response, not an
// error reply for the missing SURB.
func TestParametersRequestIsAnswered(t *testing.T) {
	h := New(&http_proxy.Config{}, logging.MustGetLogger("test"), DefaultUserForwardPayloadLength-payloadMargin)
	replies := make(chan *cborplugin.Response, 1)
	h.write = func(cmd cborplugin.Command) { replies <- cmd.(*cborplugin.Response) }
	if err := h.OnCommand(cborplugin.NewParametersRequest()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-replies:
		if !r.IsParametersResponse || r.SURB != nil || r.Payload != nil {
			t.Errorf("reply = %+v, want an empty parameters response", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
}

// rawRoundTrip sends one serialized HTTP request through the exit handler.
func rawRoundTrip(t *testing.T, cfg *http_proxy.Config, raw string) *http_proxy.Response {
	t.Helper()
	compressed, err := common.CompressData([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := cbor.Marshal(&http_proxy.Request{Payload: compressed})
	h := New(cfg, logging.MustGetLogger("test"), DefaultUserForwardPayloadLength-payloadMargin)
	replies := make(chan []byte, 1)
	h.write = func(cmd cborplugin.Command) { replies <- cmd.(*cborplugin.Response).Payload }
	h.OnCommand(&cborplugin.Request{ID: 1, SURB: []byte("surb"), Payload: payload})
	select {
	case p := <-replies:
		return decode(t, p)
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
		return nil
	}
}

// An absolute-form request line with no path once crashed the plugin.
func TestEmptyPathIsRefusedNotAPanic(t *testing.T) {
	r := rawRoundTrip(t, &http_proxy.Config{}, "POST https://x HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n")
	if r.Reason != http_proxy.ReasonExitRefused {
		t.Errorf("reply = {Reason: %q, Error: %q}, want %q", r.Reason, r.Error, http_proxy.ReasonExitRefused)
	}
}

// Any panic while serving a request answers that request and leaves the plugin running.
func TestPanicAnswersExitRefused(t *testing.T) {
	r := rawRoundTrip(t, nil, "POST /net HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n") // nil config panics
	if r.Reason != http_proxy.ReasonExitRefused || r.Error != "internal error" {
		t.Errorf("reply = {Reason: %q, Error: %q}, want internal error", r.Reason, r.Error)
	}
}

// handler returns an exit handler whose replies arrive on the returned channel.
func handler(cfg *http_proxy.Config) (*proxyRequestHandler, chan *http_proxy.Response) {
	h := New(cfg, logging.MustGetLogger("test"), DefaultUserForwardPayloadLength-payloadMargin)
	replies := make(chan *http_proxy.Response, 2*http_proxy.ExitConcurrency)
	h.write = func(cmd cborplugin.Command) {
		r := new(http_proxy.Response)
		cbor.Unmarshal(cmd.(*cborplugin.Response).Payload, r)
		replies <- r
	}
	return h, replies
}

func send(t *testing.T, h *proxyRequestHandler, id uint64, path string, body []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://nowhere"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	buf := new(bytes.Buffer)
	req.Write(buf)
	compressed, _ := common.CompressData(buf.Bytes())
	payload, _ := cbor.Marshal(&http_proxy.Request{Payload: compressed})
	h.OnCommand(&cborplugin.Request{ID: id, SURB: []byte("surb"), Payload: payload})
}

// A slow upstream holds at most ExitConcurrency slots; the next request is
// answered at once, and the exit serves again once a slot frees.
func TestBusyExitAnswersAtOnce(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)

	h, replies := handler(&http_proxy.Config{Networks: map[string]string{"slow": slow.URL}})
	for i := range http_proxy.ExitConcurrency {
		send(t, h, uint64(i), "/slow", []byte(`{}`))
	}
	send(t, h, 99, "/slow", []byte(`{}`))
	select {
	case r := <-replies:
		if r.Reason != http_proxy.ReasonExitBusy {
			t.Fatalf("reply = {Reason: %q, Error: %q}, want %q", r.Reason, r.Error, http_proxy.ReasonExitBusy)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request past the pool waited instead of being answered")
	}
	release <- struct{}{}
	<-replies
	send(t, h, 100, "/_/probe", nil)
	select {
	case r := <-replies:
		if r.Reason != "" {
			t.Fatalf("after a slot freed: %q", r.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply after a slot freed")
	}
}

func TestRequestBodyCap(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	h, replies := handler(&http_proxy.Config{Networks: map[string]string{"net": up.URL}})
	send(t, h, 1, "/net", bytes.Repeat([]byte(" "), http_proxy.MaxRequestBodyBytes+1))
	if r := <-replies; r.Reason != http_proxy.ReasonTooLarge {
		t.Errorf("reply = {Reason: %q, Error: %q}, want %q", r.Reason, r.Error, http_proxy.ReasonTooLarge)
	}
}

func TestInflatedRequestCap(t *testing.T) {
	h, replies := handler(&http_proxy.Config{})
	send(t, h, 1, "/net", bytes.Repeat([]byte(" "), http_proxy.MaxDecompressedRequestBytes))
	if r := <-replies; r.Reason != http_proxy.ReasonTooLarge {
		t.Errorf("reply = {Reason: %q, Error: %q}, want %q", r.Reason, r.Error, http_proxy.ReasonTooLarge)
	}
}

// A hung upstream reports as the upstream's failure, from the exit, not as a
// mix timeout. The client's own timeout is shortened for the test.
func TestHungUpstreamIsUpstreamUnreachable(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	h, replies := handler(&http_proxy.Config{Networks: map[string]string{"hung": hung.URL}})
	h.networks.Timeout = 200 * time.Millisecond
	send(t, h, 1, "/hung", []byte(`{}`))
	if r := <-replies; r.Reason != http_proxy.ReasonUpstreamUnreachable {
		t.Errorf("reply = {Reason: %q, Error: %q}, want %q", r.Reason, r.Error, http_proxy.ReasonUpstreamUnreachable)
	}
}
