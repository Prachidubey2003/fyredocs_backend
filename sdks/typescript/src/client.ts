import type {
  ApiKey,
  ApiResponse,
  BillingMeResponse,
  EditRequest,
  EditorDocument,
  EditorRevision,
  IssueApiKeyRequest,
  IssueApiKeyResponse,
  Plan,
  SubscribeRequest,
  Subscription,
  UsageMeResponse,
} from './types.js';

/**
 * FyredocsError is the single error type the SDK throws. Wrapping
 * `Error` rather than using bare `Error` lets callers do a
 * structural check (`err instanceof FyredocsError`) and access the
 * status + code without parsing the message.
 */
export class FyredocsError extends Error {
  public readonly status: number;
  public readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'FyredocsError';
    this.status = status;
    this.code = code;
  }
}

/** Options accepted by the FyredocsClient constructor. */
export interface FyredocsClientOptions {
  /** API key (e.g., `fyr_live_…`) for server-to-server calls.
   *  Leave undefined and pass `useCookies: true` for browser
   *  use with cookie-based auth. */
  apiKey?: string;
  /** Base URL of the Fyredocs API. Defaults to the production
   *  origin; override for self-hosted or test environments. */
  baseUrl?: string;
  /** Send cookies on requests (browser only). Use this when the
   *  SDK runs in a context where the user is already signed in
   *  via the standard JWT-cookie flow. */
  useCookies?: boolean;
  /** Custom fetch impl — injected for tests. Defaults to global `fetch`. */
  fetch?: typeof fetch;
  /** Per-request timeout in milliseconds. Default 30s. */
  timeoutMs?: number;
}

const DEFAULT_BASE_URL = 'https://api.fyredocs.com';
const DEFAULT_TIMEOUT_MS = 30_000;

/**
 * FyredocsClient is the entry point of the SDK. Construct one
 * per credential — instances are cheap and stateless beyond the
 * stored API key + base URL.
 *
 *   const client = new FyredocsClient({ apiKey: process.env.FYREDOCS_KEY });
 *   const keys = await client.apiKeys.list();
 *   const me = await client.billing.me();
 */
export class FyredocsClient {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly useCookies: boolean;
  private readonly fetchImpl: typeof fetch;
  private readonly timeoutMs: number;

  /** Namespace: `/auth/api-keys/*` */
  public readonly apiKeys: ApiKeysApi;
  /** Namespace: `/api/billing/v1/billing/*` */
  public readonly billing: BillingApi;
  /** Namespace: `/v1/usage/*` */
  public readonly usage: UsageApi;
  /** Namespace: `/api/editor/v1/documents/*` */
  public readonly documents: DocumentsApi;

  constructor(opts: FyredocsClientOptions = {}) {
    this.baseUrl = trimTrailingSlash(opts.baseUrl ?? DEFAULT_BASE_URL);
    this.apiKey = opts.apiKey;
    this.useCookies = opts.useCookies ?? false;
    // Bind fetch so it doesn't lose its `this` when stored as a class field.
    this.fetchImpl = (opts.fetch ?? globalThis.fetch).bind(globalThis);
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

    this.apiKeys = new ApiKeysApi(this);
    this.billing = new BillingApi(this);
    this.usage = new UsageApi(this);
    this.documents = new DocumentsApi(this);
  }

