// WalletShieldTransport: the minimal path. WalletShield is the ZKNet client's
// local HTTP listener; it serializes the request it receives, sends it
// through the mixnet to the `http_proxy` exit, and answers with the exit's
// relay of the upstream reply.
//
// The reply contract (WalletShield + exit, shared with this transport):
// - a reply WalletShield or the exit *originates* carries `ZKNet-Error:
//   <reason>` and a JSON body `{"error":{"reason","message"}}`; its presence,
//   never the status alone, marks a transport failure;
// - any other reply is the upstream's: its status, allowlisted headers and
//   body, relayed as-is;
// - `GET /_/health` is answered locally, without the mixnet.
//
// A WalletShield that predates the contract answers neither marker nor
// health. It still works: its replies pass through as they are, and `probe`
// reports it as `legacy`.

import { TransportError, zknetError } from './errors.js';
import { classifyTarget, TARGET_SCHEMES, type TargetClass } from './targets.js';
import type {
  ExitRequest,
  ExitResponse,
  HeaderList,
  ProbeOptions,
  ProbeResult,
  RequestOptions,
  Transport,
  TransportErrorShape,
} from './types.js';

/** WalletShield's conventional listen address (the binary has no default; `-listen` is required). */
export const DEFAULT_WALLETSHIELD_URL = 'http://127.0.0.1:7070';

/** Upstream headers relayed to the caller. Keep in step with the exit's allowlist. */
export const UPSTREAM_HEADERS: readonly string[] = ['content-type', 'retry-after'];

/** The header that marks a reply WalletShield or the exit originated. */
export const ZKNET_ERROR_HEADER = 'zknet-error';

/** Local health path (answered by WalletShield itself) and exit probe path (one mix round trip). */
export const HEALTH_PATH = '/_/health';
export const EXIT_PROBE_PATH = '/_/probe/0';

/** Default per-request deadline. Above WalletShield's own `-timeout` (20 s) so its `mix-timeout` wins. */
export const DEFAULT_TIMEOUT_MS = 30_000;

/**
 * Chromium's `fetch` `targetAddressSpace` per class; other engines ignore it.
 * Not needed for the IP literals and `.local` names this transport accepts
 * (Chromium exempts those from mixed content itself); sent so a future
 * relaxation of the target rules cannot silently lose the exemption.
 */
const ADDRESS_SPACE: Readonly<Record<TargetClass, string>> = { loopback: 'loopback', lan: 'local' };

export interface WalletShieldOptions {
  /** Base URL of the WalletShield listener. Default DEFAULT_WALLETSHIELD_URL. */
  url?: string;
  /** Per-request deadline in ms. Default DEFAULT_TIMEOUT_MS. */
  timeoutMs?: number;
  /** Test seam; the ambient `fetch` otherwise. */
  fetch?: typeof fetch;
  /** Test seam; `globalThis.isSecureContext` otherwise. */
  isSecureContext?: boolean;
}

export class WalletShieldTransport implements Transport {
  readonly peer = 'walletshield' as const;
  readonly target: TargetClass;

  readonly #base: string;
  readonly #timeoutMs: number;
  readonly #fetch: typeof fetch;
  readonly #secure: boolean;
  readonly #protocol: string;
  #closed = new AbortController();

