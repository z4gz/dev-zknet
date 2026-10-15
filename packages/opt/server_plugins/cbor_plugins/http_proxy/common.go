package http_proxy

import (
	"errors"
	"net/http"
	"os"

	"github.com/BurntSushi/toml"
)

// Request is the type which is serialized into the cborplugin request payload.
type Request struct {
	// Payload contains the proxied http request
	Payload []byte
}

// Response is the type which is serialized and sent as a response from the cborplugin.
type Response struct {
	// Payload contains the entire proxied http response or response chunk.
	Payload []byte

	// Error if not empty indicates an error condition.
	Error string

	// The fields below were added after Payload and Error. They are omitted
	// when empty, so an old peer neither sends nor expects them: an old
	// decoder ignores unknown CBOR map keys, and a new decoder reads their
	// absence as the zero value.

	// Reason classifies Error as one of the Reason* values.
	Reason string `cbor:",omitempty"`

	// Status is the upstream HTTP status code; 0 means the exit did not
	// report one.
	Status int `cbor:",omitempty"`

	// Header holds the upstream response headers named by RelayedHeaders.
	Header http.Header `cbor:",omitempty"`
}

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
	Networks map[string]string // e.g. "ethereum.mainnet" -> "https://ethereum-rpc.publicnode.com"
}

func (c *Config) Validate() error {
	for k, v := range c.Networks {
		if k == "" || v == "" {
			return errors.New("invalid entry found in config, cannot map to/from empty string")
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

	cfg := new(Config)
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
