package rpc_proxy

import (
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestInnerRequestRoundTrips(t *testing.T) {
	// The body carries CRLFs and format verbs: it must survive byte for byte.
	body := []byte("{\"jsonrpc\":\"2.0\",\r\n\"x\":\"%s %d\"}")
	b, err := NewInnerRequest(http.MethodPost, "/ethereum/sepolia", "application/json; charset=utf-8", body)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReadInnerRequest(b)
	if err != nil {
		t.Fatalf("%s\n%s", err, b)
	}
	if r.Method != http.MethodPost || r.URL.Path != "/ethereum/sepolia" || r.Host != InnerHost {
		t.Errorf("request line = %s %s, Host %s", r.Method, r.URL.Path, r.Host)
	}
	want := http.Header{
		"Content-Type":   {"application/json; charset=utf-8"},
		"Content-Length": {strconv.Itoa(len(body))},
	}
	if !reflect.DeepEqual(r.Header, want) {
		t.Errorf("headers = %v, want %v", r.Header, want)
	}
	got, _ := io.ReadAll(r.Body)
	if string(got) != string(body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestInnerRequestWithoutContentType(t *testing.T) {
	b, err := NewInnerRequest(http.MethodGet, "/_/probe/0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReadInnerRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if want := (http.Header{"Content-Length": {"0"}}); !reflect.DeepEqual(r.Header, want) {
		t.Errorf("headers = %v, want %v", r.Header, want)
	}
}

func TestNewInnerRequestRefusals(t *testing.T) {
	cases := []struct{ name, method, path, contentType string }{
		{"empty method", "", "/x", ""},
		{"method with a space", "POST /", "/x", ""},
		{"empty path", "POST", "", ""},
		{"absolute form", "POST", "https://x/y", ""},
		{"query", "POST", "/x?key=secret", ""},
		{"fragment", "POST", "/x#y", ""},
		{"space in the path", "POST", "/x HTTP/1.1\r\nUser-Agent: y", ""},
		{"header injection", "POST", "/x", "application/json\r\nUser-Agent: y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if b, err := NewInnerRequest(c.method, c.path, c.contentType, nil); err == nil {
				t.Errorf("accepted:\n%s", b)
			}
		})
	}
}

func TestReadInnerRequestRefusals(t *testing.T) {
	const ok = "POST /x HTTP/1.1\r\nHost: nowhere\r\nContent-Type: application/json\r\nContent-Length: 2\r\n"
	cases := []struct{ name, raw, field string }{
		{"user agent", ok + "User-Agent: Mozilla/5.0\r\n\r\n{}", "User-Agent"},
		{"origin", ok + "Origin: https://dapp.example\r\n\r\n{}", "Origin"},
		{"cookie", ok + "Cookie: a=b\r\n\r\n{}", "Cookie"},
		{"browser host", strings.Replace(ok, "nowhere", "localhost:7070", 1) + "\r\n{}", "Host"},
		{"no host", strings.Replace(ok, "Host: nowhere\r\n", "", 1) + "\r\n{}", "Host"},
		{"repeated header", ok + "Content-Type: text/plain\r\n\r\n{}", "Content-Type"},
		{"query", strings.Replace(ok, "/x", "/x?a=b", 1) + "\r\n{}", "query"},
		{"absolute form", strings.Replace(ok, "/x", "https://x/y", 1) + "\r\n{}", "slash"},
		{"chunked", "POST /x HTTP/1.1\r\nHost: nowhere\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\n\r\n", "Transfer-Encoding"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadInnerRequest([]byte(c.raw))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("err = %q, want it to name %s", err, c.field)
			}
		})
	}
}
