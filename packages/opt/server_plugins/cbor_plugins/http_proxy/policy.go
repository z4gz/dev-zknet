package http_proxy

import (
	"bytes"
	"compress/flate"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"

	"github.com/ZeroKnowledgeNetwork/opt/common"
)

// ErrDestinationRefused marks a dial the destination policy refused.
var ErrDestinationRefused = errors.New("destination refused")

// ErrTooLarge marks a body past its limit.
var ErrTooLarge = errors.New("too large")

// deniedPrefixes are the addresses a user-chosen upstream may not resolve
// to: not globally routable, or naming the exit's own network. Loopback,
// RFC 1918, CGNAT, link-local (cloud instance metadata lives there), unique
// local, multicast and reserved ranges, plus NAT64 and 6to4, which embed an
// IPv4 address that could be any of those.
var deniedPrefixes = prefixes(
	"0.0.0.0/8",      // "this host"
	"10.0.0.0/8",     // RFC 1918
	"100.64.0.0/10",  // CGNAT
	"127.0.0.0/8",    // loopback
	"169.254.0.0/16", // link-local, includes 169.254.169.254 metadata
	"172.16.0.0/12",  // RFC 1918
	"192.0.0.0/24",   // IETF protocol assignments
	"192.168.0.0/16", // RFC 1918
	"198.18.0.0/15",  // benchmarking
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved, includes broadcast
	"::/128",         // unspecified
	"::1/128",        // loopback
	"64:ff9b::/96",   // NAT64
	"64:ff9b:1::/48", // local-use NAT64
	"2002::/16",      // 6to4
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Policy decides which addresses a user-chosen upstream may be reached at.
type Policy struct {
	// Allow lists prefixes permitted despite the deny list. Test beds only:
	// every stub a containerized exit can reach sits on a private address.
	Allow []netip.Prefix
}

// Check refuses addr unless it is globally routable or allowed. An
// IPv4-mapped IPv6 address is checked as the IPv4 address it carries.
func (p Policy) Check(addr netip.Addr) error {
	addr = addr.Unmap()
	for _, a := range p.Allow {
		if a.Contains(addr) {
			return nil
		}
	}
	for _, d := range deniedPrefixes {
		if d.Contains(addr) {
			return fmt.Errorf("%w: %s is not globally routable", ErrDestinationRefused, addr)
		}
	}
	return nil
}

// NewUserClient returns the client for user-chosen upstreams. The policy is
// applied in the dialer's Control hook, which runs on the address actually
// being connected to, after DNS resolution and for every connection attempt:
// the check and the connection cannot disagree, which defeats DNS
// rebinding. Redirects are never followed; the 3xx is the reply. roots, when
// not nil, replaces the system roots (test beds only).
func NewUserClient(p Policy, roots *x509.CertPool) *http.Client {
	d := &net.Dialer{
		Timeout: DialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrDestinationRefused, err)
			}
			return p.Check(ap.Addr())
		},
	}
	return &http.Client{
		Timeout: UpstreamTimeout,
		Transport: &http.Transport{
			Proxy:                  nil, // never a proxy from the environment: it would dial for us, unchecked
			DialContext:            d.DialContext,
			TLSClientConfig:        &tls.Config{RootCAs: roots},
			TLSHandshakeTimeout:    TLSHandshakeTimeout,
			MaxResponseHeaderBytes: MaxResponseHeaderBytes,
			ForceAttemptHTTP2:      true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// NewNetworksClient returns the client for the operator's [Networks]
// upstreams: the same limits, no destination policy (an operator may run
// its own node on a private address).
func NewNetworksClient() *http.Client {
	return &http.Client{
		Timeout: UpstreamTimeout,
		Transport: &http.Transport{
			Proxy:                  http.ProxyFromEnvironment,
			DialContext:            (&net.Dialer{Timeout: DialTimeout}).DialContext,
			TLSHandshakeTimeout:    TLSHandshakeTimeout,
			MaxResponseHeaderBytes: MaxResponseHeaderBytes,
			ForceAttemptHTTP2:      true,
		},
	}
}

// ReadLimited reads r to the end, or fails with ErrTooLarge past max bytes
// without reading further.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
	}
	return b, nil
}

// DecompressLimited is common.DecompressData bounded to max inflated bytes.
func DecompressLimited(data []byte, max int64) ([]byte, error) {
	if common.CompressionEnabled != "true" {
		return ReadLimited(bytes.NewReader(data), max)
	}
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	return ReadLimited(r, max)
}

// CheckUserRequest checks a user-chosen upstream before anything is dialed:
// a JSON-RPC POST to an absolute https URL without credentials. It returns
// the URL to fetch, without its fragment. Where the URL's host resolves is
// checked later, at dial time.
func CheckUserRequest(rawURL, method, contentType string) (*url.URL, error) {
	if len(rawURL) > MaxURLLength {
		return nil, fmt.Errorf("the provider URL is longer than %d bytes", MaxURLLength)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("the provider URL does not parse")
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("a provider URL must be an absolute https URL")
	}
	if u.User != nil {
		return nil, errors.New("a provider URL must not carry credentials")
	}
	if method != http.MethodPost {
		return nil, errors.New("provider selection serves JSON-RPC POST only")
	}
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || mt != "application/json" {
		return nil, errors.New("provider selection serves JSON-RPC (application/json) only")
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// LogSafe describes err without the request URL a *url.Error carries: a
// provider URL often embeds an API key.
func LogSafe(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + ": " + ue.Err.Error()
	}
	return err.Error()
}
