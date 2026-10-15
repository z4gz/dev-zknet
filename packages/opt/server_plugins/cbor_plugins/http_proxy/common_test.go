package http_proxy

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// legacyResponse is Response as it was before Reason, Status and Header.
type legacyResponse struct {
	Payload []byte
	Error   string
}

func TestOldDecoderReadsNewResponse(t *testing.T) {
	b, err := cbor.Marshal(&Response{
		Payload: []byte("body"),
		Status:  429,
		Header:  http.Header{"Retry-After": {"7"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var old legacyResponse
	if err := cbor.Unmarshal(b, &old); err != nil {
		t.Fatalf("old decoder rejects new reply: %s", err)
	}
	if string(old.Payload) != "body" || old.Error != "" {
		t.Errorf("old decoder read %+v", old)
	}

	b, _ = cbor.Marshal(&Response{Error: "URI not configured in networks", Reason: ReasonExitRefused})
	old = legacyResponse{}
	if err := cbor.Unmarshal(b, &old); err != nil || old.Error != "URI not configured in networks" {
		t.Errorf("old decoder read %+v (%v)", old, err)
	}
}

func TestNewDecoderReadsOldResponse(t *testing.T) {
	b, _ := cbor.Marshal(&legacyResponse{Payload: []byte("body")})
	var r Response
	if err := cbor.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if string(r.Payload) != "body" || r.Status != 0 || r.Header != nil || r.Reason != "" {
		t.Errorf("new decoder read %+v", r)
	}
}

// The new fields cost nothing on the wire when unset.
func TestEmptyNewFieldsAreOmitted(t *testing.T) {
	newB, _ := cbor.Marshal(&Response{Payload: []byte("x")})
	oldB, _ := cbor.Marshal(&legacyResponse{Payload: []byte("x")})
	if !reflect.DeepEqual(newB, oldB) {
		t.Errorf("new encoding %x differs from old %x", newB, oldB)
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
