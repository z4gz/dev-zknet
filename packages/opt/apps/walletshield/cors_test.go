// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
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

const rpcReply = `{"jsonrpc":"2.0","result":"0x1","id":1}`

// testServer returns the CORS-wrapped handler and a counter of the requests it
// sent over the (fake) mixnet.
func testServer(t *testing.T) (http.HandlerFunc, *int) {
	t.Helper()
	sent := 0
	s := &Server{
		log: log.New(io.Discard),
		send: func(b []byte) ([]byte, error) {
			sent++
			payload, err := common.CompressData([]byte(rpcReply))
			if err != nil {
				return nil, err
			}
			return cbor.Marshal(&http_proxy.Response{Payload: payload})
		},
	}
	return withCORS(s.Handler), &sent
}

func preflight(origin string) *http.Request {
	r := httptest.NewRequest(http.MethodOptions, "/ethereum", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "content-type")
	return r
}

func post(origin string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/ethereum", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func serve(h http.HandlerFunc, r *http.Request) *http.Response {
	w := httptest.NewRecorder()
	h(w, r)
	return w.Result()
}

func TestPreflightIsAnsweredLocallyForAnyOrigin(t *testing.T) {
	for _, origin := range []string{"null", "http://localhost:5173", "https://example.org"} {
		h, sent := testServer(t)
		resp := serve(h, preflight(origin))
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("[%q] status = %d, want 204", origin, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("[%q] Access-Control-Allow-Origin = %q, want *", origin, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
			t.Errorf("[%q] Access-Control-Allow-Methods = %q, want POST", origin, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "content-type" {
			t.Errorf("[%q] Access-Control-Allow-Headers = %q, want content-type", origin, got)
		}
		if *sent != 0 {
			t.Errorf("[%q] preflight was forwarded over the mixnet %d time(s)", origin, *sent)
		}
	}
}

func TestPostIsProxiedAndReadable(t *testing.T) {
	for _, origin := range []string{"", "null", "https://example.org"} {
		h, sent := testServer(t)
		resp := serve(h, post(origin))
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != rpcReply {
			t.Errorf("[%q] got %d %q", origin, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("[%q] Content-Type = %q", origin, ct)
		}
		if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "*" {
			t.Errorf("[%q] Access-Control-Allow-Origin = %q, want *", origin, v)
		}
		if *sent != 1 {
			t.Errorf("[%q] sent %d requests, want 1", origin, *sent)
		}
	}
}

// OPTIONS that is not a CORS preflight is an ordinary request and is proxied.
func TestPlainOptionsIsStillForwarded(t *testing.T) {
	h, sent := testServer(t)
	serve(h, httptest.NewRequest(http.MethodOptions, "/ethereum", nil))
	if *sent != 1 {
		t.Errorf("sent %d requests, want 1", *sent)
	}
}
