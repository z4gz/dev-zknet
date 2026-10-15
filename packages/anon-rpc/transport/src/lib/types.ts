// The transport seam: how an anon-rpc worker hands one request to a local
// ZKNet peer that carries it through the mixnet to an exit, and gets the
// exit's reply back.
//
// Named by peer, not by protocol. Every implementation ends at the same exit
// service (`http_proxy`), which takes a serialized HTTP request, uses its
// path as a key into the operator's `[Networks]` map, and relays the upstream
// status, an allowlisted set of headers and the body. The seam carries
// exactly that — a network key and a body in, an upstream reply out — and
// nothing a particular peer protocol would add.

/**
 * anon-rpc SPEC §12 `KpsErrorCode`, reused as the worker's call-failure
 * vocabulary. This is the field a host may branch on (SPEC §7, §12).
 */
export type TransportErrorCode =
  | 'cancelled'
  | 'closed'
  | 'reset'
  | 'timeout'
  | 'network-error'
  | 'protocol-error'
  | 'unsupported'
  | 'too-large'
  | 'queue-full'
  | 'permission-denied'
  | 'internal-error';

/**
 * Why a request failed, more finely than its `code`. Stable identifiers,
 * carried as the error's `name` (the harness forwards `{ name, message, code }`
 * and nothing else).
 */
export type TransportFailure =
  /**
   * Nothing readable came back from the peer's address: it is not running
   * there, or the browser's Local Network Access gate refused the request
   * before any I/O. A worker cannot tell these apart; the host can (it knows
   * its delegation and can query its own permission state).
   */
  | 'PeerUnreachable'
  /** The peer answered, but the browser would not let the worker read the reply (CORS). */
  | 'CorsRejected'
  /** An `http:` LAN target from a secure context, blocked as mixed content. */
  | 'MixedContentBlocked'
  /** The peer reported that no reply came back over the mixnet in its own deadline. */
  | 'MixTimeout'
  /** The exit refused the request (e.g. a network key it does not serve); nothing went upstream. */
  | 'ExitRefused'
  /** The exit could not complete the upstream request. */
  | 'UpstreamUnreachable'
  /** The peer could not decode the exit's reply. */
  | 'BadReply'
  /** The request or the reply did not fit the mixnet payload. */
  | 'TooLarge'
  /** The peer answered with something this transport does not recognise. */
  | 'UnexpectedReply'
  /** The worker's own deadline passed first. */
  | 'Deadline'
  /** The caller aborted. Named like `fetch`'s own abort error. */
  | 'AbortError'
  /** The transport was closed while the request was in flight. */
  | 'Closed'
  /** The peer URL is not a local peer (or not a URL this transport speaks). Raised at construction. */
  | 'TargetRefused';

/** One request for the exit. */
export interface ExitRequest {
  /** Key into the exit's `[Networks]` map, without a leading slash: `ethereum`, `ethereum/sepolia`. */
  network: string;
  /** Request body bytes; for `/ethereum-rpc` a JSON-RPC payload (single or batch). */
  body: Uint8Array;
  /** The one request header the exit forwards upstream. */
  contentType: string;
}

/** An `[name, value]` header list, order and duplicates preserved (SPEC §9.1 `HeaderList`). */
export type HeaderList = [name: string, value: string][];

/**
 * The upstream's reply, relayed by the exit: its status, the allowlisted
 * headers (UPSTREAM_HEADERS) and the body bytes, unchanged. Any status is a
 * success at this layer — a 429 from the upstream is the upstream's answer,
 * not a transport failure.
 */
export interface ExitResponse {
  status: number;
  headers: HeaderList;
  body: Uint8Array;
}

export interface RequestOptions {
  /** Aborts the in-flight request; it then rejects with code `cancelled`. */
  signal?: AbortSignal;
}

/** How far a probe got. Ordered: each stage implies the ones before it. */
export type ProbeReach =
  /** Refused before any I/O, or nothing answered. */
  | 'none'
  /** The local peer answered (WalletShield's HTTP listener; kpclientd's connection-status event). */
  | 'peer'
  /** A probe round trip through the mixnet came back from an exit. */
  | 'exit';

export interface ProbeResult {
  reach: ProbeReach;
  /** Wall-clock milliseconds the probe took. */
  ms: number;
  /** The peer's self-reported version, when it reports one. */
  peerVersion?: string;
  /** True when the peer answered but predates the failure-marking contract (no local health answer). */
  legacy?: boolean;
  /** Why it stopped short of `exit`, when it did. */
  error?: TransportErrorShape;
}

export interface ProbeOptions {
  signal?: AbortSignal;
  /**
   * `peer` stops once the local peer is known to answer, without touching the
   * mixnet; `exit` also sends one probe through the mixnet. Default `peer`.
   */
  depth?: 'peer' | 'exit';
}

/** The serializable face of a TransportError. */
export interface TransportErrorShape {
  code: TransportErrorCode;
  name: TransportFailure;
  message: string;
}

/**
 * The seam. One instance per worker; it owns whatever connection state its
 * peer needs (none for HTTP, a WebSocket and a session for kpclientd).
 */
export interface Transport {
  /** Which local peer this transport talks to. Logged; never branched on by the worker. */
  readonly peer: 'walletshield' | 'kpclientd';
  /** Carry one request to the exit and resolve with its reply, or reject with a TransportError. */
  request(req: ExitRequest, opts?: RequestOptions): Promise<ExitResponse>;
  /** Diagnose reachability without failing: always resolves. */
  probe(opts?: ProbeOptions): Promise<ProbeResult>;
  /** Abort in-flight requests (code `closed`) and release peer state. Idempotent. */
  close(): Promise<void>;
}
