"""Tests for the molwt-filter workload and the default scaffold hooks."""

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
    StageKind,
    assert_manifest_round_trip,
)
from scimesh.workloads.library import default_sdk_registry, default_sdk_runtime
from scimesh.workloads.molwt_filter import (
    filter_molecules_by_molwt,
    molwt_filter_sdk_definition,
)


def _write_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\n"
        "WATER\tO\n"
        "ETHANOL\tCCO\n"
        "PENTANE\tCCCCC\n"
        "BROKEN\tnot-a-smiles\n"
        "HEXANE\tCCCCCC\n",
        encoding="utf-8",
    )


def _registered_molwt_filter(shard_rows: int = 2):
    registry = default_sdk_registry(shard_rows=shard_rows)
    runtime = default_sdk_runtime()
    description = next(
        item for item in registry.descriptions() if item.workload.name == "molwt-filter"
    )
    definition, negotiated = registry.require(
        description.workload.name,
        description.workload.version,
        description.package_digest,
        runtime=runtime,
    )
    return registry, runtime, description, definition, negotiated


def _request(definition, store, dataset, *, parameters) -> JobRequest:
    artifact = store.import_file(
        dataset,
        declaration=definition.manifest.inputs["input"].schema,
    )
    return JobRequest(
        workload=definition.manifest.workload,
        parameters=parameters,
        inputs={"input": ArtifactCollection.single(artifact)},
    )


def test_molwt_filter_manifest_is_registered_and_negotiable() -> None:
    _, runtime, description, definition, negotiated = _registered_molwt_filter()
    manifest = definition.manifest

    assert description.enabled is True
    assert manifest.workload.name == "molwt-filter"
    assert manifest.workload.version == "1.0.0"
    assert manifest.determinism is DeterminismProfile.BYTE_EXACT
    assert manifest.verifier.verifier.canonical == "exact-artifact@1"
    assert [stage.kind for stage in manifest.workflow.stages] == [
        StageKind.MAP,
        StageKind.REDUCE,
    ]
    assert set(definition.runners) == {manifest.workflow.stages[0].entry_point}
    assert set(definition.reducers) == {manifest.workflow.stages[1].entry_point}
    assert negotiated is not None
    assert_manifest_round_trip(manifest)
    assert runtime is not None


def test_local_sdk_executor_matches_molwt_filter_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_molwt_filter()
    store = LocalArtifactStore(tmp_path / "artifacts")
    parameters = {"min_molwt": 40.0, "max_molwt": 90.0}
    request = _request(definition, store, dataset, parameters=parameters)

    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        store,
        tmp_path / "work",
    ).execute(request, description.package_digest)
    artifact = result.outputs["result"].items[0].artifact

    reference = tmp_path / "reference.csv"
    reference_metrics = filter_molecules_by_molwt(
        dataset,
        reference,
        min_molwt=40.0,
        max_molwt=90.0,
        skip_invalid=True,
    )

    assert store.materialize(artifact).read_bytes() == reference.read_bytes()
    assert result.task_key == "reduce/final"
    assert dict(result.metrics) == {
        "partial_count": 3,
        "rows_emitted": reference_metrics["rows_emitted"],
    }
    with store.materialize(artifact).open(encoding="utf-8", newline="") as source:
        rows = list(csv.DictReader(source))
    assert [row["chembl_id"] for row in rows] == ["ETHANOL", "PENTANE", "HEXANE"]
    assert all(row["molwt"].count(".") == 1 for row in rows)


def test_molwt_filter_single_bound_and_invalid_row_policy(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    registry, runtime, _, definition, _ = _registered_molwt_filter()
    store = LocalArtifactStore(tmp_path / "artifacts")

    lower = _request(
        definition,
        store,
        dataset,
        parameters={"min_molwt": 72.0},
    )
    result = LocalCoreBatchExecutor(
        registry, runtime, store, tmp_path / "work-lower"
    ).execute(lower, definition.manifest.package.digest)
    rows = list(
        csv.DictReader(
            store.materialize(result.outputs["result"].items[0].artifact).open(
                encoding="utf-8", newline=""
            )
        )
    )
    assert [row["chembl_id"] for row in rows] == ["PENTANE", "HEXANE"]

    strict = _request(
        definition,
        store,
        dataset,
        parameters={"max_molwt": 100.0, "skip_invalid": False},
    )
    with pytest.raises(ValueError, match="invalid canonical_smiles"):
        LocalCoreBatchExecutor(
            registry, runtime, store, tmp_path / "work-strict"
        ).execute(strict, definition.manifest.package.digest)


def test_molwt_filter_rejects_invalid_parameters(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    registry, runtime, _, definition, _ = _registered_molwt_filter()
    store = LocalArtifactStore(tmp_path / "artifacts")
    input_artifact = store.import_file(
        dataset,
        declaration=definition.manifest.inputs["input"].schema,
    )

    for parameters, message in (
        ({}, "at least one of min_molwt or max_molwt"),
        ({"min_molwt": 50, "max_molwt": 10}, "min_molwt must not exceed"),
        ({"min_molwt": "heavy"}, "type mismatch"),
        ({"min_molwt": 10, "bogus": 1}, "unknown field bogus"),
    ):
        request = JobRequest(
            workload=definition.manifest.workload,
            parameters=parameters,
            inputs={"input": ArtifactCollection.single(input_artifact)},
        )
        with pytest.raises(ValueError, match=message):
            LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "bad").execute(
                request, definition.manifest.package.digest
            )


def test_molwt_filter_uses_scaffold_default_sharding(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    workload = molwt_filter_sdk_definition(shard_rows=2)
    workspace = tmp_path / "shards"
    shards = workload.partition_input(dataset, {}, workspace)
    assert [path.name for path in shards] == [
        "shard-0.tsv",
        "shard-1.tsv",
        "shard-2.tsv",
    ]
    with shards[0].open(encoding="utf-8", newline="") as source:
        assert len(list(csv.DictReader(source, delimiter="\t"))) == 2
