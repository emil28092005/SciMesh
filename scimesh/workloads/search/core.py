"""Scientific core for the SDK-built ``similarity-search`` workload.

Reuses the local reference implementation (``search_similar``) and the shared
CTX-08 partial format (full-precision ``repr`` scores) so that shard outputs
and the merged final CSV are byte-identical to the legacy distributed path and
to the single-process reference.
"""

from __future__ import annotations

import csv
import heapq
from pathlib import Path
from typing import Any, Iterator, Mapping, Sequence

from scimesh.chemistry.dataset import MoleculeRecord, parse_smiles
from scimesh.workloads.similarity_search import (
    SimilarityMatch,
    _HeapEntry,
    search_similar,
    write_search_results,
)

SEARCH_COLUMNS = ("rank", "chembl_id", "canonical_smiles", "similarity")
REQUIRED_COLUMNS = {"chembl_id", "canonical_smiles"}


def write_search_partial(
    output_path: Path,
    matches: Sequence[SimilarityMatch],
) -> None:
    """Write a worker partial with a round-trip score, not display rounding.

    The public final CSV continues to use the local CLI's six-decimal display.
    A reducer needs the full binary float representation to rank candidates
    from separate shards exactly as the single-process reference does.
    """
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(destination, fieldnames=list(SEARCH_COLUMNS))
        writer.writeheader()
        for rank, match in enumerate(matches, start=1):
            writer.writerow(
                {
                    "rank": rank,
                    "chembl_id": match.molecule_id,
                    "canonical_smiles": match.smiles,
                    "similarity": repr(match.similarity),
                }
            )


