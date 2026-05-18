"""``client.billing`` — billing-service /api/billing/v1/billing/* wrapper."""

from __future__ import annotations

from typing import TYPE_CHECKING

from ._types import (
    BillingMeResponse,
    Plan,
    SubscribeRequest,
    Subscription,
    _billing_me_from_json,
    _plan_from_json,
    _subscription_from_json,
)

if TYPE_CHECKING:
    from ._client import Client


class BillingAPI:
    """Wraps ``/api/billing/v1/billing/*``."""

    def __init__(self, client: "Client") -> None:
        self._c = client

    def plans(self) -> list[Plan]:
        """List self-serve + sales-led plans the user is eligible
        to subscribe to."""
        data = self._c.request("/api/billing/v1/billing/plans")
        if not isinstance(data, dict):
            return []
        return [_plan_from_json(p) for p in (data.get("plans") or [])]

    def me(self) -> BillingMeResponse:
        """Return the calling user's plan + subscription + current
        usage rollup."""
        data = self._c.request("/api/billing/v1/billing/me")
        if not isinstance(data, dict):
            raise ValueError("billing.me: unexpected response shape")
        return _billing_me_from_json(data)

    def subscribe(self, req: SubscribeRequest) -> Subscription:
        """Upgrade / downgrade / start a subscription. Returns the
        updated Subscription."""
        body: dict[str, object] = {"planCode": req.plan_code}
        if req.seats is not None:
            body["seats"] = req.seats
        data = self._c.request(
            "/api/billing/v1/billing/me/subscribe", method="POST", body=body,
        )
        if not isinstance(data, dict):
            raise ValueError("billing.subscribe: unexpected response shape")
        return _subscription_from_json(data)
