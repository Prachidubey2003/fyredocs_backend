"""Client + Options — the entry point of the SDK."""

from __future__ import annotations

import json
import socket
from dataclasses import dataclass, field
from typing import Any, BinaryIO, Mapping, Optional
from urllib import error as urlerror
from urllib import parse as urlparse
from urllib import request as urlrequest

from ._apikeys import APIKeysAPI
from ._billing import BillingAPI
from ._documents import DocumentsAPI
from ._errors import FyredocsError
from ._usage import UsageAPI

DEFAULT_BASE_URL = "https://api.fyredocs.com"
DEFAULT_TIMEOUT = 30.0  # seconds


@dataclass
class Options:
    """Configures a Client. All fields optional; sensible defaults
    apply for every one.

    ``user_agent`` defaults to ``"fyredocs-python"`` — override
    when building tools on top so server-side analytics can
    distinguish integrations.

    ``timeout`` is in seconds and is wired into urllib's
    per-request timeout. ``None`` disables the timeout (don't use
    for production — the v0 API has no long-poll endpoints).
    """

    api_key: Optional[str] = None
    base_url: str = DEFAULT_BASE_URL
    timeout: Optional[float] = DEFAULT_TIMEOUT
    user_agent: str = "fyredocs-python"
    # extra_headers is merged onto every request. Useful for
    # cross-cutting concerns (idempotency, tracing). Per-request
    # overrides take precedence.
    extra_headers: Mapping[str, str] = field(default_factory=dict)


