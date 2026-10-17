package http_proxy

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/ZeroKnowledgeNetwork/opt/common"
)

func TestPolicyCheck(t *testing.T) {
	refused := []string{
		"0.1.2.3", "10.1.2.3", "100.100.1.1", "127.0.0.1", "169.254.169.254", "172.16.0.1",
		"192.0.0.8", "192.168.1.1", "198.18.0.1", "224.0.0.1", "255.255.255.255",
		"::", "::1", "64:ff9b::a00:1", "64:ff9b:1::1", "2002:a00:1::1", "fd00:ec2::254", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", // IPv4-mapped: checked as IPv4
	}
	for _, s := range refused {
		if err := (Policy{}).Check(netip.MustParseAddr(s)); !errors.Is(err, ErrDestinationRefused) {
			t.Errorf("%s: got %v, want refused", s, err)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700::1111", "::ffff:1.1.1.1"} {
		if err := (Policy{}).Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	allow := Policy{Allow: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}
	if err := allow.Check(netip.MustParseAddr("172.20.0.5")); err != nil {
		t.Errorf("allowed prefix refused: %v", err)
	}
	if err := allow.Check(netip.MustParseAddr("10.0.0.1")); err == nil {
		t.Error("allow list widened past its prefix")
	}
}

// The policy runs on the resolved address being dialed, so a name that
// resolves to loopback is refused like the literal.
func TestUserClientRefusesAfterResolution(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	for _, u := range []string{srv.URL, "https://localhost" + port} {
		_, err := NewUserClient(Policy{}, nil).Post(u, "application/json", nil)
		if !errors.Is(err, ErrDestinationRefused) {
			t.Errorf("%s: got %v, want refused", u, err)
		}
	}
}

func TestUserClientDoesNotFollowRedirects(t *testing.T) {
	dialed := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dialed = true }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c := NewUserClient(Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs)
	resp, err := c.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || dialed {
		t.Errorf("status %d, redirect target dialed: %v", resp.StatusCode, dialed)
	}
}

func TestClientsCarryTheLimits(t *testing.T) {
	for name, c := range map[string]*http.Client{"user": NewUserClient(Policy{}, nil), "networks": NewNetworksClient()} {
		tr := c.Transport.(*http.Transport)
		if c.Timeout != UpstreamTimeout || tr.TLSHandshakeTimeout != TLSHandshakeTimeout || tr.MaxResponseHeaderBytes != MaxResponseHeaderBytes {
			t.Errorf("%s client: timeout %s, TLS %s, header bytes %d", name, c.Timeout, tr.TLSHandshakeTimeout, tr.MaxResponseHeaderBytes)
		}
	}
	if DialTimeout >= UpstreamTimeout || TLSHandshakeTimeout >= UpstreamTimeout {
		t.Errorf("connection set-up (dial %s, TLS %s) must fit inside UpstreamTimeout %s", DialTimeout, TLSHandshakeTimeout, UpstreamTimeout)
	}
	if NewUserClient(Policy{}, nil).Transport.(*http.Transport).Proxy != nil {
		t.Error("the user client must not take a proxy from the environment")
	}
}

func TestReadLimited(t *testing.T) {
	if b, err := ReadLimited(strings.NewReader("12345"), 5); err != nil || string(b) != "12345" {
		t.Errorf("at the limit: %q %v", b, err)
	}
	if _, err := ReadLimited(strings.NewReader("123456"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("past the limit: %v", err)
	}
}

func TestDecompressLimitedStopsAnInflateBomb(t *testing.T) {
	bomb, err := common.CompressData(bytes.Repeat([]byte{0}, MaxDecompressedRequestBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecompressLimited(bomb, MaxDecompressedRequestBytes); !errors.Is(err, ErrTooLarge) {
		t.Errorf("%d compressed bytes: %v, want too large", len(bomb), err)
	}
}
