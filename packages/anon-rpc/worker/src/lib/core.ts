// The worker: serve `POST /ethereum-rpc` (SPEC §3.2) by handing the JSON-RPC
// body to a Transport for the configured exit network, and relay the
// upstream reply as the fetch response. Everything else is refused with a
// code. Failures carry a SPEC §12 `code` and a stable `name` (see README).

import { TransportError, WalletShieldTransport, type ProbeResult, type Transport } from '@zknet/anon-rpc-transport';
import { ConfigError, resolveConfig, type ResolvedConfig } from './config.js';
import { createLogger, redact, type WorkerLogger } from './log.js';
import type { AnonFetchResponse, AnonRequestInit, AnonRpcWorkerApi, ByteBody, FetchCall } from './spec-types.js';

/** The special URL this worker serves (SPEC §3.2). */
export const ETHEREUM_RPC_PATH = '/ethereum-rpc';

/** Codes passed to `signalFailed` (permanent: this bundle cannot run here). */
export const FAILED_CODES = { badConfig: 'bad-config', unsupportedPlatform: 'unsupported-platform' } as const;

/** Least time between two diagnostic probes triggered by failing calls. */
export const PROBE_COOLDOWN_MS = 10_000;

export interface WorkerDeps {
  /** Bundle version, logged at boot. */
  version: string;
  /** Builds the transport; WalletShieldTransport by default. Test seam. */
  createTransport?: (config: ResolvedConfig) => Transport;
  now?: () => number;
}

/** A refusal or failure the host sees: `{ name, message, code }` across the harness. */
export class WorkerCallError extends Error {
  override readonly name: string;
  readonly code: string;
  constructor(code: string, name: string, message: string) {
    super(message);
    this.code = code;
    this.name = name;
  }
}

export async function runWorker(api: AnonRpcWorkerApi, deps: WorkerDeps): Promise<void> {
  const now = deps.now ?? Date.now;
  const log = createLogger(api.log);
  log.info('boot.start', { version: deps.version });

  const fail = (code: string, message: string): void => {
    log.error('worker.failed', { code, reason: message });
    api.signalFailed({ code, message });
  };

  if (typeof fetch !== 'function' || typeof AbortSignal?.any !== 'function' || typeof AbortSignal.timeout !== 'function') {
    fail(FAILED_CODES.unsupportedPlatform, 'this worker needs fetch, AbortSignal.any and AbortSignal.timeout');
    return;
  }

  let config: ResolvedConfig;
  let transport: Transport;
  try {
    config = resolveConfig(api.config);
    log.setLevel(config.logLevel);
    transport = (deps.createTransport ?? defaultTransport)(config);
  } catch (e) {
    if (e instanceof ConfigError || (e instanceof TransportError && e.name === 'TargetRefused')) {
      fail(FAILED_CODES.badConfig, e.message);
      return;
    }
    throw e;
  }
  log.info('boot.config', {
    peer: transport.peer,
    target: transport instanceof WalletShieldTransport ? transport.target : undefined,
    network: config.network,
    timeoutMs: config.timeoutMs,
  });

  // A peer that is down now may be started later: readiness does not wait on
  // it, and failure would be final (SPEC §7). The probe only diagnoses.
  api.signalReady();
  log.info('ready');

  const diagnose = probeLogger(transport, log, now);
  void diagnose(true);

  for (let seq = 1; ; seq++) {
    let call;
    try {
      call = await api.acceptCall();
    } catch (e) {
      log.error('accept.failed', { reason: redact(describe(e)) });
      await transport.close();
      return;
    }
    if (call.kind !== 'fetch') {
      log.debug('call.ignored', { kind: String((call as { kind?: unknown }).kind).slice(0, 32) });
      continue; // SPEC §8: ignore kinds this worker does not know
    }
    try {
      call.respond(handle(call, seq, config, transport, log, diagnose, now));
    } catch (e) {
      log.error('respond.failed', { seq, reason: redact(describe(e)) });
    }
  }
}

function defaultTransport(config: ResolvedConfig): Transport {
  return new WalletShieldTransport({ url: config.walletshield, timeoutMs: config.timeoutMs });
}

