"""Tests for the per-task entry point used by the Go worker agent."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


from scimesh.worker.task import _EXIT_PERMANENT, _EXIT_RETRYABLE


def _task_json(path: Path, *, query_smiles: str = "CCO") -> dict[str, object]:
    return {
        "task_id": "11111111-1111-4111-8111-111111111111",
        "attempt": 1,
        "lease_expires_at": "2026-08-02T00:00:00Z",
        "workload": "similarity-search",
        "input": {
            "uri": "/tasks/11111111-1111-4111-8111-111111111111/input",
            "sha256": "a" * 64,
        },
        "parameters": {"query_smiles": query_smiles, "top_k": 5},
    }


def _run_task(root: Path, task: dict[str, object]) -> subprocess.CompletedProcess[str]:
    task_path = root / "task.json"
    task_path.write_text(json.dumps(task), encoding="utf-8")
    output = root / "manifest.json"
    return subprocess.run(
        [
            sys.executable,
            "-m",
            "scimesh.worker.task",
            "--task-json",
            str(task_path),
            "--task-dir",
            str(root),
            "--output",
            str(output),
        ],
        capture_output=True,
        text=True,
    )


def test_task_runner_writes_a_result_manifest(tmp_path: Path) -> None:
    content = b"chembl_id\tcanonical_smiles\nQUERY\tCCO\nMATCH\tCCCO\n"
    (tmp_path / "input").write_bytes(content)
    result = _run_task(tmp_path, _task_json(tmp_path))

    assert result.returncode == 0, result.stderr
    manifest = json.loads((tmp_path / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["content_type"] == "text/csv"
    assert manifest["metrics"]["matches_emitted"] == 1
    artifact = Path(manifest["artifact_path"])
    assert artifact.is_file()
    header = artifact.read_text(encoding="utf-8").splitlines()[0]
    assert header == "rank,chembl_id,canonical_smiles,similarity"


def test_task_runner_exits_permanent_on_invalid_input(tmp_path: Path) -> None:
    (tmp_path / "input").write_text(
        "chembl_id\tcanonical_smiles\nQUERY\tCCO\n", encoding="utf-8"
    )
    result = _run_task(tmp_path, _task_json(tmp_path, query_smiles="not-a-smiles"))
    assert result.returncode == _EXIT_PERMANENT
    assert "permanent:" in result.stderr
    assert not (tmp_path / "manifest.json").exists()


def test_task_runner_exits_retryable_on_unexpected_failure(
    tmp_path: Path,
) -> None:
    content = b"chembl_id\tcanonical_smiles\nQUERY\tCCO\n"
    (tmp_path / "input").write_bytes(content)
    task_path = tmp_path / "task.json"
    task_path.write_text(json.dumps(_task_json(tmp_path)), encoding="utf-8")
    output = tmp_path / "manifest.json"
    args = [
        "--task-json",
        str(task_path),
        "--task-dir",
        str(tmp_path),
        "--output",
        str(output),
    ]
    code = (
        "import sys\n"
        "import scimesh.worker.task as t\n"
        "from scimesh.worker.runners import SciMeshRunner\n"
        "def boom(self, task, task_dir):\n"
        "    raise RuntimeError('simulated')\n"
        "SciMeshRunner.run = boom\n"
        f"sys.exit(t.main({args!r}))\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", code],
        capture_output=True,
        text=True,
    )
    assert result.returncode == _EXIT_RETRYABLE
    assert "retryable:" in result.stderr
    assert not output.exists()


def test_task_runner_rejects_malformed_task_payload(tmp_path: Path) -> None:
    task_path = tmp_path / "task.json"
    task_path.write_text('{"broken":', encoding="utf-8")
    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "scimesh.worker.task",
            "--task-json",
            str(task_path),
            "--task-dir",
            str(tmp_path),
            "--output",
            str(tmp_path / "manifest.json"),
        ],
        capture_output=True,
        text=True,
    )
    assert result.returncode == _EXIT_PERMANENT
