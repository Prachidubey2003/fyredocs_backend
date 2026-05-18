/**
 * Wire-format types for the Fyredocs API.
 *
 * These mirror docs/developer/swagger/openapi.yaml. v0 is
 * hand-maintained to avoid pulling an OpenAPI codegen toolchain
 * into the package install path; the planned follow-up is to
 * generate this file via `openapi-typescript openapi.yaml`. The
 * shapes here are the same ones the frontend's lib/* modules
 * consume, so a drift between this file and the spec surfaces
 * quickly during dogfooding.
 */

// ---------------------------------------------------------------------------
// Standard response envelope returned by every Fyredocs HTTP endpoint
// (fyredocs/shared/response). The client unwraps `data` so callers
// see the inner type directly.
// ---------------------------------------------------------------------------

export interface ApiResponse<T> {
  success: boolean;
  message?: string;
  data: T;
  error?: { code: string; details?: string };
}

// ---------------------------------------------------------------------------
// API keys (auth-service)
// ---------------------------------------------------------------------------

export type ApiKeyEnvironment = 'live' | 'test';

export interface ApiKey {
  id: string;
  ownerUserId: string;
  name: string;
  environment: ApiKeyEnvironment;
  keyPrefix: string;
  scopes?: string[];
  createdAt: string;
  lastUsedAt?: string | null;
  revokedAt?: string | null;
}

export interface IssueApiKeyRequest {
  name: string;
  environment?: ApiKeyEnvironment;
  scopes?: string[];
}

export interface IssueApiKeyResponse {
  key: ApiKey;
  /** Shown EXACTLY ONCE. Callers must persist it immediately —
   *  the server can't recover it after this response. */
  plaintext: string;
}

// ---------------------------------------------------------------------------
// Billing (billing-service)
// ---------------------------------------------------------------------------

export interface Plan {
  code: string;
  name: string;
  description: string;
  /** USD cents per user per month; -1 = contact sales. */
  monthlyPriceCents: number;
  yearlyPriceCents?: number;
  perSeat: boolean;
  selfServe: boolean;
  /** Per-event-type caps keyed by `BillableEvent.eventType`; -1 = unlimited. */
  limits: Record<string, number>;
}

export interface Subscription {
  id: string;
  userId: string;
  planCode: string;
  status: 'active' | 'canceled' | 'past_due';
  seats: number;
  currentPeriodStart: string;
  currentPeriodEnd: string;
  stripeSubscriptionId?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface UsageRollupRow {
  eventType: string;
  unit: string;
  totalQuantity: number;
  eventCount: number;
}

export interface UsageRollup {
  userId: string;
  period: string;
  items: UsageRollupRow[];
}

export interface BillingMeResponse {
  plan: Plan;
  subscription?: Subscription | null;
  /** Omitted when analytics-service was unreachable at fetch time. */
  usage?: UsageRollup | null;
}

export interface SubscribeRequest {
  planCode: string;
  seats?: number;
}

// ---------------------------------------------------------------------------
// Editor (editor-service)
// ---------------------------------------------------------------------------

export type EditorOp =
  | { type: 'page.rotate'; page: number; rotation: 0 | 90 | 180 | 270 }
  | { type: 'page.delete'; page: number }
  | { type: 'page.insert'; afterPage: number }
  | {
      type: 'annotation.add';
      page: number;
      kind?:
        | 'highlight'
        | 'underline'
        | 'strikeout'
        | 'squiggly'
        | 'square'
        | 'sticky';
      rect: [number, number, number, number];
      color?: [number, number, number];
      contents?: string;
    }
  | {
      type: 'annotation.add';
      page: number;
      kind: 'freehand';
      strokes: number[][];
      color?: [number, number, number];
      contents?: string;
    }
  | {
      type: 'annotation.add';
      page: number;
      kind: 'callout';
      rect: [number, number, number, number];
      anchor: [number, number];
      color?: [number, number, number];
      contents?: string;
    }
  | { type: 'text.replace'; page: number; find: string; replace: string }
  | { type: 'redact.apply'; page: number; rect: [number, number, number, number] };

export interface EditRequest {
  ops: EditorOp[];
  message?: string;
}

export interface EditorDocument {
  id: string;
  ownerUserId: string;
  title: string;
  currentRevId: string | null;
  sizeBytes: number;
  pageCount: number;
  status: 'ready' | 'locked' | 'quarantined';
  createdAt: string;
  updatedAt: string;
}

export interface EditorRevision {
  id: string;
  documentId: string;
  parentRevId?: string | null;
  authorUserId: string;
  message?: string;
  createdAt: string;
}

// ---------------------------------------------------------------------------
// Usage rollup (analytics-service /v1/usage/me — re-exposed shape)
// ---------------------------------------------------------------------------

export interface UsageMeResponse {
  userId: string;
  period: string;
  items: UsageRollupRow[];
}
