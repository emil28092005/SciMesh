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
            self._reject_unknown(params, {"query_id", "query_smiles", "top_k", "threshold", "threshold_direction", "max_rows", "progress_every"})
            query_id, query_smiles = params.get("query_id"), params.get("query_smiles")
            if (query_id is None) == (query_smiles is None):
                raise ValueError("exactly one of query_id or query_smiles is required")
            top_k = self._positive_int(params, "top_k", default=20)
            command += ["--query-id", self._string(params, "query_id")] if query_id is not None else ["--query-smiles", self._string(params, "query_smiles")]
            command += ["--top-k", str(top_k)]
            self._append_common_options(command, params)
        elif task.workload == "similarity-graph":
            self._reject_unknown(params, {"threshold", "threshold_direction", "block_size", "max_rows", "progress_every"})
            threshold = self._number(params, "threshold")
            command += ["--threshold", str(threshold)]
            self._append_common_options(command, params, include_threshold=False)
            if "block_size" in params:
                command += ["--block-size", str(self._positive_int(params, "block_size", default=1_000))]
        else:
            raise ValueError(f"unsupported workload: {task.workload}")
        command += ["--output", str(output_path)]
        subprocess.run(command, check=True, cwd=task_dir)  # explicit list: never shell=True
        if not output_path.is_file():
            raise RuntimeError("SciMesh CLI did not create its result")
        processed_rows = max(sum(1 for _ in output_path.open(encoding="utf-8")) - 1, 0)
        return RunResult((ProducedArtifact(output_path, "text/csv"),), {"processed_rows": processed_rows})

    def _append_common_options(self, command: list[str], params: dict[str, object], *, include_threshold: bool = True) -> None:
        if include_threshold and "threshold" in params:
            command += ["--threshold", str(self._number(params, "threshold"))]
        if "threshold_direction" in params:
            direction = params["threshold_direction"]
            if direction not in ("greater", "less"):
                raise ValueError("threshold_direction must be 'greater' or 'less'")
            command += ["--threshold-direction", str(direction)]
        if "max_rows" in params:
            command += ["--max-rows", str(self._positive_int(params, "max_rows", default=1))]
        if "progress_every" in params:
            command += ["--progress-every", str(self._nonnegative_int(params, "progress_every"))]

    @staticmethod
    def _reject_unknown(params: dict[str, object], allowed: set[str]) -> None:
        unknown = set(params) - allowed
        if unknown:
            raise ValueError(f"unsupported parameters: {', '.join(sorted(unknown))}")

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
    def _nonnegative_int(params: dict[str, object], name: str) -> int:
        value = params[name]
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise ValueError(f"{name} must be a non-negative integer")
        return value

    @staticmethod
    def _number(params: dict[str, object], name: str) -> float:
        value = params.get(name)
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not 0 <= value <= 1:
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)