  /** Throws a TransportError (`unsupported` / `TargetRefused`) for a remote or malformed URL. */
  constructor(opts: WalletShieldOptions = {}) {
    const raw = opts.url ?? DEFAULT_WALLETSHIELD_URL;
    let url: URL;
    try {
      url = new URL(raw);
    } catch {
      throw refused('the WalletShield URL is not a URL');
    }
    if (!(TARGET_SCHEMES.walletshield as readonly string[]).includes(url.protocol)) {
      throw refused(`WalletShield URL scheme must be one of ${TARGET_SCHEMES.walletshield.join(' ')}`);
    }
    if (url.username || url.password || url.search || url.hash) {
      throw refused('the WalletShield URL must not carry credentials, a query or a fragment');
    }
    const target = classifyTarget(url);
    if (!target) throw refused('the WalletShield URL is not a loopback or local-network address');
    this.target = target;
    this.#protocol = url.protocol;
    this.#base = url.href.replace(/\/+$/, '');
    const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    if (!(Number.isFinite(timeoutMs) && timeoutMs > 0)) throw refused('timeoutMs must be a positive number');
    this.#timeoutMs = timeoutMs;
    this.#fetch = opts.fetch ?? globalThis.fetch.bind(globalThis);
    this.#secure = opts.isSecureContext ?? globalThis.isSecureContext === true;
  }

  /** True when the browser is expected to block this target as mixed content (an `http:` LAN peer from a secure context). */
  get mixedContentPredicted(): boolean {
    return this.#secure && this.#protocol === 'http:' && this.target === 'lan';
  }

