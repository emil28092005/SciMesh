"""Scientific core for the SDK-built ``similarity-search-parallel`` workload.

The exact same semantics as ``similarity-search`` — identical partial format,
identical bounded merge, byte-identical output — but the per-molecule
fingerprinting and Tanimoto scoring of one shard run across a thread pool
(``threads`` parameter, default = CPU count).

Parallelism is confined to the scoring phase: ``ThreadPoolExecutor.map`` keeps
the input row order, so the results are merged exactly like the sequential
reference (same ``_HeapEntry`` logic), which makes the output byte-identical
for every thread count by construction.
"""

from __future__ import annotations

import heapq
import os
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Mapping

from rdkit import Chem
from rdkit.Chem import DataStructs

from scimesh.chemistry.dataset import MoleculeRecord, parse_smiles
from scimesh.chemistry.fingerprints import fingerprint
from scimesh.workloads.search.core import (
    run_search_shard,
    write_search_partial,
    write_search_shards,
)
from scimesh.workloads.similarity_search import (
    DatasetStats,
    SearchResult,
    SimilarityMatch,
    _HeapEntry,
    iter_valid_molecules,
)


def search_similar_parallel(
    tsv_path: Path,
    query: MoleculeRecord,
    top_k: int,
    *,
    threads: int = 0,
    max_rows: int | None = None,
    threshold: float | None = None,
    threshold_direction: str = "greater",
) -> SearchResult:
    """Exact top-k matches with a bounded heap, scored by a thread pool.

    Identical selection and ordering to ``search_similar`` for every thread
    count: the merge runs in row order over the parallel-computed scores.
    """
    if top_k < 1:
        raise ValueError("--top-k must be a positive integer")
    if threads < 0:
        raise ValueError("threads must be a non-negative integer")
    if threshold is not None and not 0.0 <= threshold <= 1.0:
        raise ValueError("--threshold must be between 0 and 1")
    if threshold_direction not in {"greater", "less"}:
        raise ValueError("--threshold-direction must be 'greater' or 'less'")
    workers = threads or (os.cpu_count() or 1)

    query_fingerprint = fingerprint(query.molecule)
    query_canonical_smiles = Chem.MolToSmiles(query.molecule, canonical=True)
    stats = DatasetStats()
    records = list(iter_valid_molecules(tsv_path, stats, max_rows=max_rows))

    def score(record: MoleculeRecord):
        candidate_smiles = Chem.MolToSmiles(record.molecule, canonical=True)
        if (
            record.molecule_id == query.molecule_id
            or candidate_smiles == query_canonical_smiles
        ):
            return None
        similarity = DataStructs.TanimotoSimilarity(
            query_fingerprint, fingerprint(record.molecule)
        )
        if threshold is not None and (
            similarity < threshold
            if threshold_direction == "greater"
            else similarity > threshold
        ):
            return None
        return similarity

    # map preserves the input order, so the merge below is exactly the
    # sequential reference's merge, just over precomputed scores.
    with ThreadPoolExecutor(max_workers=workers) as pool:
        scored = pool.map(score, records)

    heap: list[_HeapEntry] = []
    for record, similarity in zip(records, scored):
        if similarity is None:
            continue
        match = SimilarityMatch(similarity, record.molecule_id, record.smiles)
        rank_key = match.sort_key(threshold_direction)
        entry = _HeapEntry(match, rank_key)
        if len(heap) < top_k:
            heapq.heappush(heap, entry)
        elif rank_key < heap[0].rank_key:
            heapq.heapreplace(heap, entry)

    matches = [entry.match for entry in sorted(heap, key=lambda e: e.rank_key)]
    return SearchResult(matches=matches, stats=stats)


def run_search_shard_parallel(
    input_path: Path,
    parameters: Mapping[str, object],
    output_path: Path,
) -> dict[str, int]:
    """Run one planned shard with the parallel scoring core.

    Accepts the same parameters as ``similarity-search`` plus ``threads``.
    """
    allowed = {
        "query_id",
        "query_smiles",
        "top_k",
        "threshold",
        "threshold_direction",
        "progress_every",
        "threads",
    }
    unknown = set(parameters) - allowed
    if unknown:
        raise ValueError(
            f"unsupported similarity-search-parallel parameters: {', '.join(sorted(unknown))}"
        )
    query_smiles = parameters.get("query_smiles")
    query_id = parameters.get("query_id")
    if isinstance(query_id, str) and not isinstance(query_smiles, str):
        from rdkit import Chem

        from scimesh.chemistry.dataset import find_molecule_by_id

        record = find_molecule_by_id(input_path, query_id)
        query_smiles = Chem.MolToSmiles(record.molecule, canonical=True)
    if not isinstance(query_smiles, str) or not query_smiles.strip():
        raise ValueError("query_smiles is required for a distributed shard")
    molecule = parse_smiles(query_smiles)
    if molecule is None:
        raise ValueError("query_smiles is invalid")
    top_k = _positive_int(parameters.get("top_k", 20), "top_k")
    threads = _nonnegative_int(parameters.get("threads", 0), "threads")
    threshold = None
    if "threshold" in parameters:
        threshold = _unit_interval(parameters["threshold"], "threshold")
    direction = parameters.get("threshold_direction", "greater")
    if direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    assert isinstance(direction, str)

    result = search_similar_parallel(
        input_path,
        MoleculeRecord("query", query_smiles, molecule),
        top_k=top_k,
        threads=threads,
        threshold=threshold,
        threshold_direction=direction,
    )
    write_search_partial(output_path, result.matches)
    return {
        "scanned_rows": result.stats.scanned,
        "valid_molecules": result.stats.valid,
        "invalid_smiles": result.stats.invalid,
        "matches_emitted": len(result.matches),
    }


def _positive_int(value: object, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise ValueError(f"{name} must be a positive integer")
    return value


def _nonnegative_int(value: object, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{name} must be a non-negative integer")
    return value


def _unit_interval(value: object, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be a number between 0 and 1")
    return float(value)


# The shared shard writer is re-exported so the workload definition can reuse
# the deterministic partitioning without importing search internals.
__all__ = [
    "search_similar_parallel",
    "run_search_shard_parallel",
    "write_search_shards",
    "run_search_shard",
]
