from __future__ import annotations

import hashlib
import logging
from pathlib import Path
import time
from datetime import datetime, timedelta, timezone
from urllib.request import Request

import pytest

from scimesh.worker.config import WorkerConfig
from scimesh.worker import cli as worker_cli
from scimesh.worker.cli import build_parser
from scimesh.worker.coordinator import CoordinatorTransientError
from scimesh.worker.daemon import LeaseHeartbeat, RunOnceOutcome, WorkerDaemon
from scimesh.worker.models import (
    ClaimedTask,
    InputArtifact,
    ProducedArtifact,
    RegisteredWorker,
    RunResult,
    UploadedArtifact,
)
from scimesh.worker.artifacts import HttpArtifactClient, _SameOriginAuthRedirectHandler, _origin
from scimesh.worker.runners import SciMeshRunner
from scimesh.worker.transport import NoRedirectHandler


class FakeCoordinator:
    def __init__(self, task: ClaimedTask | None) -> None:
        self.task, self.submissions, self.failures, self.heartbeats = task, [], [], []

    def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
        task, self.task = self.task, None
        return task

    def register(
        self, name: str, capabilities: tuple[str, ...], cpu_count: int, memory_mb: int | None
    ) -> RegisteredWorker:
        return RegisteredWorker("11111111-1111-4111-8111-111111111111", 15)

    def submit(self, task: ClaimedTask, payload: dict) -> None:
        self.submissions.append(payload)

    def fail(self, task: ClaimedTask, payload: dict) -> None:
        self.failures.append(payload)

    def heartbeat(self, task: ClaimedTask, worker_id: str) -> str:
        self.heartbeats.append((task.task_id, task.attempt, worker_id))
        return (datetime.now(timezone.utc) + timedelta(seconds=1)).isoformat()


class FakeArtifacts:
    def __init__(self, content: bytes) -> None:
        self.content, self.uploaded = content, []

    def download(self, uri: str, destination: Path) -> None:
        destination.write_bytes(self.content)

    def upload(
        self, task: ClaimedTask, worker_id: str, artifact: ProducedArtifact
    ) -> UploadedArtifact:
        self.uploaded.append((task.task_id, worker_id, artifact.path))
        content = artifact.path.read_bytes()
        return UploadedArtifact(
            "22222222-2222-4222-8222-222222222222",
            f"https://example.test/tasks/{task.task_id}/artifacts/{artifact.path.name}",
            hashlib.sha256(content).hexdigest(),
            len(content),
        )

class FakeRunner:
    def __init__(self) -> None:
        self.calls = 0

    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
        self.calls += 1
        output = task_dir / "result.csv"
        output.write_text("id,score\na,1\n", encoding="utf-8")
        return RunResult((ProducedArtifact(output, "text/csv"),), {"processed_rows": 1})


def make_task(content: bytes, checksum: str | None = None) -> ClaimedTask:
    lease = (datetime.now(timezone.utc) + timedelta(seconds=60)).isoformat()
    return ClaimedTask("task-1", 1, lease, "similarity-search", InputArtifact("https://example.test/input", checksum or hashlib.sha256(content).hexdigest()), {"query_id": "CHEMBL1"})


def daemon(tmp_path: Path, task: ClaimedTask | None, content: bytes):
    coordinator, artifacts, runner = FakeCoordinator(task), FakeArtifacts(content), FakeRunner()
    config = WorkerConfig("https://example.test", "worker-1", tmp_path / "work")
    return WorkerDaemon(config, coordinator, artifacts, runner), coordinator, artifacts, runner, config


