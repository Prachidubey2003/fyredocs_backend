"""``client.documents`` — editor-service /api/editor/v1/documents/* wrapper."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, BinaryIO, Mapping, Optional, Sequence
from urllib.parse import quote

from ._types import (
    EditorDocument,
    EditorRevision,
    _document_from_json,
    _revision_from_json,
)

if TYPE_CHECKING:
    from ._client import Client


class DocumentsAPI:
    """Wraps ``/api/editor/v1/documents/*``."""

    def __init__(self, client: "Client") -> None:
        self._c = client

    def list(
        self,
        *,
        page: Optional[int] = None,
        limit: Optional[int] = None,
    ) -> list[EditorDocument]:
        """Newest-first list of the calling user's documents."""
        query: dict[str, Any] = {}
        if page:
            query["page"] = page
        if limit:
            query["limit"] = limit
        data = self._c.request("/api/editor/v1/documents", query=query or None)
        return [_document_from_json(d) for d in (data or [])]

    def get(self, document_id: str) -> EditorDocument:
        """Fetch one document's metadata by ID."""
        data = self._c.request(
            f"/api/editor/v1/documents/{quote(document_id, safe='')}",
        )
        if not isinstance(data, dict):
            raise ValueError("documents.get: unexpected response shape")
        return _document_from_json(data)

    def revisions(self, document_id: str) -> list[EditorRevision]:
        """List every revision for a document."""
        data = self._c.request(
            f"/api/editor/v1/documents/{quote(document_id, safe='')}/revisions",
        )
        return [_revision_from_json(d) for d in (data or [])]

    def edit(
        self,
        document_id: str,
        ops: Sequence[Mapping[str, Any]],
        *,
        message: Optional[str] = None,
    ) -> EditorRevision:
        """Apply a batch of sPDOM ops as one revision.

        ``ops`` is a sequence of mapping objects (typically plain
        dicts). The op shapes are documented in
        ``docs/developer/swagger/openapi.yaml`` — we don't model
        the EditorOp union in Python because the server adds new
        op types in the API contract, and locking the SDK to a
        closed set would force a release per op-type ship.

        ``OP_*`` string constants in ``fyredocs._types`` keep
        compile-time checks on the type names without closing the
        shape::

            from fyredocs._types import OP_PAGE_ROTATE
            client.documents.edit(doc_id, ops=[
                {"type": OP_PAGE_ROTATE, "page": 1, "rotation": 90},
            ])
        """
        body: dict[str, Any] = {"ops": list(ops)}
        if message is not None:
            body["message"] = message
        data = self._c.request(
            f"/api/editor/v1/documents/{quote(document_id, safe='')}/edit",
            method="POST",
            body=body,
        )
        if not isinstance(data, dict):
            raise ValueError("documents.edit: unexpected response shape")
        return _revision_from_json(data)

    def download(
        self,
        document_id: str,
        *,
        rev_id: Optional[str] = None,
        dst: Optional[BinaryIO] = None,
    ) -> bytes:
        """Stream the PDF bytes for ``document_id``'s current
        revision (or ``rev_id`` when set).

        If ``dst`` is supplied, the response is streamed into it
        and ``b""`` is returned. Otherwise the whole body is
        returned as a single ``bytes``. Use the streaming form
        for documents larger than a few MB.
        """
        if rev_id:
            path = (
                f"/api/editor/v1/documents/{quote(document_id, safe='')}"
                f"/revisions/{quote(rev_id, safe='')}/download"
            )
        else:
            path = f"/api/editor/v1/documents/{quote(document_id, safe='')}/download"
        return self._c.request_stream(path, dst=dst)

    def delete(self, document_id: str) -> None:
        """Soft-delete a document. Idempotent — the server flips
        ``status`` to ``"deleted"`` and cleanup-worker eventually
        purges the underlying bytes."""
        self._c.request(
            f"/api/editor/v1/documents/{quote(document_id, safe='')}",
            method="DELETE",
        )
        return None
