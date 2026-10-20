// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/rpc_proxy"
)

// loggedServer is a handler whose (fake) mixnet answers with send, logging at
// level into the returned buffer.
func loggedServer(level log.Level, send func() ([]byte, error)) (http.HandlerFunc, *bytes.Buffer) {
	var buf bytes.Buffer
	s := &Server{
		log:  newLogger(&buf, level),
		send: func([]byte, string) ([]byte, error) { return send() },
	}
	return withCORS(s.Handler), &buf
}

const (
	secretBody  = `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc0000000000000000000000000000000000def","latest"]}`
	secretURL   = "https://rpc.example/v3/p-secret?apikey=u-secret"
	secretAuth  = "Bearer h-secret"
	upstreamOut = `{"jsonrpc":"2.0","id":1,"result":"0x1"}`
)

// keyedRequest is a provider-URL request carrying a key in its URL, an
// Authorization header and a JSON-RPC body (pretty-printed, as a wallet may
// send it).
func keyedRequest() *http.Request {
	var pretty bytes.Buffer
	json.Indent(&pretty, []byte(secretBody), "", "  ")
	r := httptest.NewRequest(http.MethodPost, rpc_proxy.UserURLPath, &pretty)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", secretAuth)
	r.Header.Set(rpc_proxy.URLHeader, secretURL)
	return r
}

func okReply(t *testing.T) func() ([]byte, error) {
	return exitReply(t, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, upstreamOut)
}

// lines returns the log lines whose message is msg.
func lines(out, msg string) []string {
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, " walletshield: "+msg+" ") {
			got = append(got, l)
		}
	}
	return got
}

// field returns the raw value of key in line: up to the field next, or to the
// end of the line when next is empty.
func field(t *testing.T, line, key, next string) string {
	t.Helper()
	i := strings.Index(line, " "+key+"=")
	if i < 0 {
		t.Fatalf("no %s= in %q", key, line)
	}
	v := line[i+len(key)+2:]
	if next != "" {
		v = v[:strings.Index(v, " "+next+"=")]
	}
	return v
}

// noKeys asserts that line carries none of keys: the rpc line has them.
func noKeys(t *testing.T, line string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if strings.Contains(line, " "+k+"=") {
			t.Errorf("%s= repeats the rpc line: %s", k, line)
		}
	}
}

