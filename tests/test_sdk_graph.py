"""Tests for the SDK-built similarity-graph workload."""

from __future__ import annotations

import csv
from pathlib import Path

import pytest

from scimesh.sdk import (
    ArtifactCollection,
    DeterminismProfile,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    LocalPlanningContext,
    StageKind,
    assert_manifest_round_trip,
)
from scimesh.workloads.graph import (
    check_pair_coverage,
    merge_edge_partials,
    similarity_graph_sdk_definition,
)
from scimesh.workloads.library import default_sdk_registry, default_sdk_runtime
from scimesh.workloads.similarity_graph import (
    build_similarity_graph,
    write_graph_edges,
)


def _write_tiny_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\n"
        "A\tCCO\n"
        "B\tCCCC\n"
        "C\tCCN\n"
        "D\tCCCCCC\n"
        "E\tnot-a-smiles\n"
        "F\tCCOCC\n"
        "G\tc1ccccc1\n",
        encoding="utf-8",
    )


def _registered_similarity_graph():
    registry = default_sdk_registry()
    runtime = default_sdk_runtime()
    description = next(
        item
        for item in registry.descriptions()
        if item.workload.name == "similarity-graph"
    )
    definition, negotiated = registry.require(
        description.workload.name,
        description.workload.version,
        description.package_digest,
        runtime=runtime,
    )
    return registry, runtime, description, definition, negotiated


def _request_for(
    dataset: Path,
    artifact_store: LocalArtifactStore,
    definition,
    *,
    threshold: float = 0.3,
    threshold_direction: str = "greater",
    block_size: int = 2,
) -> JobRequest:
    input_port = definition.manifest.inputs["input"]
    dataset_artifact = artifact_store.import_file(
        dataset,
        declaration=input_port.schema,
    )
    return JobRequest(
        workload=definition.manifest.workload,
        parameters={
            "threshold": threshold,
            "threshold_direction": threshold_direction,
            "block_size": block_size,
        },
        inputs={"input": ArtifactCollection.single(dataset_artifact)},
    )


def test_similarity_graph_manifest_is_registered_and_negotiable() -> None:
    _, runtime, description, definition, negotiated = _registered_similarity_graph()
    manifest = definition.manifest

    assert description.enabled is True
    assert manifest.workload.name == "similarity-graph"
    assert manifest.workload.version == "1.0.0"
    assert manifest.determinism is DeterminismProfile.BYTE_EXACT
    assert manifest.verifier.verifier.canonical == "exact-artifact@1"
    assert set(mode.value for mode in manifest.trust_modes) == {
        "trusted",
        "untrusted_quorum",
    }
    assert [stage.kind for stage in manifest.workflow.stages] == [
        StageKind.MAP,
        StageKind.REDUCE,
    ]
    assert set(definition.runners) == {manifest.workflow.stages[0].entry_point}
    assert set(definition.reducers) == {manifest.workflow.stages[1].entry_point}
    assert negotiated is not None
    assert_manifest_round_trip(manifest)
    assert runtime is not None


@pytest.mark.parametrize("threshold_direction", ("greater", "less"))
def test_local_sdk_executor_matches_similarity_graph_reference(
    tmp_path: Path,
    threshold_direction: str,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_graph()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(
        dataset,
        artifact_store,
        definition,
        threshold_direction=threshold_direction,
    )

    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, description.package_digest)
    result_artifact = result.outputs["result"].items[0].artifact

    reference_path = tmp_path / "reference.csv"
    reference = build_similarity_graph(
        dataset,
        threshold=0.3,
        block_size=1_000,
        threshold_direction=threshold_direction,
    )
    write_graph_edges(reference_path, reference.edges)

    assert (
        artifact_store.materialize(result_artifact).read_bytes()
        == reference_path.read_bytes()
    )
    assert result.task_key == "reduce/final"
    assert result.metrics["partial_count"] == 6
    assert result.metrics["edges_emitted"] == len(reference.edges)


