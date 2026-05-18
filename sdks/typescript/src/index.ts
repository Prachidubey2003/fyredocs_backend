/**
 * @fyredocs/sdk — Official TypeScript SDK for the Fyredocs API.
 *
 * ```ts
 * import { FyredocsClient } from '@fyredocs/sdk';
 *
 * const client = new FyredocsClient({ apiKey: process.env.FYREDOCS_KEY! });
 *
 * const keys = await client.apiKeys.list();
 * const me = await client.billing.me();
 * const usage = await client.usage.me({ period: '2026-05' });
 * ```
 *
 * See README.md for the full surface.
 */

export { FyredocsClient, FyredocsError } from './client.js';
export type { FyredocsClientOptions } from './client.js';

// Curated, hand-named types are the SDK's stable public surface.
// These names won't shift across patch releases.
export type {
  ApiKey,
  ApiKeyEnvironment,
  ApiResponse,
  BillingMeResponse,
  EditRequest,
  EditorDocument,
  EditorOp,
  EditorRevision,
  IssueApiKeyRequest,
  IssueApiKeyResponse,
  Plan,
  SubscribeRequest,
  Subscription,
  UsageMeResponse,
  UsageRollup,
  UsageRollupRow,
} from './types.js';

// OpenAPI-driven types — re-exported under the `openapi` namespace
// so callers can opt into the raw shape generated from
// docs/developer/swagger/openapi.yaml:
//
//   import type { openapi } from '@fyredocs/sdk';
//   type RawPlan = openapi.components['schemas']['Plan'];
//   type BillingMe = openapi.paths['/v1/billing/me']['get']['responses']['200']['content']['application/json'];
//
// Useful for callers that already work in OpenAPI shapes (e.g.,
// teams that generate clients in multiple languages from the
// same spec) and want type-level parity across them.
export type * as openapi from './generated.js';
