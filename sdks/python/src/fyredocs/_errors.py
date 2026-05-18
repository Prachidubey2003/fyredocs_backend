"""FyredocsError — the single exception type the SDK raises."""

from __future__ import annotations


class FyredocsError(Exception):
    """Raised for every API failure.

    Use isinstance to inspect status + code without parsing the
    message string::

        try:
            client.documents.get(doc_id)
        except FyredocsError as e:
            if e.status == 401:   # re-auth
                ...
            if e.code == "RATE_LIMITED":
                ...

    A ``status`` of 0 means the request never reached the server
    (network failure, DNS, timeout). ``code`` is then ``"NETWORK"``
    or ``"READ_FAILED"`` so callers can distinguish.
    """

    __slots__ = ("status", "code", "message")

    def __init__(self, status: int, code: str, message: str) -> None:
        self.status = status
        self.code = code
        self.message = message
        super().__init__(f"{message} ({code} {status})" if message else f"{code} {status}")

    def __repr__(self) -> str:  # pragma: no cover — display only
        return f"FyredocsError(status={self.status}, code={self.code!r}, message={self.message!r})"
