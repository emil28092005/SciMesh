"""Configuration parsing for the worker command."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import os


@dataclass(frozen=True)
class WorkerConfig:
    coordinator_url: str
    worker_id: str
    work_dir: Path
    poll_interval: float = 2.0
    request_timeout: float = 30.0
    heartbeat_interval: float = 15.0
    bearer_token: str | None = None
    cleanup_after_seconds: float | None = None
    capabilities: tuple[str, ...] = ("similarity-search", "similarity-graph")

    @classmethod
    def from_environment(cls) -> "WorkerConfig":
        url = os.getenv("SCIMESH_COORDINATOR_URL")
        worker_id = os.getenv("SCIMESH_WORKER_ID")
        if not url or not worker_id:
            raise ValueError("SCIMESH_COORDINATOR_URL and SCIMESH_WORKER_ID are required")
        cleanup = os.getenv("SCIMESH_CLEANUP_AFTER_SECONDS")
        return cls(
            coordinator_url=url.rstrip("/"),
            worker_id=worker_id,
            work_dir=Path(os.getenv("SCIMESH_WORK_DIR", "./scimesh-worker-data")),
            poll_interval=float(os.getenv("SCIMESH_POLL_INTERVAL", "2")),
            request_timeout=float(os.getenv("SCIMESH_REQUEST_TIMEOUT", "30")),
            heartbeat_interval=float(os.getenv("SCIMESH_HEARTBEAT_INTERVAL", "15")),
            bearer_token=os.getenv("SCIMESH_BEARER_TOKEN"),
            cleanup_after_seconds=float(cleanup) if cleanup else None,
        )