class Client:
    """Holds the credential and base URL, exposes namespaced APIs.

    >>> client = Client(api_key="fyr_live_…")
    >>> client.api_keys.list()
    >>> client.billing.me()
    >>> client.usage.me(period="2026-05")
    >>> client.documents.get("doc_01HV…")

    Instances are cheap and stateless beyond the stored options.
    Construct one per credential (or per environment when running
    against multiple Fyredocs deployments).
    """

    def __init__(
        self,
        api_key: Optional[str] = None,
        *,
        base_url: str = DEFAULT_BASE_URL,
        timeout: Optional[float] = DEFAULT_TIMEOUT,
        user_agent: str = "fyredocs-python",
        extra_headers: Optional[Mapping[str, str]] = None,
        options: Optional[Options] = None,
    ) -> None:
        # Accept either ``Client(options=Options(...))`` (mirrors
        # the TS/Go SDKs) or the flat-kwarg form (more Pythonic
        # for one-line setup). Flat kwargs win when both are
        # passed because they're more explicit at the call site.
        if options is not None:
            self.api_key = api_key if api_key is not None else options.api_key
            self.base_url = base_url if base_url != DEFAULT_BASE_URL else options.base_url
            self.timeout = timeout if timeout != DEFAULT_TIMEOUT else options.timeout
            self.user_agent = user_agent if user_agent != "fyredocs-python" else options.user_agent
            self._extra_headers = dict(extra_headers or options.extra_headers)
        else:
            self.api_key = api_key
            self.base_url = base_url
            self.timeout = timeout
            self.user_agent = user_agent
            self._extra_headers = dict(extra_headers or {})

        # Strip trailing slash so callers can hand us either form.
        self.base_url = self.base_url.rstrip("/")

        # Namespaced APIs.
        self.api_keys = APIKeysAPI(self)
        self.billing = BillingAPI(self)
        self.usage = UsageAPI(self)
        self.documents = DocumentsAPI(self)

    # ---- HTTP --------------------------------------------------

    def request(
        self,
        path: str,
        *,
        method: str = "GET",
        query: Optional[Mapping[str, Any]] = None,
        body: Any = None,
        headers: Optional[Mapping[str, str]] = None,
    ) -> Any:
        """Run one request. Returns the parsed envelope ``data``
        on 2xx; raises ``FyredocsError`` on non-2xx.

        Public so the namespace classes call it without
        indirection — the documented surface is the per-namespace
        methods, but ``request`` is the escape hatch for endpoints
        the SDK doesn't yet wrap.
        """
        req = self._build_request(path, method=method, query=query, body=body, headers=headers)
        raw, status = self._do(req)
        if status == 204 or not raw:
            return None
        env = _parse_envelope(raw)
        if env is None:
            # Non-enveloped 2xx — return the parsed JSON as-is.
            try:
                return json.loads(raw)
            except json.JSONDecodeError:
                return raw
        if env.get("success") is False:
            raise FyredocsError(
                status, "SUCCESS_FALSE_ON_2XX",
                "server returned success=false on a 2xx response",
            )
        return env.get("data")

    def request_stream(
        self,
        path: str,
        *,
        method: str = "GET",
        query: Optional[Mapping[str, Any]] = None,
        headers: Optional[Mapping[str, str]] = None,
        dst: Optional[BinaryIO] = None,
    ) -> bytes:
        """Run one request and stream the response body to ``dst``
        (or return it as bytes when ``dst`` is None). The Fyredocs
        JSON envelope is NOT unwrapped — use for endpoints that
        return binary (``/download`` → ``application/pdf``).

        Non-2xx responses parse the body as an envelope so the
        raised ``FyredocsError`` still carries the server's
        message.
        """
        req = self._build_request(path, method=method, query=query, body=None, headers=headers)
        try:
            with urlrequest.urlopen(req, timeout=self.timeout) as resp:
                if dst is None:
                    return resp.read()
                while True:
                    chunk = resp.read(64 * 1024)
                    if not chunk:
                        return b""
                    dst.write(chunk)
        except urlerror.HTTPError as e:
            raw = b""
            try:
                raw = e.read()
            except Exception:  # noqa: BLE001
                pass
            raise _http_error_from_response(e.code, raw) from None
        except (urlerror.URLError, socket.timeout, TimeoutError) as e:
            raise FyredocsError(0, "NETWORK", str(e)) from None

    # ---- internal ----------------------------------------------

    def _build_request(
        self,
        path: str,
        *,
        method: str,
        query: Optional[Mapping[str, Any]],
        body: Any,
        headers: Optional[Mapping[str, str]],
    ) -> urlrequest.Request:
        url = self.base_url + path
        if query:
            # Drop None / empty so callers can pass optional
            # filters without manual filtering.
            qs = urlparse.urlencode(
                {k: _to_query(v) for k, v in query.items() if v not in (None, "")}
            )
            if qs:
                sep = "&" if "?" in url else "?"
                url = f"{url}{sep}{qs}"

        payload: Optional[bytes] = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")

        req = urlrequest.Request(url=url, data=payload, method=method)
        req.add_header("Accept", "application/json")
        req.add_header("User-Agent", self.user_agent)
        if self.api_key:
            req.add_header("Authorization", f"Bearer {self.api_key}")
        if payload is not None:
            req.add_header("Content-Type", "application/json")
        for k, v in self._extra_headers.items():
            req.add_header(k, v)
        if headers:
            for k, v in headers.items():
                # urllib's `add_header` re-adds rather than
                # overrides. Strip-then-add gives a per-request
                # override of any cross-cutting header.
                req.headers.pop(k.capitalize(), None)
                req.add_header(k, v)
        return req

    def _do(self, req: urlrequest.Request) -> tuple[bytes, int]:
        try:
            with urlrequest.urlopen(req, timeout=self.timeout) as resp:
                return resp.read(), resp.status
        except urlerror.HTTPError as e:
            raw = b""
            try:
                raw = e.read()
            except Exception:  # noqa: BLE001
                pass
            raise _http_error_from_response(e.code, raw) from None
        except (urlerror.URLError, socket.timeout, TimeoutError) as e:
            raise FyredocsError(0, "NETWORK", str(e)) from None


def _to_query(v: Any) -> str:
    if isinstance(v, bool):
        return "true" if v else "false"
    return str(v)


def _parse_envelope(raw: bytes) -> Optional[dict[str, Any]]:
    """Decode the JSON envelope, returning None if the body
    doesn't look like ``{success, ...}``. Non-enveloped 2xx
    responses bypass the unwrap (the request layer falls
    through to raw JSON)."""
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return None
    if isinstance(parsed, dict) and "success" in parsed:
        return parsed
    return None


def _http_error_from_response(status: int, raw: bytes) -> FyredocsError:
    if not raw:
        return FyredocsError(status, f"HTTP_{status}", "")
    try:
        env = json.loads(raw)
    except json.JSONDecodeError:
        return FyredocsError(status, f"HTTP_{status}", raw.decode("utf-8", "replace").strip())
    if isinstance(env, dict):
        err = env.get("error")
        if isinstance(err, dict):
            return FyredocsError(
                status,
                str(err.get("code") or f"HTTP_{status}"),
                str(err.get("details") or env.get("message") or ""),
            )
        if env.get("message"):
            return FyredocsError(status, f"HTTP_{status}", str(env["message"]))
    return FyredocsError(status, f"HTTP_{status}", raw.decode("utf-8", "replace").strip())
