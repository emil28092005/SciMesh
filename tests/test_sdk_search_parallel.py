"""Tests for the SDK-built similarity-search-parallel workload."""

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
)
from scimesh.workloads.library import default_sdk_registry, default_sdk_runtime
from scimesh.workloads.search.core import run_search_shard, write_search_shards
from scimesh.workloads.search_parallel import (
    run_search_shard_parallel,
    search_similar_parallel,
)
from scimesh.workloads.similarity_search import (
    find_molecule_by_id,
    search_similar,
    write_search_results,
)


def _write_dataset(path: Path, molecules: list[tuple[str, str]]) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\n"
        + "".join(f"{mid}\t{smiles}\n" for mid, smiles in molecules),
        encoding="utf-8",
    )


def _tie_dataset(path: Path) -> None:
    # Deliberate similarity ties: propanol isomers and duplicated rows, so the
    # parallel merge must reproduce the sequential row-order preference.
    _write_dataset(
        path,
        [
            ("QUERY", "CCO"),
            ("A1", "CCCO"),
            ("A2", "C(CC)O"),
            ("B", "CCN"),
            ("C1", "CCC"),
            ("C2", "CCC"),
            ("D", "CCCC"),
        ],
    )


def test_parallel_matches_sequential_byte_exactly(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _tie_dataset(dataset)
    query = find_molecule_by_id(dataset, "QUERY")

    reference = search_similar(dataset, query, top_k=5, progress_every=0)
    reference_path = tmp_path / "reference.csv"
    write_search_results(reference_path, reference.matches)

    for threads in (1, 2, 4):
        parallel = search_similar_parallel(dataset, query, top_k=5, threads=threads)
        parallel_path = tmp_path / f"parallel-{threads}.csv"
        write_search_results(parallel_path, parallel.matches)
        assert parallel_path.read_bytes() == reference_path.read_bytes(), (
            f"threads={threads} diverged from the reference"
        )


def test_parallel_shard_matches_sequential_shard(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _tie_dataset(dataset)
    shard_dir = tmp_path / "shards"
    shard_dir.mkdir()
    shards = write_search_shards(dataset, shard_dir, shard_rows=2)
    parameters = {"query_smiles": "CCO", "top_k": 3, "threads": 4}

    sequential_out = tmp_path / "seq.tsv"
    parallel_out = tmp_path / "par.tsv"
    sequential_parameters = dict(parameters)
    sequential_parameters.pop("threads")
    run_search_shard(shards[0], sequential_parameters, sequential_out)
    run_search_shard_parallel(shards[0], parameters, parallel_out)
    assert parallel_out.read_bytes() == sequential_out.read_bytes()

    with parallel_out.open(encoding="utf-8") as handle:
        rows = list(csv.DictReader(handle))
    assert rows[0]["rank"] == "1"
    assert rows[0]["similarity"].startswith("0.5")  # CCO vs CCCO
    assert len(rows) <= 3


def test_parallel_rejects_bad_parameters(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _tie_dataset(dataset)
    output = tmp_path / "out.tsv"

    with pytest.raises(ValueError, match="threads must be a non-negative integer"):
        run_search_shard_parallel(
            dataset, {"query_smiles": "CCO", "threads": -1}, output
        )
    with pytest.raises(ValueError, match="unsupported"):
        run_search_shard_parallel(dataset, {"query_smiles": "CCO", "nope": 1}, output)
    with pytest.raises(ValueError, match="query_smiles is invalid"):
        run_search_shard_parallel(dataset, {"query_smiles": "СС"}, output)


def _registered_parallel_search(shard_rows: int = 2):
    registry = default_sdk_registry(shard_rows=shard_rows)
    runtime = default_sdk_runtime()
    description = next(
        item
        for item in registry.descriptions()
        if item.workload.name == "similarity-search-parallel"
    )
    definition, negotiated = registry.require(
        description.workload.name,
        description.workload.version,
        description.package_digest,
        runtime=runtime,
    )
    return registry, runtime, description, definition, negotiated


def test_parallel_manifest_is_registered_and_negotiable() -> None:
    _, runtime, description, definition, negotiated = _registered_parallel_search()
    manifest = definition.manifest

    assert description.enabled is True
    assert manifest.workload.name == "similarity-search-parallel"
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
    assert "threads" in manifest.parameters_schema["properties"]
    assert negotiated is not None
    assert runtime is not None


def test_parallel_executor_matches_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _tie_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_parallel_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    input_port = definition.manifest.inputs["input"]
    dataset_artifact = artifact_store.import_file(
        dataset,
        declaration=input_port.schema,
    )
    request = JobRequest(
        workload=definition.manifest.workload,
        parameters={"query_id": "QUERY", "top_k": 3, "threads": 2},
        inputs={"input": ArtifactCollection.single(dataset_artifact)},
    )

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
