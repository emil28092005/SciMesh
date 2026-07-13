#!/usr/bin/env python3
"""SciMesh: streaming search for ChEMBL molecules similar to gefitinib."""

from __future__ import annotations

import argparse
import csv
import heapq
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from rdkit import Chem, DataStructs, RDLogger
from rdkit.Chem import Draw, rdFingerprintGenerator


REFERENCE_CHEMBL_ID = "CHEMBL939"
FP_RADIUS = 2
FP_SIZE = 2048

# Invalid records are intentionally skipped, so do not emit one RDKit error per row.
RDLogger.DisableLog("rdApp.error")


@dataclass
class SearchStats:
    """Counters collected while scanning the candidate records."""

    scanned: int = 0
    valid: int = 0
    invalid: int = 0
    stopped_early: bool = False


def rows(tsv_path: Path) -> Iterator[dict[str, str]]:
    """Read ChEMBL records one at a time without loading the file into memory."""
    with tsv_path.open("r", encoding="utf-8", newline="") as source:
        yield from csv.DictReader(source, delimiter="\t")


def molecule(smiles: str | None) -> Chem.Mol | None:
    """Return a parsed molecule, or None when a SMILES is empty or invalid."""
    if not smiles:
        return None
    return Chem.MolFromSmiles(smiles)


def find_reference_smiles(tsv_path: Path) -> str:
    """Locate CHEMBL939 and return its canonical_smiles."""
    for row in rows(tsv_path):
        if row.get("chembl_id") == REFERENCE_CHEMBL_ID:
            smiles = row.get("canonical_smiles", "")
            if molecule(smiles) is None:
                raise ValueError(f"{REFERENCE_CHEMBL_ID} has an invalid canonical_smiles")
            return smiles
    raise ValueError(f"{REFERENCE_CHEMBL_ID} was not found in {tsv_path}")


def find_similar(
    tsv_path: Path,
    reference_smiles: str,
    limit: int,
    progress_every: int,
    max_rows: int | None,
) -> tuple[list[tuple[float, str, str]], SearchStats]:
    """Calculate top similarities, retaining only ``limit`` rows in memory."""
    generator = rdFingerprintGenerator.GetMorganGenerator(
        radius=FP_RADIUS, fpSize=FP_SIZE
    )
    reference_fp = generator.GetFingerprint(molecule(reference_smiles))
    best: list[tuple[float, str, str]] = []
    stats = SearchStats()
    started_at = time.perf_counter()
    last_report_at = started_at
    last_report_rows = 0

    print("Searching candidates...", file=sys.stderr)

    for row in rows(tsv_path):
        if max_rows is not None and stats.scanned >= max_rows:
            stats.stopped_early = True
            break
        stats.scanned += 1
        chembl_id = row.get("chembl_id", "")
        smiles = row.get("canonical_smiles", "")
        if chembl_id != REFERENCE_CHEMBL_ID:
            mol = molecule(smiles)
            if mol is None:
                stats.invalid += 1
            else:
                stats.valid += 1
                similarity = DataStructs.TanimotoSimilarity(
                    reference_fp, generator.GetFingerprint(mol)
                )
                candidate = (similarity, chembl_id, smiles)
                if len(best) < limit:
                    heapq.heappush(best, candidate)
                elif candidate > best[0]:
                    heapq.heapreplace(best, candidate)

        if progress_every and stats.scanned % progress_every == 0:
            now = time.perf_counter()
            interval_seconds = now - last_report_at
            total_seconds = now - started_at
            interval_rows = stats.scanned - last_report_rows
            current_rate = interval_rows / interval_seconds if interval_seconds else 0.0
            average_rate = stats.scanned / total_seconds if total_seconds else 0.0
            print(
                "Processed "
                f"{stats.scanned:,} rows | {current_rate:,.0f} rows/s current | "
                f"{average_rate:,.0f} rows/s average | {total_seconds:.1f}s elapsed | "
                f"{stats.valid:,} valid | {stats.invalid:,} invalid | top {len(best)}",
                file=sys.stderr,
            )
            last_report_at = now
            last_report_rows = stats.scanned

    return sorted(best, reverse=True), stats


