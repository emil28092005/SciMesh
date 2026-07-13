"""Streaming readers for ChEMBL-style TSV datasets."""

from __future__ import annotations

import csv
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from rdkit import Chem, RDLogger


ID_COLUMN = "chembl_id"
SMILES_COLUMN = "canonical_smiles"

# Invalid records are expected in large datasets; suppress one RDKit error per row.
RDLogger.DisableLog("rdApp.error")


@dataclass
class DatasetStats:
    """Counters collected while streaming a dataset."""

    scanned: int = 0
    valid: int = 0
    invalid: int = 0
    stopped_early: bool = False


@dataclass(frozen=True)
class MoleculeRecord:
    """A valid molecule parsed from a ChEMBL TSV row."""

    molecule_id: str
    smiles: str
    molecule: Chem.Mol


def parse_smiles(smiles: str | None) -> Chem.Mol | None:
    """Return a molecule for a non-empty valid SMILES, otherwise None."""
    if not smiles:
        return None
    return Chem.MolFromSmiles(smiles)


def iter_rows(tsv_path: Path) -> Iterator[dict[str, str]]:
    """Yield TSV rows without loading the full file into memory."""
    with tsv_path.open("r", encoding="utf-8", newline="") as source:
        reader = csv.DictReader(source, delimiter="\t")
        fieldnames = set(reader.fieldnames or [])
        missing = {ID_COLUMN, SMILES_COLUMN} - fieldnames
        if missing:
            raise ValueError(f"Dataset is missing required columns: {', '.join(sorted(missing))}")
        yield from reader


def iter_valid_molecules(
    tsv_path: Path, stats: DatasetStats, max_rows: int | None = None
) -> Iterator[MoleculeRecord]:
    """Yield valid molecules while updating streaming statistics."""
    for row in iter_rows(tsv_path):
        if max_rows is not None and stats.scanned >= max_rows:
            stats.stopped_early = True
            break
        stats.scanned += 1
        smiles = row.get(SMILES_COLUMN, "")
        molecule = parse_smiles(smiles)
        if molecule is None:
            stats.invalid += 1
            continue
        stats.valid += 1
        yield MoleculeRecord(row.get(ID_COLUMN, ""), smiles, molecule)


def find_molecule_by_id(tsv_path: Path, molecule_id: str) -> MoleculeRecord:
    """Find and validate a molecule by ChEMBL identifier in a streaming pass."""
    for row in iter_rows(tsv_path):
        if row.get(ID_COLUMN) != molecule_id:
            continue
        smiles = row.get(SMILES_COLUMN, "")
        molecule = parse_smiles(smiles)
        if molecule is None:
            raise ValueError(f"{molecule_id} has an invalid canonical_smiles")
        return MoleculeRecord(molecule_id, smiles, molecule)
    raise ValueError(f"{molecule_id} was not found in {tsv_path}")
