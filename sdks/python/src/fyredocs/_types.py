"""Wire-format types for the Fyredocs API.

These mirror ``docs/developer/swagger/openapi.yaml`` and stay in
lockstep with the TypeScript SDK's ``types.ts`` and the Go SDK's
``types.go``. Hand-maintained because the API surface is small
enough that an OpenAPI codegen pipeline would add more friction
than it solves for v0.

The dataclasses ride bare ``dict``/``list`` values for JSON
fields (``Plan.limits``, ``EditorOp`` payloads) — callers pass
dicts; the SDK calls ``json.dumps``/``json.loads``. We do NOT
auto-marshal nested dataclasses on the request side because the
``EditorOp`` shape is intentionally open (the server adds new op
types in the API contract, and the SDK shouldn't have to be
re-released for every one).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional

# ---------------------------------------------------------------------------
# API keys (auth-service)
# ---------------------------------------------------------------------------

# `Literal["live", "test"]` would be more precise, but the
# server may introduce new environments and we don't want a
# blocking SDK release every time. Plain `str` with a constant
# pair for documentation is the same compromise the Go SDK
# makes (`APIKeyLive`/`APIKeyTest`).
APIKeyEnvironment = str
API_KEY_LIVE: APIKeyEnvironment = "live"
API_KEY_TEST: APIKeyEnvironment = "test"


@dataclass
class APIKey:
    """Metadata for an issued API key. The plaintext secret only
    appears in ``IssueAPIKeyResponse.plaintext`` — call sites
    must persist it immediately; the server can't recover it."""

    id: str
    name: str
    environment: APIKeyEnvironment
    key_prefix: str
    created_at: str
    owner_user_id: Optional[str] = None
    scopes: list[str] = field(default_factory=list)
    last_used_at: Optional[str] = None
    revoked_at: Optional[str] = None


@dataclass
class IssueAPIKeyRequest:
    """Body for ``client.api_keys.issue``."""

    name: str
    environment: Optional[APIKeyEnvironment] = None
    scopes: Optional[list[str]] = None


@dataclass
class IssueAPIKeyResponse:
    """Returned by ``client.api_keys.issue``. ``plaintext`` is
    shown EXACTLY ONCE — display + persist it immediately."""

    key: APIKey
    plaintext: str


# ---------------------------------------------------------------------------
# Billing (billing-service)
# ---------------------------------------------------------------------------


@dataclass
class Plan:
    """A self-serve or sales-led pricing tier."""

    code: str
    name: str
    description: str
    monthly_price_cents: int  # -1 = contact sales
    per_seat: bool
    self_serve: bool
    limits: dict[str, int] = field(default_factory=dict)  # -1 = unlimited
    yearly_price_cents: Optional[int] = None


@dataclass
class Subscription:
    """The calling user's current billing subscription."""

    id: str
    user_id: str
    plan_code: str
    status: str  # "active" | "canceled" | "past_due"
    seats: int
    current_period_start: str
    current_period_end: str
    created_at: str
    updated_at: str
    stripe_subscription_id: Optional[str] = None


@dataclass
class UsageRollupRow:
    event_type: str
    unit: str
    total_quantity: float
    event_count: int


@dataclass
class UsageRollup:
    user_id: str
    period: str
    items: list[UsageRollupRow] = field(default_factory=list)


@dataclass
class BillingMeResponse:
    """Returned by ``client.billing.me``."""

    plan: Plan
    subscription: Optional[Subscription] = None
    usage: Optional[UsageRollup] = None


@dataclass
class SubscribeRequest:
    plan_code: str
    seats: Optional[int] = None


# ---------------------------------------------------------------------------
# Editor (editor-service) — op constants mirror the TS / Go SDKs.
# ---------------------------------------------------------------------------

OP_PAGE_ROTATE = "page.rotate"
OP_PAGE_DELETE = "page.delete"
OP_PAGE_INSERT = "page.insert"
OP_ANNOTATION_ADD = "annotation.add"
OP_TEXT_REPLACE = "text.replace"
OP_TEXT_INSERT = "text.insert"
OP_TEXT_DELETE = "text.delete"
OP_REDACT_APPLY = "redact.apply"
OP_TABLE_CELL_EDIT = "table.cell.edit"


