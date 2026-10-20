// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/fxamacker/cbor/v2"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/rpc_proxy"
)

// replyServer returns the CORS-wrapped handler whose (fake) mixnet answers
// every request with send's result, and a counter of the requests sent.
func replyServer(send func() ([]byte, error)) (http.HandlerFunc, *int) {
	sent := 0
	s := &Server{
		log: log.New(io.Discard),
		send: func([]byte, string) ([]byte, error) {
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
		return cbor.Marshal(&rpc_proxy.Response{Payload: payload, Status: status, Header: header})
	}
}

func exitError(reason, msg string) func() ([]byte, error) {
	return func() ([]byte, error) {
		return cbor.Marshal(&rpc_proxy.Response{Error: msg, Reason: reason})
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
	if got := resp.Header.Get(rpc_proxy.ErrorHeader); got != reason {
		t.Errorf("%s = %q, want %q", rpc_proxy.ErrorHeader, got, reason)
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
		"Content-Type":        {"text/plain; charset=utf-8"},
		"Retry-After":         {"7"},
		"Set-Cookie":          {"tracker=1"},
		rpc_proxy.ErrorHeader: {"mix-timeout"},
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
	if v, ok := resp.Header[rpc_proxy.ErrorHeader]; ok {
		t.Errorf("upstream reply carries %s = %q", rpc_proxy.ErrorHeader, v)
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

func TestFailuresAreMarked(t *testing.T) {
	cases := []struct {
		name   string
		send   func() ([]byte, error)
		reason string
		status int
	}{
		{"send fails", func() ([]byte, error) { return nil, errors.New("context deadline exceeded") },
			rpc_proxy.ReasonMixTimeout, http.StatusGatewayTimeout},
		{"request too large", func() ([]byte, error) { return nil, errTooLarge },
			rpc_proxy.ReasonTooLarge, http.StatusRequestEntityTooLarge},
		{"exit refused", exitError(rpc_proxy.ReasonExitRefused, "URI not configured in networks"),
			rpc_proxy.ReasonExitRefused, http.StatusBadGateway},
		{"upstream unreachable", exitError(rpc_proxy.ReasonUpstreamUnreachable, "HTTP request failed"),
			rpc_proxy.ReasonUpstreamUnreachable, http.StatusBadGateway},
		{"reply too large", exitError(rpc_proxy.ReasonTooLarge, "HTTP response is too big"),
			rpc_proxy.ReasonTooLarge, http.StatusRequestEntityTooLarge},
		{"exit busy", exitError(rpc_proxy.ReasonExitBusy, "the exit is serving its maximum number of requests"),
			rpc_proxy.ReasonExitBusy, http.StatusServiceUnavailable},
		{"undecodable reply", func() ([]byte, error) { return []byte{0xff, 0x00}, nil },
			rpc_proxy.ReasonBadReply, http.StatusBadGateway},
		{"empty reply", func() ([]byte, error) { return nil, nil },
			rpc_proxy.ReasonBadReply, http.StatusBadGateway},
		{"undecompressable reply", func() ([]byte, error) {
			return cbor.Marshal(&rpc_proxy.Response{Payload: []byte("not compressed"), Status: 200})
		}, rpc_proxy.ReasonBadReply, http.StatusBadGateway},
		{"invalid upstream status", exitReply(t, 42, nil, rpcReply),
			rpc_proxy.ReasonBadReply, http.StatusBadGateway},
		{"no upstream status", exitReply(t, 0, nil, rpcReply),
			rpc_proxy.ReasonBadReply, http.StatusBadGateway},
		{"provider selection off", exitError(rpc_proxy.ReasonProviderSelectionOff, "this exit does not serve user-chosen provider URLs"),
			rpc_proxy.ReasonProviderSelectionOff, http.StatusBadGateway},
		{"unknown reason", exitError("from-the-future", "?"),
			rpc_proxy.ReasonExitRefused, http.StatusBadGateway},
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
	_, err := sendRequest(nil, big, "")
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

// sentTo records what the (fake) mixnet was asked to carry.
type sentTo struct {
	request []byte
	userURL string
}

func recordingServer(t *testing.T) (http.HandlerFunc, *[]sentTo) {
	t.Helper()
	var sent []sentTo
	reply := exitReply(t, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, rpcReply)
	var buf bytes.Buffer
	s := &Server{
		log: log.New(&buf),
		send: func(b []byte, userURL string) ([]byte, error) {
			sent = append(sent, sentTo{b, userURL})
			return reply()
		},
	}
	t.Cleanup(func() {
		if strings.Contains(buf.String(), "/v3/secret") {
			t.Errorf("the provider URL's path reached the log:\n%s", buf.String())
		}
	})
	return withCORS(s.Handler), &sent
}

func TestUserURLGoesInTheRequestNotTheHeader(t *testing.T) {
	h, sent := recordingServer(t)
	r := httptest.NewRequest(http.MethodPost, rpc_proxy.UserURLPath, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(rpc_proxy.URLHeader, "https://rpc.example/v3/secret")
	if resp := serve(h, r); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(*sent) != 1 || (*sent)[0].userURL != "https://rpc.example/v3/secret" {
		t.Fatalf("sent %+v", *sent)
	}
	if bytes.Contains(bytes.ToLower((*sent)[0].request), []byte("zknet-url")) || bytes.Contains((*sent)[0].request, []byte("secret")) {
		t.Errorf("the inner request still carries the URL:\n%s", (*sent)[0].request)
	}
}

func TestURLHeaderIsIgnoredOffTheUserURLPath(t *testing.T) {
	h, sent := recordingServer(t)
	r := post("")
	r.Header.Set(rpc_proxy.URLHeader, "https://rpc.example/v3/secret")
	serve(h, r)
	if len(*sent) != 1 || (*sent)[0].userURL != "" || bytes.Contains((*sent)[0].request, []byte("secret")) {
		t.Errorf("a network-key request carried the URL: %+v", *sent)
	}
}

func TestUserURLPathWithoutHeaderIsRefusedLocally(t *testing.T) {
	h, sent := recordingServer(t)
	r := httptest.NewRequest(http.MethodPost, rpc_proxy.UserURLPath, strings.NewReader(`{}`))
	checkFailure(t, serve(h, r), rpc_proxy.ReasonExitRefused, http.StatusBadGateway)
	if len(*sent) != 0 {
		t.Errorf("sent %d requests, want 0", len(*sent))
	}
}

// A version stamped with -X main.version wins over the VCS stamp.
func TestHealthReportsTheStampedVersion(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "v0.0.0-test"
	h, _ := replyServer(func() ([]byte, error) { return nil, errors.New("mixnet down") })
	var b map[string]string
	json.NewDecoder(serve(h, httptest.NewRequest(http.MethodGet, healthPath, nil)).Body).Decode(&b)
	if b["version"] != "v0.0.0-test" {
		t.Errorf("version = %q, want v0.0.0-test", b["version"])
	}
}

// browserRequest is a request to path as a browser sends it: with headers
// that identify the browser and the page, none of which the exit needs.
func browserRequest(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://localhost:7070"+path+"?trace=1", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range map[string]string{
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0",
		"Accept":          "*/*",
		"Accept-Language": "en-US,en;q=0.5",
		"Accept-Encoding": "gzip, deflate, br, zstd",
		"Sec-Fetch-Dest":  "empty",
		"Sec-Fetch-Mode":  "cors",
		"Sec-Fetch-Site":  "cross-site",
		"Origin":          "https://dapp.example",
		"Referer":         "https://dapp.example/swap",
		"Cookie":          "session=abc",
		"Authorization":   "Bearer token",
		"Connection":      "keep-alive",
	} {
		r.Header.Set(k, v)
	}
	return r
}

// checkInner asserts the inner request sent is exactly the allow-list: path,
// Host "nowhere", Content-Type and Content-Length, and the body byte for byte.
func checkInner(t *testing.T, sent []sentTo, path, body string) {
	t.Helper()
	if len(sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(sent))
	}
	r, err := rpc_proxy.ReadInnerRequest(sent[0].request)
	if err != nil {
		t.Fatalf("the exit would refuse the inner request: %s\n%s", err, sent[0].request)
	}
	if r.Method != http.MethodPost || r.RequestURI != path || r.Host != rpc_proxy.InnerHost {
		t.Errorf("request line = %s %s, Host %s; want POST %s, Host %s", r.Method, r.RequestURI, r.Host, path, rpc_proxy.InnerHost)
	}
	want := http.Header{
		"Content-Type":   {"application/json; charset=utf-8"},
		"Content-Length": {strconv.Itoa(len(body))},
	}
	if !reflect.DeepEqual(r.Header, want) {
		t.Errorf("inner headers = %v, want %v", r.Header, want)
	}
	if got, _ := io.ReadAll(r.Body); string(got) != body {
		t.Errorf("inner body = %q, want %q", got, body)
	}
}

// The body carries format verbs and a CRLF: it must reach the exit byte-exact.
const browserBody = "{\"jsonrpc\":\"2.0\",\"id\":1,\r\n\"method\":\"eth_chainId\",\"params\":[\"%s\"]}"

func TestBrowserHeadersNeverReachTheExit(t *testing.T) {
	h, sent := recordingServer(t)
	if resp := serve(h, browserRequest("/ethereum", browserBody)); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	checkInner(t, *sent, "/ethereum", browserBody)
}

func TestBrowserHeadersNeverReachTheExitOnTheUserURLPath(t *testing.T) {
	h, sent := recordingServer(t)
	r := browserRequest(rpc_proxy.UserURLPath, browserBody)
	r.Header.Set(rpc_proxy.URLHeader, "https://rpc.example/v3/secret")
	if resp := serve(h, r); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	checkInner(t, *sent, rpc_proxy.UserURLPath, browserBody)
	if (*sent)[0].userURL != "https://rpc.example/v3/secret" {
		t.Errorf("userURL = %q", (*sent)[0].userURL)
	}
}
