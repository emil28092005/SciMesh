"""HTTP boundary for the coordinator; the daemon never accesses a database."""

from __future__ import annotations

import json
from typing import Any, Protocol
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener

from .models import ClaimedTask, RegisteredWorker
from .transport import NoRedirectHandler


class CoordinatorError(RuntimeError):
    """A non-retriable coordinator response."""


class CoordinatorTransientError(CoordinatorError):
    """A timeout, connection error, or 5xx coordinator response."""


class CoordinatorConflictError(CoordinatorError):
    """The worker no longer owns the task lease or attempted a conflicting mutation."""


class CoordinatorClient(Protocol):
    def register(
        self, name: str, capabilities: tuple[str, ...], cpu_count: int, memory_mb: int | None
    ) -> RegisteredWorker: ...

    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None: ...

    def submit(self, task: ClaimedTask, payload: dict[str, Any]) -> None: ...

    def fail(self, task: ClaimedTask, payload: dict[str, Any]) -> None: ...

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> str: ...


class HttpCoordinatorClient:
    def __init__(self, base_url: str, timeout: float, bearer_token: str | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.bearer_token = bearer_token
        self._opener = build_opener(NoRedirectHandler())

    def register(
        self, name: str, capabilities: tuple[str, ...], cpu_count: int, memory_mb: int | None
    ) -> RegisteredWorker:
        payload: dict[str, Any] = {
            "name": name,
            "capabilities": list(capabilities),
            "cpu_count": cpu_count,
        }
        if memory_mb is not None:
            payload["memory_mb"] = memory_mb
        status, body = self._request("POST", "/workers/register", payload)
        if status != 200:
            raise CoordinatorError(f"worker registration rejected with status {status}")
        try:
            return RegisteredWorker.from_json(body)
        except ValueError as error:
            raise CoordinatorError("invalid worker registration response") from error

    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
        status, body = self._request("POST", "/tasks/claim", {
            "worker_id": worker_id, "capabilities": list(capabilities), "max_concurrency": 1,
        })
        if status == 204:
            return None
        if status != 200:
            raise CoordinatorError(f"unexpected claim status {status}")
        return ClaimedTask.from_json(body)

    def submit(self, task: ClaimedTask, payload: dict[str, Any]) -> None:
        status, _ = self._request("POST", f"/tasks/{task.task_id}/result", payload)
        # 200/201/202 include a successful or idempotent duplicate result response.
        if status not in (200, 201, 202):
            if status == 409:
                raise CoordinatorConflictError("result rejected because the task lease was lost")
            raise CoordinatorError(f"result rejected with status {status}")

    def fail(self, task: ClaimedTask, payload: dict[str, Any]) -> None:
        status, _ = self._request("POST", f"/tasks/{task.task_id}/failure", payload)
        if status not in (200, 201, 202):
            if status == 409:
                raise CoordinatorConflictError("failure rejected because the task lease was lost")
            raise CoordinatorError(f"failure report rejected with status {status}")

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> str:
        status, body = self._request(
            "POST", f"/tasks/{task.task_id}/heartbeat",
            {"worker_id": worker_id, "attempt": task.attempt},
        )
        if status != 200:
            if status == 409:
                raise CoordinatorConflictError("heartbeat rejected because the task lease was lost")
            raise CoordinatorError(f"heartbeat rejected with status {status}")
        lease_expires_at = body.get("lease_expires_at")
        if not isinstance(lease_expires_at, str):
            raise CoordinatorError("heartbeat response is missing lease_expires_at")
        return lease_expires_at

    def _request(self, method: str, path: str, payload: dict[str, Any]) -> tuple[int, dict[str, Any]]:
        request = Request(
            f"{self.base_url}{path}", data=json.dumps(payload).encode(), method=method,
            headers={"Content-Type": "application/json", **self._auth_header()},
        )
        try:
            with self._opener.open(request, timeout=self.timeout) as response:
                raw = response.read()
                try:
                    return response.status, json.loads(raw) if raw else {}
                except json.JSONDecodeError as error:
                    raise CoordinatorError("coordinator returned invalid JSON") from error
        except HTTPError as error:
            if error.code >= 500:
                raise CoordinatorTransientError(f"coordinator returned {error.code}") from error
            return error.code, {}
        except (URLError, TimeoutError) as error:
            raise CoordinatorTransientError("coordinator request failed") from error

    def _auth_header(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.bearer_token}"} if self.bearer_token else {}
