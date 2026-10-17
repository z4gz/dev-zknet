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
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	big := make([]byte, 2*DefaultUserForwardPayloadLength)
	rand.Read(big) // incompressible
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	defer huge.Close()

	cases := []struct {
		name, path, reason string
	}{
		{"unconfigured network", "/missing", http_proxy.ReasonExitRefused},
		{"unreachable upstream", "/closed", http_proxy.ReasonUpstreamUnreachable},
		{"oversized reply", "/huge", http_proxy.ReasonTooLarge},
	}
	networks := map[string]string{"closed": closed.URL, "huge": huge.URL}
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
