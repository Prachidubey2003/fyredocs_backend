import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { FyredocsClient, FyredocsError } from '../client.js';

// Helper that builds an `okJson` Response, mirroring the standard
// {success, data} envelope every Fyredocs endpoint returns.
function okJson<T>(data: T): Response {
  return new Response(JSON.stringify({ success: true, data }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

describe('FyredocsClient', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  let client: FyredocsClient;

  beforeEach(() => {
    fetchSpy = vi.fn();
    client = new FyredocsClient({
      apiKey: 'fyr_test_abc123_secret',
      baseUrl: 'https://api.example.com',
      fetch: fetchSpy as unknown as typeof fetch,
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe('apiKeys', () => {
    it('list() unwraps the envelope and falls back to [] on null data', async () => {
      fetchSpy.mockResolvedValueOnce(okJson(null));
      const got = await client.apiKeys.list();
      expect(got).toEqual([]);

      const [url, init] = fetchSpy.mock.calls[0];
      expect(String(url)).toBe('https://api.example.com/auth/api-keys');
      expect((init as RequestInit).method).toBe('GET');
      // Authorization header is set to Bearer + the API key
      expect((init as RequestInit).headers).toBeDefined();
      const headers = (init as RequestInit).headers as Record<string, string>;
      expect(headers.Authorization).toBe('Bearer fyr_test_abc123_secret');
    });

    it('list({ revoked: true }) appends the query param', async () => {
      fetchSpy.mockResolvedValueOnce(okJson([]));
      await client.apiKeys.list({ revoked: true });
      const [url] = fetchSpy.mock.calls[0];
      expect(String(url)).toContain('revoked=true');
    });

    it('issue() POSTs the body and returns plaintext-bearing response', async () => {
      const payload = {
        key: {
          id: 'k1',
          ownerUserId: 'u1',
          name: 'CI',
          environment: 'live' as const,
          keyPrefix: 'fyr_live_abc',
          createdAt: '2026-05-16T00:00:00Z',
        },
        plaintext: 'fyr_live_abc_secrettoken',
      };
      fetchSpy.mockResolvedValueOnce(
        new Response(JSON.stringify({ success: true, data: payload }), {
          status: 201,
          headers: { 'content-type': 'application/json' },
        }),
      );
      const got = await client.apiKeys.issue({ name: 'CI', environment: 'live' });
      expect(got.plaintext).toBe(payload.plaintext);

      const init = fetchSpy.mock.calls[0][1] as RequestInit;
      expect(init.method).toBe('POST');
      expect(JSON.parse(init.body as string)).toEqual({ name: 'CI', environment: 'live' });
    });

    it('revoke() URL-encodes the id', async () => {
      fetchSpy.mockResolvedValueOnce(new Response(null, { status: 204 }));
      await client.apiKeys.revoke('k 1/two');
      const [url] = fetchSpy.mock.calls[0];
      expect(String(url)).toContain('/auth/api-keys/k%201%2Ftwo/revoke');
    });
  });

  describe('billing', () => {
    it('plans() unwraps `data.plans`', async () => {
      fetchSpy.mockResolvedValueOnce(
        okJson({
          plans: [{ code: 'free', name: 'Free', description: 'x', monthlyPriceCents: 0, perSeat: false, selfServe: true, limits: {} }],
        }),
      );
      const got = await client.billing.plans();
      expect(got).toHaveLength(1);
      expect(got[0].code).toBe('free');
    });

    it('me() returns the BillingMeResponse', async () => {
      fetchSpy.mockResolvedValueOnce(
        okJson({
          plan: { code: 'pro', name: 'Pro', description: 'x', monthlyPriceCents: 1500, perSeat: false, selfServe: true, limits: {} },
          subscription: null,
        }),
      );
      const got = await client.billing.me();
      expect(got.plan.code).toBe('pro');
    });
  });

  describe('usage', () => {
    it('me() passes through the period query param', async () => {
      fetchSpy.mockResolvedValueOnce(
        okJson({ userId: 'u1', period: '2026-05', items: [] }),
      );
      await client.usage.me({ period: '2026-05' });
      const [url] = fetchSpy.mock.calls[0];
      expect(String(url)).toContain('/v1/usage/me');
      expect(String(url)).toContain('period=2026-05');
    });

    it('me() omits the query param when period is undefined', async () => {
      fetchSpy.mockResolvedValueOnce(
        okJson({ userId: 'u1', period: '2026-05', items: [] }),
      );
      await client.usage.me();
      const [url] = fetchSpy.mock.calls[0];
      expect(String(url)).not.toContain('period=');
    });
  });

  describe('documents', () => {
    it('edit() POSTs the request body', async () => {
      fetchSpy.mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            success: true,
            data: { id: 'r1', documentId: 'd1', authorUserId: 'u1', createdAt: '2026-05-16T00:00:00Z' },
          }),
          { status: 201, headers: { 'content-type': 'application/json' } },
        ),
      );
      const got = await client.documents.edit('d1', {
        ops: [{ type: 'page.rotate', page: 1, rotation: 90 }],
      });
      expect(got.id).toBe('r1');
      const init = fetchSpy.mock.calls[0][1] as RequestInit;
      const body = JSON.parse(init.body as string);
      expect(body.ops[0].type).toBe('page.rotate');
    });
  });

  describe('error handling', () => {
    it('throws FyredocsError with code + status on 4xx', async () => {
      fetchSpy.mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            success: false,
            error: { code: 'INVALID_PLAN', details: 'Unknown plan code' },
          }),
          { status: 400, headers: { 'content-type': 'application/json' } },
        ),
      );
      await expect(client.billing.subscribe({ planCode: 'fake' })).rejects.toMatchObject({
        name: 'FyredocsError',
        status: 400,
        code: 'INVALID_PLAN',
      });
    });

    it('synthesises HTTP_<status> code when envelope lacks error.code', async () => {
      fetchSpy.mockResolvedValueOnce(
        new Response('Bad Gateway', { status: 502 }),
      );
      await expect(client.billing.me()).rejects.toMatchObject({
        status: 502,
        code: 'HTTP_502',
      });
    });

    it('maps network failures to FyredocsError(code=NETWORK)', async () => {
      fetchSpy.mockRejectedValueOnce(new TypeError('connect ECONNREFUSED'));
      await expect(client.billing.me()).rejects.toMatchObject({
        name: 'FyredocsError',
        code: 'NETWORK',
      });
    });

    it('maps timeouts to FyredocsError(code=TIMEOUT)', async () => {
      // Tiny timeout + a fetch that never resolves on a clean
      // path forces the abort. The client maps AbortError to a
      // typed timeout failure.
      const slowClient = new FyredocsClient({
        apiKey: 'k',
        baseUrl: 'https://api.example.com',
        timeoutMs: 5,
        fetch: ((_url: string, init?: RequestInit) =>
          new Promise<Response>((_, reject) => {
            init?.signal?.addEventListener('abort', () => {
              const e = new Error('aborted');
              e.name = 'AbortError';
              reject(e);
            });
          })) as unknown as typeof fetch,
      });
      await expect(slowClient.billing.me()).rejects.toMatchObject({
        code: 'TIMEOUT',
      });
    });
  });

  describe('FyredocsError', () => {
    it('is throwable + instanceof-checkable', () => {
      const e = new FyredocsError(401, 'UNAUTHORIZED', 'Please log in');
      expect(e).toBeInstanceOf(Error);
      expect(e).toBeInstanceOf(FyredocsError);
      expect(e.status).toBe(401);
      expect(e.code).toBe('UNAUTHORIZED');
      expect(e.message).toBe('Please log in');
    });
  });

  describe('options', () => {
    it('useCookies sets credentials to include', async () => {
      const c = new FyredocsClient({
        baseUrl: 'https://api.example.com',
        useCookies: true,
        fetch: fetchSpy as unknown as typeof fetch,
      });
      fetchSpy.mockResolvedValueOnce(okJson([]));
      await c.apiKeys.list();
      const init = fetchSpy.mock.calls[0][1] as RequestInit;
      expect(init.credentials).toBe('include');
    });

    it('apiKey is omitted from headers when not provided', async () => {
      const c = new FyredocsClient({
        baseUrl: 'https://api.example.com',
        useCookies: true,
        fetch: fetchSpy as unknown as typeof fetch,
      });
      fetchSpy.mockResolvedValueOnce(okJson([]));
      await c.apiKeys.list();
      const headers = (fetchSpy.mock.calls[0][1] as RequestInit).headers as Record<string, string>;
      expect(headers.Authorization).toBeUndefined();
    });

    it('trims trailing slash from baseUrl', async () => {
      const c = new FyredocsClient({
        baseUrl: 'https://api.example.com/',
        apiKey: 'k',
        fetch: fetchSpy as unknown as typeof fetch,
      });
      fetchSpy.mockResolvedValueOnce(okJson([]));
      await c.apiKeys.list();
      const [url] = fetchSpy.mock.calls[0];
      // No double-slash before /auth/api-keys.
      expect(String(url)).toBe('https://api.example.com/auth/api-keys');
    });
  });
});