def run_search_shard(
    input_path: Path,
    parameters: Mapping[str, object],
    output_path: Path,
) -> dict[str, int]:
    """Run one planned shard with the local reference implementation.

    This is the worker entry used by the SDK-built runner. It deliberately
    accepts only resolved ``query_smiles``: resolving an identifier
    independently in each shard would make the distributed search
    scientifically invalid, so identifier resolution happens once in the
    planner (or at the worker bridge for v1-wire tasks that still carry
    ``query_id``).
    """
    allowed = {
        "query_smiles",
        "top_k",
        "threshold",
        "threshold_direction",
        "progress_every",
    }
    unknown = set(parameters) - allowed
    if unknown:
        raise ValueError(
            f"unsupported similarity-search parameters: {', '.join(sorted(unknown))}"
        )
    query_smiles = parameters.get("query_smiles")
    if not isinstance(query_smiles, str) or not query_smiles.strip():
        raise ValueError("query_smiles is required for a distributed shard")
    molecule = parse_smiles(query_smiles)
    if molecule is None:
        raise ValueError("query_smiles is invalid")
    top_k = _positive_int(parameters.get("top_k", 20), "top_k")
    threshold = None
    if "threshold" in parameters:
        threshold = _unit_interval(parameters["threshold"], "threshold")
    direction = parameters.get("threshold_direction", "greater")
    if direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    assert isinstance(direction, str)
    progress_every = 0
    if "progress_every" in parameters:
        progress_every = _nonnegative_int(
            parameters["progress_every"], "progress_every"
        )
    result = search_similar(
        input_path,
        MoleculeRecord("query", query_smiles, molecule),
        top_k=top_k,
        progress_every=progress_every,
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


def write_search_shards(
    input_path: Path,
    workspace: Path,
    shard_rows: int,
    max_rows: int | None = None,
) -> list[Path]:
    """Split the input TSV into deterministic row-bounded shards with headers."""
    if (
        isinstance(shard_rows, bool)
        or not isinstance(shard_rows, int)
        or shard_rows < 1
    ):
        raise ValueError("shard_rows must be a positive integer")
    if max_rows is not None and (
        isinstance(max_rows, bool) or not isinstance(max_rows, int) or max_rows < 1
    ):
        raise ValueError("max_rows must be a positive integer")
    paths: list[Path] = []
    current: Path | None = None
    destination = None
    writer = None
    rows_in_shard = 0
    seen_rows = 0
    try:
        with input_path.open("r", encoding="utf-8", newline="") as source:
            reader = csv.DictReader(source, delimiter="\t")
            fieldnames = tuple(reader.fieldnames or ())
            if not REQUIRED_COLUMNS.issubset(set(fieldnames)):
                missing = sorted(REQUIRED_COLUMNS - set(fieldnames))
                raise ValueError(
                    f"dataset is missing required columns: {', '.join(missing)}"
                )
            for row in reader:
                if max_rows is not None and seen_rows >= max_rows:
                    break
                if destination is None or rows_in_shard == shard_rows:
                    if destination is not None:
                        destination.close()
                    current = workspace / f"shard-{len(paths)}.tsv"
                    destination = current.open("w", encoding="utf-8", newline="")
                    writer = csv.DictWriter(
                        destination,
                        fieldnames=list(fieldnames),
                        delimiter="\t",
                        lineterminator="\n",
                    )
                    writer.writeheader()
                    paths.append(current)
                    rows_in_shard = 0
                assert writer is not None
                writer.writerow(row)
                rows_in_shard += 1
                seen_rows += 1
    finally:
        if destination is not None:
            destination.close()
    if not paths:
        raise ValueError("dataset has no data rows")
    return paths


def iter_search_partial(
    path: Path,
    threshold_direction: str,
) -> Iterator[SimilarityMatch]:
    """Yield strictly ordered partial matches with full-precision scores."""
    if not path.is_file():
        raise ValueError("materialized partial result is missing")
    if threshold_direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    previous_key: tuple[float, str, str] | None = None
    with path.open("r", encoding="utf-8", newline="") as source:
        reader = csv.DictReader(source)
        if tuple(reader.fieldnames or ()) != SEARCH_COLUMNS:
            raise ValueError("partial result has an invalid CSV header")
        for expected_rank, row in enumerate(reader, start=1):
            if set(row) != set(SEARCH_COLUMNS) or row["rank"] != str(expected_rank):
                raise ValueError("partial result has an invalid rank")
            try:
                similarity = float(row["similarity"])
            except (TypeError, ValueError) as error:
                raise ValueError("partial result has an invalid similarity") from error
            if not 0 <= similarity <= 1:
                raise ValueError("partial result has an invalid similarity")
            match = SimilarityMatch(
                similarity, row["chembl_id"], row["canonical_smiles"]
            )
            key = match.sort_key(threshold_direction)
            if previous_key is not None and key < previous_key:
                raise ValueError("partial result is not sorted deterministically")
            previous_key = key
            yield match


def merge_search_partials(
    partial_paths: Sequence[Path],
    parameters: Mapping[str, Any],
    output_path: Path,
) -> dict[str, int]:
    """Merge sorted shard partials into one deterministic final top-k CSV.

    Mirrors the CTX-08/CTX-09 reducer: a bounded heap with the local
    tie-breaker, so the merged file equals the single-process reference
    byte-for-byte for the same input and options.
    """
    if not partial_paths:
        raise ValueError("at least one partial result is required")
    raw_top_k = parameters.get("top_k", 20)
    if isinstance(raw_top_k, bool) or not isinstance(raw_top_k, int) or raw_top_k < 1:
        raise ValueError("top_k must be a positive integer")
    top_k = raw_top_k
    direction = parameters.get("threshold_direction", "greater")
    if direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    heap: list[_HeapEntry] = []
    for path in partial_paths:
        for match in iter_search_partial(path, direction):
            rank_key = match.sort_key(direction)
            entry = _HeapEntry(match, rank_key)
            if len(heap) < top_k:
                heapq.heappush(heap, entry)
            elif rank_key < heap[0].rank_key:
                heapq.heapreplace(heap, entry)
    matches = sorted(
        (entry.match for entry in heap),
        key=lambda match: match.sort_key(direction),
    )
    write_search_results(output_path, matches)
    return {"matches_emitted": len(matches), "partial_count": len(partial_paths)}
