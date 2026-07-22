"""Input/output artifact transport kept separate from the daemon state machine."""

from __future__ import annotations

import hashlib
import http.client
import json
from pathlib import Path
from typing import Protocol
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

from .models import ClaimedTask, ProducedArtifact


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

    def upload(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact
    ) -> str: ...


class HttpArtifactClient:
    """Transfers artifacts through the coordinator without leaking credentials."""

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

    def upload(self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact) -> str:
        """Stream one result artifact to the coordinator and return its stable URI."""
        url = (
            f"{self.coordinator_url}/tasks/{quote(task.task_id, safe='')}/artifacts/"
            f"{quote(artifact.path.name, safe='')}"
        )
        parsed = urlsplit(url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError("coordinator URL must be an absolute HTTP(S) URL")
        connection_class = (
            http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
        )
        connection = connection_class(parsed.hostname, parsed.port, timeout=self.timeout)
        try:
            path = parsed.path + (f"?{parsed.query}" if parsed.query else "")
            connection.putrequest("PUT", path)
            connection.putheader("Content-Type", artifact.content_type)
            connection.putheader("Content-Length", str(artifact.path.stat().st_size))
            connection.putheader("X-Worker-ID", worker_id)
            connection.putheader("X-Task-Attempt", str(task.attempt))
            for name, value in self._auth_headers_for(url).items():
                connection.putheader(name, value)
            connection.endheaders()
            with artifact.path.open("rb") as source:
                while chunk := source.read(1024 * 1024):
                    connection.send(chunk)
            response = connection.getresponse()
            body = response.read()
            if not 200 <= response.status < 300:
                raise RuntimeError(f"artifact upload rejected with status {response.status}")
            if body:
                try:
                    response_data = json.loads(body)
                except json.JSONDecodeError as error:
                    raise RuntimeError("artifact upload returned invalid JSON") from error
                response_uri = response_data.get("uri") if isinstance(response_data, dict) else None
                if isinstance(response_uri, str) and response_uri:
                    return response_uri
            return url
        finally:
            connection.close()

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
