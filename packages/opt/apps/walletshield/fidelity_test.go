// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/fxamacker/cbor/v2"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/http_proxy"
)

// replyServer returns the CORS-wrapped handler whose (fake) mixnet answers
// every request with send's result, and a counter of the requests sent.
func replyServer(send func() ([]byte, error)) (http.HandlerFunc, *int) {
	sent := 0
	s := &Server{
		log: log.New(io.Discard),
		send: func([]byte) ([]byte, error) {
			sent++
			return send()
		},
	}
	return withCORS(s.Handler), &sent
}

// exitReply is the CBOR reply of an exit that relayed body with status and
// header.
func exitReply(t *testing.T, status int, header http.Header, body string) func() ([]byte, error) {
	t.Helper()
	payload, err := common.CompressData([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return func() ([]byte, error) {
		return cbor.Marshal(&http_proxy.Response{Payload: payload, Status: status, Header: header})
	}
}

func exitError(reason, msg string) func() ([]byte, error) {
	return func() ([]byte, error) {
		return cbor.Marshal(&http_proxy.Response{Error: msg, Reason: reason})
	}
}

type failureBody struct {
	Error struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"error"`
}

// checkFailure asserts resp is an originated failure with reason and status.
func checkFailure(t *testing.T, resp *http.Response, reason string, status int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Errorf("status = %d, want %d", resp.StatusCode, status)
	}
	if got := resp.Header.Get(http_proxy.ErrorHeader); got != reason {
		t.Errorf("%s = %q, want %q", http_proxy.ErrorHeader, got, reason)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	var b failureBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatalf("failure body is not JSON: %s", err)
	}
	if b.Error.Reason != reason || b.Error.Message == "" {
		t.Errorf("failure body = %+v, want reason %q and a message", b, reason)
	}
}

func TestUpstreamStatusHeadersAndBodyAreRelayed(t *testing.T) {
	// The body carries format verbs: it must reach the client byte-exact.
	const body = `{"error":"rate limited at 100%s %d %%"}`
	upstream := http.Header{
		"Content-Type":         {"text/plain; charset=utf-8"},
		"Retry-After":          {"7"},
		"Set-Cookie":           {"tracker=1"},
		http_proxy.ErrorHeader: {"mix-timeout"},
	}
	h, _ := replyServer(exitReply(t, http.StatusTooManyRequests, upstream, body))
	resp := serve(h, post("https://example.org"))
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "7" {
		t.Errorf("Retry-After = %q, want 7", ra)
	}
	if v := resp.Header.Values("Set-Cookie"); len(v) != 0 {
		t.Errorf("unlisted header relayed: Set-Cookie = %q", v)
	}
	if v, ok := resp.Header[http_proxy.ErrorHeader]; ok {
		t.Errorf("upstream reply carries %s = %q", http_proxy.ErrorHeader, v)
	}
}

// Checked over a real connection: ResponseRecorder keeps the nil entry that
// tells net/http not to sniff, which a real server never puts on the wire.
func TestUpstreamWithoutContentTypeGetsNone(t *testing.T) {
	h, _ := replyServer(exitReply(t, http.StatusOK, http.Header{}, `<html>not sniffed</html>`))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/ethereum", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v, ok := resp.Header["Content-Type"]; ok {
		t.Errorf("Content-Type = %q, want absent", v)
	}
}

// An exit that predates Response.Status relays only the body.
func TestLegacyExitReplyIsOKJSON(t *testing.T) {
	h, _ := replyServer(exitReply(t, 0, nil, rpcReply))
	resp := serve(h, post(""))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != rpcReply {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if _, ok := resp.Header[http_proxy.ErrorHeader]; ok {
		t.Errorf("relayed reply carries %s", http_proxy.ErrorHeader)
	}
}

func TestFailuresAreMarked(t *testing.T) {
	cases := []struct {
		name   string
		send   func() ([]byte, error)
		reason string
		status int
	}{
		{"send fails", func() ([]byte, error) { return nil, errors.New("context deadline exceeded") },
			http_proxy.ReasonMixTimeout, http.StatusGatewayTimeout},
		{"request too large", func() ([]byte, error) { return nil, errTooLarge },
			http_proxy.ReasonTooLarge, http.StatusRequestEntityTooLarge},
		{"exit refused", exitError(http_proxy.ReasonExitRefused, "URI not configured in networks"),
			http_proxy.ReasonExitRefused, http.StatusBadGateway},
		{"upstream unreachable", exitError(http_proxy.ReasonUpstreamUnreachable, "HTTP request failed"),
			http_proxy.ReasonUpstreamUnreachable, http.StatusBadGateway},
		{"reply too large", exitError(http_proxy.ReasonTooLarge, "HTTP response is too big"),
			http_proxy.ReasonTooLarge, http.StatusRequestEntityTooLarge},
		{"exit busy", exitError(http_proxy.ReasonExitBusy, "the exit is serving its maximum number of requests"),
			http_proxy.ReasonExitBusy, http.StatusServiceUnavailable},
		{"undecodable reply", func() ([]byte, error) { return []byte{0xff, 0x00}, nil },
			http_proxy.ReasonBadReply, http.StatusBadGateway},
		{"empty reply", func() ([]byte, error) { return nil, nil },
			http_proxy.ReasonBadReply, http.StatusBadGateway},
		{"undecompressable reply", func() ([]byte, error) {
			return cbor.Marshal(&http_proxy.Response{Payload: []byte("not compressed"), Status: 200})
		}, http_proxy.ReasonBadReply, http.StatusBadGateway},
		{"invalid upstream status", exitReply(t, 42, nil, rpcReply),
			http_proxy.ReasonBadReply, http.StatusBadGateway},
		// An exit that predates Response.Reason sends only Error.
		{"legacy refusal", exitError("", "URI not configured in networks"),
			http_proxy.ReasonExitRefused, http.StatusBadGateway},
		{"legacy upstream failure", exitError("", "HTTP request failed"),
			http_proxy.ReasonUpstreamUnreachable, http.StatusBadGateway},
		{"legacy read failure", exitError("", "io.ReadAll(resp.Body) failed: unexpected EOF"),
			http_proxy.ReasonUpstreamUnreachable, http.StatusBadGateway},
		{"legacy too large", exitError("", "HTTP response is too big"),
			http_proxy.ReasonTooLarge, http.StatusRequestEntityTooLarge},
		{"unknown reason", exitError("from-the-future", "?"),
			http_proxy.ReasonExitRefused, http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := replyServer(c.send)
			resp := serve(h, post("https://example.org"))
			checkFailure(t, resp, c.reason, c.status)
			if got := resp.Header.Get("Access-Control-Expose-Headers"); got != exposedHeaders {
				t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, exposedHeaders)
			}
		})
	}
}

// sendRequest refuses an oversized request before it touches the mixnet.
func TestSendRequestRefusesOversizedRequest(t *testing.T) {
	big := make([]byte, 2*UserForwardPayloadLength)
	rand.Read(big) // incompressible
	_, err := sendRequest(nil, big)
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want errTooLarge", err)
	}
}

func TestHealthIsAnsweredLocally(t *testing.T) {
	h, sent := replyServer(func() ([]byte, error) { return nil, errors.New("mixnet down") })
	r := httptest.NewRequest(http.MethodGet, healthPath, nil)
	r.Header.Set("Origin", "null")
	resp := serve(h, r)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	var b map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil || b["version"] == "" {
		t.Errorf("body = %v (%v), want a version", b, err)
	}
	if *sent != 0 {
		t.Errorf("health check sent %d mixnet requests, want 0", *sent)
	}
}

func TestExposeHeadersIncludesErrorAndRelayedHeaders(t *testing.T) {
	h, _ := testServer(t)
	got := serve(h, post("https://example.org")).Header.Get("Access-Control-Expose-Headers")
	if got != "ZKNet-Error, Content-Type, Retry-After" {
		t.Errorf("Access-Control-Expose-Headers = %q", got)
	}
}
