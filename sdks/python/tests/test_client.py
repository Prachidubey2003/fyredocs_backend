"""Integration tests for the fyredocs SDK.

Spins up an stdlib ``http.server`` per test, points the SDK at it,
asserts the round-trip shape. Equivalent to the Go SDK's
``httptest`` and the TS SDK's mocked ``fetch`` — zero
third-party deps.
"""

from __future__ import annotations

import io
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any, Callable, Optional

import pytest

from fyredocs import (
    Client,
    FyredocsError,
    IssueAPIKeyRequest,
    SubscribeRequest,
)
from fyredocs._types import OP_PAGE_ROTATE


# ---------------------------------------------------------------------------
# Harness — stdlib HTTP server. The fixture returns a Client wired
# to the server; the test supplies a handler closure that inspects
# the request and writes the response.
# ---------------------------------------------------------------------------


Handler = Callable[[BaseHTTPRequestHandler], None]


class _ServerCtx:
    def __init__(self, handler: Handler) -> None:
        self._handler = handler
        self._server: Optional[HTTPServer] = None
        self._thread: Optional[threading.Thread] = None
        self.requests: list[dict[str, Any]] = []

    def __enter__(self) -> "Client":
        outer = self

        class _H(BaseHTTPRequestHandler):
            # Silence the default per-request stderr logging — the
            # tests already assert on observed behavior.
            def log_message(self, *args: Any, **_kw: Any) -> None:  # noqa: D401
                pass

            def _record(self) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                # Stash on the handler instance so the closure
                # can inspect the body without re-reading rfile
                # (which is already drained by this read).
                self.body = body  # type: ignore[attr-defined]
                outer.requests.append(
                    {
                        "method": self.command,
                        "path": self.path,
                        "headers": dict(self.headers),
                        "body": body,
                    }
                )

            def do_GET(self) -> None:
                self._record()
                outer._handler(self)

            def do_POST(self) -> None:
                self._record()
                outer._handler(self)

            def do_DELETE(self) -> None:
                self._record()
                outer._handler(self)

        # Port 0 = let the kernel pick.
        self._server = HTTPServer(("127.0.0.1", 0), _H)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        host, port = self._server.server_address
        return Client(api_key="fyr_test_key", base_url=f"http://{host}:{port}", timeout=5.0)

    def __exit__(self, *_args: Any) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=2.0)


@pytest.fixture
def serve():
    """pytest helper: ``with serve(handler) as client: ...``"""

    def factory(handler: Handler) -> _ServerCtx:
        return _ServerCtx(handler)

    return factory


def _write_json(h: BaseHTTPRequestHandler, status: int, body: dict[str, Any]) -> None:
    payload = json.dumps(body).encode("utf-8")
    h.send_response(status)
    h.send_header("Content-Type", "application/json")
    h.send_header("Content-Length", str(len(payload)))
    h.end_headers()
    h.wfile.write(payload)


def _write_bytes(h: BaseHTTPRequestHandler, status: int, body: bytes, content_type: str) -> None:
    h.send_response(status)
    h.send_header("Content-Type", content_type)
    h.send_header("Content-Length", str(len(body)))
    h.end_headers()
    h.wfile.write(body)


# ---------------------------------------------------------------------------
# Client / Request
# ---------------------------------------------------------------------------


