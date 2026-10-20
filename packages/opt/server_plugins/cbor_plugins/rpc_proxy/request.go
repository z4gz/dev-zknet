package rpc_proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The inner request is the HTTP/1.1 request carried, compressed, in
// Request.Payload. It is not the caller's request: WalletShield builds it
// afresh with NewInnerRequest, so nothing that identifies the caller's
// software (User-Agent, Accept-Language, Sec-*, Origin, Referer, cookies, the
// caller's Host) ever crosses the mixnet. It carries exactly:
//
//   - the request line: method and origin-form path, without a query;
//   - Host: InnerHost, a fixed name that identifies nothing;
//   - Content-Type, when the caller sent one;
//   - Content-Length;
//   - the body, byte for byte.
//
// These are the fields the exit uses: the path selects the [Networks] entry,
// the probe or provider selection; provider selection checks the method and
// Content-Type; the upstream gets the body and Content-Type only. The exit
// reads the payload with ReadInnerRequest, which refuses anything else, so a
// client that leaks a header fails loudly instead of silently.

// InnerHost is the Host of every inner request.
const InnerHost = "nowhere"

// innerHeaders are the headers an inner request may carry besides Host.
var innerHeaders = map[string]bool{
	"Content-Type":   true,
	"Content-Length": true,
}

// NewInnerRequest serializes the inner request for method, path and body.
// path is origin-form and carries no query; contentType is omitted when empty.
func NewInnerRequest(method, path, contentType string, body []byte) ([]byte, error) {
	if method == "" || strings.ContainsFunc(method, notToken) {
		return nil, fmt.Errorf("invalid method %q", method)
	}
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if strings.ContainsFunc(contentType, notFieldValue) {
		return nil, errors.New("invalid Content-Type")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\nHost: %s\r\n", method, path, InnerHost)
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	return b.Bytes(), nil
}

// ReadInnerRequest parses an inner request and refuses one that carries
// anything NewInnerRequest does not write. The error names the offending
// field, never its value.
func ReadInnerRequest(b []byte) (*http.Request, error) {
	r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(b)))
	if err != nil {
		return nil, fmt.Errorf("not an HTTP request: %w", err)
	}
	if err := checkPath(r.RequestURI); err != nil {
		return nil, err
	}
	if r.Host != InnerHost {
		return nil, fmt.Errorf("Host is not %s", InnerHost)
	}
	if len(r.TransferEncoding) > 0 {
		return nil, errors.New("Transfer-Encoding is not allowed")
	}
	for k, v := range r.Header {
		if !innerHeaders[k] {
			return nil, fmt.Errorf("header %s is not allowed", k)
		}
		if len(v) != 1 {
			return nil, fmt.Errorf("header %s is repeated", k)
		}
	}
	return r, nil
}

// checkPath accepts an origin-form path without a query.
func checkPath(path string) error {
	if path == "" || path[0] != '/' {
		return errors.New("the path must start with a slash")
	}
	if strings.ContainsAny(path, "?#") || strings.ContainsFunc(path, notToken) {
		return errors.New("the path must be origin-form without a query")
	}
	return nil
}

// notToken reports a byte that may not appear in a method or a request
// target: a control character, a space or a non-ASCII byte.
func notToken(r rune) bool {
	return r <= ' ' || r >= 0x7f
}

// notFieldValue reports a byte that may not appear in a header value.
func notFieldValue(r rune) bool {
	return (r < ' ' && r != '\t') || r == 0x7f
}
