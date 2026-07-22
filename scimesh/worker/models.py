"""Value objects shared by the worker daemon components."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class InputArtifact:
    uri: str
    sha256: str


@dataclass(frozen=True)
class ClaimedTask:
    task_id: str
    attempt: int
    lease_expires_at: str
    workload: str
    input: InputArtifact
    parameters: dict[str, Any]

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "ClaimedTask":
        try:
            input_data = data["input"]
            return cls(
                task_id=str(data["task_id"]),
                attempt=int(data["attempt"]),
                lease_expires_at=str(data["lease_expires_at"]),
                workload=str(data["workload"]),
                input=InputArtifact(uri=str(input_data["uri"]), sha256=str(input_data["sha256"])),
                parameters=dict(data.get("parameters", {})),
            )
        except (KeyError, TypeError, ValueError) as error:
            raise ValueError("invalid claimed-task response") from error


@dataclass(frozen=True)
class ProducedArtifact:
    path: Path
    content_type: str


@dataclass(frozen=True)
class RunResult:
    artifacts: tuple[ProducedArtifact, ...]
    metrics: dict[str, int | float]