  async request(req: ExitRequest, opts: RequestOptions = {}): Promise<ExitResponse> {
    const network = req.network.replace(/^\/+/, '');
    return this.#exchange(`/${network}`, {
      method: 'POST',
      headers: { 'content-type': req.contentType },
      // A copy over exactly the request bytes: `fetch` reads a view's whole buffer otherwise.
      body: req.body.slice(),
    }, opts.signal, async (res) => {
      const body = new Uint8Array(await res.arrayBuffer());
      const headers: HeaderList = [];
      res.headers.forEach((value, name) => {
        if (UPSTREAM_HEADERS.includes(name.toLowerCase())) headers.push([name.toLowerCase(), value]);
      });
      return { status: res.status, headers, body };
    });
  }

  async probe(opts: ProbeOptions = {}): Promise<ProbeResult> {
    const started = Date.now();
    const done = (r: Omit<ProbeResult, 'ms'>): ProbeResult => ({ ...r, ms: Date.now() - started });

    // Peer tier: WalletShield answers its health path locally.
    let peerVersion: string | undefined;
    let legacy: boolean;
    try {
      peerVersion = await this.#exchange(HEALTH_PATH, { method: 'GET' }, opts.signal, async (res) => {
        const json = (await res.json().catch(() => undefined)) as { version?: unknown } | undefined;
        return res.status === 200 && typeof json?.version === 'string' ? json.version : undefined;
      });
      legacy = peerVersion === undefined;
    } catch (e) {
      const error = shape(e);
      if (error.name === 'PeerUnreachable' && (await this.#answersOpaquely(opts.signal))) {
        // Something answers, but its replies are unreadable from here.
        return done({ reach: 'peer', error: { code: 'permission-denied', name: 'CorsRejected', message: 'WalletShield answered, but its reply is not readable from this origin (CORS)' } });
      }
      // A marked failure on the health path: a WalletShield that forwarded it instead of answering.
      if (MARKED.has(error.name)) return done({ reach: 'peer', legacy: true, error });
      return done({ reach: 'none', error });
    }

    if ((opts.depth ?? 'peer') === 'peer') return done({ reach: 'peer', peerVersion, legacy });

    // Exit tier: one probe through the mixnet; the exit answers its probe path itself.
    try {
      await this.#exchange(EXIT_PROBE_PATH, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '' }, opts.signal, async (res) => {
        await res.arrayBuffer();
      });
      return done({ reach: 'exit', peerVersion, legacy });
    } catch (e) {
      const error = shape(e);
      // The exit answered unless the failure happened before it could.
      if (MARKED.has(error.name) && error.name !== 'MixTimeout' && error.name !== 'TooLarge') return done({ reach: 'exit', peerVersion, legacy });
      return done({ reach: 'peer', peerVersion, legacy, error });
    }
  }

  async close(): Promise<void> {
    this.#closed.abort();
    this.#closed = new AbortController();
  }

  /**
   * One HTTP exchange with WalletShield under the caller's signal, the
   * deadline and `close()`. A `ZKNet-Error` reply rejects; `read` consumes
   * every other reply.
   */
  async #exchange<T>(path: string, init: RequestInit, signal: AbortSignal | undefined, read: (res: Response) => Promise<T>): Promise<T> {
    if (signal?.aborted) throw aborted();
    const deadline = AbortSignal.timeout(this.#timeoutMs);
    const closed = this.#closed.signal;
    const combined = AbortSignal.any([deadline, closed, ...(signal ? [signal] : [])]);
    const why = (): TransportError | undefined =>
      signal?.aborted
        ? aborted()
        : closed.aborted
          ? new TransportError('closed', 'Closed', 'the transport was closed')
          : deadline.aborted
            ? new TransportError('timeout', 'Deadline', `no reply from WalletShield within ${this.#timeoutMs} ms`)
            : undefined;
    try {
      const res = await this.#fetch(`${this.#base}${path}`, {
        ...init,
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
        signal: combined,
        targetAddressSpace: ADDRESS_SPACE[this.target],
      } as RequestInit);
      const reason = res.headers.get(ZKNET_ERROR_HEADER);
      if (reason !== null) throw zknetError(reason.trim(), await errorMessage(res));
      return await read(res);
    } catch (e) {
      if (e instanceof TransportError) throw e;
      throw why() ?? this.#networkFailure(e);
    }
  }

  /**
   * Name a `fetch` that failed before a readable reply. From inside the
   * harness's frame the browser shows one `TypeError` for every cause — the
   * peer not running, Local Network Access not delegated or not granted —
   * and `permissions.query` there reads `denied` even when access works, so
   * those stay one name. Only a mixed-content block is predictable.
   */
  #networkFailure(cause: unknown): TransportError {
    if (this.mixedContentPredicted) {
      return new TransportError('unsupported', 'MixedContentBlocked', 'the browser blocks an http: local-network peer from a secure context (mixed content)', { cause });
    }
    return new TransportError(
      'network-error',
      'PeerUnreachable',
      'WalletShield did not answer: it is not running at the configured address, or the browser blocked local network access (not delegated by the host, or not granted)',
      { cause },
    );
  }

  /** Whether anything answers the health path when CORS is taken out of the picture. */
  async #answersOpaquely(signal: AbortSignal | undefined): Promise<boolean> {
    try {
      await this.#fetch(`${this.#base}${HEALTH_PATH}`, {
        method: 'GET',
        mode: 'no-cors',
        credentials: 'omit',
        cache: 'no-store',
        signal: AbortSignal.any([AbortSignal.timeout(this.#timeoutMs), this.#closed.signal, ...(signal ? [signal] : [])]),
        targetAddressSpace: ADDRESS_SPACE[this.target],
      } as RequestInit);
      return true;
    } catch {
      return false;
    }
  }
}

/** Failures marked by a `ZKNet-Error` reply, i.e. the peer answered. */
const MARKED: ReadonlySet<string> = new Set(['MixTimeout', 'ExitRefused', 'UpstreamUnreachable', 'BadReply', 'TooLarge', 'UnexpectedReply']);

function refused(message: string): TransportError {
  return new TransportError('unsupported', 'TargetRefused', message);
}

function aborted(): TransportError {
  return new TransportError('cancelled', 'AbortError', 'the request was aborted');
}

function shape(e: unknown): TransportErrorShape {
  if (e instanceof TransportError) return e.toJSON();
  return { code: 'internal-error', name: 'UnexpectedReply', message: e instanceof Error ? e.message : String(e) };
}

/** The diagnostic message of a `ZKNet-Error` reply's JSON body, if it has one. */
async function errorMessage(res: Response): Promise<string | undefined> {
  try {
    const json = (await res.json()) as { error?: { message?: unknown } };
    return typeof json?.error?.message === 'string' ? json.error.message : undefined;
  } catch {
    return undefined;
  }
}
