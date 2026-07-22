from __future__ import annotations

import hashlib
from pathlib import Path
import time
from urllib.request import Request

import pytest

from scimesh.worker.config import WorkerConfig
from scimesh.worker.coordinator import CoordinatorTransientError
from scimesh.worker.daemon import WorkerDaemon
from scimesh.worker.models import ClaimedTask, InputArtifact, ProducedArtifact, RunResult
from scimesh.worker.artifacts import HttpArtifactClient, _SameOriginAuthRedirectHandler, _origin
from scimesh.worker.runners import SciMeshRunner


class FakeCoordinator:
    def __init__(self, task: ClaimedTask | None) -> None:
        self.task, self.submissions, self.heartbeats = task, [], []

    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
        task, self.task = self.task, None
        return task

    def submit(self, task: ClaimedTask, payload: dict) -> None:
        self.submissions.append(payload)

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> None:
        self.heartbeats.append((task.task_id, task.attempt, worker_id))


class FakeArtifacts:
    def __init__(self, content: bytes) -> None:
        self.content = content

    def download(self, uri: str, destination: Path) -> None:
        destination.write_bytes(self.content)

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
    assert coordinator.heartbeats == [("task-1", 1, "worker-1")]
    assert coordinator.submissions[0]["status"] == "completed"
    assert coordinator.submissions[0]["result"]["content_type"] == "text/csv"
    assert coordinator.submissions[0]["result"]["uri"].startswith("worker://worker-1/")


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


def test_input_token_is_sent_only_to_the_coordinator_origin() -> None:
    client = HttpArtifactClient("https://coordinator.example/api", 10, "secret")
    assert client._auth_headers_for("https://coordinator.example/tasks/1/input") == {"Authorization": "Bearer secret"}
    assert client._auth_headers_for("https://bucket.example/presigned") == {}


def test_redirect_to_external_storage_strips_authorization() -> None:
    handler = _SameOriginAuthRedirectHandler(_origin("https://coordinator.example"))
    source = Request(
        "https://coordinator.example/tasks/1/input", headers={"Authorization": "Bearer secret"}
    )
    redirected = handler.redirect_request(source, None, 302, "Found", {}, "https://bucket.example/presigned")
    assert redirected is not None
    assert redirected.get_header("Authorization") is None


def test_lease_is_renewed_while_a_runner_is_still_working(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, coordinator, _, _, config = daemon(tmp_path, make_task(content), content)

    class SlowRunner(FakeRunner):
        def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
            time.sleep(0.05)
            return super().run(task, task_dir)

    worker.runner = SlowRunner()
    worker.config = WorkerConfig(**{**config.__dict__, "heartbeat_interval": 0.01})
    worker.run_once()
    assert len(coordinator.heartbeats) >= 2


def test_runner_maps_graph_and_smiles_search_parameters(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    commands: list[list[str]] = []

    def fake_run(command: list[str], **_: object) -> None:
        commands.append(command)
        output = Path(command[command.index("--output") + 1])
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text("a,b\n", encoding="utf-8")

    monkeypatch.setattr("scimesh.worker.runners.subprocess.run", fake_run)
    runner = SciMeshRunner()
    graph = ClaimedTask("graph", 1, "2026-07-30T00:00:00Z", "similarity-graph", InputArtifact("https://example/input", "x"), {"threshold": 0.2, "threshold_direction": "less", "block_size": 42, "max_rows": 7, "progress_every": 0})
    search = ClaimedTask("search", 1, "2026-07-30T00:00:00Z", "similarity-search", InputArtifact("https://example/input", "x"), {"query_smiles": "CCO", "top_k": 3})
    runner.run(graph, tmp_path / "graph")
    runner.run(search, tmp_path / "search")
    assert "--threshold-direction" in commands[0] and "less" in commands[0]
    assert "--block-size" in commands[0] and "42" in commands[0]
    assert "--max-rows" in commands[0] and "7" in commands[0]
    assert "--query-smiles" in commands[1] and "CCO" in commands[1]
