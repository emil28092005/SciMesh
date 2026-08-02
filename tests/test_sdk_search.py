"""Tests for the SDK-built similarity-search workload."""

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
from scimesh.workloads.library import default_sdk_registry, default_sdk_runtime
from scimesh.workloads.similarity_search import (
    find_molecule_by_id,
    search_similar,
    write_search_results,
)


def _write_tiny_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\textra\n"
        "QUERY\tCCO\tquery\n"
        "ALCOHOL\tCCCO\talcohol\n"
        "ALKANE\tCCCC\talkane\n"
        "BROKEN\tnot-a-smiles\tinvalid\n"
        "DUPLICATE\tCCO\tduplicate\n"
        "AMINE\tCCN\tamine\n",
        encoding="utf-8",
    )


def _registered_similarity_search(shard_rows: int = 2):
    registry = default_sdk_registry(shard_rows=shard_rows)
    runtime = default_sdk_runtime()
    description = next(
        item
        for item in registry.descriptions()
        if item.workload.name == "similarity-search"
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
) -> JobRequest:
    input_port = definition.manifest.inputs["input"]
    dataset_artifact = artifact_store.import_file(
        dataset,
        declaration=input_port.schema,
    )
    return JobRequest(
        workload=definition.manifest.workload,
        parameters={"query_id": "QUERY", "top_k": 3, "progress_every": 0},
        inputs={"input": ArtifactCollection.single(dataset_artifact)},
    )


def test_similarity_search_manifest_is_registered_and_negotiable() -> None:
    _, runtime, description, definition, negotiated = _registered_similarity_search()
    manifest = definition.manifest

    assert description.enabled is True
    assert manifest.workload.name == "similarity-search"
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
    assert negotiated.manifest == manifest
    assert_manifest_round_trip(manifest)
    assert runtime is not None


def test_local_sdk_executor_matches_similarity_search_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)

    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, description.package_digest)
    result_artifact = result.outputs["result"].items[0].artifact

    reference_path = tmp_path / "reference.csv"
    query = find_molecule_by_id(dataset, "QUERY")
    reference = search_similar(dataset, query, top_k=3, progress_every=0)
    write_search_results(reference_path, reference.matches)

    assert (
        artifact_store.materialize(result_artifact).read_bytes()
        == reference_path.read_bytes()
    )
    assert result.task_key == "reduce/final"
    assert dict(result.metrics) == {"matches_emitted": 3, "partial_count": 3}


def test_similarity_search_planning_is_deterministic_ordered_and_path_free(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)
    input_artifact = request.inputs["input"].items[0].artifact

    first = registry.plan(
        request,
        description.package_digest,
        runtime,
        LocalPlanningContext(
            artifact_store,
            artifact_store,
            tmp_path / "first-plan",
            allowed_artifacts=(input_artifact,),
        ),
    )
    second = registry.plan(
        request,
        description.package_digest,
        runtime,
        LocalPlanningContext(
            artifact_store,
            artifact_store,
            tmp_path / "second-plan",
            allowed_artifacts=(input_artifact,),
        ),
    )

    assert first.to_json() == second.to_json()
    assert first.digest == second.digest
    assert first.package_digest == definition.manifest.package.digest
    assert first.manifest_digest == definition.manifest.digest
    assert [task.task_key for task in first.tasks] == [
        "map/00000000",
        "map/00000001",
        "map/00000002",
    ]
    assert all(task.stage_id == "map" for task in first.tasks)
    assert all("query_id" not in task.parameters for task in first.tasks)
    assert all(task.parameters["query_smiles"] == "CCO" for task in first.tasks)
    assert all(task.parameters["top_k"] == 3 for task in first.tasks)
    assert all(task.package_digest == first.package_digest for task in first.tasks)
    assert all(task.manifest_digest == first.manifest_digest for task in first.tasks)
    assert first.resolved_parameters["query_source"] == {
        "kind": "chembl_id",
        "value": "QUERY",
    }

    shard_ids: list[list[str]] = []
    for task in first.tasks:
        artifact = task.inputs["input"].items[0].artifact
        with artifact_store.materialize(artifact).open(
            encoding="utf-8", newline=""
        ) as source:
            shard_ids.append(
                [row["chembl_id"] for row in csv.DictReader(source, delimiter="\t")]
            )
    assert shard_ids == [
        ["QUERY", "ALCOHOL"],
        ["ALKANE", "BROKEN"],
        ["DUPLICATE", "AMINE"],
    ]

    wire_payload = first.to_json()
    assert str(tmp_path) not in wire_payload
    assert "file://" not in wire_payload
    assert "worker://" not in wire_payload
    assert "workspace" not in wire_payload


def test_similarity_search_rejects_ambiguous_or_mistyped_parameters(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, _, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    input_artifact = artifact_store.import_file(
        dataset,
        declaration=definition.manifest.inputs["input"].schema,
    )
    base = JobRequest(
        workload=definition.manifest.workload,
        parameters={"query_id": "QUERY", "top_k": 3},
        inputs={"input": ArtifactCollection.single(input_artifact)},
    )

    for bad_parameters, message in (
        ({"query_id": "QUERY", "query_smiles": "CCO"}, "oneOf did not match"),
        ({"query_id": "QUERY", "top_k": 0}, "violates minimum"),
    ):
        request = JobRequest(
            workload=base.workload,
            parameters=bad_parameters,
            inputs=base.inputs,
        )
        with pytest.raises(ValueError, match=message):
            registry.plan(
                request,
                definition.manifest.package.digest,
                runtime,
                LocalPlanningContext(
                    artifact_store,
                    artifact_store,
                    tmp_path / "bad-plan",
                    allowed_artifacts=(input_artifact,),
                ),
            )


def test_similarity_search_workload_definition_is_discoverable() -> None:
    from scimesh.sdk import WorkloadDefinition
    from scimesh.workloads.search import workload_definition

    definition = workload_definition()
    assert isinstance(definition, WorkloadDefinition)
    assert definition.manifest.workload.name == "similarity-search"
    assert definition.manifest.workload.version == "1.0.0"
