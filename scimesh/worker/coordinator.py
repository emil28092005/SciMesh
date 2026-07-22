"""HTTP boundary for the coordinator; the daemon never accesses a database."""

from __future__ import annotations

import json
from typing import Any, Protocol
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from .models import ClaimedTask


class CoordinatorError(RuntimeError):
    """A non-retriable coordinator response."""


class CoordinatorTransientError(CoordinatorError):
    """A timeout, connection error, or 5xx coordinator response."""


class CoordinatorClient(Protocol):
    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None: ...

    def submit(self, task: ClaimedTask, payload: dict[str, Any]) -> None: ...

    def fail(self, task: ClaimedTask, payload: dict[str, Any]) -> None: ...

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> str: ...


class HttpCoordinatorClient:
    def __init__(self, base_url: str, timeout: float, bearer_token: str | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.bearer_token = bearer_token

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
            raise CoordinatorError(f"result rejected with status {status}")

    def fail(self, task: ClaimedTask, payload: dict[str, Any]) -> None:
        status, _ = self._request("POST", f"/tasks/{task.task_id}/failure", payload)
        if status not in (200, 201, 202):
            raise CoordinatorError(f"failure report rejected with status {status}")

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> str:
        status, body = self._request(
            "POST", f"/tasks/{task.task_id}/heartbeat",
            {"worker_id": worker_id, "attempt": task.attempt},
        )
        if status != 200:
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
            with urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
                return response.status, json.loads(raw) if raw else {}
        except HTTPError as error:
            if error.code >= 500:
                raise CoordinatorTransientError(f"coordinator returned {error.code}") from error
            return error.code, {}
        except (URLError, TimeoutError) as error:
            raise CoordinatorTransientError("coordinator request failed") from error

    def _auth_header(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.bearer_token}"} if self.bearer_token else {}