@dataclass
class EditorDocument:
    """Document metadata."""

    id: str
    title: str
    owner_user_id: Optional[str] = None
    current_rev_id: Optional[str] = None
    storage_key: Optional[str] = None
    size_bytes: int = 0
    page_count: int = 0
    status: Optional[str] = None
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


@dataclass
class EditorRevision:
    """One entry in a document's revision history."""

    id: str
    document_id: str
    parent_rev_id: Optional[str] = None
    author_user_id: Optional[str] = None
    message: Optional[str] = None
    created_at: Optional[str] = None


# ---------------------------------------------------------------------------
# Usage (analytics-service)
# ---------------------------------------------------------------------------


@dataclass
class UsageMeResponse:
    """Returned by ``client.usage.me``."""

    user_id: str
    period: str
    items: list[UsageRollupRow] = field(default_factory=list)


# ---------------------------------------------------------------------------
# Helpers used by namespace modules to turn server JSON (camelCase)
# into dataclasses (snake_case). Kept here so every type owns its
# own decode logic in one place.
# ---------------------------------------------------------------------------


def _api_key_from_json(d: dict[str, Any]) -> APIKey:
    return APIKey(
        id=d["id"],
        name=d.get("name", ""),
        environment=d.get("environment", "live"),
        key_prefix=d.get("keyPrefix", ""),
        created_at=d.get("createdAt", ""),
        owner_user_id=d.get("ownerUserId"),
        scopes=list(d.get("scopes") or []),
        last_used_at=d.get("lastUsedAt"),
        revoked_at=d.get("revokedAt"),
    )


def _plan_from_json(d: dict[str, Any]) -> Plan:
    return Plan(
        code=d["code"],
        name=d.get("name", ""),
        description=d.get("description", ""),
        monthly_price_cents=int(d.get("monthlyPriceCents", 0)),
        per_seat=bool(d.get("perSeat", False)),
        self_serve=bool(d.get("selfServe", False)),
        limits=dict(d.get("limits") or {}),
        yearly_price_cents=d.get("yearlyPriceCents"),
    )


def _subscription_from_json(d: dict[str, Any]) -> Subscription:
    return Subscription(
        id=d["id"],
        user_id=d.get("userId", ""),
        plan_code=d.get("planCode", ""),
        status=d.get("status", ""),
        seats=int(d.get("seats", 0)),
        current_period_start=d.get("currentPeriodStart", ""),
        current_period_end=d.get("currentPeriodEnd", ""),
        created_at=d.get("createdAt", ""),
        updated_at=d.get("updatedAt", ""),
        stripe_subscription_id=d.get("stripeSubscriptionId"),
    )


def _usage_row_from_json(d: dict[str, Any]) -> UsageRollupRow:
    return UsageRollupRow(
        event_type=d.get("eventType", ""),
        unit=d.get("unit", ""),
        total_quantity=float(d.get("totalQuantity", 0)),
        event_count=int(d.get("eventCount", 0)),
    )


def _usage_rollup_from_json(d: dict[str, Any]) -> UsageRollup:
    return UsageRollup(
        user_id=d.get("userId", ""),
        period=d.get("period", ""),
        items=[_usage_row_from_json(r) for r in (d.get("items") or [])],
    )


def _billing_me_from_json(d: dict[str, Any]) -> BillingMeResponse:
    sub = d.get("subscription")
    usage = d.get("usage")
    return BillingMeResponse(
        plan=_plan_from_json(d["plan"]),
        subscription=_subscription_from_json(sub) if sub else None,
        usage=_usage_rollup_from_json(usage) if usage else None,
    )


def _document_from_json(d: dict[str, Any]) -> EditorDocument:
    return EditorDocument(
        id=d["id"],
        title=d.get("title", ""),
        owner_user_id=d.get("ownerUserId"),
        current_rev_id=d.get("currentRevId"),
        storage_key=d.get("storageKey"),
        size_bytes=int(d.get("sizeBytes", 0)),
        page_count=int(d.get("pageCount", 0)),
        status=d.get("status"),
        created_at=d.get("createdAt"),
        updated_at=d.get("updatedAt"),
    )


def _revision_from_json(d: dict[str, Any]) -> EditorRevision:
    return EditorRevision(
        id=d["id"],
        document_id=d.get("documentId", ""),
        parent_rev_id=d.get("parentRevId"),
        author_user_id=d.get("authorUserId"),
        message=d.get("message"),
        created_at=d.get("createdAt"),
    )
