"""Value objects shared by the worker daemon components."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from math import isfinite
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit
from uuid import UUID


def _required_string(value: object, field: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field} must be a non-empty string")
    return value


def _http_uri(value: object, field: str) -> str:
    uri = _required_string(value, field)
    parsed = urlsplit(uri)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise ValueError(f"{field} must be an absolute HTTP(S) URL")
    return uri


def _sha256(value: object, field: str) -> str:
    digest = _required_string(value, field).lower()
    if len(digest) != 64 or any(character not in "0123456789abcdef" for character in digest):
        raise ValueError(f"{field} must be a SHA-256 hex digest")
    return digest


@dataclass(frozen=True)
class InputArtifact:
    uri: str
    sha256: str


@dataclass(frozen=True)
class ClaimedTask:
    task_id: str
    attempt: int
    lease_expires_at: str
    workload: str
    input: InputArtifact
    parameters: dict[str, Any]

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "ClaimedTask":
        try:
            input_data = data["input"]
            if not isinstance(input_data, dict):
                raise ValueError("input must be an object")
            raw_attempt = data["attempt"]
            if isinstance(raw_attempt, bool) or not isinstance(raw_attempt, int) or raw_attempt < 1:
                raise ValueError("attempt must be a positive integer")
            task_id = str(UUID(_required_string(data["task_id"], "task_id")))
            lease_expires_at = _required_string(data["lease_expires_at"], "lease_expires_at")
            if datetime.fromisoformat(lease_expires_at.replace("Z", "+00:00")).tzinfo is None:
                raise ValueError("lease_expires_at must include a timezone")
            parameters = data.get("parameters", {})
            if not isinstance(parameters, dict):
                raise ValueError("parameters must be an object")
            return cls(
                task_id=task_id,
                attempt=raw_attempt,
                lease_expires_at=lease_expires_at,
                workload=_required_string(data["workload"], "workload"),
                input=InputArtifact(
                    uri=_http_uri(input_data["uri"], "input.uri"),
                    sha256=_sha256(input_data["sha256"], "input.sha256"),
                ),
                parameters=parameters,
            )
        except (KeyError, TypeError, ValueError) as error:
            raise ValueError("invalid claimed-task response") from error


@dataclass(frozen=True)
class ProducedArtifact:
    path: Path
    content_type: str


@dataclass(frozen=True)
class UploadedArtifact:
    """Coordinator-owned artifact metadata returned after a successful upload."""

    artifact_id: str
    uri: str
    sha256: str
    size_bytes: int

    @classmethod
    def from_json(cls, data: object) -> "UploadedArtifact":
        if not isinstance(data, dict):
            raise ValueError("artifact upload response must be an object")
        raw_size = data.get("size_bytes")
        if isinstance(raw_size, bool) or not isinstance(raw_size, int) or raw_size < 0:
            raise ValueError("artifact size_bytes must be a non-negative integer")
        try:
            return cls(
                artifact_id=str(UUID(_required_string(data.get("artifact_id"), "artifact_id"))),
                uri=_http_uri(data.get("uri"), "uri"),
                sha256=_sha256(data.get("sha256"), "sha256"),
                size_bytes=raw_size,
            )
        except ValueError as error:
            raise ValueError("invalid artifact upload response") from error


@dataclass(frozen=True)
class RegisteredWorker:
    """Identity and heartbeat policy returned by worker registration."""

    worker_id: str
    heartbeat_interval_seconds: float

    @classmethod
    def from_json(cls, data: object) -> "RegisteredWorker":
        if not isinstance(data, dict):
            raise ValueError("worker registration response must be an object")
        raw_interval = data.get("heartbeat_interval_seconds")
        if (
            isinstance(raw_interval, bool)
            or not isinstance(raw_interval, (int, float))
            or not isfinite(raw_interval)
            or raw_interval <= 0
        ):
            raise ValueError("heartbeat_interval_seconds must be positive")
        try:
            return cls(
                worker_id=str(UUID(_required_string(data.get("worker_id"), "worker_id"))),
                heartbeat_interval_seconds=float(raw_interval),
            )
        except ValueError as error:
            raise ValueError("invalid worker registration response") from error


@dataclass(frozen=True)
class RunResult:
    artifacts: tuple[ProducedArtifact, ...]
    metrics: dict[str, int | float]
