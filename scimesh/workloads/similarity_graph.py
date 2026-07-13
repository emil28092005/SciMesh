"""Exact sparse molecular similarity graph workload."""

from __future__ import annotations

import argparse
import csv
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from rdkit import DataStructs

from scimesh.chemistry.dataset import DatasetStats, MoleculeRecord, iter_valid_molecules
from scimesh.chemistry.fingerprints import fingerprint


@dataclass(frozen=True)
class GraphMolecule:
    """A valid record and its fingerprint, built once for graph construction."""

    molecule_id: str
    fingerprint: Any


@dataclass(frozen=True)
class SimilarityEdge:
    """A thresholded, undirected similarity edge represented once as i < j."""

    source_id: str
    target_id: str
    similarity: float


@dataclass
class GraphResult:
    """Edges, dataset statistics, and pair-comparison statistics."""

    edges: list[SimilarityEdge]
    stats: DatasetStats
    checked_pairs: int
    elapsed_seconds: float


def _fingerprinted_molecules(
    tsv_path: Path, max_rows: int | None
) -> tuple[list[GraphMolecule], DatasetStats]:
    stats = DatasetStats()
    molecules = [
        GraphMolecule(record.molecule_id, fingerprint(record.molecule))
        for record in iter_valid_molecules(tsv_path, stats, max_rows=max_rows)
    ]
    return molecules, stats


def build_similarity_graph(
    tsv_path: Path,
    threshold: float,
    block_size: int,
    max_rows: int | None = None,
    progress_every: int = 0,
) -> GraphResult:
    """Build an exact sparse graph without creating a dense similarity matrix."""
    if not 0.0 <= threshold <= 1.0:
        raise ValueError("--threshold must be between 0 and 1")
    if block_size < 1:
        raise ValueError("--block-size must be a positive integer")

    molecules, stats = _fingerprinted_molecules(tsv_path, max_rows)
    edges: list[SimilarityEdge] = []
    checked_pairs = 0
    started_at = time.perf_counter()
    next_report = progress_every

    for left_block_start in range(0, len(molecules), block_size):
        left_block_end = min(left_block_start + block_size, len(molecules))
        for right_block_start in range(left_block_start, len(molecules), block_size):
            right_block_end = min(right_block_start + block_size, len(molecules))
            same_block = left_block_start == right_block_start
            for left_index in range(left_block_start, left_block_end):
                right_start = left_index + 1 if same_block else right_block_start
                for right_index in range(right_start, right_block_end):
                    checked_pairs += 1
                    similarity = DataStructs.TanimotoSimilarity(
                        molecules[left_index].fingerprint,
                        molecules[right_index].fingerprint,
                    )
                    if similarity >= threshold:
                        edges.append(
                            SimilarityEdge(
                                molecules[left_index].molecule_id,
                                molecules[right_index].molecule_id,
                                similarity,
                            )
                        )
                    if progress_every and checked_pairs >= next_report:
                        elapsed = time.perf_counter() - started_at
                        rate = checked_pairs / elapsed if elapsed else 0.0
                        print(
                            f"Checked {checked_pairs:,} pairs | {len(edges):,} edges | "
                            f"{rate:,.0f} pairs/s | {elapsed:.1f}s elapsed",
                            file=sys.stderr,
                        )
                        next_report += progress_every

    elapsed_seconds = time.perf_counter() - started_at
    edges.sort(key=lambda edge: (edge.source_id, edge.target_id, -edge.similarity))
    return GraphResult(edges, stats, checked_pairs, elapsed_seconds)


def write_graph_edges(output_path: Path, edges: list[SimilarityEdge]) -> None:
    """Write a deterministic sparse edge list CSV."""
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(destination, fieldnames=["source_id", "target_id", "similarity"])
        writer.writeheader()
        for edge in edges:
            writer.writerow(
                {
                    "source_id": edge.source_id,
                    "target_id": edge.target_id,
                    "similarity": f"{edge.similarity:.6f}",
                }
            )


class SimilarityGraphWorkload:
    """CLI adapter for exact block-wise sparse similarity graph construction."""

    name = "similarity-graph"
    help = "Build an exact sparse graph of thresholded molecular similarities."

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        parser.add_argument("input", type=Path, help="Path to ChEMBL TSV file")
        parser.add_argument(
            "--threshold", type=float, required=True,
            help="Create edges at or above this Tanimoto similarity",
        )
        parser.add_argument(
            "--block-size", type=int, default=1_000,
            help="Number of molecules per comparison block (default: 1000)",
        )
        parser.add_argument(
            "--max-rows", type=int,
            help="Read only the first N dataset rows",
        )
        parser.add_argument(
            "--progress-every", type=int, default=100_000,
            help="Print progress after this many pairs; 0 disables it",
        )
        parser.add_argument(
            "-o", "--output", type=Path, default=Path("similarity_graph.csv"),
            help="Output edge-list CSV path",
        )

    def run(self, args: argparse.Namespace) -> int:
        if args.max_rows is not None and args.max_rows < 1:
            raise ValueError("--max-rows must be a positive integer")
        if args.progress_every < 0:
            raise ValueError("--progress-every cannot be negative")
        result = build_similarity_graph(
            args.input,
            args.threshold,
            args.block_size,
            args.max_rows,
            args.progress_every,
        )
        write_graph_edges(args.output, result.edges)
        rate = (
            result.checked_pairs / result.elapsed_seconds
            if result.elapsed_seconds
            else 0.0
        )
        print(
            f"Valid molecules: {result.stats.valid:,} | invalid SMILES: "
            f"{result.stats.invalid:,} | scanned rows: {result.stats.scanned:,}"
        )
        print(
            f"Checked pairs: {result.checked_pairs:,} | edges: {len(result.edges):,} | "
            f"{rate:,.0f} pairs/s | {result.elapsed_seconds:.1f}s elapsed"
        )
        if result.stats.stopped_early:
            print("Stopped early because of --max-rows; graph covers only that subset.")
        print(f"Saved {len(result.edges)} edges to {args.output}")
        return 0