def test_claims_runs_uploads_and_submits_csv(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, coordinator, artifacts, runner, _ = daemon(tmp_path, make_task(content), content)
    assert worker.run_once() == RunOnceOutcome(claimed=True, completed=True)
    assert runner.calls == 1
    assert len(artifacts.uploaded) == 1
    assert coordinator.heartbeats == [("task-1", 1, "worker-1")]
    assert "status" not in coordinator.submissions[0]
    assert coordinator.submissions[0]["result"] == {
        "artifact_id": "22222222-2222-4222-8222-222222222222"
    }


def test_no_task_does_not_create_directory(tmp_path: Path) -> None:
    worker, _, _, runner, config = daemon(tmp_path, None, b"")
    assert worker.run_once() == RunOnceOutcome(claimed=False, completed=False)
    assert runner.calls == 0
    assert not config.work_dir.exists()


def test_once_worker_exits_after_an_empty_claim(tmp_path: Path, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.INFO, logger="scimesh.worker")
    worker, _, _, runner, _ = daemon(tmp_path, None, b"")
    worker.config = WorkerConfig(**{**worker.config.__dict__, "exit_when_idle": True, "max_tasks": 1})
    assert worker.run_forever() is True
    assert runner.calls == 0
    assert "queue_empty" in caplog.text


def test_worker_stops_after_the_configured_number_of_claims(tmp_path: Path, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.INFO, logger="scimesh.worker")
    content = b"input fixture"
    worker, _, _, runner, _ = daemon(tmp_path, make_task(content), content)
    worker.config = WorkerConfig(**{**worker.config.__dict__, "max_tasks": 1})
    assert worker.run_forever() is True
    assert runner.calls == 1
    assert "max_tasks_reached" in caplog.text


def test_keyboard_interrupt_stops_worker_without_propagating(tmp_path: Path, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.INFO, logger="scimesh.worker")
    class InterruptingCoordinator(FakeCoordinator):
        def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
            raise KeyboardInterrupt

    worker, _, _, _, _ = daemon(tmp_path, None, b"")
    worker.coordinator = InterruptingCoordinator(None)
    assert worker.run_forever() is False
    assert "interrupted" in caplog.text


def test_interrupting_an_active_task_reports_a_sanitized_failure(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, coordinator, _, _, _ = daemon(tmp_path, make_task(content), content)

    class InterruptingRunner(FakeRunner):
        def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
            raise KeyboardInterrupt

    worker.runner = InterruptingRunner()
    with pytest.raises(KeyboardInterrupt):
        worker.run_once()
    assert coordinator.failures == [
        {
            "worker_id": "worker-1",
            "attempt": 1,
            "error_code": "InterruptedError",
            "error_message": "worker interrupted by operator",
        }
    ]


def test_max_tasks_counts_successes_not_failed_claims(tmp_path: Path) -> None:
    successful_content = b"successful input"

    class SequencedCoordinator(FakeCoordinator):
        def __init__(self) -> None:
            super().__init__(None)
            self.tasks = [
                make_task(b"bad input", "wrong-checksum"),
                ClaimedTask(
                    "task-2",
                    1,
                    (datetime.now(timezone.utc) + timedelta(seconds=60)).isoformat(),
                    "similarity-search",
                    InputArtifact(
                        "https://example.test/input",
                        hashlib.sha256(successful_content).hexdigest(),
                    ),
                    {"query_id": "CHEMBL1"},
                ),
            ]

        def claim(self, worker_id: str, capabilities: tuple[str, ...]) -> ClaimedTask | None:
            return self.tasks.pop(0) if self.tasks else None

    coordinator = SequencedCoordinator()
    artifacts, runner = FakeArtifacts(successful_content), FakeRunner()
    config = WorkerConfig("https://example.test", "worker-1", tmp_path / "work", max_tasks=1)
    worker = WorkerDaemon(config, coordinator, artifacts, runner)
    assert worker.run_forever() is True
    assert len(coordinator.failures) == 1
    assert len(coordinator.submissions) == 1
    assert runner.calls == 1


def test_worker_cli_lifecycle_options_are_explicit_and_exclusive() -> None:
    parser = build_parser()
    assert parser.parse_args(["--once"]).once is True
    assert parser.parse_args(["--max-tasks", "2"]).max_tasks == 2
    with pytest.raises(SystemExit):
        parser.parse_args(["--once", "--max-tasks", "2"])


def test_worker_cli_uses_a_nonzero_exit_code_for_interruption(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    class InterruptedDaemon:
        def __init__(self, *_: object) -> None:
            pass

        def run_forever(self) -> bool:
            return False

    monkeypatch.setattr(worker_cli, "WorkerDaemon", InterruptedDaemon)
    assert worker_cli.main(
        ["--coordinator-url", "https://example.test", "--work-dir", str(tmp_path)]
    ) == 130


@pytest.mark.parametrize("value", [0, -1, True])
def test_max_tasks_must_be_positive(value: object, tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="max_tasks"):
        WorkerConfig("https://example.test", None, tmp_path, max_tasks=value)  # type: ignore[arg-type]


def test_bad_checksum_reports_failure_without_running(tmp_path: Path) -> None:
    worker, coordinator, _, runner, _ = daemon(tmp_path, make_task(b"actual", "not-the-hash"), b"actual")
    assert worker.run_once() == RunOnceOutcome(claimed=True, completed=False)
    assert runner.calls == 0
    assert coordinator.failures[0]["error_code"] == "ValueError"
    assert not coordinator.submissions


def test_directory_creation_failure_is_reported(tmp_path: Path) -> None:
    content = b"input fixture"
    worker, coordinator, _, _, config = daemon(tmp_path, make_task(content), content)
    (config.work_dir / "task-1" / "1").mkdir(parents=True)
    assert worker.run_once() == RunOnceOutcome(claimed=True, completed=False)
    assert coordinator.failures[0]["error_code"] == "FileExistsError"


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


def test_relative_input_uri_is_resolved_against_the_coordinator() -> None:
    client = HttpArtifactClient("https://coordinator.example/api", 10, "secret")
    assert client._auth_headers_for("https://coordinator.example/tasks/1/input") == {
        "Authorization": "Bearer secret"
    }
    # The coordinator's contract returns root-relative artifact paths.
    assert client.coordinator_url == "https://coordinator.example/api"


def test_redirect_to_external_storage_strips_authorization() -> None:
    handler = _SameOriginAuthRedirectHandler(_origin("https://coordinator.example"))
    source = Request(
        "https://coordinator.example/tasks/1/input", headers={"Authorization": "Bearer secret"}
    )
    redirected = handler.redirect_request(source, None, 302, "Found", {}, "https://bucket.example/presigned")
    assert redirected is not None
    assert redirected.get_header("Authorization") is None


def test_api_requests_never_follow_redirects() -> None:
    handler = NoRedirectHandler()
    request = Request("https://coordinator.example/tasks/claim", headers={"Authorization": "Bearer secret"})
    assert handler.redirect_request(request, None, 302, "Found", {}, "https://other.example") is None


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


def test_heartbeat_reschedules_from_the_renewed_lease(tmp_path: Path) -> None:
    class ShortLeaseCoordinator(FakeCoordinator):
        def heartbeat(self, task: ClaimedTask, worker_id: str) -> str:
            self.heartbeats.append((task.task_id, task.attempt, worker_id))
            return (datetime.now(timezone.utc) + timedelta(seconds=0.02)).isoformat()

    config = WorkerConfig(
        "https://example.test", "worker-1", tmp_path / "work", heartbeat_interval=1
    )
    coordinator = ShortLeaseCoordinator(None)
    heartbeat = LeaseHeartbeat(make_task(b"fixture"), coordinator, config)
    heartbeat.start()
    time.sleep(0.06)
    heartbeat.stop()
    assert len(coordinator.heartbeats) >= 3


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


def test_runner_accepts_coordinator_workload_names(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    commands: list[list[str]] = []

    def fake_run(command: list[str], **_: object) -> None:
        commands.append(command)
        output = Path(command[command.index("--output") + 1])
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text("a,b\\n", encoding="utf-8")

    monkeypatch.setattr("scimesh.worker.runners.subprocess.run", fake_run)
    task = ClaimedTask(
        "search", 1, "2026-07-30T00:00:00Z", "similarity_search",
        InputArtifact("https://example/input", "a" * 64), {"query_smiles": "CCO"},
    )
    SciMeshRunner().run(task, tmp_path / "search")
    assert commands[0][3] == "similarity-search"


def test_claimed_task_rejects_path_traversal_and_invalid_metadata() -> None:
    payload = {
        "task_id": "../outside",
        "attempt": 1,
        "lease_expires_at": "2026-07-30T00:00:00Z",
        "workload": "similarity-search",
        "input": {"uri": "https://example.test/input", "sha256": "a" * 64},
        "parameters": {},
    }
    with pytest.raises(ValueError, match="invalid claimed-task response"):
        ClaimedTask.from_json(payload)

    payload["task_id"] = "11111111-1111-4111-8111-111111111111"
    payload["input"] = {"uri": "//outside.example/input", "sha256": "a" * 64}
    with pytest.raises(ValueError, match="invalid claimed-task response"):
        ClaimedTask.from_json(payload)

    payload["input"] = {"uri": "/tasks/../outside/input", "sha256": "a" * 64}
    with pytest.raises(ValueError, match="invalid claimed-task response"):
        ClaimedTask.from_json(payload)


def test_claimed_task_accepts_a_coordinator_relative_input_path() -> None:
    task = ClaimedTask.from_json(
        {
            "task_id": "11111111-1111-4111-8111-111111111111",
            "attempt": 1,
            "lease_expires_at": "2026-07-30T00:00:00Z",
            "workload": "similarity_search",
            "input": {"uri": "/tasks/11111111-1111-4111-8111-111111111111/input", "sha256": "a" * 64},
            "parameters": {},
        }
    )
    assert task.input.uri.startswith("/tasks/")


def test_uploaded_artifact_requires_complete_durable_metadata() -> None:
    artifact = UploadedArtifact.from_json(
        {
            "artifact_id": "22222222-2222-4222-8222-222222222222",
            "uri": "https://coordinator.example/artifacts/222/download",
            "sha256": "a" * 64,
            "size_bytes": 12,
        }
    )
    assert artifact.size_bytes == 12
    with pytest.raises(ValueError, match="artifact size_bytes"):
        UploadedArtifact.from_json({"artifact_id": "missing"})


def test_environment_overrides_allow_cli_only_configuration(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.delenv("SCIMESH_COORDINATOR_URL", raising=False)
    config = WorkerConfig.from_environment(
        {
            "coordinator_url": "https://coordinator.example",
            "work_dir": tmp_path,
            "worker_name": "test-worker",
        }
    )
    assert config.coordinator_url == "https://coordinator.example"
    assert config.worker_id is None
    assert "similarity-search" in config.capabilities
    assert "similarity_search" in config.capabilities


def test_relative_work_dir_is_normalized_for_runner_subprocesses(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.chdir(tmp_path)
    config = WorkerConfig("https://coordinator.example", None, Path("./worker-data"))
    assert config.work_dir == tmp_path / "worker-data"

    task_dir = config.work_dir / "task" / "1"
    task_dir.mkdir(parents=True)
    (task_dir / "input").write_text("fixture", encoding="utf-8")
    command: list[str] = []

    def fake_run(args: list[str], **_: object) -> None:
        command.extend(args)
        output = Path(args[args.index("--output") + 1])
        output.write_text("id,score\n", encoding="utf-8")

    monkeypatch.setattr("scimesh.worker.runners.subprocess.run", fake_run)
    task = ClaimedTask(
        "task", 1, "2026-07-30T00:00:00Z", "similarity-search",
        InputArtifact("https://example.test/input", "a" * 64), {"query_smiles": "CCO"},
    )
    SciMeshRunner().run(task, task_dir)
    assert command[4] == str(task_dir / "input")


def test_worker_registration_sets_returned_identity(tmp_path: Path) -> None:
    worker, _, _, _, _ = daemon(tmp_path, None, b"")
    worker._register_worker()
    assert worker.worker_id == "11111111-1111-4111-8111-111111111111"
    assert worker.config.heartbeat_interval == 15
