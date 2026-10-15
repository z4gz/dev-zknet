# @zknet/anon-rpc-transport

This package is the seam between the ZKNet anon-rpc worker and the local ZKNet
peer that carries a request through the mixnet to the `http_proxy` exit. It is
named after the peer it talks to, not the protocol:

- **`WalletShieldTransport`** sends HTTP to WalletShield's listener. This is
  the path in use today.
- **`KpclientdTransport`** is a stub signature. The next path is a wasm thin
  client speaking WebSocket and the Katzenpost thin protocol to a local
  `kpclientd`.

```ts
interface Transport {
  readonly peer: 'walletshield' | 'kpclientd';
  request(req: { network: string; body: Uint8Array; contentType: string }, opts?: { signal?: AbortSignal }): Promise<{ status: number; headers: HeaderList; body: Uint8Array }>;
  probe(opts?: { signal?: AbortSignal; depth?: 'peer' | 'exit' }): Promise<ProbeResult>; // never rejects
  close(): Promise<void>;
}
```

`request` is cut at the exit's contract: a network key (`ethereum`,
`ethereum/sepolia`) and a body go in, and the upstream's reply comes out.
Failures are `TransportError`s with a SPEC §12 `code` and a stable `name`. The
worker README lists them.

## Where a peer may live — `src/lib/targets.ts`

Peers must be on loopback or the local network; remote peers are refused. The
rules are plain arrays: `TARGET_CIDRS`, `TARGET_HOSTNAMES` (`localhost`,
`.local`) and `TARGET_SCHEMES`. Edit the arrays, not the code.

## WalletShield's reply contract

- **Marked failures.** A reply that WalletShield or the exit originates
  carries `ZKNet-Error: <reason>` and a JSON body `{"error":{"reason","message"}}`.
  The transport branches on this header and never on the status alone. The
  browser can read the header only if WalletShield lists it in
  `Access-Control-Expose-Headers`.
- **Upstream replies.** Every other reply is the upstream's. The transport
  relays its status and `UPSTREAM_HEADERS` (`content-type`, `retry-after`).
  The browser hides `retry-after` unless WalletShield exposes it too.
- **Health.** `GET /_/health` is answered locally with
  `{"version": "..."}`. This is the probe's `peer` tier. The `exit` tier is
  `POST /_/probe/0`, which costs one mixnet round trip.
- **Older WalletShield.** A WalletShield that predates this contract still
  works: its replies pass through as they are, and `probe` reports `legacy`.

## Testing

`@zknet/anon-rpc-transport/testing` exports `startStubWalletShield()`. It is
a real local HTTP listener that speaks the contract with no mixnet behind it.
Both packages' tests use it.

```sh
pnpm nx test @zknet/anon-rpc-transport
```
