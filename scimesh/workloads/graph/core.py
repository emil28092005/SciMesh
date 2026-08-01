"""Scientific core for the SDK-built ``similarity-graph`` workload.

Molecules are parsed once into deterministic row-ordered blocks; every block
pair ``(i, j)`` with ``i <= j`` becomes one map task (diagonal tasks compare
pairs ``a < b`` inside a block, off-diagonal tasks compare every molecule
across two blocks). The reducer enforces the CTX-10 pair-coverage invariant:
the union of task pair sets must equal all unordered molecule pairs exactly
once, and the merged edge set must contain no duplicate unordered pair.
"""

from __future__ import annotations

import csv
from pathlib import Path
from typing import Iterable, Sequence

from rdkit import Chem, DataStructs

from scimesh.chemistry.dataset import iter_valid_molecules
from scimesh.chemistry.fingerprints import fingerprint

EDGE_COLUMNS = ("source_id", "target_id", "similarity")

MoleculeBlock = list[tuple[str, str]]  # (chembl_id, smiles), row-ordered


def parse_molecule_blocks(
    input_path: Path,
    block_size: int,
    max_rows: int | None = None,
) -> tuple[list[MoleculeBlock], dict[str, int]]:
    """Parse valid molecules into deterministic row-ordered blocks.

    Mirrors the local reference's strictness: an empty or duplicate
    ``chembl_id`` fails the run, because the edge identity is the molecule id.
    Invalid SMILES rows are skipped and counted.
    """
    if (
        isinstance(block_size, bool)
        or not isinstance(block_size, int)
        or block_size < 1
    ):
        raise ValueError("block_size must be a positive integer")
    from scimesh.chemistry.dataset import DatasetStats

    stats = DatasetStats()
    blocks: list[MoleculeBlock] = []
    current: MoleculeBlock = []
    seen_ids: set[str] = set()
    for record in iter_valid_molecules(input_path, stats, max_rows=max_rows):
        if not record.molecule_id:
            raise ValueError("dataset contains an empty chembl_id")
        if record.molecule_id in seen_ids:
            raise ValueError(
                f"dataset contains a duplicate chembl_id: {record.molecule_id}"
            )
        seen_ids.add(record.molecule_id)
        current.append((record.molecule_id, record.smiles))
        if len(current) == block_size:
            blocks.append(current)
            current = []
    if current:
        blocks.append(current)
    if not blocks:
        raise ValueError("dataset has no valid molecules")
    return blocks, {
        "rows_scanned": stats.scanned,
        "valid_molecules": stats.valid,
        "invalid_smiles": stats.invalid,
        "block_count": len(blocks),
    }