  /**
   * Internal request helper. Public so the namespace classes can
   * call it without an indirection layer, but not part of the
   * documented surface — direct callers should use the namespace
   * methods instead.
   */
  public async request<T>(
    path: string,
    options: { method?: string; body?: unknown; query?: Record<string, string | undefined> } = {},
  ): Promise<T> {
    const url = new URL(this.baseUrl + path);
    if (options.query) {
      for (const [k, v] of Object.entries(options.query)) {
        if (v !== undefined && v !== '') url.searchParams.set(k, v);
      }
    }

    const headers: Record<string, string> = { Accept: 'application/json' };
    if (this.apiKey) {
      headers.Authorization = `Bearer ${this.apiKey}`;
    }
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), this.timeoutMs);

    let response: Response;
    try {
      response = await this.fetchImpl(url.toString(), {
        method: options.method ?? 'GET',
        headers,
        body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
        credentials: this.useCookies ? 'include' : 'same-origin',
        signal: controller.signal,
      });
    } catch (err) {
      clearTimeout(timeout);
      if ((err as Error).name === 'AbortError') {
        throw new FyredocsError(0, 'TIMEOUT', `Request to ${path} timed out after ${this.timeoutMs}ms`);
      }
      throw new FyredocsError(0, 'NETWORK', (err as Error).message);
    }
    clearTimeout(timeout);

    if (response.status === 204) {
      return undefined as T;
    }

    const text = await response.text();
    let envelope: Partial<ApiResponse<T>> | null = null;
    try {
      envelope = text ? (JSON.parse(text) as ApiResponse<T>) : null;
    } catch {
      // Non-JSON response (rare; gateway error pages). Surface
      // verbatim — better than swallowing the diagnostic.
    }

    if (!response.ok) {
      const code = envelope?.error?.code ?? `HTTP_${response.status}`;
      const detail = envelope?.error?.details ?? envelope?.message ?? text ?? response.statusText;
      throw new FyredocsError(response.status, code, detail || 'Request failed');
    }
    // Some endpoints return non-enveloped JSON (file blobs etc.);
    // when we don't see {success, data}, surface the raw body.
    if (envelope && typeof envelope === 'object' && 'success' in envelope && envelope.success) {
      return envelope.data as T;
    }
    return (envelope as T) ?? (undefined as T);
  }
}

class ApiKeysApi {
  constructor(private readonly client: FyredocsClient) {}

  list(opts: { revoked?: boolean } = {}): Promise<ApiKey[]> {
    return this.client.request<ApiKey[] | null>('/auth/api-keys', {
      method: 'GET',
      query: opts.revoked === undefined ? {} : { revoked: opts.revoked ? 'true' : 'false' },
    }).then((rows) => rows ?? []);
  }

  issue(input: IssueApiKeyRequest): Promise<IssueApiKeyResponse> {
    return this.client.request<IssueApiKeyResponse>('/auth/api-keys', {
      method: 'POST',
      body: input,
    });
  }

  revoke(id: string): Promise<void> {
    return this.client.request<void>(`/auth/api-keys/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
    });
  }
}

class BillingApi {
  constructor(private readonly client: FyredocsClient) {}

  plans(): Promise<Plan[]> {
    return this.client.request<{ plans: Plan[] }>('/api/billing/v1/billing/plans', {
      method: 'GET',
    }).then((d) => d.plans ?? []);
  }

  me(): Promise<BillingMeResponse> {
    return this.client.request<BillingMeResponse>('/api/billing/v1/billing/me', {
      method: 'GET',
    });
  }

  subscribe(input: SubscribeRequest): Promise<Subscription> {
    return this.client.request<Subscription>('/api/billing/v1/billing/me/subscribe', {
      method: 'POST',
      body: input,
    });
  }
}

class UsageApi {
  constructor(private readonly client: FyredocsClient) {}

  /** Calling user's usage rollup. `period` defaults to the current
   *  UTC month server-side; pass YYYY-MM to query a specific one. */
  me(opts: { period?: string } = {}): Promise<UsageMeResponse> {
    return this.client.request<UsageMeResponse>('/v1/usage/me', {
      method: 'GET',
      query: opts.period ? { period: opts.period } : {},
    });
  }
}

class DocumentsApi {
  constructor(private readonly client: FyredocsClient) {}

  /** Fetch a document's metadata by ID. */
  get(id: string): Promise<EditorDocument> {
    return this.client.request<EditorDocument>(
      `/api/editor/v1/documents/${encodeURIComponent(id)}`,
      { method: 'GET' },
    );
  }

  /** Apply a batch of edit ops; returns the new Revision. */
  edit(id: string, request: EditRequest): Promise<EditorRevision> {
    return this.client.request<EditorRevision>(
      `/api/editor/v1/documents/${encodeURIComponent(id)}/edit`,
      { method: 'POST', body: request },
    );
  }
}

function trimTrailingSlash(s: string): string {
  return s.endsWith('/') ? s.slice(0, -1) : s;
}
