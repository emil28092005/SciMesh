"""Configuration parsing for the worker command."""

from __future__ import annotations

import json
from dataclasses import dataclass
from math import isfinite
from pathlib import Path
import os
import socket
from typing import Mapping
from urllib.parse import urlsplit

from scimesh.sdk.registry import AllowedPackage, workload_allowlist_from_json


def _clean_url(value: object | None) -> str | None:
    """Normalise an optional URL: drop a blank one, strip a trailing slash."""
    if value is None:
        return None
    text = str(value).strip()
    return text.rstrip("/") or None


def _int_value(value: object | None, name: str) -> int | None:
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float, str)):
        raise ValueError(f"{name} must be a number")
    return int(value)


def _float_value(value: object | None, name: str) -> float | None:
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float, str)):
        raise ValueError(f"{name} must be a number")
    return float(value)


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


def _capabilities(value: object) -> tuple[str, ...]:
    """Parse a comma-separated capability list into unique non-empty names."""
    if value is None:
        return ("similarity-search", "similarity_search")
    if not isinstance(value, str) or not value.strip():
        raise ValueError("capabilities must be a comma-separated list")
    names = tuple(
        dict.fromkeys(item.strip() for item in value.split(",") if item.strip())
    )
    if not names:
        raise ValueError("capabilities cannot be empty")
    return names