def write_results(output_path: Path, matches: list[tuple[float, str, str]]) -> None:
    """Write ranked matching records as CSV."""
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(
            destination,
            fieldnames=["rank", "chembl_id", "canonical_smiles", "similarity"],
        )
        writer.writeheader()
        for rank, (similarity, chembl_id, smiles) in enumerate(matches, start=1):
            writer.writerow(
                {
                    "rank": rank,
                    "chembl_id": chembl_id,
                    "canonical_smiles": smiles,
                    "similarity": f"{similarity:.6f}",
                }
            )


def write_images(
    output_dir: Path,
    reference_smiles: str,
    matches: list[tuple[float, str, str]],
    columns: int,
) -> tuple[Path, Path]:
    """Create PNG depictions for the reference molecule and ranked matches."""
    output_dir.mkdir(parents=True, exist_ok=True)

    reference_path = output_dir / "CHEMBL939_gefitinib.png"
    reference_image = Draw.MolToImage(
        molecule(reference_smiles), size=(600, 400), legend="CHEMBL939 (gefitinib)"
    )
    reference_image.save(reference_path)

    candidate_path = output_dir / "top_candidates.png"
    candidate_molecules = [molecule(smiles) for _, _, smiles in matches]
    legends = [
        f"#{rank} {chembl_id}\nTanimoto: {similarity:.4f}"
        for rank, (similarity, chembl_id, _) in enumerate(matches, start=1)
    ]
    candidate_image = Draw.MolsToGridImage(
        candidate_molecules,
        molsPerRow=columns,
        subImgSize=(350, 250),
        legends=legends,
    )
    candidate_image.save(candidate_path)
    return reference_path, candidate_path


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Find ChEMBL molecules similar to gefitinib (CHEMBL939)."
    )
    parser.add_argument("input", type=Path, help="Path to ChEMBL TSV file")
    parser.add_argument(
        "-o",
        "--output",
        type=Path,
        default=Path("gefitinib_similarities.csv"),
        help="Output CSV path (default: gefitinib_similarities.csv)",
    )
    parser.add_argument(
        "--top",
        type=int,
        default=20,
        help="Number of matches to retain (default: 20)",
    )
    parser.add_argument(
        "--progress-every",
        type=int,
        default=100_000,
        help="Print progress after this many rows; 0 disables it (default: 100000)",
    )
    parser.add_argument(
        "--max-rows",
        type=int,
        help="For testing, scan only the first N TSV rows after locating CHEMBL939",
    )
    parser.add_argument(
        "--images-dir",
        type=Path,
        help="Create reference and top-candidate PNG images in this directory",
    )
    parser.add_argument(
        "--image-columns",
        type=int,
        default=4,
        help="Number of molecules per row in the candidate image (default: 4)",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.top < 1:
        print("--top must be a positive integer", file=sys.stderr)
        return 2
    if args.progress_every < 0:
        print("--progress-every cannot be negative", file=sys.stderr)
        return 2
    if args.max_rows is not None and args.max_rows < 1:
        print("--max-rows must be a positive integer", file=sys.stderr)
        return 2
    if args.image_columns < 1:
        print("--image-columns must be a positive integer", file=sys.stderr)
        return 2
    if not args.input.is_file():
        print(f"Input file not found: {args.input}", file=sys.stderr)
        return 2

    try:
        reference_smiles = find_reference_smiles(args.input)
        matches, stats = find_similar(
            args.input,
            reference_smiles,
            args.top,
            args.progress_every,
            args.max_rows,
        )
    except ValueError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1

    write_results(args.output, matches)
    image_paths: tuple[Path, Path] | None = None
    if args.images_dir:
        image_paths = write_images(
            args.images_dir, reference_smiles, matches, args.image_columns
        )
    print(f"Reference {REFERENCE_CHEMBL_ID}: {reference_smiles}")
    print(
        f"Scanned {stats.scanned:,} rows: {stats.valid:,} valid, "
        f"{stats.invalid:,} invalid SMILES."
    )
    if stats.stopped_early:
        print("Stopped early because of --max-rows; results cover only that subset.")
    print(f"Saved {len(matches)} matches to {args.output}")
    if image_paths:
        print(f"Saved reference image to {image_paths[0]}")
        print(f"Saved candidate image to {image_paths[1]}")
    for rank, (similarity, chembl_id, smiles) in enumerate(matches, start=1):
        print(f"{rank:>2}. {chembl_id}\t{similarity:.6f}\t{smiles}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
