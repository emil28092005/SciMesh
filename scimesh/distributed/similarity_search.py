"""Distributed planning and reduction for exact molecular similarity search."""

from __future__ import annotations

import csv
import hashlib
import heapq
import math
from pathlib import Path
from typing import Any, Iterator, Mapping, Sequence
from uuid import UUID, uuid5

from rdkit import Chem

from scimesh.chemistry.dataset import MoleculeRecord, find_molecule_by_id, parse_smiles
from scimesh.chemistry.fingerprints import FP_RADIUS, FP_SIZE
from scimesh.workloads.similarity_search import (
    SimilarityMatch,
    _HeapEntry,
    search_similar,
    write_search_results,
)

from .models import ArtifactReference, CompletedPartial, DistributedPlan, FinalResult, PlannedTask


_TSV_CONTENT_TYPE = "text/tab-separated-values"
_CSV_CONTENT_TYPE = "text/csv"
_SEARCH_COLUMNS = ("rank", "chembl_id", "canonical_smiles", "similarity")
_REQUIRED_COLUMNS = {"chembl_id", "canonical_smiles"}


def write_similarity_search_partial(output_path: Path, matches: Sequence[SimilarityMatch]) -> None:
    """Write a worker partial with a round-trip score, not display rounding.

    The public final CSV continues to use the local CLI's six-decimal display.
    A reducer needs the full binary float representation to rank candidates
    from separate shards exactly as the single-process reference does.
    """
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(destination, fieldnames=_SEARCH_COLUMNS)
        writer.writeheader()
        for rank, match in enumerate(matches, start=1):
            writer.writerow({
                "rank": rank,
                "chembl_id": match.molecule_id,
                "canonical_smiles": match.smiles,
                "similarity": repr(match.similarity),
            })


