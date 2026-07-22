from __future__ import annotations

import hashlib
from pathlib import Path

import pytest

from scimesh.worker.config import WorkerConfig
from scimesh.worker.coordinator import CoordinatorTransientError
from scimesh.worker.daemon import WorkerDaemon
from scimesh.worker.models import ClaimedTask, InputArtifact, ProducedArtifact, RunResult


class FakeCoordinator:
    def __init__(self, task: ClaimedTask | None) -> None:
        self.task, self.submissions = task, []

    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
        task, self.task = self.task, None
        return task

    def submit(self, task: ClaimedTask, payload: dict) -> None:
        self.submissions.append(payload)


class FakeArtifacts:
    def __init__(self, content: bytes) -> None:
        self.content, self.uploaded = content, []

    def download(self, uri: str, destination: Path) -> None:
        destination.write_bytes(self.content)

    def upload(self, task: ClaimedTask, artifact: ProducedArtifact) -> str:
        self.uploaded.append(artifact.path)
        return f"https://example.test/results/{artifact.path.name}"


class FakeRunner:
    def __init__(self) -> None:
        self.calls = 0

    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
        self.calls += 1
        output = task_dir / "result.csv"
        output.write_text("id,score\na,1\n", encoding="utf-8")
        return RunResult((ProducedArtifact(output, "text/csv"),), {"processed_rows": 1})


def make_task(content: bytes, checksum: str | None = None) -> ClaimedTask:
    return ClaimedTask("task-1", 1, "2026-07-30T00:00:00Z", "similarity-search", InputArtifact("https://example.test/input", checksum or hashlib.sha256(content).hexdigest()), {"query_id": "CHEMBL1"})


def daemon(tmp_path: Path, task: ClaimedTask | None, content: bytes):
    coordinator, artifacts, runner = FakeCoordinator(task), FakeArtifacts(content), FakeRunner()
    config = WorkerConfig("https://example.test", "worker-1", tmp_path / "work")
    return WorkerDaemon(config, coordinator, artifacts, runner), coordinator, artifacts, runner, config


def test_claims_runs_uploads_and_submits_csv(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, coordinator, artifacts, runner, _ = daemon(tmp_path, make_task(content), content)
    assert worker.run_once() is True
    assert runner.calls == 1
    assert len(artifacts.uploaded) == 1
    assert coordinator.submissions[0]["status"] == "completed"
    assert coordinator.submissions[0]["result"]["content_type"] == "text/csv"


def test_no_task_does_not_create_directory(tmp_path: Path) -> None:
    worker, _, _, runner, config = daemon(tmp_path, None, b"")
    assert worker.run_once() is False
    assert runner.calls == 0
    assert not config.work_dir.exists()


def test_bad_checksum_reports_failure_without_running(tmp_path: Path) -> None:
    worker, coordinator, _, runner, _ = daemon(tmp_path, make_task(b"actual", "not-the-hash"), b"actual")
    assert worker.run_once() is True
    assert runner.calls == 0
    assert coordinator.submissions[0]["status"] == "failed"
    assert coordinator.submissions[0]["error_code"] == "ValueError"


def test_transient_claim_error_is_propagated_for_bounded_backoff(tmp_path: Path) -> None:
    class UnavailableCoordinator(FakeCoordinator):
        def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
            raise CoordinatorTransientError("temporary outage")

    worker, _, _, _, _ = daemon(tmp_path, None, b"")
    worker.coordinator = UnavailableCoordinator(None)
    with pytest.raises(CoordinatorTransientError):
        worker.run_once()


def test_task_directories_are_retained_until_cleanup_is_enabled(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, _, _, _, config = daemon(tmp_path, make_task(content), content)
    worker.run_once()
    task_dir = config.work_dir / "task-1" / "1"
    assert task_dir.is_dir()
    worker.config = WorkerConfig(**{**config.__dict__, "cleanup_after_seconds": 0})
    worker._cleanup_expired_directories()
    assert not task_dir.exists()
