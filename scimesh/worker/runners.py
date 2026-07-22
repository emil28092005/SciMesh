"""Local workload adapters.  They receive no arbitrary commands from the network."""

from __future__ import annotations

from pathlib import Path
import subprocess
import sys
from typing import Protocol

from .models import ClaimedTask, ProducedArtifact, RunResult


class Runner(Protocol):
    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult: ...


class SciMeshRunner:
    """Allowlisted adapter from coordinator workloads to the local SciMesh CLI."""

    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
        input_path = task_dir / "input"
        output_path = task_dir / "result.csv"
        command = [sys.executable, "-m", "scimesh.cli", task.workload, str(input_path)]
        params = task.parameters
        if task.workload == "similarity-search":
            query_id = self._string(params, "query_id")
            top_k = self._positive_int(params, "top_k", default=20)
            command += ["--query-id", query_id, "--top-k", str(top_k)]
        elif task.workload == "similarity-graph":
            threshold = self._number(params, "threshold")
            command += ["--threshold", str(threshold)]
        else:
            raise ValueError(f"unsupported workload: {task.workload}")
        command += ["--output", str(output_path)]
        subprocess.run(command, check=True, cwd=task_dir)  # explicit list: never shell=True
        if not output_path.is_file():
            raise RuntimeError("SciMesh CLI did not create its result")
        processed_rows = max(sum(1 for _ in output_path.open(encoding="utf-8")) - 1, 0)
        return RunResult((ProducedArtifact(output_path, "text/csv"),), {"processed_rows": processed_rows})

    @staticmethod
    def _string(params: dict[str, object], name: str) -> str:
        value = params.get(name)
        if not isinstance(value, str) or not value.strip() or len(value) > 200:
            raise ValueError(f"{name} must be a non-empty string")
        return value

    @staticmethod
    def _positive_int(params: dict[str, object], name: str, default: int) -> int:
        value = params.get(name, default)
        if isinstance(value, bool) or not isinstance(value, int) or value < 1 or value > 100_000:
            raise ValueError(f"{name} must be a positive integer")
        return value

    @staticmethod
    def _number(params: dict[str, object], name: str) -> float:
        value = params.get(name)
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not 0 <= value <= 1:
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)