// At INFO the log carries no body, no header value and no URL beyond the
// provider's host.
func TestInfoLogNeverCarriesBodiesHeadersOrURLs(t *testing.T) {
	h, logged := loggedServer(log.InfoLevel, okReply(t))
	serve(h, keyedRequest())
	serve(h, post(""))

	out := logged.String()
	for _, leak := range []string{"jsonrpc", "0xabc", "secret", "Bearer", "/v3/", "headers=", "body=", "url="} {
		if strings.Contains(out, leak) {
			t.Errorf("the INFO log carries %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{
		"INFO walletshield: rpc id=1 route=url to=rpc.example rpc=eth_getBalance status=200 in=145B out=39B took=",
		"INFO walletshield: rpc id=2 route=network to=ethereum rpc=eth_chainId status=200 in=59B out=39B took=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n != 2 {
		t.Errorf("the INFO log has %d lines, want one per request:\n%s", n, out)
	}
}

// At DEBUG each request attaches a request line and a reply line to its rpc
// record, carrying the full URL, headers and bodies, each on one line, with
// the JSON raw, and nothing the rpc line already has.
func TestDebugLogShowsEverythingRaw(t *testing.T) {
	h, logged := loggedServer(log.DebugLevel, okReply(t))
	serve(h, keyedRequest())
	out := logged.String()

	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n != 3 {
		t.Fatalf("the DEBUG log has %d lines, want request, reply and rpc:\n%s", n, out)
	}
	req, rep, rpc := lines(out, "request"), lines(out, "reply"), lines(out, "rpc")
	if len(req) != 1 || len(rep) != 1 || len(rpc) != 1 {
		t.Fatalf("want one request, reply and rpc line:\n%s", out)
	}
	if !strings.Contains(req[0], "DEBU walletshield: request id=1 route=url url="+secretURL+" headers=") {
		t.Errorf("request line: %s", req[0])
	}
	if !strings.Contains(rep[0], "DEBU walletshield: reply id=1 headers=") {
		t.Errorf("reply line: %s", rep[0])
	}
	noKeys(t, req[0], "to", "rpc", "in", "status", "out")
	noKeys(t, rep[0], "route", "to", "rpc", "in", "status", "out")
	if !(strings.Index(out, "request id=1") < strings.Index(out, "reply id=1") && strings.Index(out, "reply id=1") < strings.Index(out, "rpc id=1")) {
		t.Errorf("want request, reply, rpc in order:\n%s", out)
	}

	// The request headers are those that arrived, ZKNet-URL included.
	var inHeaders http.Header
	if err := json.Unmarshal([]byte(field(t, req[0], "headers", "body")), &inHeaders); err != nil {
		t.Fatalf("request headers= is not raw JSON: %s\n%s", err, req[0])
	}
	if inHeaders.Get("Authorization") != secretAuth || inHeaders.Get(rpc_proxy.URLHeader) != secretURL {
		t.Errorf("request headers = %v", inHeaders)
	}
	if got := field(t, req[0], "body", ""); got != secretBody {
		t.Errorf("request body = %s, want the compacted body %s", got, secretBody)
	}

	// The reply headers are those that came back from the exit, without
	// WalletShield's own.
	outHeaders := field(t, rep[0], "headers", "body")
	if outHeaders != `{"Content-Type":["application/json"]}` {
		t.Errorf("reply headers = %s, want the upstream's only", outHeaders)
	}
	if strings.Contains(outHeaders, "Access-Control-") {
		t.Errorf("reply headers carry the CORS headers: %s", outHeaders)
	}
	if got := field(t, rep[0], "body", ""); got != upstreamOut {
		t.Errorf("reply body = %s, want %s", got, upstreamOut)
	}
}

// The network form's URL is the one the request addressed; a body that is
// not JSON is a quoted string.
func TestDebugLogNetworkURLAndNonJSONBody(t *testing.T) {
	h, logged := loggedServer(log.DebugLevel, exitReply(t, http.StatusOK, nil, "plain\ntext"))
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7070/ethereum/sepolia?x=1", strings.NewReader("not json"))
	serve(h, r)
	out := logged.String()
	want := `request id=1 route=network url=http://127.0.0.1:7070/ethereum/sepolia?x=1 headers={} body="not json"`
	if !strings.Contains(out, want) {
		t.Errorf("the log lacks %q:\n%s", want, out)
	}
	if !strings.Contains(out, `reply id=1 headers={} body="plain\ntext"`) {
		t.Errorf("the reply body is not a quoted string:\n%s", out)
	}
}

// An exit refusal has a reply line: the envelope the caller receives.
func TestDebugLogShowsTheExitRefusal(t *testing.T) {
	h, logged := loggedServer(log.DebugLevel, exitError(rpc_proxy.ReasonTooLarge, "HTTP response is too big"))
	serve(h, post(""))
	out := logged.String()
	rep := lines(out, "reply")
	if len(rep) != 1 {
		t.Fatalf("no reply line for the refusal:\n%s", out)
	}
	if got := field(t, rep[0], "headers", "body"); got != `{"Zknet-Error":["too-large"]}` {
		t.Errorf("reply headers = %s, want the error header only", got)
	}
	if got := field(t, rep[0], "body", ""); got != `{"error":{"reason":"too-large","message":"HTTP response is too big"}}` {
		t.Errorf("reply body = %s", got)
	}
	if !strings.Contains(out, "WARN walletshield: rpc id=1 route=network to=ethereum rpc=eth_chainId status=413 reason=too-large in=59B took=") {
		t.Errorf("the log lacks the WARN rpc line:\n%s", out)
	}
}

// A failure WalletShield detects itself has no reply line; its WARN rpc line
// carries the method and size.
func TestWarnNamesTheFailure(t *testing.T) {
	h, logged := loggedServer(log.DebugLevel, func() ([]byte, error) { return nil, errors.New("context deadline exceeded") })
	serve(h, post(""))
	out := logged.String()
	if len(lines(out, "reply")) != 0 {
		t.Errorf("a mix timeout has a reply line:\n%s", out)
	}
	want := `WARN walletshield: rpc id=1 route=network to=ethereum rpc=eth_chainId status=504 reason=mix-timeout in=59B took=`
	if !strings.Contains(out, want) {
		t.Errorf("the log lacks %q:\n%s", want, out)
	}
}

// A too-large request is identifiable at INFO by its method and size.
func TestWarnNamesATooLargeRequest(t *testing.T) {
	h, logged := loggedServer(log.InfoLevel, func() ([]byte, error) {
		return nil, fmt.Errorf("%w: payload size 40000 exceeds maximum 30000 bytes", errTooLarge)
	})
	r := httptest.NewRequest(http.MethodPost, "/ethereum", strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"eth_call"},{"jsonrpc":"2.0","id":2,"method":"eth_call"}]`))
	serve(h, r)
	want := "WARN walletshield: rpc id=1 route=network to=ethereum rpc=batch(2) status=413 reason=too-large in=91B took="
	if out := logged.String(); !strings.Contains(out, want) {
		t.Errorf("the log lacks %q:\n%s", want, out)
	}
}

// Probe mode logs its tally as key=value, in the units of took.
func TestProbeTallyLine(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger(&buf, log.InfoLevel)
	var p probeStats
	p.lost()
	p.log(l)
	p.received(1540 * time.Millisecond)
	p.received(2120 * time.Millisecond)
	p.received(1950 * time.Millisecond)
	p.log(l)
	out := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i, want := range []string{
		"INFO walletshield: probe sent=1 received=0 loss=100%",
		"INFO walletshield: probe sent=4 received=3 loss=25% rtt_min=1.54s rtt_avg=1.87s rtt_max=2.12s",
	} {
		if !strings.HasSuffix(out[i], want) {
			t.Errorf("line %d = %q, want …%q", i, out[i], want)
		}
	}
}

func TestRPCMethod(t *testing.T) {
	for body, want := range map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`: "eth_chainId",
		`[{"method":"a"},{"method":"b"},{"method":"c"}]`:  "batch(3)",
		`not json`: "?",
		`null`:     "?",
		``:         "?",
	} {
		if got := rpcMethod([]byte(body)); got != want {
			t.Errorf("rpcMethod(%q) = %q, want %q", body, got, want)
		}
	}
}

// The exit answers a probe as an empty 200, which logs as a normal rpc line.
func TestProbeLogsAsARequest(t *testing.T) {
	h, logged := loggedServer(log.InfoLevel, exitReply(t, http.StatusOK, nil, ""))
	resp := serve(h, httptest.NewRequest(http.MethodPost, "/_/probe/0", nil))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	want := "INFO walletshield: rpc id=1 route=probe to=exit rpc=? status=200 in=0B out=0B took="
	if out := logged.String(); !strings.Contains(out, want) || strings.Contains(out, "WARN") {
		t.Errorf("the log lacks %q:\n%s", want, out)
	}
}