def test_similarity_graph_result_is_invariant_to_block_size(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, _, definition, _ = _registered_similarity_graph()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")

    outputs = []
    for block_size in (2, 3):
        request = _request_for(
            dataset,
            artifact_store,
            definition,
            block_size=block_size,
        )
        result = LocalCoreBatchExecutor(
            registry,
            runtime,
            artifact_store,
            tmp_path / f"sdk-work-{block_size}",
        ).execute(request, definition.manifest.package.digest)
        artifact = result.outputs["result"].items[0].artifact
        outputs.append(artifact_store.materialize(artifact).read_bytes())
        assert result.metrics["partial_count"] == {2: 6, 3: 3}[block_size]

    assert outputs[0] == outputs[1]


def test_similarity_graph_planning_covers_each_block_pair_once(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_graph()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)
    input_artifact = request.inputs["input"].items[0].artifact

    plan = registry.plan(
        request,
        description.package_digest,
        runtime,
        LocalPlanningContext(
            artifact_store,
            artifact_store,
            tmp_path / "plan",
            allowed_artifacts=(input_artifact,),
        ),
    )

    assert [task.task_key for task in plan.tasks] == [
        "map/0000x0000",
        "map/0000x0001",
        "map/0000x0002",
        "map/0001x0001",
        "map/0001x0002",
        "map/0002x0002",
    ]
    for task in plan.tasks:
        assert set(task.inputs) == {"left", "right"}
        assert task.inputs["left"].items[0].artifact is not None
    pairs = {
        (int(left), int(right))
        for task in plan.tasks
        for left, right in (task.task_key[len("map/") :].split("x"),)
    }
    check_pair_coverage(tuple(sorted(pairs)))

    wire_payload = plan.to_json()
    assert str(tmp_path) not in wire_payload
    assert "file://" not in wire_payload
    assert "worker://" not in wire_payload
    assert "workspace" not in wire_payload


def test_similarity_graph_rejects_duplicate_molecule_ids(tmp_path: Path) -> None:
    dataset = tmp_path / "duplicates.tsv"
    dataset.write_text(
        "chembl_id\tcanonical_smiles\nA\tCCO\nB\tCCCC\nA\tCCN\n",
        encoding="utf-8",
    )
    registry, runtime, _, definition, _ = _registered_similarity_graph()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)

    with pytest.raises(ValueError, match="duplicate chembl_id"):
        LocalCoreBatchExecutor(
            registry,
            runtime,
            artifact_store,
            tmp_path / "work",
        ).execute(request, definition.manifest.package.digest)


def test_similarity_graph_reducer_rejects_duplicate_unordered_pairs(
    tmp_path: Path,
) -> None:
    first = tmp_path / "first.csv"
    first.write_text(
        "source_id,target_id,similarity\nA,B,0.500000\nC,D,0.100000\n",
        encoding="utf-8",
    )
    second = tmp_path / "second.csv"
    second.write_text(
        "source_id,target_id,similarity\nB,A,0.500000\n",
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="duplicate unordered pair"):
        merge_edge_partials((first, second), tmp_path / "result.csv")


def test_similarity_graph_pair_coverage_rejects_missing_block_pair() -> None:
    with pytest.raises(ValueError, match="do not cover the full block pair set"):
        check_pair_coverage(((0, 0), (0, 1)))

    with pytest.raises(ValueError, match="duplicate block pair"):
        check_pair_coverage(((0, 0), (0, 0), (0, 1), (1, 1)))


def test_similarity_graph_merge_is_deterministically_sorted(tmp_path: Path) -> None:
    first = tmp_path / "first.csv"
    first.write_text(
        "source_id,target_id,similarity\nC,A,0.200000\nB,C,0.400000\n",
        encoding="utf-8",
    )
    second = tmp_path / "second.csv"
    second.write_text(
        "source_id,target_id,similarity\nA,B,0.900000\n",
        encoding="utf-8",
    )
    result_path = tmp_path / "result.csv"
    metrics = merge_edge_partials((first, second), result_path)

    assert metrics == {"partial_count": 2, "edges_emitted": 3}
    rows = list(csv.DictReader(result_path.open(encoding="utf-8", newline="")))
    assert [
        (row["source_id"], row["target_id"], row["similarity"]) for row in rows
    ] == [
        ("A", "B", "0.900000"),
        ("B", "C", "0.400000"),
        ("C", "A", "0.200000"),
    ]