class SimilaritySearchDistributedWorkload:
    """Planner/reducer for exact global top-k Tanimoto similarity search."""

    name = "similarity-search"
    description = "Exact top-k molecular similarity search over deterministic TSV shards."

    def validate_job(self, parameters: Mapping[str, object]) -> None:
        allowed = {
            "query_id", "query_smiles", "top_k", "threshold",
            "threshold_direction", "max_rows", "progress_every",
        }
        unknown = set(parameters) - allowed
        if unknown:
            raise ValueError(f"unsupported similarity-search parameters: {', '.join(sorted(unknown))}")
        query_id = parameters.get("query_id")
        query_smiles = parameters.get("query_smiles")
        if (query_id is None) == (query_smiles is None):
            raise ValueError("exactly one of query_id or query_smiles is required")
        if query_id is not None:
            self._string(query_id, "query_id")
        if query_smiles is not None:
            self._string(query_smiles, "query_smiles")
        self._positive_int(parameters.get("top_k", 20), "top_k")
        if "max_rows" in parameters:
            self._positive_int(parameters["max_rows"], "max_rows")
        if "progress_every" in parameters:
            self._nonnegative_int(parameters["progress_every"], "progress_every")
        if "threshold" in parameters:
            self._unit_interval(parameters["threshold"], "threshold")
        if "threshold_direction" in parameters and parameters["threshold_direction"] not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")

    def plan(
        self,
        input_path: Path,
        input_artifact_id: str,
        parameters: Mapping[str, object],
        shard_rows: int,
        workspace: Path,
    ) -> DistributedPlan:
        self.validate_job(parameters)
        if not input_path.is_file():
            raise ValueError("input_path must be a readable dataset file")
        if isinstance(shard_rows, bool) or not isinstance(shard_rows, int) or shard_rows < 1:
            raise ValueError("shard_rows must be a positive integer")
        try:
            input_id = UUID(input_artifact_id)
        except ValueError as error:
            raise ValueError("input_artifact_id must be a UUID") from error

        query_smiles, query_source = self._resolve_query(input_path, parameters)
        resolved = self._resolved_parameters(parameters, query_smiles, query_source)
        workspace.mkdir(parents=True, exist_ok=True)
        shard_paths: list[Path] = []
        try:
            shard_paths = self._write_shards(input_path, workspace, shard_rows, resolved.get("max_rows"))
            tasks = tuple(
                PlannedTask(
                    chunk_index=index,
                    input_artifact=ArtifactReference(
                        artifact_id=str(uuid5(input_id, f"scimesh:similarity-search:shard:{index}")),
                        sha256=_sha256_file(path),
                        content_type=_TSV_CONTENT_TYPE,
                    ),
                    parameters=self._task_parameters(resolved),
                )
                for index, path in enumerate(shard_paths)
            )
        except Exception:
            for path in shard_paths:
                path.unlink(missing_ok=True)
            raise
        return DistributedPlan(self.name, resolved, tasks)

    def reduce(
        self,
        partial_results: Sequence[CompletedPartial],
        parameters: Mapping[str, object],
        workspace: Path,
    ) -> FinalResult:
        """Merge materialized partial CSVs into one deterministic final CSV.

        The coordinator bridge materializes each downloaded artifact at
        ``workspace / artifact_id`` before it calls this method. Those local
        paths are an ephemeral bridge detail, never present in the plan or task
        payload. CTX-09 owns the durable final-artifact upload and job state.
        """
        if not partial_results:
            raise ValueError("at least one partial result is required")
        resolved = self._validate_resolved_parameters(parameters)
        top_k = resolved["top_k"]
        direction = resolved["threshold_direction"]
        heap: list[_HeapEntry] = []

        ordered_partials = tuple(sorted(partial_results, key=lambda partial: partial.chunk_index))
        indexes = [partial.chunk_index for partial in ordered_partials]
        if len(indexes) != len(set(indexes)):
            raise ValueError("partial results must have unique chunk_index values")
        for partial in ordered_partials:
            path = workspace / partial.artifact.artifact_id
            if not path.is_file():
                raise ValueError("materialized partial result is missing")
            if _sha256_file(path) != partial.artifact.sha256:
                raise ValueError("materialized partial result checksum does not match its artifact reference")
            for match in self._read_partial(path, direction):
                rank_key = match.sort_key(direction)
                entry = _HeapEntry(match, rank_key)
                if len(heap) < top_k:
                    heapq.heappush(heap, entry)
                elif rank_key < heap[0].rank_key:
                    heapq.heapreplace(heap, entry)

        matches = sorted((entry.match for entry in heap), key=lambda match: match.sort_key(direction))
        output = workspace / "result.csv"
        write_search_results(output, matches)
        final_id = uuid5(
            UUID(ordered_partials[0].artifact.artifact_id),
            "scimesh:similarity-search:final:" + ",".join(
                partial.artifact.artifact_id for partial in ordered_partials
            ),
        )
        return FinalResult(
            ArtifactReference(str(final_id), _sha256_file(output), _CSV_CONTENT_TYPE),
            {"matches_emitted": len(matches), "partial_count": len(ordered_partials)},
        )

    def _resolve_query(
        self, input_path: Path, parameters: Mapping[str, object]
    ) -> tuple[str, dict[str, str]]:
        query_id = parameters.get("query_id")
        if isinstance(query_id, str):
            record = find_molecule_by_id(input_path, query_id)
            return Chem.MolToSmiles(record.molecule, canonical=True), {"kind": "chembl_id", "value": query_id}
        supplied = parameters["query_smiles"]
        assert isinstance(supplied, str)  # checked by validate_job
        molecule = parse_smiles(supplied)
        if molecule is None:
            raise ValueError("query_smiles is invalid")
        return Chem.MolToSmiles(molecule, canonical=True), {"kind": "smiles", "value": supplied}

    def _resolved_parameters(
        self, parameters: Mapping[str, object], query_smiles: str, query_source: Mapping[str, str]
    ) -> dict[str, object]:
        resolved: dict[str, object] = {
            "query_smiles": query_smiles,
            "query_source": dict(query_source),
            "top_k": self._positive_int(parameters.get("top_k", 20), "top_k"),
            "threshold_direction": parameters.get("threshold_direction", "greater"),
            "fingerprint": {"algorithm": "morgan", "radius": FP_RADIUS, "fp_size": FP_SIZE},
        }
        if "threshold" in parameters:
            resolved["threshold"] = self._unit_interval(parameters["threshold"], "threshold")
        if "max_rows" in parameters:
            resolved["max_rows"] = self._positive_int(parameters["max_rows"], "max_rows")
        if "progress_every" in parameters:
            resolved["progress_every"] = self._nonnegative_int(parameters["progress_every"], "progress_every")
        return resolved

    def _validate_resolved_parameters(self, parameters: Mapping[str, object]) -> dict[str, object]:
        query_smiles = self._string(parameters.get("query_smiles"), "query_smiles")
        if parse_smiles(query_smiles) is None:
            raise ValueError("query_smiles is invalid")
        resolved = self._resolved_parameters(
            parameters,
            Chem.MolToSmiles(parse_smiles(query_smiles), canonical=True),
            {"kind": "resolved", "value": query_smiles},
        )
        # A reducer receives immutable plan metadata, whose query source and
        # fixed fingerprint are observational context rather than worker input.
        if "fingerprint" in parameters:
            fingerprint = parameters["fingerprint"]
            if fingerprint != {"algorithm": "morgan", "radius": FP_RADIUS, "fp_size": FP_SIZE}:
                raise ValueError("resolved fingerprint does not match SciMesh defaults")
        return resolved

    @staticmethod
    def _task_parameters(resolved: Mapping[str, object]) -> dict[str, object]:
        # max_rows is applied before sharding. Passing it to each task would
        # silently scan N rows per shard instead of the requested global prefix.
        return {
            key: value for key, value in resolved.items()
            if key in {"query_smiles", "top_k", "threshold", "threshold_direction", "progress_every"}
        }

    def _write_shards(
        self, input_path: Path, workspace: Path, shard_rows: int, max_rows: object
    ) -> list[Path]:
        limit = int(max_rows) if isinstance(max_rows, int) else None
        paths: list[Path] = []
        current: Path | None = None
        destination = None
        rows_in_shard = 0
        seen_rows = 0
        try:
            with input_path.open("r", encoding="utf-8", newline="") as source:
                reader = csv.DictReader(source, delimiter="\t")
                fieldnames = reader.fieldnames or []
                if not _REQUIRED_COLUMNS.issubset(set(fieldnames)):
                    missing = sorted(_REQUIRED_COLUMNS - set(fieldnames))
                    raise ValueError(f"dataset is missing required columns: {', '.join(missing)}")
                for row in reader:
                    if limit is not None and seen_rows >= limit:
                        break
                    if destination is None or rows_in_shard == shard_rows:
                        if destination is not None:
                            destination.close()
                        current = workspace / f"shard-{len(paths)}.tsv"
                        destination = current.open("w", encoding="utf-8", newline="")
                        writer = csv.DictWriter(destination, fieldnames=fieldnames, delimiter="\t", lineterminator="\n")
                        writer.writeheader()
                        paths.append(current)
                        rows_in_shard = 0
                    writer.writerow(row)
                    rows_in_shard += 1
                    seen_rows += 1
        finally:
            if destination is not None:
                destination.close()
        if not paths:
            raise ValueError("dataset has no data rows")
        return paths

    @staticmethod
    def _read_partial(path: Path, direction: object) -> Iterator[SimilarityMatch]:
        if not path.is_file():
            raise ValueError("materialized partial result is missing")
        if direction not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        previous_key: tuple[float, str, str] | None = None
        with path.open("r", encoding="utf-8", newline="") as source:
            reader = csv.DictReader(source)
            if tuple(reader.fieldnames or ()) != _SEARCH_COLUMNS:
                raise ValueError("partial result has an invalid CSV header")
            for expected_rank, row in enumerate(reader, start=1):
                if set(row) != set(_SEARCH_COLUMNS) or row["rank"] != str(expected_rank):
                    raise ValueError("partial result has an invalid rank")
                try:
                    similarity = float(row["similarity"])
                except (TypeError, ValueError) as error:
                    raise ValueError("partial result has an invalid similarity") from error
                if not math.isfinite(similarity) or not 0 <= similarity <= 1:
                    raise ValueError("partial result has an invalid similarity")
                match = SimilarityMatch(similarity, row["chembl_id"], row["canonical_smiles"])
                key = match.sort_key(direction)
                if previous_key is not None and key < previous_key:
                    raise ValueError("partial result is not sorted deterministically")
                previous_key = key
                yield match

    @staticmethod
    def _string(value: object, name: str) -> str:
        if not isinstance(value, str) or not value.strip() or len(value) > 200:
            raise ValueError(f"{name} must be a non-empty string")
        return value

    @staticmethod
    def _positive_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError(f"{name} must be a positive integer")
        return value

    @staticmethod
    def _nonnegative_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise ValueError(f"{name} must be a non-negative integer")
        return value

    @staticmethod
    def _unit_interval(value: object, name: str) -> float:
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or not 0 <= value <= 1:
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)


