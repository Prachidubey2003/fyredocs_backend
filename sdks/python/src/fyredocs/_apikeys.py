"""``client.api_keys`` — auth-service /auth/api-keys/* wrapper."""

from __future__ import annotations

from typing import TYPE_CHECKING, Optional
from urllib.parse import quote

from ._types import (
    APIKey,
    IssueAPIKeyRequest,
    IssueAPIKeyResponse,
    _api_key_from_json,
)

if TYPE_CHECKING:
    from ._client import Client


class APIKeysAPI:
    """Wraps ``/auth/api-keys/*``."""

    def __init__(self, client: "Client") -> None:
        self._c = client

    def list(self, *, revoked: bool = False) -> list[APIKey]:
        """List the calling user's API keys.

        Pass ``revoked=True`` to switch to the audit archive of
        revoked keys instead of active keys.
        """
        data = self._c.request(
            "/auth/api-keys",
            query={"revoked": "true"} if revoked else None,
        )
        return [_api_key_from_json(d) for d in (data or [])]

    def issue(self, req: IssueAPIKeyRequest) -> IssueAPIKeyResponse:
        """Mint a new API key. The plaintext is in the returned
        response — display + persist it immediately, the server
        can't recover it."""
        body: dict[str, object] = {"name": req.name}
        if req.environment:
            body["environment"] = req.environment
        if req.scopes is not None:
            body["scopes"] = list(req.scopes)
        data = self._c.request("/auth/api-keys", method="POST", body=body)
        if not isinstance(data, dict):
            raise ValueError("api_keys.issue: unexpected response shape")
        return IssueAPIKeyResponse(
            key=_api_key_from_json(data["key"]),
            plaintext=str(data.get("plaintext", "")),
        )

    def revoke(self, key_id: str) -> None:
        """Mark the key as revoked. Idempotent — calling revoke
        on an already-revoked key is a no-op at the server."""
        self._c.request(
            f"/auth/api-keys/{quote(key_id, safe='')}/revoke",
            method="POST",
        )
        return None
