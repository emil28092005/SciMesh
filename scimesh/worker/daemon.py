"""The worker state machine and its safe failure handling."""

from __future__ import annotations

import logging
from dataclasses import replace
from pathlib import Path
import random
import shutil
import threading
import time
from datetime import datetime, timezone

from .artifacts import ArtifactClient, sha256_file
from .config import WorkerConfig
from .coordinator import CoordinatorClient, CoordinatorConflictError, CoordinatorTransientError
from .models import ClaimedTask, UploadedArtifact
from .runners import Runner


class LeaseHeartbeat:
    """Renews a claimed task lease while local work is in progress."""

    def __init__(self, task: ClaimedTask, coordinator: CoordinatorClient, config: WorkerConfig) -> None:
        self.task, self.coordinator, self.config = task, coordinator, config
        self._stop = threading.Event()
        self._error: Exception | None = None
        self._thread: threading.Thread | None = None
        self._lease_expires_at = task.lease_expires_at

    def start(self) -> None:
        # Verify ownership before expensive download or calculation begins.
        self._lease_expires_at = self.coordinator.heartbeat(
            self.task, self.config.worker_id
        )
        self._next_delay()
        self._thread = threading.Thread(target=self._run, name=f"lease-{self.task.task_id}", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join()

    def raise_if_failed(self) -> None:
        if self._error:
            raise self._error

    def _run(self) -> None:
        delay = self._next_delay()
        while not self._stop.wait(max(delay, 0.01)):
            try:
                self._lease_expires_at = self.coordinator.heartbeat(
                    self.task, self.config.worker_id
                )
                delay = self._next_delay()
            except Exception as error:  # Surface the lease loss in the main state machine.
                self._error = error
                return

    def _next_delay(self) -> float:
        return min(self.config.heartbeat_interval, self._seconds_until_expiry() / 2)

    def _seconds_until_expiry(self) -> float:
        try:
            expiry = datetime.fromisoformat(self._lease_expires_at.replace("Z", "+00:00"))
        except ValueError as error:
            raise ValueError("invalid lease_expires_at") from error
        seconds = (expiry - datetime.now(timezone.utc)).total_seconds()
        if seconds <= 0:
            raise ValueError("claimed task lease has already expired")
        return seconds


class WorkerDaemon:
    def __init__(self, config: WorkerConfig, coordinator: CoordinatorClient, artifacts: ArtifactClient, runner: Runner) -> None:
        self.config, self.coordinator, self.artifacts, self.runner = config, coordinator, artifacts, runner
        self.worker_id = config.worker_id
        self._registered = False
        self.log = logging.getLogger("scimesh.worker")

    def run_forever(self) -> None:
        failures = 0
        processed_tasks = 0
        self._log(
            "started",
            max_tasks=self.config.max_tasks,
            exit_when_idle=self.config.exit_when_idle,
        )
        try:
            while True:
                try:
                    if not self._registered:
                        self._register_worker()
                    self._cleanup_expired_directories()
                    claimed = self.run_once()
                    failures = 0
                    if claimed:
                        processed_tasks += 1
                        if self.config.max_tasks is not None and processed_tasks >= self.config.max_tasks:
                            self._log("stopped", reason="max_tasks_reached", processed_tasks=processed_tasks)
                            return
                    elif self.config.exit_when_idle:
                        self._log("stopped", reason="queue_empty", processed_tasks=processed_tasks)
                        return
                    else:
                        self._sleep(self.config.poll_interval)
                except CoordinatorTransientError as error:
                    failures += 1
                    self._log("failed", error_type=type(error).__name__)
                    self._sleep(min(self.config.poll_interval * 2 ** min(failures, 6), 60.0))
        except KeyboardInterrupt:
            self._log("stopped", reason="interrupted", processed_tasks=processed_tasks)

    def run_once(self) -> bool:
        worker_id = self._worker_id()
        self._log("claiming", log_level=logging.DEBUG)
        task = self.coordinator.claim(worker_id, self.config.capabilities)
        if task is None:
            self._log("idle", log_level=logging.DEBUG)
            return False
        started = time.monotonic()
        task_dir = self.config.work_dir / task.task_id / str(task.attempt)
        heartbeat = LeaseHeartbeat(task, self.coordinator, self.config)
        try:
            task_dir.mkdir(parents=True, exist_ok=False)
            heartbeat.start()
            self._log("downloading", task)
            input_path = task_dir / "input"
            self.artifacts.download(task.input.uri, input_path)
            if sha256_file(input_path).lower() != task.input.sha256.lower():
                raise ValueError("input checksum mismatch")
            self._log("running", task)
            result = self.runner.run(task, task_dir)
            heartbeat.raise_if_failed()
            if len(result.artifacts) != 1:
                raise ValueError("runner must produce exactly one result artifact")
            artifact = result.artifacts[0]
            uploaded = self.artifacts.upload(task, worker_id, artifact)
            manifest = self._result_manifest(uploaded)
            self._log("submitting", task)
            heartbeat.raise_if_failed()
            self.coordinator.submit(
                task,
                {
                    "worker_id": worker_id,
                    "attempt": task.attempt,
                    "result": manifest,
                    "metrics": {
                        **result.metrics,
                        "elapsed_seconds": round(time.monotonic() - started, 3),
                    },
                },
            )
            self._log("completed", task, elapsed_seconds=round(time.monotonic() - started, 3))
        except CoordinatorConflictError as error:
            self._log("lease_lost", task, error_type=type(error).__name__)
        except Exception as error:
            self._log("failed", task, error_type=type(error).__name__)
            self._report_failure(task, error)
        finally:
            heartbeat.stop()
        return True

    def _report_failure(self, task: ClaimedTask, error: Exception) -> None:
        message = str(error).replace(str(self.config.work_dir), "<worker-dir>")[:300]
        try:
            self.coordinator.fail(task, {"worker_id": self._worker_id(), "attempt": task.attempt, "error_code": type(error).__name__, "error_message": message})
        except CoordinatorTransientError:
            raise
        except Exception:
            self._log("failed", task, error_type="FailureReportError")

    def _register_worker(self) -> None:
        registered = self.coordinator.register(
            self.config.worker_name,
            self.config.capabilities,
            self.config.cpu_count,
            self.config.memory_mb,
        )
        self.worker_id = registered.worker_id
        self.config = replace(
            self.config,
            worker_id=registered.worker_id,
            heartbeat_interval=registered.heartbeat_interval_seconds,
        )
        self._registered = True
        self._log("registered")

    def _worker_id(self) -> str:
        if not self.worker_id:
            raise ValueError("worker is not registered")
        return self.worker_id

    @staticmethod
    def _result_manifest(uploaded: UploadedArtifact) -> dict[str, object]:
        """Keep completion payload exact: coordinator owns all artifact metadata."""
        return {"artifact_id": uploaded.artifact_id}

    def _log(
        self,
        state: str,
        task: ClaimedTask | None = None,
        *,
        log_level: int = logging.INFO,
        **extra: object,
    ) -> None:
        fields = {"worker_id": self.config.worker_id, "task_id": task.task_id if task else None, "attempt": task.attempt if task else None, "state": state, **extra}
        self.log.log(log_level, "worker_event %s", fields)

    def _cleanup_expired_directories(self) -> None:
        """Remove only old task attempt directories when retention was configured."""
        if self.config.cleanup_after_seconds is None or not self.config.work_dir.exists():
            return
        cutoff = time.time() - self.config.cleanup_after_seconds
        for task_dir in self.config.work_dir.iterdir():
            if not task_dir.is_dir():
                continue
            for attempt_dir in task_dir.iterdir():
                if attempt_dir.is_dir() and attempt_dir.stat().st_mtime < cutoff:
                    shutil.rmtree(attempt_dir)
            if not any(task_dir.iterdir()):
                task_dir.rmdir()

    @staticmethod
    def _sleep(delay: float) -> None:
        time.sleep(delay * random.uniform(0.75, 1.25))
