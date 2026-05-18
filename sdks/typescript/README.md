# @fyredocs/sdk

Official TypeScript SDK for the [Fyredocs](https://fyredocs.com) API.

```bash
npm install @fyredocs/sdk
```

## Quick start

```ts
import { FyredocsClient } from '@fyredocs/sdk';

const client = new FyredocsClient({
  apiKey: process.env.FYREDOCS_KEY!, // `fyr_live_…` from /account/api-keys
});

// List your API keys
const keys = await client.apiKeys.list();

// Inspect your subscription + current-period usage
const me = await client.billing.me();
console.log(me.plan.name, me.usage?.items);

// Apply an edit to a document
const revision = await client.documents.edit('doc_abc123', {
  ops: [
    { type: 'page.rotate', page: 1, rotation: 90 },
    {
      type: 'annotation.add',
      page: 1,
      kind: 'highlight',
      rect: [100, 100, 300, 120],
    },
  ],
});
```

## Auth options

The SDK supports two auth flows:

- **API key** (server-to-server): pass `apiKey: 'fyr_live_…'`. The SDK sets `Authorization: Bearer …` on every request.
- **Browser cookies** (already-signed-in user): pass `useCookies: true` and omit `apiKey`. Requests use `credentials: 'include'`.

```ts
// Browser, calling Fyredocs from an authenticated SPA:
const client = new FyredocsClient({
  baseUrl: '/api', // same-origin proxy
  useCookies: true,
});
```

## Namespaces

| Namespace | Backed by | Methods |
|---|---|---|
| `apiKeys` | auth-service | `list({ revoked? })`, `issue({ name, environment?, scopes? })`, `revoke(id)` |
| `billing` | billing-service | `plans()`, `me()`, `subscribe({ planCode, seats? })` |
| `usage` | analytics-service | `me({ period? })` |
| `documents` | editor-service | `get(id)`, `edit(id, request)` |

Every method returns a typed promise; failures throw `FyredocsError` carrying `status` (HTTP), `code` (envelope error code or `HTTP_<status>`), and `message`.

```ts
import { FyredocsClient, FyredocsError } from '@fyredocs/sdk';

try {
  await client.billing.subscribe({ planCode: 'fake' });
} catch (err) {
  if (err instanceof FyredocsError && err.code === 'INVALID_PLAN') {
    // Distinguishable from a network failure or auth issue.
  }
  throw err;
}
```

## Configuration

```ts
new FyredocsClient({
  apiKey: 'fyr_live_…',
  baseUrl: 'https://api.fyredocs.com', // default
  useCookies: false,                    // default
  timeoutMs: 30_000,                    // default
  fetch: customFetch,                   // injection point for tests
});
```

## Versioning

The SDK follows semver. Breaking changes in the wire format land in a major bump; additive endpoints are minor; bug-fixes are patches. The exported type names match the OpenAPI spec at `docs/developer/swagger/openapi.yaml` and are stable.

## Types: curated public API + OpenAPI-driven raw types

Two layers ship together:

- **`src/types.ts`** — the SDK's stable, hand-curated public types (`Plan`, `Subscription`, `BillingMeResponse`, `EditorOp`, …). These are the names the README documents and patch releases won't rename them.
- **`src/generated.ts`** — auto-generated from [`docs/developer/swagger/openapi.yaml`](../../docs/developer/swagger/openapi.yaml) via [`openapi-typescript`](https://github.com/openapi-ts/openapi-typescript). Re-exported under the `openapi` namespace for callers who want raw OpenAPI shapes (typically teams that share types across languages).

```ts
import { FyredocsClient, type openapi } from '@fyredocs/sdk';

// Curated:
const client = new FyredocsClient({ apiKey: '...' });
const me = await client.billing.me(); // typed as BillingMeResponse

// Raw OpenAPI:
type RawPlan = openapi.components['schemas']['Plan'];
type BillingMeBody =
  openapi.paths['/v1/billing/me']['get']['responses']['200']['content']['application/json'];
```

### Regenerating types

When the OpenAPI spec changes, regenerate the OpenAPI-driven file with:

```bash
npm run generate:types     # writes src/generated.ts
```

CI runs `npm run check:types-fresh`, which regenerates into a tempfile and `diff`s — a failure means somebody changed the spec without re-running `generate:types`. The drift detector in `src/__tests__/generated.test.ts` additionally type-checks that the symbols the SDK's hand-curated types depend on still exist with the expected shape.

## License

Apache-2.0