def _workload_allowlist(value: object) -> tuple[AllowedPackage, ...]:
    return workload_allowlist_from_json(value)


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
    # A long-lived per-user credential. When set (with userservice_url), the
    # worker exchanges it for short-lived JWTs instead of using bearer_token,
    # binding the worker to that user's account.
    worker_key: str | None = None
    userservice_url: str | None = None
    cleanup_after_seconds: float | None = None
    max_tasks: int | None = None
    exit_when_idle: bool = False
    # Distributed similarity-graph requires triangular block-pair planning and
    # is deliberately not advertised until CTX-10. A normal worker must never
    # make a multi-shard graph job appear scientifically complete.
    # The local CLI uses hyphens; the first coordinator contract used
    # underscores, so retain the search alias during migration.
    capabilities: tuple[str, ...] = (
        "similarity-search",
        "similarity_search",
    )
    # Optional allowlist of installed SDK workload packages to execute. When
    # empty, the worker runs the built-in similarity-search only. Entries are
    # ``{distribution, name, version, digest}`` JSON objects matching the
    # installed ``scimesh.workloads`` entry points.
    workload_allowlist: tuple[AllowedPackage, ...] = ()

    def __post_init__(self) -> None:
        parsed = urlsplit(self.coordinator_url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError("coordinator_url must be an absolute HTTP(S) URL")
        if not isinstance(self.worker_name, str) or not self.worker_name.strip():
            raise ValueError("worker_name must be non-empty")
        if self.userservice_url is not None:
            us = urlsplit(self.userservice_url)
            if us.scheme not in {"http", "https"} or not us.hostname:
                raise ValueError("userservice_url must be an absolute HTTP(S) URL")
        if self.worker_key is not None and not self.userservice_url:
            raise ValueError(
                "worker_key requires userservice_url (SCIMESH_USERSERVICE_URL)"
            )
        if (
            isinstance(self.cpu_count, bool)
            or not isinstance(self.cpu_count, int)
            or self.cpu_count < 1
        ):
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
            _positive_number(
                self.cleanup_after_seconds, "cleanup_after_seconds", allow_zero=True
            )
        if self.max_tasks is not None:
            if (
                isinstance(self.max_tasks, bool)
                or not isinstance(self.max_tasks, int)
                or self.max_tasks < 1
            ):
                raise ValueError("max_tasks must be positive when set")
        if not isinstance(self.exit_when_idle, bool):
            raise ValueError("exit_when_idle must be a boolean")
        if not self.capabilities:
            raise ValueError("capabilities cannot be empty")
        if any(
            not isinstance(capability, str) or not capability.strip()
            for capability in self.capabilities
        ):
            raise ValueError("capabilities must contain non-empty names")
        if len(self.capabilities) != len(set(self.capabilities)):
            raise ValueError("capabilities must be unique")
        if any(
            not isinstance(package, AllowedPackage)
            for package in self.workload_allowlist
        ):
            raise ValueError("workload_allowlist must contain AllowedPackage values")
        # Runner subprocesses use a task directory as their cwd. Keep the
        # configured root absolute so input/output paths remain valid there
        # even when the CLI received a convenient relative --work-dir value.
        object.__setattr__(self, "work_dir", self.work_dir.expanduser().resolve())

    @classmethod
    def from_environment(
        cls, overrides: Mapping[str, object] | None = None
    ) -> "WorkerConfig":
        """Build config from environment, allowing typed CLI values to override it."""
        values = overrides or {}

        def value(
            name: str, environment: str, default: object | None = None
        ) -> object | None:
            override = values.get(name)
            return override if override is not None else os.getenv(environment, default)

        url = value("coordinator_url", "SCIMESH_COORDINATOR_URL")
        if not isinstance(url, str) or not url:
            raise ValueError("SCIMESH_COORDINATOR_URL or --coordinator-url is required")
        cleanup = value("cleanup_after_seconds", "SCIMESH_CLEANUP_AFTER_SECONDS")
        cpu_count = value("cpu_count", "SCIMESH_CPU_COUNT", os.cpu_count() or 1)
        memory_mb = value("memory_mb", "SCIMESH_MEMORY_MB")
        max_tasks = value("max_tasks", "SCIMESH_MAX_TASKS")
        capabilities = value("capabilities", "SCIMESH_CAPABILITIES")
        allowlist = value("workload_allowlist", "SCIMESH_WORKLOAD_ALLOWLIST")
        worker_id = value("worker_id", "SCIMESH_WORKER_ID")
        work_dir = value("work_dir", "SCIMESH_WORK_DIR", "./scimesh-worker-data")
        worker_name = value("worker_name", "SCIMESH_WORKER_NAME", socket.gethostname())
        poll_interval = value("poll_interval", "SCIMESH_POLL_INTERVAL", "2")
        request_timeout = value("request_timeout", "SCIMESH_REQUEST_TIMEOUT", "30")
        heartbeat_interval = value(
            "heartbeat_interval", "SCIMESH_HEARTBEAT_INTERVAL", "15"
        )
        bearer_token = value("bearer_token", "SCIMESH_BEARER_TOKEN")
        worker_key = value("worker_key", "SCIMESH_WORKER_KEY")
        userservice_url = _clean_url(
            value("userservice_url", "SCIMESH_USERSERVICE_URL")
        )
        return cls(
            coordinator_url=url.rstrip("/"),
            worker_id=str(worker_id) if worker_id is not None else None,
            work_dir=Path(str(work_dir)),
            worker_name=str(worker_name),
            cpu_count=_int_value(cpu_count, "cpu_count") or 1,
            memory_mb=_int_value(memory_mb, "memory_mb"),
            poll_interval=_float_value(poll_interval, "poll_interval") or 2.0,
            request_timeout=_float_value(request_timeout, "request_timeout") or 30.0,
            heartbeat_interval=_float_value(heartbeat_interval, "heartbeat_interval") or 15.0,
            bearer_token=str(bearer_token) if bearer_token is not None else None,
            worker_key=str(worker_key) if worker_key is not None else None,
            userservice_url=userservice_url,
            cleanup_after_seconds=_float_value(cleanup, "cleanup_after_seconds"),
            max_tasks=_int_value(max_tasks, "max_tasks"),
            exit_when_idle=bool(values.get("exit_when_idle", False)),
            capabilities=_capabilities(capabilities),
            workload_allowlist=_workload_allowlist(allowlist),
        )
