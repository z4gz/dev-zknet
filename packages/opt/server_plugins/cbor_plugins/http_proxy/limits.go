package http_proxy

import "time"

// The exit's limits, in one place. The reply payload bound is not here: it
// comes from the mix server's Sphinx geometry at start.
const (
	// ExitConcurrency is how many requests the exit serves at once. The CBOR
	// plugin server hands over one command at a time, so the exit runs each
	// request on its own goroutine; past this many in flight it answers
	// ReasonExitBusy at once. It bounds a slow upstream to this many slots
	// for UpstreamTimeout each.
	ExitConcurrency = 16

	// UpstreamTimeout bounds one upstream exchange: dial, TLS, request and
	// the whole reply body. It stays well below WalletShield's -timeout
	// (20 s), so a hung upstream reports as ReasonUpstreamUnreachable rather
	// than ReasonMixTimeout.
	UpstreamTimeout = 10 * time.Second
	// DialTimeout and TLSHandshakeTimeout bound the connection set-up within
	// UpstreamTimeout.
	DialTimeout         = 3 * time.Second
	TLSHandshakeTimeout = 3 * time.Second

	// MaxRequestBodyBytes bounds the body the exit forwards upstream. A
	// JSON-RPC read is under 1 kB and a signed transaction far below this.
	MaxRequestBodyBytes = 128 << 10
	// MaxDecompressedRequestBytes bounds the inflated request, so a 30 kB
	// packet cannot inflate to tens of megabytes.
	MaxDecompressedRequestBytes = 1 << 20
	// MaxResponseBodyBytes bounds the upstream reply read into memory. A
	// reply that fits one reply packet compresses 2-5:1 (measured on
	// mainnet blocks), so is far below this; past it the reply is
	// ReasonTooLarge without reading further.
	MaxResponseBodyBytes = 1 << 20
	// MaxResponseHeaderBytes bounds the upstream reply's header block.
	MaxResponseHeaderBytes = 16 << 10
	// MaxURLLength bounds a user-chosen upstream URL.
	MaxURLLength = 2 << 10
)
