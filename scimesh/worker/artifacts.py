"""Input/output artifact transport kept separate from the daemon state machine."""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Protocol
from urllib.request import Request, urlopen

from .models import ClaimedTask, ProducedArtifact


class ArtifactClient(Protocol):
    def download(self, uri: str, destination: Path) -> None: ...

    def upload(self, task: ClaimedTask, artifact: ProducedArtifact) -> str: ...


class HttpArtifactClient:
    """Default coordinator artifact convention.

    Results are PUT to /tasks/{task_id}/artifacts/{filename}.  The coordinator may
    return a JSON body containing ``uri``; otherwise the upload URL is reported.
    """

    def __init__(self, coordinator_url: str, timeout: float, bearer_token: str | None = None) -> None:
        self.coordinator_url = coordinator_url.rstrip("/")
        self.timeout = timeout
        self.bearer_token = bearer_token

    def download(self, uri: str, destination: Path) -> None:
        destination.parent.mkdir(parents=True, exist_ok=True)
        request = Request(uri, headers=self._auth_header())
        with urlopen(request, timeout=self.timeout) as response, destination.open("wb") as target:
            while chunk := response.read(1024 * 1024):
                target.write(chunk)

    def upload(self, task: ClaimedTask, artifact: ProducedArtifact) -> str:
        url = f"{self.coordinator_url}/tasks/{task.task_id}/artifacts/{artifact.path.name}"
        request = Request(
            url, data=artifact.path.read_bytes(), method="PUT",
            headers={"Content-Type": artifact.content_type, **self._auth_header()},
        )
        with urlopen(request, timeout=self.timeout) as response:
            # An empty response is valid; the conventional endpoint itself is the URI.
            return url if not response.read() else url

    def _auth_header(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.bearer_token}"} if self.bearer_token else {}


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
