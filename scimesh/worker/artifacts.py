"""Input/output artifact transport kept separate from the daemon state machine."""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Protocol
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


def _origin(uri: str) -> tuple[str, str, int | None]:
    parsed = urlsplit(uri)
    scheme = parsed.scheme.lower()
    default_port = {"http": 80, "https": 443}.get(scheme)
    return scheme, (parsed.hostname or "").lower(), parsed.port or default_port


class _SameOriginAuthRedirectHandler(HTTPRedirectHandler):
    """Do not forward the coordinator token when a download changes origin."""

    def __init__(self, coordinator_origin: tuple[str, str, int | None]) -> None:
        super().__init__()
        self.coordinator_origin = coordinator_origin

    def redirect_request(self, req: Request, fp: object, code: int, msg: str, headers: object, newurl: str) -> Request | None:
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected and _origin(newurl) != self.coordinator_origin:
            redirected.remove_header("Authorization")
        return redirected

class ArtifactClient(Protocol):
    def download(self, uri: str, destination: Path) -> None: ...


class HttpArtifactClient:
    """Downloads task inputs without exposing credentials to external storage.

    The present coordinator contract persists a result *manifest* at ``/result``
    and deliberately defines no artifact-upload endpoint.  Output storage can be
    added later as a separate ArtifactClient implementation.
    """

    def __init__(self, coordinator_url: str, timeout: float, bearer_token: str | None = None) -> None:
        self.coordinator_url = coordinator_url.rstrip("/")
        self.timeout = timeout
        self.bearer_token = bearer_token
        self.coordinator_origin = _origin(coordinator_url)
        self._opener = build_opener(_SameOriginAuthRedirectHandler(self.coordinator_origin))

    def download(self, uri: str, destination: Path) -> None:
        destination.parent.mkdir(parents=True, exist_ok=True)
        request = Request(uri, headers=self._auth_headers_for(uri))
        with self._opener.open(request, timeout=self.timeout) as response, destination.open("wb") as target:
            while chunk := response.read(1024 * 1024):
                target.write(chunk)

    def _auth_headers_for(self, uri: str) -> dict[str, str]:
        """Only coordinator-owned URLs receive the coordinator bearer token."""
        if self.bearer_token and _origin(uri) == self.coordinator_origin:
            return {"Authorization": f"Bearer {self.bearer_token}"}
        return {}

def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
