"""The worker state machine and its safe failure handling."""

from __future__ import annotations

import logging
from pathlib import Path
import random
import shutil
import time

from .artifacts import ArtifactClient, sha256_file
from .config import WorkerConfig
from .coordinator import CoordinatorClient, CoordinatorTransientError
from .models import ClaimedTask
from .runners import Runner


class WorkerDaemon:
    def __init__(self, config: WorkerConfig, coordinator: CoordinatorClient, artifacts: ArtifactClient, runner: Runner) -> None:
        self.config, self.coordinator, self.artifacts, self.runner = config, coordinator, artifacts, runner
        self.log = logging.getLogger("scimesh.worker")

    def run_forever(self) -> None:
        failures = 0
        while True:
            try:
                self._cleanup_expired_directories()
                claimed = self.run_once()
                failures = 0
                if not claimed:
                    self._sleep(self.config.poll_interval)
            except CoordinatorTransientError as error:
                failures += 1
                self._log("failed", error_type=type(error).__name__)
                self._sleep(min(self.config.poll_interval * 2 ** min(failures, 6), 60.0))

    def run_once(self) -> bool:
        self._log("claiming")
        task = self.coordinator.claim(self.config.worker_id, self.config.capabilities)
        if task is None:
            self._log("idle")
            return False
        started = time.monotonic()
        task_dir = self.config.work_dir / task.task_id / str(task.attempt)
        task_dir.mkdir(parents=True, exist_ok=False)
        try:
            self._log("downloading", task)
            input_path = task_dir / "input"
            self.artifacts.download(task.input.uri, input_path)
            if sha256_file(input_path).lower() != task.input.sha256.lower():
                raise ValueError("input checksum mismatch")
            self._log("running", task)
            result = self.runner.run(task, task_dir)
            self._log("uploading", task)
            manifests = [
                {"uri": self.artifacts.upload(task, artifact), "sha256": sha256_file(artifact.path), "content_type": artifact.content_type}
                for artifact in result.artifacts
            ]
            if not manifests:
                raise ValueError("runner produced no artifacts")
            self._log("submitting", task)
            self.coordinator.submit(task, {"worker_id": self.config.worker_id, "attempt": task.attempt, "status": "completed", "result": manifests[0], "artifacts": manifests, "metrics": {**result.metrics, "elapsed_seconds": round(time.monotonic() - started, 3)}})
            self._log("idle", task, elapsed_seconds=round(time.monotonic() - started, 3))
        except Exception as error:
            self._log("failed", task, error_type=type(error).__name__)
            self._report_failure(task, error)
        return True

    def _report_failure(self, task: ClaimedTask, error: Exception) -> None:
        message = str(error).replace(str(self.config.work_dir), "<worker-dir>")[:300]
        try:
            self.coordinator.submit(task, {"worker_id": self.config.worker_id, "attempt": task.attempt, "status": "failed", "error_code": type(error).__name__, "error_message": message})
        except CoordinatorTransientError:
            raise
        except Exception:
            self._log("failed", task, error_type="FailureReportError")

    def _log(self, state: str, task: ClaimedTask | None = None, **extra: object) -> None:
        fields = {"worker_id": self.config.worker_id, "task_id": task.task_id if task else None, "attempt": task.attempt if task else None, "state": state, **extra}
        self.log.info("worker_event %s", fields)

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
