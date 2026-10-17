// window.e2e, the host page's test API. Shared by the page and the specs.

export interface ErrorInfo {
  name: string;
  message: string;
  /** The Anon-RPC SPEC v0.3.2 §12 code, when the harness forwards one. */
  code?: string;
}

export interface BootRequest {
  id: string;
  /** WorkerSpecifier address. */
  address: string;
  /** JSON-RPC URL the page reads the specifier through (preExisting.rpcProvider). */
  rpcUrl: string;
  /** WorkerInit.config, the worker's own schema. Omitted: no config at all. */
  config?: unknown;
  /** WorkerInit.network (PROPOSED). */
  network?: { policy?: { allow?: string[] } };
  readyTimeoutMs: number;
}

export interface BootResult {
  ok: boolean;
  readyMs: number;
  error?: ErrorInfo;
  /** Attributes of the harness frame hosting the worker. */
  sandbox: string | null;
  allow: string | null;
}

export interface FetchRequest {
  id: string;
  url: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  timeoutMs: number;
}

export interface FetchResult {
  ok: boolean;
  ms: number;
  status?: number;
  headers?: [string, string][];
  bodyText?: string;
  error?: ErrorInfo;
}

export interface LogLine {
  level: string;
  args: unknown[];
}

export interface E2EPageApi {
  /** Which harness build the page bundled, e.g. "npm:@anon-rpc/browser-harness@0.3.3". */
  harness: string;
  boot(req: BootRequest): Promise<BootResult>;
  fetch(req: FetchRequest): Promise<FetchResult>;
  /** Drain up to `max` worker log entries, waiting at most `waitMs` for each. */
  logs(id: string, max: number, waitMs: number): Promise<LogLine[]>;
  close(id: string): void;
}

declare global {
  interface Window {
    e2e: E2EPageApi;
  }
}
