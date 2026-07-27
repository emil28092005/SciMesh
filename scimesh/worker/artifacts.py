"""Input/output artifact transport kept separate from the daemon state machine."""

from __future__ import annotations

import hashlib
import http.client
import json
from pathlib import Path
from typing import Protocol
from urllib.error import HTTPError
from urllib.parse import quote, urljoin, urlsplit
from urllib.request import Request, build_opener

from .auth import StaticTokenProvider, TokenProvider
from .coordinator import CoordinatorConflictError
from .models import ClaimedTask, ProducedArtifact, UploadedArtifact
from .transport import SameOriginAuthRedirectHandler, origin

# Compatibility aliases for focused transport tests.
_SameOriginAuthRedirectHandler = SameOriginAuthRedirectHandler
_origin = origin

class ArtifactClient(Protocol):
    def download(self, uri: str, destination: Path) -> None: ...

    def upload(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact
    ) -> UploadedArtifact: ...


class HttpArtifactClient:
    """Transfers artifacts through the coordinator without leaking credentials."""

    def __init__(
        self,
        coordinator_url: str,
        timeout: float,
        bearer_token: str | None = None,
        *,
        token_provider: TokenProvider | None = None,
    ) -> None:
        self.coordinator_url = coordinator_url.rstrip("/")
        self.timeout = timeout
        self._tokens: TokenProvider = token_provider or StaticTokenProvider(bearer_token)
        self.coordinator_origin = origin(coordinator_url)
        self._opener = build_opener(SameOriginAuthRedirectHandler(self.coordinator_origin))

    @property
    def bearer_token(self) -> str | None:
        return self._tokens.token()

    def download(self, uri: str, destination: Path) -> None:
        destination.parent.mkdir(parents=True, exist_ok=True)
        resolved_uri = urljoin(f"{self.coordinator_url}/", uri)
        self._download_once(resolved_uri, destination, allow_refresh=True)

    def _download_once(self, resolved_uri: str, destination: Path, *, allow_refresh: bool) -> None:
        request = Request(resolved_uri, headers=self._auth_headers_for(resolved_uri))
        try:
            with self._opener.open(request, timeout=self.timeout) as response, destination.open("wb") as target:
                while chunk := response.read(1024 * 1024):
                    target.write(chunk)
        except HTTPError as error:
            # Refresh an expired token and retry once, mirroring the coordinator
            # client, so a token that lapses mid-task does not fail the download.
            if error.code == 401 and allow_refresh:
                self._tokens.refresh()
                self._download_once(resolved_uri, destination, allow_refresh=False)
                return
            raise

    def upload(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact
    ) -> UploadedArtifact:
        return self._upload_once(task, worker_id, artifact, allow_refresh=True)

    def _upload_once(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact, *, allow_refresh: bool
    ) -> UploadedArtifact:
        """Stream an artifact and require durable coordinator-owned metadata."""
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
        local_size = artifact.path.stat().st_size
        local_sha256 = sha256_file(artifact.path)
        try:
            path = parsed.path + (f"?{parsed.query}" if parsed.query else "")
            connection.putrequest("PUT", path)
            connection.putheader("Content-Type", artifact.content_type)
            connection.putheader("Content-Length", str(local_size))
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
            if response.status == 401 and allow_refresh:
                # Token lapsed mid-task: refresh and retry the upload once.
                self._tokens.refresh()
                connection.close()
                return self._upload_once(task, worker_id, artifact, allow_refresh=False)
            if response.status == 409:
                raise CoordinatorConflictError("artifact upload rejected because the task lease was lost")
            if response.status != 200:
                raise RuntimeError(f"artifact upload rejected with status {response.status}")
            try:
                response_data = json.loads(body)
                uploaded = UploadedArtifact.from_json(response_data)
            except (ValueError, json.JSONDecodeError) as error:
                raise RuntimeError("artifact upload returned invalid metadata") from error
            if uploaded.sha256 != local_sha256 or uploaded.size_bytes != local_size:
                raise RuntimeError("artifact upload metadata does not match local artifact")
            return uploaded
        finally:
            connection.close()

    def _auth_headers_for(self, uri: str) -> dict[str, str]:
        """Only coordinator-owned URLs receive the coordinator bearer token."""
        token = self._tokens.token()
        if token and origin(uri) == self.coordinator_origin:
            return {"Authorization": f"Bearer {token}"}
        return {}

def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