async function handle(
  call: FetchCall,
  seq: number,
  config: ResolvedConfig,
  transport: Transport,
  log: WorkerLogger,
  diagnose: (force?: boolean) => Promise<void>,
  now: () => number,
): Promise<AnonFetchResponse> {
  const started = now();
  const init: AnonRequestInit = call.requestInit ?? {};

  const refusal = refuse(call.url, init.method);
  if (refusal) {
    log.warn('call.refused', { seq, reason: refusal.reason });
    throw new WorkerCallError('unsupported', 'UnsupportedRequest', refusal.message);
  }

  try {
    const body = await readAll(init.body, init.signal);
    const res = await transport.request(
      { network: config.network, body, contentType: header(init, 'content-type') ?? 'application/json' },
      { signal: init.signal },
    );
    log.debug('call.done', { seq, ms: now() - started, outcome: 'ok', status: res.status, bytesOut: body.byteLength, bytesIn: res.body.byteLength });
    return { status: res.status, headers: res.headers, body: res.body, url: call.url };
  } catch (e) {
    const error = e instanceof TransportError ? e : init.signal?.aborted ? aborted() : internal(e);
    log.warn('call.failed', { seq, code: error.code, name: error.name, ms: now() - started });
    if (error.code === 'network-error' || error.code === 'permission-denied' || error.code === 'unsupported') void diagnose();
    throw error;
  }
}

/** Why a call is not served, or undefined when it is. */
function refuse(url: string, method = 'GET'): { reason: 'url' | 'method'; message: string } | undefined {
  let path: string;
  let extra: boolean;
  try {
    const parsed = new URL(url, 'http://worker.invalid');
    path = parsed.pathname;
    extra = parsed.search !== '' || parsed.hash !== '';
  } catch {
    return { reason: 'url', message: `this worker serves only ${ETHEREUM_RPC_PATH}` };
  }
  if (path !== ETHEREUM_RPC_PATH || extra) return { reason: 'url', message: `this worker serves only ${ETHEREUM_RPC_PATH}` };
  if (method.toUpperCase() !== 'POST') return { reason: 'method', message: `${ETHEREUM_RPC_PATH} takes POST only` };
  return undefined;
}

function header(init: AnonRequestInit, name: string): string | undefined {
  return init.headers?.find(([k]) => k.toLowerCase() === name)?.[1];
}

/** Probe at boot and after failing calls (one at a time; failure-triggered probes rate-limited), logging what it finds. */
function probeLogger(transport: Transport, log: WorkerLogger, now: () => number): (force?: boolean) => Promise<void> {
  let running = false;
  let last = -Infinity;
  let legacyLogged = false;
  return async (force = false) => {
    if (running || (!force && now() - last < PROBE_COOLDOWN_MS)) return;
    running = true;
    if (!force) last = now();
    try {
      const r: ProbeResult = await transport.probe();
      const fields = { reach: r.reach, ms: r.ms, version: r.peerVersion, code: r.error?.code, name: r.error?.name };
      if (r.error) log.warn('probe', fields);
      else log.info('probe', fields);
      if (r.legacy && !legacyLogged) {
        legacyLogged = true;
        log.warn('walletshield.legacy', { hint: 'this WalletShield predates /_/health and ZKNet-Error; failures may arrive as plain HTTP replies' });
      }
    } catch (e) {
      log.warn('probe', { reach: 'none', reason: redact(describe(e)) });
    } finally {
      running = false;
    }
  };
}

async function readAll(body: ByteBody | undefined, signal: AbortSignal | undefined): Promise<Uint8Array> {
  if (body === undefined) return new Uint8Array(0);
  if (body instanceof Uint8Array) return body;
  const chunks: Uint8Array[] = [];
  let total = 0;
  const reader = body.getReader();
  const onAbort = () => void reader.cancel().catch(() => {});
  signal?.addEventListener('abort', onAbort, { once: true });
  try {
    for (;;) {
      if (signal?.aborted) throw aborted();
      const { value, done } = await reader.read();
      if (done) break;
      chunks.push(value);
      total += value.byteLength;
    }
  } finally {
    signal?.removeEventListener('abort', onAbort);
  }
  if (signal?.aborted) throw aborted();
  const out = new Uint8Array(total);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.byteLength;
  }
  return out;
}

function aborted(): TransportError {
  return new TransportError('cancelled', 'AbortError', 'the request was aborted');
}

function internal(e: unknown): WorkerCallError {
  return new WorkerCallError('internal-error', 'InternalError', redact(describe(e)));
}

function describe(e: unknown): string {
  return e instanceof Error ? `${e.name}: ${e.message}` : String(e);
}
