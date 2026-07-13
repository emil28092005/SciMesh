"""Top-k molecular similarity search workload."""

from __future__ import annotations

import argparse
import csv
import heapq
import sys
import time
from dataclasses import dataclass
from pathlib import Path

from rdkit import Chem, DataStructs
from rdkit.Chem import Draw

from scimesh.chemistry.dataset import (
    DatasetStats,
    MoleculeRecord,
    find_molecule_by_id,
    iter_valid_molecules,
    parse_smiles,
)
from scimesh.chemistry.fingerprints import fingerprint


@dataclass(frozen=True)
class SimilarityMatch:
    """A candidate ranked by descending similarity and stable tie-breakers."""

    similarity: float
    molecule_id: str
    smiles: str

    def sort_key(self) -> tuple[float, str, str]:
        return (-self.similarity, self.molecule_id, self.smiles)


@dataclass
class _HeapEntry:
    """Heap item whose minimum is the worst retained match."""

    match: SimilarityMatch

    def __lt__(self, other: object) -> bool:
        if not isinstance(other, _HeapEntry):
            return NotImplemented
        return self.match.sort_key() > other.match.sort_key()


@dataclass
class SearchResult:
    """Results and scan statistics for a similarity search."""

    matches: list[SimilarityMatch]
    stats: DatasetStats


def search_similar(
    tsv_path: Path,
    query: MoleculeRecord,
    top_k: int,
    max_rows: int | None = None,
    progress_every: int = 0,
) -> SearchResult:
    """Stream top-k matches, retaining only a bounded heap in memory."""
    if top_k < 1:
        raise ValueError("--top-k must be a positive integer")
    query_fingerprint = fingerprint(query.molecule)
    query_canonical_smiles = Chem.MolToSmiles(query.molecule, canonical=True)
    stats = DatasetStats()
    heap: list[_HeapEntry] = []
    started_at = time.perf_counter()
    last_report_at = started_at
    last_report_rows = 0

    for record in iter_valid_molecules(tsv_path, stats, max_rows=max_rows):
        candidate_canonical_smiles = Chem.MolToSmiles(record.molecule, canonical=True)
        if (
            record.molecule_id == query.molecule_id
            or candidate_canonical_smiles == query_canonical_smiles
        ):
            continue
        match = SimilarityMatch(
            DataStructs.TanimotoSimilarity(query_fingerprint, fingerprint(record.molecule)),
            record.molecule_id,
            record.smiles,
        )
        entry = _HeapEntry(match)
        if len(heap) < top_k:
            heapq.heappush(heap, entry)
        elif match.sort_key() < heap[0].match.sort_key():
            heapq.heapreplace(heap, entry)

        if progress_every and stats.scanned % progress_every == 0:
            now = time.perf_counter()
            interval = now - last_report_at
            total = now - started_at
            current_rate = (stats.scanned - last_report_rows) / interval if interval else 0.0
            average_rate = stats.scanned / total if total else 0.0
            print(
                f"Processed {stats.scanned:,} rows | {current_rate:,.0f} rows/s current | "
                f"{average_rate:,.0f} rows/s average | {total:.1f}s elapsed | "
                f"{stats.valid:,} valid | {stats.invalid:,} invalid | top {len(heap)}",
                file=sys.stderr,
            )
            last_report_at = now
            last_report_rows = stats.scanned

    return SearchResult(sorted((entry.match for entry in heap), key=SimilarityMatch.sort_key), stats)


def write_search_results(output_path: Path, matches: list[SimilarityMatch]) -> None:
    """Write ranked matches to a deterministic CSV file."""
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(
            destination,
            fieldnames=["rank", "chembl_id", "canonical_smiles", "similarity"],
        )
        writer.writeheader()
        for rank, match in enumerate(matches, start=1):
            writer.writerow(
                {
                    "rank": rank,
                    "chembl_id": match.molecule_id,
                    "canonical_smiles": match.smiles,
                    "similarity": f"{match.similarity:.6f}",
                }
            )


def write_search_images(
    output_dir: Path, query: MoleculeRecord, matches: list[SimilarityMatch], columns: int
) -> tuple[Path, Path]:
    """Create PNG depictions for the query molecule and retained matches."""
    output_dir.mkdir(parents=True, exist_ok=True)
    query_path = output_dir / "query.png"
    Draw.MolToImage(
        query.molecule, size=(600, 400), legend=f"Query: {query.molecule_id}"
    ).save(query_path)

    candidates_path = output_dir / "top_candidates.png"
    molecules = [parse_smiles(match.smiles) for match in matches]
    legends = [
        f"#{rank} {match.molecule_id}\nTanimoto: {match.similarity:.4f}"
        for rank, match in enumerate(matches, start=1)
    ]
    Draw.MolsToGridImage(
        molecules, molsPerRow=columns, subImgSize=(350, 250), legends=legends
    ).save(candidates_path)
    return query_path, candidates_path


class SimilaritySearchWorkload:
    """CLI adapter for streaming top-k molecular similarity search."""

    name = "similarity-search"
    help = "Find top-k molecules similar to a query molecule."

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        parser.add_argument("input", type=Path, help="Path to ChEMBL TSV file")
        query_group = parser.add_mutually_exclusive_group(required=True)
        query_group.add_argument("--query-id", help="ChEMBL ID of the query molecule")
        query_group.add_argument("--query-smiles", help="SMILES of the query molecule")
        parser.add_argument(
            "--top-k", "--top", dest="top_k", type=int, default=20,
            help="Number of matches to retain (default: 20)",
        )
        parser.add_argument(
            "-o", "--output", type=Path, default=Path("similarity_results.csv"),
            help="Output CSV path",
        )
        parser.add_argument(
            "--progress-every", type=int, default=100_000,
            help="Print progress after this many rows; 0 disables it",
        )
        parser.add_argument(
            "--max-rows", type=int,
            help="Scan only the first N rows after resolving the query",
        )
        parser.add_argument(
            "--images-dir", type=Path,
            help="Directory for query and top-candidate PNG images",
        )
        parser.add_argument(
            "--image-columns", type=int, default=4,
            help="Number of molecules per row in the candidate image",
        )

    def run(self, args: argparse.Namespace) -> int:
        if args.progress_every < 0:
            raise ValueError("--progress-every cannot be negative")
        if args.max_rows is not None and args.max_rows < 1:
            raise ValueError("--max-rows must be a positive integer")
        if args.image_columns < 1:
            raise ValueError("--image-columns must be a positive integer")
        if args.query_id:
            query = find_molecule_by_id(args.input, args.query_id)
        else:
            molecule = parse_smiles(args.query_smiles)
            if molecule is None:
                raise ValueError("--query-smiles is invalid")
            query = MoleculeRecord("query", args.query_smiles, molecule)

        result = search_similar(
            args.input, query, args.top_k, args.max_rows, args.progress_every
        )
        write_search_results(args.output, result.matches)
        image_paths: tuple[Path, Path] | None = None
        if args.images_dir:
            image_paths = write_search_images(
                args.images_dir, query, result.matches, args.image_columns
            )

        print(f"Query {query.molecule_id}: {query.smiles}")
        print(
            f"Scanned {result.stats.scanned:,} rows: {result.stats.valid:,} valid, "
            f"{result.stats.invalid:,} invalid SMILES."
        )
        if result.stats.stopped_early:
            print("Stopped early because of --max-rows; results cover only that subset.")
        print(f"Saved {len(result.matches)} matches to {args.output}")
        if image_paths:
            print(f"Saved query image to {image_paths[0]}")
            print(f"Saved candidate image to {image_paths[1]}")
        return 0
