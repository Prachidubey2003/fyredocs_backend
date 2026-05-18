"""Official Python SDK for the Fyredocs API.

>>> from fyredocs import Client
>>> client = Client(api_key=os.environ["FYREDOCS_KEY"])
>>> me = client.billing.me()
>>> rev = client.documents.edit("doc_01HV…", ops=[
...     {"type": "page.rotate", "page": 1, "rotation": 90},
... ])

Surface mirrors `@fyredocs/sdk` (TypeScript) and
`github.com/fyredocs/fyredocs-go` (Go): same envelope unwrap,
same Authorization header, same wire shapes. Method names are
snake_case to match Python style.
"""

from __future__ import annotations

from ._client import Client, Options
from ._errors import FyredocsError
from ._types import (
    APIKey,
    APIKeyEnvironment,
    BillingMeResponse,
    EditorDocument,
    EditorRevision,
    IssueAPIKeyRequest,
    IssueAPIKeyResponse,
    Plan,
    SubscribeRequest,
    Subscription,
    UsageMeResponse,
    UsageRollup,
    UsageRollupRow,
)

__all__ = [
    "Client",
    "Options",
    "FyredocsError",
    "APIKey",
    "APIKeyEnvironment",
    "BillingMeResponse",
    "EditorDocument",
    "EditorRevision",
    "IssueAPIKeyRequest",
    "IssueAPIKeyResponse",
    "Plan",
    "SubscribeRequest",
    "Subscription",
    "UsageMeResponse",
    "UsageRollup",
    "UsageRollupRow",
]
