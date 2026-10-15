// A stub WalletShield for tests: a real local HTTP listener that speaks the
// reply contract (see ../lib/walletshield.ts), with no mixnet behind it.
//
// Each POST is answered by `upstream(network, body)`; tests swap it per case.

import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';

export interface StubReply {
  status: number;
  headers?: Record<string, string>;
  body: string | Uint8Array;
}

export interface StubRequest {
  method: string;
  path: string;
  headers: IncomingMessage['headers'];
  body: Uint8Array;
}

export interface StubWalletShield {
  url: string;
  /** Every request received, in order. */
  requests: StubRequest[];
  /** Answers POSTs other than the exit probe. Default: a JSON-RPC result for `eth_blockNumber`. */
  upstream: (network: string, body: Uint8Array) => StubReply | Promise<StubReply> | 'hang';
  /** `health` false makes `/_/health` behave like a WalletShield that predates it (forwarded, answered 404). */
  health: boolean;
  close(): Promise<void>;
}

/** A `ZKNet-Error` reply, as WalletShield or the exit originates it. */
export function marked(reason: string, status: number, message = reason): StubReply {
  return {
    status,
    headers: { 'content-type': 'application/json', 'zknet-error': reason },
    body: JSON.stringify({ error: { reason, message } }),
  };
}

export async function startStubWalletShield(version = 'stub-0.0.0'): Promise<StubWalletShield> {
  const stub: StubWalletShield = {
    url: '',
    requests: [],
    health: true,
    upstream: () => ({ status: 200, headers: { 'content-type': 'application/json' }, body: '{"jsonrpc":"2.0","id":1,"result":"0x10"}' }),
    close: async () => {},
  };
  const open = new Set<ServerResponse>();

  const server: Server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    const path = req.url ?? '/';
    stub.requests.push({ method: req.method ?? '', path, headers: req.headers, body: new Uint8Array(Buffer.concat(chunks)) });

    res.setHeader('access-control-allow-origin', '*');
    res.setHeader('access-control-expose-headers', 'ZKNet-Error');
    let reply: StubReply | 'hang';
    if (req.method === 'GET' && path === '/_/health') {
      reply = stub.health
        ? { status: 200, headers: { 'content-type': 'application/json' }, body: JSON.stringify({ version }) }
        : { status: 404, body: 'custom 404\n' };
    } else if (path.startsWith('/_/probe')) {
      reply = { status: 200, body: '' };
    } else {
      reply = await stub.upstream(path.slice(1), new Uint8Array(Buffer.concat(chunks)));
    }
    if (reply === 'hang') {
      open.add(res);
      return;
    }
    res.writeHead(reply.status, reply.headers ?? {});
    res.end(reply.body);
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  stub.url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  stub.close = () =>
    new Promise<void>((resolve) => {
      for (const res of open) res.destroy();
      server.closeAllConnections();
      server.close(() => resolve());
    });
  return stub;
}
