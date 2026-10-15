// Live tier, for a human: the same host page and npm-pinned harness as the
// pinned tier, but the worker talks to a REAL WalletShield, which carries each
// call over a real ZKNet mixnet to an exit and its upstream RPC. Nothing here
// starts a mixnet or a WalletShield; start them first (see README § Live tier).
//
//   WALLETSHIELD_URL  the WalletShield listener (default http://127.0.0.1:7070)
//   LIVE_NETWORK      the exit [Networks] key /ethereum-rpc maps to (default "ethereum")
//   LIVE_TIMEOUT_MS   the worker's per-call deadline (default 60000)

import { expect, test, type Page } from '@playwright/test';
import type { FetchResult, LogLine } from '../page/api.js';
import { startChain, type Chain } from '../src/chain.js';
import { bundlePage, pinnedHarness, servePage, type PageServer } from '../src/page.js';
import { WORKER_BUNDLE } from '../src/paths.js';

const WALLETSHIELD = process.env.WALLETSHIELD_URL ?? 'http://127.0.0.1:7070';
const NETWORK = process.env.LIVE_NETWORK ?? 'ethereum';
const TIMEOUT_MS = Number(process.env.LIVE_TIMEOUT_MS ?? 60_000);

const CALLS = [
  { method: 'eth_chainId', params: [] },
  { method: 'eth_blockNumber', params: [] },
  { method: 'eth_getBalance', params: ['0x0000000000000000000000000000000000000000', 'latest'] },
];

let chain: Chain;
let site: PageServer;
let address: string;

const say = (line = '') => console.log(line);

test.beforeAll(async () => {
  say(`live tier: worker → WalletShield ${WALLETSHIELD} → mixnet → exit [Networks] "${NETWORK}"`);
  // WalletShield answers /_/health itself, without a mixnet round trip.
  try {
    const res = await fetch(`${WALLETSHIELD}/_/health`, { signal: AbortSignal.timeout(5_000) });
    say(`WalletShield /_/health: HTTP ${res.status} ${(await res.text()).trim()}`);
  } catch (e) {
    throw new Error(
      `WalletShield is not answering at ${WALLETSHIELD} (${String(e)}). Start it, or set WALLETSHIELD_URL.`,
    );
  }
  chain = await startChain(); // a local anvil for the specifier only; the RPC under test is behind the exit
  address = await chain.deploy(WORKER_BUNDLE);
  site = await servePage(await bundlePage(pinnedHarness()));
  say(
    `host page ${site.origin}/ (loopback: no Local Network Access prompt), specifier ${address} on ${chain.rpcUrl}`,
  );
});

test.afterAll(async () => {
  await site?.close();
  await chain?.stop();
});

function rpc(page: Page, method: string, params: unknown[]): Promise<FetchResult> {
  const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method, params });
  return page.evaluate((req) => window.e2e.fetch(req), {
    id: 'w',
    url: '/ethereum-rpc',
    body,
    timeoutMs: TIMEOUT_MS + 5_000,
  });
}

test('JSON-RPC through a real WalletShield and mixnet', async ({ page }) => {
  await page.goto(`${site.origin}/`);
  await page.waitForFunction(() => window.e2e !== undefined);
  const booted = await page.evaluate((req) => window.e2e.boot(req), {
    id: 'w',
    address,
    rpcUrl: chain.rpcUrl,
    config: {
      walletshield: WALLETSHIELD,
      network: NETWORK,
      timeoutMs: TIMEOUT_MS,
      logLevel: 'debug',
    },
    readyTimeoutMs: 30_000,
  });
  expect(booted.error).toBeUndefined();
  say(`worker ready in ${Math.round(booted.readyMs)} ms`);

  const failures: string[] = [];
  for (const { method, params } of CALLS) {
    const r = await rpc(page, method, params);
    if (r.ok) {
      say(`${method}: HTTP ${r.status} in ${Math.round(r.ms)} ms → ${r.bodyText}`);
      let parsed: { result?: unknown; error?: unknown } = {};
      try {
        parsed = JSON.parse(r.bodyText ?? '');
      } catch {
        // reported below
      }
      if (r.status !== 200 || parsed.result === undefined)
        failures.push(`${method}: HTTP ${r.status} ${r.bodyText}`);
    } else {
      say(
        `${method}: FAILED in ${Math.round(r.ms)} ms → ${r.error?.code} / ${r.error?.name}: ${r.error?.message}`,
      );
      failures.push(`${method}: ${r.error?.code} / ${r.error?.name}`);
    }
  }

  const logs: LogLine[] = await page.evaluate(() => window.e2e.logs('w', 200, 500));
  say('\nworker log:');
  for (const l of logs)
    say(
      `  [${l.level}] ${l.args.map((a) => (typeof a === 'string' ? a : JSON.stringify(a))).join(' ')}`,
    );

  say(`
What to look at:
  - eth_chainId must be the chain of the exit's "${NETWORK}" upstream (0x1 mainnet, 0xaa36a7 sepolia).
  - WalletShield's log: one request per call above, POST /${NETWORK}, and its round-trip time.
  - The exit (http_proxy) log: the same number of requests, sent to the "${NETWORK}" upstream.
  - The worker log: a probe after boot whose reach is not "none", then call.done per call.
  - On failure, the name says where it broke: PeerUnreachable (WalletShield not answering),
    MixTimeout (no reply over the mixnet in time), ExitRefused (network key not served by the
    exit), UpstreamUnreachable (exit could not reach the RPC), BadReply / UnexpectedReply.`);
  expect(failures).toEqual([]);
});