def run_similarity_search_shard(
    input_path: Path, parameters: Mapping[str, object], output_path: Path
) -> dict[str, int]:
    """Run one planned shard using the local reference implementation.

    This is the worker adapter used by CTX-08. It deliberately accepts only
    resolved ``query_smiles``: resolving an identifier independently in each
    shard would make the distributed search scientifically invalid.
    """
    allowed = {"query_smiles", "top_k", "threshold", "threshold_direction", "progress_every"}
    unknown = set(parameters) - allowed
    if unknown:
        raise ValueError(f"unsupported similarity-search parameters: {', '.join(sorted(unknown))}")
    query_smiles = parameters.get("query_smiles")
    if not isinstance(query_smiles, str) or not query_smiles.strip():
        raise ValueError("query_smiles is required for a distributed shard")
    molecule = parse_smiles(query_smiles)
    if molecule is None:
        raise ValueError("query_smiles is invalid")
    top_k = SimilaritySearchDistributedWorkload._positive_int(parameters.get("top_k", 20), "top_k")
    threshold = None
    if "threshold" in parameters:
        threshold = SimilaritySearchDistributedWorkload._unit_interval(parameters["threshold"], "threshold")
    direction = parameters.get("threshold_direction", "greater")
    if direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    progress_every = 0
    if "progress_every" in parameters:
        progress_every = SimilaritySearchDistributedWorkload._nonnegative_int(
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
    write_similarity_search_partial(output_path, result.matches)
    return {
        "scanned_rows": result.stats.scanned,
        "valid_molecules": result.stats.valid,
        "invalid_smiles": result.stats.invalid,
        "matches_emitted": len(result.matches),
    }


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()
