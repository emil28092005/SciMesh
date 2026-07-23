"""Configuration parsing for the worker command."""

from __future__ import annotations

from dataclasses import dataclass
from math import isfinite
from pathlib import Path
import os
import socket
from typing import Mapping
from urllib.parse import urlsplit


def _positive_number(value: object, name: str, *, allow_zero: bool = False) -> None:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not isfinite(value)
        or value < 0
        or (not allow_zero and value == 0)
    ):
        qualifier = "non-negative" if allow_zero else "positive"
        raise ValueError(f"{name} must be {qualifier}")


@dataclass(frozen=True)
class WorkerConfig:
    coordinator_url: str
    worker_id: str | None
    work_dir: Path
    worker_name: str = "scimesh-worker"
    cpu_count: int = 1
    memory_mb: int | None = None
    poll_interval: float = 2.0
    request_timeout: float = 30.0
    heartbeat_interval: float = 15.0
    bearer_token: str | None = None
    cleanup_after_seconds: float | None = None
    # The local CLI uses hyphens; the first coordinator contract used
    # underscores. Advertise both stable spellings while jobs are migrated.
    capabilities: tuple[str, ...] = (
        "similarity-search",
        "similarity-graph",
        "similarity_search",
        "similarity_graph",
    )

    def __post_init__(self) -> None:
        parsed = urlsplit(self.coordinator_url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError("coordinator_url must be an absolute HTTP(S) URL")
        if not isinstance(self.worker_name, str) or not self.worker_name.strip():
            raise ValueError("worker_name must be non-empty")
        if isinstance(self.cpu_count, bool) or not isinstance(self.cpu_count, int) or self.cpu_count < 1:
            raise ValueError("cpu_count must be positive")
        if self.worker_id is not None and not isinstance(self.worker_id, str):
            raise ValueError("worker_id must be a string when set")
        if self.memory_mb is not None and (
            isinstance(self.memory_mb, bool)
            or not isinstance(self.memory_mb, int)
            or self.memory_mb < 1
        ):
            raise ValueError("memory_mb must be positive when set")
        _positive_number(self.poll_interval, "poll_interval")
        _positive_number(self.request_timeout, "request_timeout")
        _positive_number(self.heartbeat_interval, "heartbeat_interval")
        if self.cleanup_after_seconds is not None:
            _positive_number(self.cleanup_after_seconds, "cleanup_after_seconds", allow_zero=True)
        if not self.capabilities:
            raise ValueError("capabilities cannot be empty")

    @classmethod
    def from_environment(
        cls, overrides: Mapping[str, object] | None = None
    ) -> "WorkerConfig":
        """Build config from environment, allowing typed CLI values to override it."""
        values = overrides or {}

        def value(name: str, environment: str, default: object | None = None) -> object | None:
            override = values.get(name)
            return override if override is not None else os.getenv(environment, default)

        url = value("coordinator_url", "SCIMESH_COORDINATOR_URL")
        if not isinstance(url, str) or not url:
            raise ValueError("SCIMESH_COORDINATOR_URL or --coordinator-url is required")
        cleanup = value("cleanup_after_seconds", "SCIMESH_CLEANUP_AFTER_SECONDS")
        cpu_count = value("cpu_count", "SCIMESH_CPU_COUNT", os.cpu_count() or 1)
        memory_mb = value("memory_mb", "SCIMESH_MEMORY_MB")
        return cls(
            coordinator_url=url.rstrip("/"),
            worker_id=value("worker_id", "SCIMESH_WORKER_ID"),
            work_dir=Path(value("work_dir", "SCIMESH_WORK_DIR", "./scimesh-worker-data")),
            worker_name=str(value("worker_name", "SCIMESH_WORKER_NAME", socket.gethostname())),
            cpu_count=int(cpu_count),
            memory_mb=int(memory_mb) if memory_mb is not None else None,
            poll_interval=float(value("poll_interval", "SCIMESH_POLL_INTERVAL", "2")),
            request_timeout=float(value("request_timeout", "SCIMESH_REQUEST_TIMEOUT", "30")),
            heartbeat_interval=float(value("heartbeat_interval", "SCIMESH_HEARTBEAT_INTERVAL", "15")),
            bearer_token=value("bearer_token", "SCIMESH_BEARER_TOKEN"),
            cleanup_after_seconds=float(cleanup) if cleanup else None,
        )
