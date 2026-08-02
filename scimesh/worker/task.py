"""Per-task entry point executed by the Go worker agent.

Reads a claimed task from JSON, runs the SDK-built workload with the same
bridge the Python daemon uses (``SciMeshRunner``), and writes a result
manifest consumed by the agent:

- on success: ``{artifact_path, content_type, metrics}`` and exit 0;
- on invalid input/validation failures: a sanitized message on stderr and
  exit 3 (permanent, non-retryable);
- on any other failure: exit 1 (retryable by the agent).

The agent passes task parameters through unchanged; all scientific policy
lives in the workload.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .models import ClaimedTask
from .runners import SciMeshRunner

_EXIT_RETRYABLE = 1
_EXIT_PERMANENT = 3


def _load_task(path: Path) -> ClaimedTask:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise ValueError("task payload is unreadable") from error
    return ClaimedTask.from_json(payload)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="scimesh.worker.task")
    parser.add_argument("--task-json", required=True, type=Path)
    parser.add_argument("--task-dir", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        task = _load_task(args.task_json)
        task_dir = args.task_dir.resolve()
        task_dir.mkdir(parents=True, exist_ok=True)
        result = SciMeshRunner().run(task, task_dir)
        if len(result.artifacts) != 1:
            raise ValueError("runner must produce exactly one result artifact")
        artifact = result.artifacts[0]
        if not artifact.path.is_file():
            raise ValueError("runner artifact is missing")
        manifest = {
            "artifact_path": str(artifact.path),
            "content_type": artifact.content_type,
            "metrics": dict(result.metrics),
        }
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(manifest), encoding="utf-8")
        return 0
    except ValueError as error:
        print(f"permanent: {error}", file=sys.stderr)
        return _EXIT_PERMANENT
    except Exception as error:  # noqa: BLE001 - exit code is the contract
        print(f"retryable: {error}", file=sys.stderr)
        return _EXIT_RETRYABLE


if __name__ == "__main__":
    raise SystemExit(main())
