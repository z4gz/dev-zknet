package rpc_proxy

import (
	"bytes"
	"net/http"
	"reflect"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func TestEmptyURLIsOmitted(t *testing.T) {
	without, _ := cbor.Marshal(&Request{Payload: []byte("x")})
	with, _ := cbor.Marshal(&Request{Payload: []byte("x"), URL: "https://rpc.example/v3/key"})
	if bytes.Contains(without, []byte("URL")) || !bytes.Contains(with, []byte("URL")) {
		t.Errorf("without URL %x, with URL %x", without, with)
	}
}

func TestLoadUserURLs(t *testing.T) {
	cfg, err := Load([]byte("[Networks]\n  \"ethereum\" = \"https://rpc.example\"\n"))
	if err != nil || !cfg.UserURLs.Enable {
		t.Fatalf("absent [UserURLs] must mean enabled: %+v %v", cfg, err)
	}
	cfg, err = Load([]byte("[UserURLs]\n  Enable = false\n  AllowCIDRs = [\"172.16.0.0/12\"]\n"))
	if err != nil || cfg.UserURLs.Enable || len(cfg.Policy.Allow) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := Load([]byte("[UserURLs]\n  AllowCIDRs = [\"not-a-prefix\"]\n")); err == nil {
		t.Error("a bad prefix loads")
	}
	if _, err := Load([]byte("[UserURLs]\n  RootCAs = \"/nonexistent.pem\"\n")); err == nil {
		t.Error("a missing RootCAs file loads")
	}
}

func TestPackagedConfigEnablesProviderSelection(t *testing.T) {
	cfg, err := LoadFile("rpc_proxy_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UserURLs.Enable || len(cfg.Policy.Allow) != 0 || cfg.Roots != nil {
		t.Errorf("packaged [UserURLs] = %+v", cfg.UserURLs)
	}
}

func TestFilterHeaders(t *testing.T) {
	got := FilterHeaders(http.Header{
		"Content-Type": {"application/json"},
		"Retry-After":  {"7"},
		"Set-Cookie":   {"a=b"},
		ErrorHeader:    {"mix-timeout"},
	})
	want := http.Header{"Content-Type": {"application/json"}, "Retry-After": {"7"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterHeaders = %v, want %v", got, want)
	}
}
