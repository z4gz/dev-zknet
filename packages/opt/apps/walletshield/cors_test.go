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

// testServer returns a handler under the given policy and a counter of the
// requests it sent over the (fake) mixnet.
func testServer(t *testing.T, origins string) (http.HandlerFunc, *int) {
	t.Helper()
	policy, err := parseCORSOrigins(origins)
	if err != nil {
		t.Fatal(err)
	}
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
	return policy.wrap(s.Handler), &sent
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

func TestPreflightAdmittedOriginIsAnsweredLocally(t *testing.T) {
	h, sent := testServer(t, "http://localhost:5173")
	resp := serve(h, preflight("http://localhost:5173"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q, want POST", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.EqualFold(got, "content-type") {
		t.Errorf("Access-Control-Allow-Headers = %q", got)
	}
	if *sent != 0 {
		t.Errorf("preflight was forwarded over the mixnet %d time(s)", *sent)
	}
}

func TestPreflightOtherOriginGetsNoGrantAndIsNotForwarded(t *testing.T) {
	for _, origins := range []string{"", "http://localhost:5173"} {
		h, sent := testServer(t, origins)
		resp := serve(h, preflight("https://evil.example"))
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("[%q] status = %d, want 204", origins, resp.StatusCode)
		}
		for _, k := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
			if v := resp.Header.Get(k); v != "" {
				t.Errorf("[%q] %s = %q, want none", origins, k, v)
			}
		}
		if *sent != 0 {
			t.Errorf("[%q] preflight was forwarded over the mixnet", origins)
		}
	}
}

// A sandboxed frame presents origin "null", and so can any page that makes
// one: it is never admitted as a side effect of admitting a real origin.
func TestNullOriginOnlyWhenListed(t *testing.T) {
	h, _ := testServer(t, "http://localhost:5173")
	if v := serve(h, preflight("null")).Header.Get("Access-Control-Allow-Origin"); v != "" {
		t.Errorf("unlisted null origin granted %q", v)
	}
	h, _ = testServer(t, "http://localhost:5173,null")
	if v := serve(h, preflight("null")).Header.Get("Access-Control-Allow-Origin"); v != "null" {
		t.Errorf("listed null origin: Access-Control-Allow-Origin = %q, want null", v)
	}
}

func TestPostAdmittedOriginIsReadable(t *testing.T) {
	h, sent := testServer(t, "null")
	resp := serve(h, post("null"))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != rpcReply {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "null" {
		t.Errorf("Access-Control-Allow-Origin = %q, want null", v)
	}
	if *sent != 1 {
		t.Errorf("sent %d requests, want 1", *sent)
	}
}

// Requests without an Origin (curl, wallets, the e2e tests) and requests from
// origins the policy does not admit keep the behaviour they had before.
func TestPostWithoutGrantIsUnchanged(t *testing.T) {
	for _, origin := range []string{"", "https://evil.example"} {
		h, sent := testServer(t, "http://localhost:5173")
		resp := serve(h, post(origin))
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != rpcReply {
			t.Errorf("[%q] got %d %q", origin, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("[%q] Content-Type = %q", origin, ct)
		}
		if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("[%q] Access-Control-Allow-Origin = %q, want none", origin, v)
		}
		if *sent != 1 {
			t.Errorf("[%q] sent %d requests, want 1", origin, *sent)
		}
	}
}

// OPTIONS that is not a CORS preflight is an ordinary request and is proxied.
func TestPlainOptionsIsStillForwarded(t *testing.T) {
	h, sent := testServer(t, "")
	serve(h, httptest.NewRequest(http.MethodOptions, "/ethereum", nil))
	if *sent != 1 {
		t.Errorf("sent %d requests, want 1", *sent)
	}
}

func TestParseCORSOrigins(t *testing.T) {
	for _, ok := range []string{"", "null", "http://localhost:5173", " http://127.0.0.1:5173 , null ", "https://example.org"} {
		if _, err := parseCORSOrigins(ok); err != nil {
			t.Errorf("parseCORSOrigins(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"*", "localhost:5173", "http://localhost:5173/", "http://x/path", "file://"} {
		if _, err := parseCORSOrigins(bad); err == nil {
			t.Errorf("parseCORSOrigins(%q) accepted", bad)
		}
	}
}
