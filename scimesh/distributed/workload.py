"""Protocol implemented by coordinator-independent distributed workloads."""

from __future__ import annotations

from pathlib import Path
from typing import Mapping, Protocol, Sequence

from .models import CompletedPartial, DistributedPlan, FinalResult


class DistributedWorkload(Protocol):
    """Validate, plan, and reduce one explicit scientific workload.

    ``input_path`` and ``workspace`` are bridge-provided temporary local paths.
    They must never be included in returned plans or persisted task payloads.
    """

    name: str
    description: str

    def validate_job(self, parameters: Mapping[str, object]) -> None:
        """Reject invalid public parameters before the bridge writes metadata."""

    def plan(
        self,
        input_path: Path,
        input_artifact_id: str,
        parameters: Mapping[str, object],
        shard_rows: int,
        workspace: Path,
    ) -> DistributedPlan:
        """Build a JSON-safe plan containing only coordinator artifact references."""

    def reduce(
        self,
        partial_results: Sequence[CompletedPartial],
        parameters: Mapping[str, object],
        workspace: Path,
    ) -> FinalResult:
        """Reduce coordinator-owned partial artifacts in ascending chunk order."""
