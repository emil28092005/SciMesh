"""Contract tests for the coordinator-independent distributed workload boundary."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Mapping, Sequence
from uuid import NAMESPACE_URL, uuid5

import pytest

from scimesh.distributed import (
    ArtifactReference,
    CompletedPartial,
    DistributedPlan,
    DistributedWorkloadRegistry,
    FinalResult,
    PlannedTask,
    PlanningService,
)


def artifact(seed: str, content_type: str = "text/tab-separated-values") -> ArtifactReference:
    return ArtifactReference(
        artifact_id=str(uuid5(NAMESPACE_URL, seed)),
        sha256=(seed.encode("utf-8").hex() * 64)[:64],
        content_type=content_type,
    )


class DummyWorkload:
    """A deterministic fake workload used to test the generic CTX-07 bridge."""

    name = "dummy-workload"
    description = "A deterministic test workload."

    def __init__(self) -> None:
        self.plan_calls = 0
        self.received_partials: tuple[CompletedPartial, ...] = ()

    def validate_job(self, parameters: Mapping[str, object]) -> None:
        if parameters != {"mode": "valid"}:
            raise ValueError("mode must be valid")

    def plan(
        self,
        input_path: Path,
        input_artifact_id: str,
        parameters: Mapping[str, object],
        shard_rows: int,
        workspace: Path,
    ) -> DistributedPlan:
        self.plan_calls += 1
        assert input_path.name == "input.tsv"
        assert workspace.name == "workspace"
        return DistributedPlan(
            workload=self.name,
            resolved_parameters={"mode": parameters["mode"], "source": input_artifact_id},
            tasks=(
                PlannedTask(0, artifact(f"{input_artifact_id}:0"), {"mode": "valid"}),
                PlannedTask(1, artifact(f"{input_artifact_id}:1"), {"mode": "valid"}),
            ),
        )

    def reduce(
        self,
        partial_results: Sequence[CompletedPartial],
        parameters: Mapping[str, object],
        workspace: Path,
    ) -> FinalResult:
        self.received_partials = tuple(partial_results)
        return FinalResult(artifact("final", "text/csv"), {"partial_count": len(partial_results)})


def service() -> tuple[PlanningService, DummyWorkload]:
    workload = DummyWorkload()
    registry = DistributedWorkloadRegistry()
    registry.register(workload)
    return PlanningService(registry), workload


def test_unknown_workload_is_rejected_before_a_plan_is_written(tmp_path: Path) -> None:
    planner, workload = service()

    with pytest.raises(ValueError, match="unknown distributed workload"):
        planner.plan(
            "unknown-workload", tmp_path / "input.tsv", artifact("input").artifact_id,
            {"mode": "valid"}, 10, tmp_path / "workspace",
        )

    assert workload.plan_calls == 0


def test_invalid_job_is_rejected_before_the_planner_runs(tmp_path: Path) -> None:
    planner, workload = service()

    with pytest.raises(ValueError, match="mode must be valid"):
        planner.plan(
            "dummy-workload", tmp_path / "input.tsv", artifact("input").artifact_id,
            {"mode": "invalid"}, 10, tmp_path / "workspace",
        )

    assert workload.plan_calls == 0


def test_two_shard_plan_is_deterministic_and_json_serializable(tmp_path: Path) -> None:
    planner, _ = service()
    input_artifact_id = artifact("input").artifact_id
    first = planner.plan(
        "dummy-workload", tmp_path / "input.tsv", input_artifact_id,
        {"mode": "valid"}, 10, tmp_path / "workspace",
    )
    second = planner.plan(
        "dummy-workload", tmp_path / "input.tsv", input_artifact_id,
        {"mode": "valid"}, 10, tmp_path / "workspace",
    )

    assert first.to_json() == second.to_json()
    payload = json.loads(first.to_json())
    assert [task["chunk_index"] for task in payload["tasks"]] == [0, 1]
    assert all(set(task) == {"chunk_index", "input_artifact", "parameters"} for task in payload["tasks"])
    assert DistributedPlan.from_json(first.to_json()) == first


def test_plan_rejects_unsafe_or_non_deterministic_task_payloads() -> None:
    with pytest.raises(ValueError, match="unique, ascending"):
        DistributedPlan(
            workload="dummy-workload",
            resolved_parameters={},
            tasks=(
                PlannedTask(1, artifact("one"), {}),
                PlannedTask(0, artifact("zero"), {}),
            ),
        )

    with pytest.raises(ValueError, match="JSON-compatible"):
        PlannedTask(0, artifact("bad"), {"path": Path("not-serializable")})

    with pytest.raises(ValueError, match="URI or local path"):
        PlannedTask(0, artifact("uri"), {"input": "file:///tmp/input.tsv"})

    with pytest.raises(ValueError, match="canonical hyphenated"):
        DistributedPlan("dummy_workload", {}, (PlannedTask(0, artifact("one"), {}),))


def test_reducer_receives_completed_partials_in_chunk_order(tmp_path: Path) -> None:
    planner, workload = service()
    result = planner.reduce(
        "dummy-workload",
        (
            CompletedPartial(3, artifact("three", "text/csv"), {"scanned_rows": 10}),
            CompletedPartial(1, artifact("one", "text/csv"), {"scanned_rows": 10}),
        ),
        {"mode": "valid"},
        tmp_path / "workspace",
    )

    assert [partial.chunk_index for partial in workload.received_partials] == [1, 3]
    assert result.metrics == {"partial_count": 2}


def test_reducer_rejects_duplicate_chunk_indexes_before_invocation(tmp_path: Path) -> None:
    planner, workload = service()
    duplicate = CompletedPartial(0, artifact("partial", "text/csv"), {"scanned_rows": 1})

    with pytest.raises(ValueError, match="unique chunk_index"):
        planner.reduce("dummy-workload", (duplicate, duplicate), {"mode": "valid"}, tmp_path)

    assert workload.received_partials == ()


def test_artifact_references_never_accept_paths_or_uris() -> None:
    with pytest.raises(ValueError, match="UUID"):
        ArtifactReference("file:///tmp/input.tsv", "a" * 64, "text/csv")
    with pytest.raises(ValueError, match="lowercase SHA-256"):
        ArtifactReference(str(uuid5(NAMESPACE_URL, "input")), "A" * 64, "text/csv")


def test_registry_descriptions_are_stable_and_duplicate_names_are_rejected() -> None:
    registry = DistributedWorkloadRegistry()
    first, second = DummyWorkload(), DummyWorkload()
    registry.register(first)

    assert registry.descriptions()[0].name == "dummy-workload"
    with pytest.raises(ValueError, match="already registered"):
        registry.register(second)
