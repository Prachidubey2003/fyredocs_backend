"""``client.usage`` — analytics-service /v1/usage/* wrapper."""

from __future__ import annotations

from typing import TYPE_CHECKING, Optional

from ._types import UsageMeResponse, _usage_row_from_json

if TYPE_CHECKING:
    from ._client import Client


class UsageAPI:
    """Wraps ``/v1/usage/*``."""

    def __init__(self, client: "Client") -> None:
        self._c = client

    def me(self, *, period: Optional[str] = None) -> UsageMeResponse:
        """Calling user's usage rollup. ``period`` is ``YYYY-MM``;
        omit for the current UTC month (server-side default)."""
        data = self._c.request(
            "/v1/usage/me",
            query={"period": period} if period else None,
        )
        if not isinstance(data, dict):
            raise ValueError("usage.me: unexpected response shape")
        return UsageMeResponse(
            user_id=str(data.get("userId", "")),
            period=str(data.get("period", "")),
            items=[_usage_row_from_json(r) for r in (data.get("items") or [])],
        )
