"""Small HTTP transport helpers shared by coordinator and artifact clients."""

from __future__ import annotations

from urllib.request import HTTPRedirectHandler, Request
from urllib.parse import urlsplit


def origin(uri: str) -> tuple[str, str, int | None]:
    """Return a normalized HTTP origin for authorization decisions."""
    parsed = urlsplit(uri)
    scheme = parsed.scheme.lower()
    default_port = {"http": 80, "https": 443}.get(scheme)
    return scheme, (parsed.hostname or "").lower(), parsed.port or default_port


class SameOriginAuthRedirectHandler(HTTPRedirectHandler):
    """Strip coordinator authorization when an artifact redirect changes origin."""

    def __init__(self, coordinator_origin: tuple[str, str, int | None]) -> None:
        super().__init__()
        self.coordinator_origin = coordinator_origin

    def redirect_request(
        self,
        req: Request,
        fp: object,
        code: int,
        msg: str,
        headers: object,
        newurl: str,
    ) -> Request | None:
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected and origin(newurl) != self.coordinator_origin:
            redirected.remove_header("Authorization")
        return redirected


class NoRedirectHandler(HTTPRedirectHandler):
    """Reject redirects for mutating coordinator API calls."""

    def redirect_request(
        self,
        req: Request,
        fp: object,
        code: int,
        msg: str,
        headers: object,
        newurl: str,
    ) -> Request | None:
        return None