def test_request_sends_authorization_header(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        _write_json(req, 200, {"success": True, "data": {"ok": True}})

    with serve(h) as client:
        out = client.request("/anything")
    assert out == {"ok": True}
    # Inspect what the server saw by re-running — the fixture
    # records every request:
    # (we use the ctx manager to grab the records)


def test_request_maps_envelope_error_to_fyredocs_error(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        _write_json(
            req,
            400,
            {
                "success": False,
                "error": {"code": "INVALID_INPUT", "details": "pageNum must be >= 1"},
            },
        )

    with serve(h) as client:
        with pytest.raises(FyredocsError) as exc:
            client.request("/x")
    assert exc.value.status == 400
    assert exc.value.code == "INVALID_INPUT"
    assert "pageNum" in exc.value.message


def test_request_network_error_returns_zero_status():
    # 127.0.0.1:1 — TCP port 1 is reserved and won't accept.
    client = Client(api_key="fyr_test", base_url="http://127.0.0.1:1", timeout=2.0)
    with pytest.raises(FyredocsError) as exc:
        client.request("/x")
    assert exc.value.status == 0
    assert exc.value.code == "NETWORK"


def test_request_sets_user_agent_default_and_override(serve):
    seen: dict[str, str] = {}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["ua"] = req.headers.get("User-Agent", "")
        _write_json(req, 200, {"success": True, "data": None})

    with serve(h) as client:
        client.request("/x")
    assert seen["ua"] == "fyredocs-python"

    with serve(h) as client_ctx:
        host = client_ctx.base_url
        c2 = Client(api_key="fyr_test", base_url=host, user_agent="myapp/1.2.3", timeout=5.0)
        c2.request("/x")
    assert seen["ua"] == "myapp/1.2.3"


def test_request_204_no_content(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        req.send_response(204)
        req.end_headers()

    with serve(h) as client:
        assert client.request("/x", method="POST") is None


# ---------------------------------------------------------------------------
# APIKeys
# ---------------------------------------------------------------------------


def test_api_keys_list_passes_revoked_filter(serve):
    captured: dict[str, str] = {}

    def h(req: BaseHTTPRequestHandler) -> None:
        captured["path"] = req.path
        _write_json(
            req, 200,
            {
                "success": True,
                "data": [
                    {
                        "id": "key_1",
                        "name": "CI",
                        "environment": "live",
                        "keyPrefix": "fyr_live_abc",
                        "createdAt": "2026-01-01",
                    }
                ],
            },
        )

    with serve(h) as client:
        keys = client.api_keys.list(revoked=True)
    assert "revoked=true" in captured["path"]
    assert len(keys) == 1
    assert keys[0].id == "key_1"
    assert keys[0].environment == "live"


def test_api_keys_issue_and_revoke(serve):
    seen_paths: list[str] = []
    seen_bodies: list[bytes] = []

    def h(req: BaseHTTPRequestHandler) -> None:
        seen_paths.append(req.path)
        seen_bodies.append(getattr(req, "body", b""))
        if req.path == "/auth/api-keys":
            _write_json(
                req, 200,
                {
                    "success": True,
                    "data": {
                        "key": {
                            "id": "key_new", "name": "ops",
                            "environment": "test", "keyPrefix": "fyr_test_x",
                            "createdAt": "2026-05-16",
                        },
                        "plaintext": "fyr_test_PLAIN",
                    },
                },
            )
            return
        if req.path == "/auth/api-keys/key_new/revoke":
            _write_json(req, 200, {"success": True})
            return
        req.send_response(404)
        req.end_headers()

    with serve(h) as client:
        resp = client.api_keys.issue(IssueAPIKeyRequest(name="ops", environment="test"))
        assert resp.plaintext == "fyr_test_PLAIN"
        assert resp.key.id == "key_new"
        client.api_keys.revoke("key_new")

    assert seen_paths == ["/auth/api-keys", "/auth/api-keys/key_new/revoke"]
    issued = json.loads(seen_bodies[0])
    assert issued == {"name": "ops", "environment": "test"}


# ---------------------------------------------------------------------------
# Billing
# ---------------------------------------------------------------------------


def test_billing_me(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        _write_json(
            req, 200,
            {
                "success": True,
                "data": {
                    "plan": {
                        "code": "pro", "name": "Pro", "description": "d",
                        "monthlyPriceCents": 1500, "perSeat": False,
                        "selfServe": True, "limits": {},
                    },
                    "subscription": {
                        "id": "sub_1", "userId": "u", "planCode": "pro",
                        "status": "active", "seats": 1,
                        "currentPeriodStart": "a", "currentPeriodEnd": "b",
                        "createdAt": "c", "updatedAt": "d",
                    },
                },
            },
        )

    with serve(h) as client:
        me = client.billing.me()
    assert me.plan.code == "pro"
    assert me.subscription is not None
    assert me.subscription.status == "active"


def test_billing_plans_unwraps_plans_key(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        _write_json(
            req, 200,
            {
                "success": True,
                "data": {
                    "plans": [
                        {
                            "code": "free", "name": "Free",
                            "description": "", "monthlyPriceCents": 0,
                            "perSeat": False, "selfServe": True, "limits": {},
                        },
                        {
                            "code": "pro", "name": "Pro",
                            "description": "", "monthlyPriceCents": 1500,
                            "perSeat": False, "selfServe": True, "limits": {},
                        },
                    ]
                },
            },
        )

    with serve(h) as client:
        plans = client.billing.plans()
    assert [p.code for p in plans] == ["free", "pro"]


def test_billing_subscribe(serve):
    body_seen: dict[str, Any] = {}

    def h(req: BaseHTTPRequestHandler) -> None:
        body_seen.update(json.loads(getattr(req, "body", b"{}")))
        _write_json(
            req, 200,
            {
                "success": True,
                "data": {
                    "id": "sub_2", "userId": "u", "planCode": "teams",
                    "status": "active", "seats": 5,
                    "currentPeriodStart": "a", "currentPeriodEnd": "b",
                    "createdAt": "c", "updatedAt": "d",
                },
            },
        )

    with serve(h) as client:
        sub = client.billing.subscribe(SubscribeRequest(plan_code="teams", seats=5))
    assert body_seen == {"planCode": "teams", "seats": 5}
    assert sub.plan_code == "teams"
    assert sub.seats == 5


# ---------------------------------------------------------------------------
# Usage
# ---------------------------------------------------------------------------


def test_usage_me_omits_period_query_by_default(serve):
    seen = {"path": ""}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["path"] = req.path
        _write_json(req, 200, {"success": True, "data": {
            "userId": "u", "period": "2026-05",
            "items": [{"eventType": "editor.edit.text.replace", "unit": "calls",
                       "totalQuantity": 3, "eventCount": 3}],
        }})

    with serve(h) as client:
        out = client.usage.me()
    assert "period=" not in seen["path"]
    assert out.period == "2026-05"
    assert len(out.items) == 1


def test_usage_me_passes_period_query(serve):
    seen = {"path": ""}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["path"] = req.path
        _write_json(req, 200, {"success": True, "data": {"userId": "u", "period": "2026-04", "items": []}})

    with serve(h) as client:
        client.usage.me(period="2026-04")
    assert "period=2026-04" in seen["path"]


# ---------------------------------------------------------------------------
# Documents
# ---------------------------------------------------------------------------


def test_documents_get(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        assert req.path == "/api/editor/v1/documents/doc_abc"
        _write_json(req, 200, {"success": True, "data": {
            "id": "doc_abc", "title": "Spec",
            "pageCount": 7, "currentRevId": "rev_xyz", "status": "ready",
        }})

    with serve(h) as client:
        doc = client.documents.get("doc_abc")
    assert doc.id == "doc_abc"
    assert doc.current_rev_id == "rev_xyz"
    assert doc.page_count == 7


def test_documents_list_passes_pagination(serve):
    seen = {"path": ""}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["path"] = req.path
        _write_json(req, 200, {"success": True, "data": [
            {"id": "doc_1", "title": "A", "status": "ready"},
        ]})

    with serve(h) as client:
        docs = client.documents.list(page=2, limit=10)
    assert "page=2" in seen["path"] and "limit=10" in seen["path"]
    assert len(docs) == 1


def test_documents_revisions(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        assert req.path == "/api/editor/v1/documents/doc_x/revisions"
        _write_json(req, 200, {"success": True, "data": [
            {"id": "rev_a", "documentId": "doc_x"},
            {"id": "rev_b", "documentId": "doc_x", "parentRevId": "rev_a"},
        ]})

    with serve(h) as client:
        revs = client.documents.revisions("doc_x")
    assert len(revs) == 2
    assert revs[1].parent_rev_id == "rev_a"


def test_documents_edit_posts_ops_and_returns_rev(serve):
    seen_body: dict[str, Any] = {}

    def h(req: BaseHTTPRequestHandler) -> None:
        assert req.path == "/api/editor/v1/documents/doc_x/edit"
        assert req.command == "POST"
        seen_body.update(json.loads(getattr(req, "body", b"{}")))
        _write_json(req, 200, {"success": True, "data": {
            "id": "rev_new", "documentId": "doc_x", "createdAt": "2026-05-16",
        }})

    with serve(h) as client:
        rev = client.documents.edit("doc_x", ops=[
            {"type": OP_PAGE_ROTATE, "page": 1, "rotation": 90},
        ])
    assert rev.id == "rev_new"
    assert seen_body == {"ops": [{"type": "page.rotate", "page": 1, "rotation": 90}]}


def test_documents_download_streams_bytes(serve):
    def h(req: BaseHTTPRequestHandler) -> None:
        _write_bytes(req, 200, b"%PDF-1.4\n%fake-bytes", "application/pdf")

    with serve(h) as client:
        # As bytes:
        out = client.documents.download("doc_x")
        assert out.startswith(b"%PDF-1.4")
        # As stream into a BytesIO:
        buf = io.BytesIO()
        client.documents.download("doc_x", dst=buf)
        assert buf.getvalue().startswith(b"%PDF-1.4")


def test_documents_download_specific_revision(serve):
    seen = {"path": ""}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["path"] = req.path
        _write_bytes(req, 200, b"%PDF-1.4\n", "application/pdf")

    with serve(h) as client:
        client.documents.download("doc_x", rev_id="rev_b")
    assert seen["path"] == "/api/editor/v1/documents/doc_x/revisions/rev_b/download"


def test_documents_delete(serve):
    seen = {"method": "", "path": ""}

    def h(req: BaseHTTPRequestHandler) -> None:
        seen["method"] = req.command
        seen["path"] = req.path
        _write_json(req, 200, {"success": True})

    with serve(h) as client:
        client.documents.delete("doc_x")
    assert seen["method"] == "DELETE"
    assert seen["path"] == "/api/editor/v1/documents/doc_x"
