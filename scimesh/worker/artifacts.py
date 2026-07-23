"""Input/output artifact transport kept separate from the daemon state machine."""

from __future__ import annotations

import hashlib
import http.client
import json
from pathlib import Path
from typing import Protocol
from urllib.parse import quote, urlsplit
from urllib.request import Request, build_opener

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

    def __init__(self, coordinator_url: str, timeout: float, bearer_token: str | None = None) -> None:
        self.coordinator_url = coordinator_url.rstrip("/")
        self.timeout = timeout
        self.bearer_token = bearer_token
        self.coordinator_origin = origin(coordinator_url)
        self._opener = build_opener(SameOriginAuthRedirectHandler(self.coordinator_origin))

    def download(self, uri: str, destination: Path) -> None:
        destination.parent.mkdir(parents=True, exist_ok=True)
        request = Request(uri, headers=self._auth_headers_for(uri))
        with self._opener.open(request, timeout=self.timeout) as response, destination.open("wb") as target:
            while chunk := response.read(1024 * 1024):
                target.write(chunk)

    def upload(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact
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
            if response.status == 409:
                raise CoordinatorConflictError("artifact upload rejected because the task lease was lost")
            if response.status != 201:
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
        if self.bearer_token and origin(uri) == self.coordinator_origin:
            return {"Authorization": f"Bearer {self.bearer_token}"}
        return {}

def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
