package rpc_proxy

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"

	"github.com/BurntSushi/toml"
)

// Request is the type which is serialized into the cborplugin request payload.
type Request struct {
	// Payload is the inner request, compressed: the HTTP/1.1 request that
	// NewInnerRequest writes and ReadInnerRequest reads. It is built afresh
	// from method, path, Content-Type and body; the caller's own headers never
	// cross the mixnet. The exit forwards its body and Content-Type.
	Payload []byte

	// URL, when set, is the upstream the user chose: provider selection,
	// served in the shape of Anon-RPC SPEC v0.3.2 §3.2 "general web request
	// access" but limited to JSON-RPC POST to a public https URL. The exit
	// fetches it under the destination policy instead of a [Networks]
	// entry. When empty, the Payload's path selects the [Networks] entry.
	URL string `cbor:",omitempty"`
}

// Response is the type which is serialized and sent as a response from the cborplugin.
type Response struct {
	// Payload contains the entire proxied http response or response chunk.
	Payload []byte

	// Error if not empty indicates an error condition.
	Error string

	// The fields below are omitted when empty.

	// Reason classifies Error as one of the Reason* values.
	Reason string `cbor:",omitempty"`

	// Status is the upstream HTTP status code; 0 means the exit did not
	// report one.
	Status int `cbor:",omitempty"`

	// Header holds the upstream response headers named by RelayedHeaders.
	Header http.Header `cbor:",omitempty"`
}

// UserURLPath is WalletShield's local endpoint for provider selection: a
// POST there is sent to the exit with Request.URL set from URLHeader. It
// sits in the "_" namespace the exit reserves, so it never names a
// [Networks] key.
const UserURLPath = "/_/url"

// URLHeader carries the user's provider URL from the caller to WalletShield.
// It is a header rather than a query parameter so the URL, which often embeds
// an API key, stays out of request lines and access logs. It never crosses
// the mixnet as a header.
const URLHeader = "ZKNet-URL"

// ErrorHeader marks a reply that WalletShield or the exit originated rather
// than relayed from the upstream. Its value is one of the Reason* values.
const ErrorHeader = "ZKNet-Error"

// Reasons a reply was originated instead of relayed.
const (
	ReasonMixTimeout          = "mix-timeout"
	ReasonExitRefused         = "exit-refused"
	ReasonUpstreamUnreachable = "upstream-unreachable"
	ReasonTooLarge            = "too-large"
	ReasonBadReply            = "bad-reply"
	// ReasonExitBusy: the exit already serves ExitConcurrency requests.
	ReasonExitBusy = "exit-busy"
	// ReasonProviderSelectionOff: the operator turned [UserURLs] off. The
	// exit never falls back to [Networks] for a user-chosen URL, because a
	// user who chose a provider expects that provider.
	ReasonProviderSelectionOff = "provider-selection-off"
)

// RelayedHeaders lists the upstream response headers carried back across the
// mixnet. Every other upstream header is dropped.
var RelayedHeaders = []string{
	"Content-Type",
	"Retry-After",
}

// FilterHeaders returns the RelayedHeaders present in h. ErrorHeader is never
// returned, so an upstream cannot impersonate an originated failure.
func FilterHeaders(h http.Header) http.Header {
	out := http.Header{}
	for _, name := range RelayedHeaders {
		key := http.CanonicalHeaderKey(name)
		if key == ErrorHeader {
			continue
		}
		if v, ok := h[key]; ok {
			out[key] = v
		}
	}
	return out
}

type Config struct {
	Networks map[string]string // e.g. "ethereum/sepolia" -> "https://ethereum-sepolia-rpc.publicnode.com"
	UserURLs UserURLs

	// Policy and Roots are derived from UserURLs by Validate.
	Policy Policy         `toml:"-"`
	Roots  *x509.CertPool `toml:"-"`
}

// UserURLs configures provider selection: requests that carry Request.URL.
type UserURLs struct {
	// Enable serves user-chosen URLs. On unless the config turns it off;
	// when off, such requests are refused with ReasonProviderSelectionOff.
	Enable bool
	// AllowCIDRs are prefixes the destination policy permits anyway. Test
	// beds only.
	AllowCIDRs []string
	// RootCAs is a PEM file that replaces the system roots for user-chosen
	// URLs. Test beds only.
	RootCAs string
}

func (c *Config) Validate() error {
	for k, v := range c.Networks {
		if k == "" || v == "" {
			return errors.New("invalid entry found in config, cannot map to/from empty string")
		}
	}
	c.Policy = Policy{}
	for _, s := range c.UserURLs.AllowCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("UserURLs.AllowCIDRs: %w", err)
		}
		c.Policy.Allow = append(c.Policy.Allow, p.Masked())
	}
	c.Roots = nil
	if c.UserURLs.RootCAs != "" {
		pem, err := os.ReadFile(c.UserURLs.RootCAs)
		if err != nil {
			return fmt.Errorf("UserURLs.RootCAs: %w", err)
		}
		c.Roots = x509.NewCertPool()
		if !c.Roots.AppendCertsFromPEM(pem) {
			return fmt.Errorf("UserURLs.RootCAs: no certificate in %s", c.UserURLs.RootCAs)
		}
	}
	return nil
}

// Load parses and validates the provided buffer b as a config file body and
// returns the Config.
func Load(b []byte) (*Config, error) {
	if b == nil {
		return nil, errors.New("No nil buffer as config file")
	}

	cfg := &Config{UserURLs: UserURLs{Enable: true}}
	err := toml.Unmarshal(b, cfg)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFile loads, parses and validates the provided file and returns the
// Config.
func LoadFile(f string) (*Config, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	return Load(b)
}
