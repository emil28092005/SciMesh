"""Scientific reference tests for the CTX-08 distributed search workload."""

from __future__ import annotations

import csv
import hashlib
from pathlib import Path
from uuid import NAMESPACE_URL, uuid5

import pytest

from scimesh.chemistry.dataset import find_molecule_by_id
from scimesh.distributed import (
    ArtifactReference,
    CompletedPartial,
    PlanningService,
    default_distributed_registry,
)
from scimesh.distributed.registry import DistributedWorkloadRegistry
from scimesh.distributed.similarity_search import (
    SimilaritySearchDistributedWorkload,
    run_similarity_search_shard,
    write_similarity_search_partial,
)
from scimesh.workloads.similarity_search import search_similar, write_search_results


def make_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\textra\n"
        "CHEMBL_QUERY\tCCO\tquery\n"
        "CHEMBL_A\tCCCO\ta\n"
        "CHEMBL_B\tCCCC\tb\n"
        "CHEMBL_INVALID\tnot-a-smiles\tbad\n"
        "CHEMBL_DUPLICATE\tCCO\tduplicate\n"
        "CHEMBL_C\tCCN\tc\n",
        encoding="utf-8",
    )


def planner() -> tuple[PlanningService, SimilaritySearchDistributedWorkload]:
    workload = SimilaritySearchDistributedWorkload()
    registry = DistributedWorkloadRegistry()
    registry.register(workload)
    return PlanningService(registry), workload


def test_default_registry_exposes_only_supported_distributed_search() -> None:
    assert [item.name for item in default_distributed_registry().descriptions()] == ["similarity-search"]


def checksum(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def test_query_id_is_resolved_once_before_deterministic_shards(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    dataset = tmp_path / "chembl.tsv"
    workspace = tmp_path / "workspace"
    make_dataset(dataset)
    service, _ = planner()
    calls = 0
    real_find = find_molecule_by_id

    def count_find(path: Path, query_id: str) -> MoleculeRecord:
        nonlocal calls
        calls += 1
        return real_find(path, query_id)

    monkeypatch.setattr("scimesh.distributed.similarity_search.find_molecule_by_id", count_find)
    input_id = str(uuid5(NAMESPACE_URL, "dataset"))
    plan = service.plan(
        "similarity-search", dataset, input_id,
        {"query_id": "CHEMBL_QUERY", "top_k": 3, "max_rows": 5, "progress_every": 0},
        2, workspace,
    )

    assert calls == 1
    assert plan.resolved_parameters["query_smiles"] == "CCO"
    assert plan.resolved_parameters["query_source"] == {"kind": "chembl_id", "value": "CHEMBL_QUERY"}
    assert [task.chunk_index for task in plan.tasks] == [0, 1, 2]
    assert all("query_id" not in task.parameters for task in plan.tasks)
    assert all("max_rows" not in task.parameters for task in plan.tasks)
    assert all(task.parameters["query_smiles"] == "CCO" for task in plan.tasks)
    assert [
        sum(1 for _ in path.open(encoding="utf-8")) - 1
        for path in sorted(workspace.glob("shard-*.tsv"))
    ] == [2, 2, 1]


def test_distributed_reduction_matches_single_process_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "chembl.tsv"
    workspace = tmp_path / "workspace"
    make_dataset(dataset)
    service, workload = planner()
    plan = service.plan(
        "similarity-search", dataset, str(uuid5(NAMESPACE_URL, "dataset")),
        {"query_smiles": "CCO", "top_k": 3, "threshold": 0.0}, 2, workspace,
    )

    partials: list[CompletedPartial] = []
    # Worker two finishes the latter shards first. Worker one loses its first
    # attempt for shard zero, then retries it last. The reducer must remain
    # independent of both completion and retry order.
    for task in reversed(plan.tasks):
        shard = workspace / f"shard-{task.chunk_index}.tsv"
        temporary_partial = workspace / f"worker-output-{task.chunk_index}.csv"
        metrics = run_similarity_search_shard(shard, task.parameters, temporary_partial)
        partial_id = str(uuid5(NAMESPACE_URL, f"partial:{task.chunk_index}"))
        partials.append(
            CompletedPartial(
                task.chunk_index,
                ArtifactReference(
                    partial_id, checksum(temporary_partial), "text/csv",
                ),
                metrics,
            )
        )
        # The reducer materializes result files under their own coordinator IDs,
        # not shard input IDs. Keep this fixture faithful to that boundary.
        temporary_partial.rename(workspace / partial_id)

    final = workload.reduce(tuple(partials), plan.resolved_parameters, workspace)
    reference = tmp_path / "reference.csv"
    query_record = find_molecule_by_id(dataset, "CHEMBL_QUERY")
    write_search_results(reference, search_similar(dataset, query_record, top_k=3, threshold=0.0).matches)

    assert (workspace / "result.csv").read_bytes() == reference.read_bytes()
    assert final.metrics == {"matches_emitted": 3, "partial_count": 3}
    rows = list(csv.DictReader((workspace / "result.csv").open(encoding="utf-8")))
    assert {row["chembl_id"] for row in rows}.isdisjoint({"CHEMBL_QUERY", "CHEMBL_DUPLICATE"})


def test_reducer_rejects_unsorted_or_invalid_partial_csv(tmp_path: Path) -> None:
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    workload = SimilaritySearchDistributedWorkload()
    artifact_id = str(uuid5(NAMESPACE_URL, "bad"))
    partial_path = workspace / artifact_id
    partial_path.write_text(
        "rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.1\n2,B,CCC,0.9\n",
        encoding="utf-8",
    )
    artifact = ArtifactReference(artifact_id, checksum(partial_path), "text/csv")

    with pytest.raises(ValueError, match="not sorted"):
        workload.reduce(
            (CompletedPartial(0, artifact, {"scanned_rows": 2}),),
            {"query_smiles": "CCO", "top_k": 2, "threshold_direction": "greater", "fingerprint": {"algorithm": "morgan", "radius": 2, "fp_size": 2048}},
            workspace,
        )


def test_partial_csv_preserves_exact_scores_for_global_ranking(tmp_path: Path) -> None:
    partial = tmp_path / "partial.csv"
    # Both values look identical in a six-decimal final CSV. The exact value
    # must survive shard transport so the global reducer can still rank them.
    from scimesh.workloads.similarity_search import SimilarityMatch

    write_similarity_search_partial(
        partial,
        [SimilarityMatch(0.50000049, "A", "CC"), SimilarityMatch(0.50000048, "B", "CCC")],
    )
    values = list(csv.DictReader(partial.open(encoding="utf-8")))
    assert values[0]["similarity"] == repr(0.50000049)
    assert values[1]["similarity"] == repr(0.50000048)


def test_planner_rejects_fingerprint_override_and_invalid_query(tmp_path: Path) -> None:
    dataset = tmp_path / "chembl.tsv"
    make_dataset(dataset)
    service, _ = planner()

    with pytest.raises(ValueError, match="unsupported similarity-search parameters"):
        service.plan(
            "similarity-search", dataset, str(uuid5(NAMESPACE_URL, "dataset")),
            {"query_smiles": "CCO", "fingerprint": {"radius": 1}}, 2, tmp_path / "workspace",
        )
    with pytest.raises(ValueError, match="query_smiles is invalid"):
        service.plan(
            "similarity-search", dataset, str(uuid5(NAMESPACE_URL, "dataset")),
            {"query_smiles": "invalid"}, 2, tmp_path / "workspace",
        )
