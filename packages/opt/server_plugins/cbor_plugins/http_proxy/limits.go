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
	// the whole reply body. It is set by the client, not by WalletShield's
	// -timeout: the katzenpost v0.0.107 client daemon keeps a SURB's reply
	// state for the encoded round trip plus (2·layers + 3) × perHopSlopMs
	// (500 ms, private to client/daemon.go), and silently drops a later
	// reply. That window is about 4.5 s at 3 layers, and per-hop scheduler,
	// network and unwrap overhead consumes part of it (measured: an
	// exit-side delay of 4 s comes back, 5 s never does). 3.5 s leaves about
	// 1 s of margin, so a hung upstream reports as ReasonUpstreamUnreachable
	// instead of being lost as a mix timeout.
	UpstreamTimeout = 3500 * time.Millisecond
	// DialTimeout and TLSHandshakeTimeout bound the connection set-up; each
	// stays well inside UpstreamTimeout, which caps the whole exchange.
	DialTimeout         = 1500 * time.Millisecond
	TLSHandshakeTimeout = 1500 * time.Millisecond

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
