"""Registry and orchestration helpers for distributed workload contracts."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, Sequence

from .models import CompletedPartial, DistributedPlan, FinalResult, _workload_name
from .workload import DistributedWorkload


@dataclass(frozen=True)
class WorkloadDescription:
    """Safe metadata that a future coordinator or UI may display."""

    name: str
    description: str


class DistributedWorkloadRegistry:
    """Collect distributed workloads without coupling them to the CLI registry."""

    def __init__(self) -> None:
        self._workloads: dict[str, DistributedWorkload] = {}

    def register(self, workload: DistributedWorkload) -> None:
        name = _workload_name(workload.name)
        if name in self._workloads:
            raise ValueError(f"distributed workload already registered: {name}")
        if not isinstance(workload.description, str) or not workload.description.strip():
            raise ValueError("distributed workload description must be non-empty")
        self._workloads[name] = workload

    def require(self, name: str) -> DistributedWorkload:
        try:
            return self._workloads[_workload_name(name)]
        except KeyError as error:
            raise ValueError(f"unknown distributed workload: {name}") from error

    def descriptions(self) -> tuple[WorkloadDescription, ...]:
        return tuple(
            WorkloadDescription(name, workload.description)
            for name, workload in sorted(self._workloads.items())
        )


class PlanningService:
    """Small bridge-safe orchestration around a distributed workload registry.

    It writes neither jobs nor artifacts.  A Go coordinator bridge can therefore
    validate and produce a plan before opening its own all-or-nothing persistence
    transaction; CTX-08/09 will implement that concrete bridge and reducers.
    """

    def __init__(self, registry: DistributedWorkloadRegistry) -> None:
        self._registry = registry

    def plan(
        self,
        workload_name: str,
        input_path: Path,
        input_artifact_id: str,
        parameters: Mapping[str, object],
        shard_rows: int,
        workspace: Path,
    ) -> DistributedPlan:
        if isinstance(shard_rows, bool) or not isinstance(shard_rows, int) or shard_rows < 1:
            raise ValueError("shard_rows must be a positive integer")
        workload = self._registry.require(workload_name)
        workload.validate_job(parameters)
        plan = workload.plan(input_path, input_artifact_id, parameters, shard_rows, workspace)
        if not isinstance(plan, DistributedPlan):
            raise ValueError("distributed planner must return a DistributedPlan")
        if plan.workload != workload.name:
            raise ValueError("distributed planner returned a plan for another workload")
        # Round-trip through the strict wire schema now, before a future bridge
        # persists anything. This catches non-JSON values and undeclared fields.
        return DistributedPlan.from_json(plan.to_json())

    def reduce(
        self,
        workload_name: str,
        partial_results: Sequence[CompletedPartial],
        parameters: Mapping[str, object],
        workspace: Path,
    ) -> FinalResult:
        workload = self._registry.require(workload_name)
        indexes = [partial.chunk_index for partial in partial_results]
        if len(indexes) != len(set(indexes)):
            raise ValueError("partial results must have unique chunk_index values")
        ordered = tuple(sorted(partial_results, key=lambda partial: partial.chunk_index))
        result = workload.reduce(ordered, parameters, workspace)
        if not isinstance(result, FinalResult):
            raise ValueError("distributed reducer must return a FinalResult")
        return result