def write_block_tsv(rows: MoleculeBlock, path: Path) -> None:
    """Write one molecule block as a header TSV with the input column names."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(
            destination,
            fieldnames=["chembl_id", "canonical_smiles"],
            delimiter="\t",
            lineterminator="\n",
        )
        writer.writeheader()
        for molecule_id, smiles in rows:
            writer.writerow({"chembl_id": molecule_id, "canonical_smiles": smiles})


def read_block_rows(path: Path) -> MoleculeBlock:
    rows: MoleculeBlock = []
    with path.open("r", encoding="utf-8", newline="") as source:
        reader = csv.DictReader(source, delimiter="\t")
        for row in reader:
            molecule_id = row.get("chembl_id", "")
            smiles = row.get("canonical_smiles", "")
            if not molecule_id or not smiles:
                raise ValueError("block artifact contains an invalid row")
            rows.append((molecule_id, smiles))
    return rows


def compute_block_edges(
    left: MoleculeBlock,
    right: MoleculeBlock,
    threshold: float,
    threshold_direction: str,
) -> list[tuple[str, str, float]]:
    """Compare one planned block pair and emit only thresholded edges."""
    if not 0.0 <= threshold <= 1.0:
        raise ValueError("threshold must be between 0 and 1")
    if threshold_direction not in {"greater", "less"}:
        raise ValueError("threshold_direction must be 'greater' or 'less'")
    left_fingerprints = [
        (molecule_id, fingerprint(Chem.MolFromSmiles(smiles)))
        for molecule_id, smiles in left
    ]
    right_fingerprints = [
        (molecule_id, fingerprint(Chem.MolFromSmiles(smiles)))
        for molecule_id, smiles in right
    ]
    diagonal = left is right or left == right
    edges: list[tuple[str, str, float]] = []
    for left_index, (left_id, left_fp) in enumerate(left_fingerprints):
        right_start = left_index + 1 if diagonal else 0
        for right_index in range(right_start, len(right_fingerprints)):
            right_id, right_fp = right_fingerprints[right_index]
            similarity = DataStructs.TanimotoSimilarity(left_fp, right_fp)
            matches_threshold = (
                similarity >= threshold
                if threshold_direction == "greater"
                else similarity <= threshold
            )
            if matches_threshold:
                edges.append((left_id, right_id, similarity))
    return edges


def write_edge_csv(output_path: Path, edges: Iterable[tuple[str, str, float]]) -> None:
    """Write an edge table CSV with six-decimal similarity values.

    Uses the CSV module's default ``\\r\\n`` line terminator so the bytes match
    the local ``write_graph_edges`` reference exactly.
    """
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(destination, fieldnames=list(EDGE_COLUMNS))
        writer.writeheader()
        for source_id, target_id, similarity in edges:
            writer.writerow(
                {
                    "source_id": source_id,
                    "target_id": target_id,
                    "similarity": f"{similarity:.6f}",
                }
            )


def read_edge_csv(path: Path) -> list[tuple[str, str, float]]:
    """Read a materialized edge CSV with strict row validation."""
    if not path.is_file():
        raise ValueError("materialized edge partial is missing")
    edges: list[tuple[str, str, float]] = []
    with path.open("r", encoding="utf-8", newline="") as source:
        reader = csv.DictReader(source)
        if tuple(reader.fieldnames or ()) != EDGE_COLUMNS:
            raise ValueError("edge partial has an invalid CSV header")
        for row in reader:
            if set(row) != set(EDGE_COLUMNS):
                raise ValueError("edge partial has an invalid row")
            source_id = row["source_id"]
            target_id = row["target_id"]
            if not source_id or not target_id:
                raise ValueError("edge partial contains an empty molecule id")
            try:
                similarity = float(row["similarity"])
            except (TypeError, ValueError) as error:
                raise ValueError("edge partial has an invalid similarity") from error
            if not 0 <= similarity <= 1:
                raise ValueError("edge partial has an invalid similarity")
            edges.append((source_id, target_id, similarity))
    return edges


def block_pair_from_key(key: str) -> tuple[int, int]:
    """Parse ``map.<i>x<j>`` partial keys into block indices."""
    prefix = "map."
    if not key.startswith(prefix):
        raise ValueError("graph partial key must use map.<left>x<right>")
    raw = key[len(prefix) :]
    left_raw, separator, right_raw = raw.partition("x")
    if not separator or not left_raw.isdigit() or not right_raw.isdigit():
        raise ValueError("graph partial key must use map.<left>x<right>")
    return int(left_raw), int(right_raw)


def check_pair_coverage(pairs: Sequence[tuple[int, int]]) -> None:
    """Enforce the pair-coverage invariant: every block pair exactly once.

    ``pairs`` must contain every ``(i, j)`` with ``0 <= i <= j < n`` exactly
    once, where ``n`` is derived from the largest referenced block index.
    """
    if not pairs:
        raise ValueError("graph partial keys cover no block pairs")
    unique = set(pairs)
    if len(unique) != len(pairs):
        raise ValueError("graph partial keys contain a duplicate block pair")
    if any(left > right or left < 0 or right < 0 for left, right in unique):
        raise ValueError("graph partial keys reference an invalid block pair")
    n = max(right for _, right in unique) + 1
    expected = {(left, right) for left in range(n) for right in range(left, n)}
    missing = sorted(expected - unique)
    if missing:
        raise ValueError(
            "graph partial keys do not cover the full block pair set: "
            + ", ".join(f"{left}x{right}" for left, right in missing)
        )
    unexpected = sorted(unique - expected)
    if unexpected:
        raise ValueError(
            "graph partial keys cover pairs outside the block pair set: "
            + ", ".join(f"{left}x{right}" for left, right in unexpected)
        )


def merge_edge_partials(
    partial_paths: Sequence[Path],
    output_path: Path,
) -> dict[str, int]:
    """Merge edge partials with duplicate detection and deterministic sort.

    The merged edge list is sorted by ``(source_id, target_id, -similarity)``,
    matching the local brute-force reference exactly.
    """
    if not partial_paths:
        raise ValueError("graph reducer requires at least one edge partial")
    edges: list[tuple[str, str, float]] = []
    seen_pairs: set[tuple[str, str]] = set()
    for path in partial_paths:
        for source_id, target_id, similarity in read_edge_csv(path):
            unordered = (min(source_id, target_id), max(source_id, target_id))
            if unordered in seen_pairs:
                raise ValueError(
                    f"graph partials contain a duplicate unordered pair: {unordered[0]}, {unordered[1]}"
                )
            seen_pairs.add(unordered)
            edges.append((source_id, target_id, similarity))
    edges.sort(key=lambda edge: (edge[0], edge[1], -edge[2]))
    write_edge_csv(output_path, edges)
    return {"partial_count": len(partial_paths), "edges_emitted": len(edges)}
