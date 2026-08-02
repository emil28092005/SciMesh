"""Pinned RDKit 2D descriptor computation for the descriptor-batch workload.

The scientific contract of ``descriptor-batch`` is deliberately small and
fully pinned:

- exactly one output row per valid input molecule, in input order;
- RDKit canonical SMILES recomputed with ``MolToSmiles(..., canonical=True)``;
- the descriptor set is an explicit, versioned tuple of RDKit
  ``Descriptors.descList`` names (2D only), not a scan of installed names;
- float values are serialized with fixed ``%.6f`` formatting so that the
  output is byte-identical for identical inputs and a pinned environment;
- invalid SMILES rows are either skipped (counted) or fail the run, selected
  by the explicit ``skip_invalid`` parameter.
"""

from __future__ import annotations

import csv
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Iterator, Sequence

from rdkit import Chem
from rdkit.ML.Descriptors.MoleculeDescriptors import MolecularDescriptorCalculator

from scimesh.chemistry.dataset import iter_rows

# Explicit pinned list. Names must exist in the installed RDKit ``descList``;
# the list itself is the reproducibility contract and must change version
# together with the workload (descriptor-batch@1.0.0).
DESCRIPTOR_NAMES: tuple[str, ...] = (
    "ExactMolWt",
    "MolWt",
    "HeavyAtomMolWt",
    "HeavyAtomCount",
    "NumHDonors",
    "NumHAcceptors",
    "NumRotatableBonds",
    "NumHeteroatoms",
    "NumRadicalElectrons",
    "NumValenceElectrons",
    "FractionCSP3",
    "RingCount",
    "NumAromaticRings",
    "NumSaturatedRings",
    "NumAliphaticRings",
    "NumAromaticHeterocycles",
    "NumSaturatedHeterocycles",
    "NumAliphaticHeterocycles",
    "NumAromaticCarbocycles",
    "NumSaturatedCarbocycles",
    "NumAliphaticCarbocycles",
    "TPSA",
    "LabuteASA",
    "MolLogP",
    "MolMR",
    "BalabanJ",
    "BertzCT",
    "HallKierAlpha",
    "Kappa1",
    "Kappa2",
    "Kappa3",
    "Chi0",
    "Chi1",
    "Chi0n",
    "Chi1n",
    "Chi2n",
    "Chi3n",
    "Chi4n",
    "Chi0v",
    "Chi1v",
    "Chi2v",
    "Chi3v",
    "Chi4v",
    "PEOE_VSA1",
    "PEOE_VSA2",
    "PEOE_VSA3",
    "PEOE_VSA4",
    "PEOE_VSA5",
    "PEOE_VSA6",
    "PEOE_VSA7",
    "PEOE_VSA8",
    "PEOE_VSA9",
    "PEOE_VSA10",
    "PEOE_VSA11",
    "PEOE_VSA12",
    "PEOE_VSA13",
    "PEOE_VSA14",
    "SMR_VSA1",
    "SMR_VSA2",
    "SMR_VSA3",
    "SMR_VSA4",
    "SMR_VSA5",
    "SMR_VSA6",
    "SMR_VSA7",
    "SMR_VSA8",
    "SMR_VSA9",
    "SMR_VSA10",
    "SlogP_VSA1",
    "SlogP_VSA2",
    "SlogP_VSA3",
    "SlogP_VSA4",
    "SlogP_VSA5",
    "SlogP_VSA6",
    "SlogP_VSA7",
    "SlogP_VSA8",
    "SlogP_VSA9",
    "SlogP_VSA10",
    "SlogP_VSA11",
    "SlogP_VSA12",
    "NHOHCount",
    "NOCount",
)

DESCRIPTOR_COLUMNS: tuple[str, ...] = (
    "chembl_id",
    "canonical_smiles",
) + DESCRIPTOR_NAMES


@lru_cache(maxsize=1)
def descriptor_calculator() -> MolecularDescriptorCalculator:
    """Build the pinned calculator once per process."""
    return MolecularDescriptorCalculator(DESCRIPTOR_NAMES)


def validate_descriptor_names() -> None:
    """Fail fast when the pinned list is unavailable in the installed RDKit."""
    from rdkit.Chem import Descriptors

    available = {name for name, _ in Descriptors.descList}
    missing = [name for name in DESCRIPTOR_NAMES if name not in available]
    if missing:
        raise ValueError(
            "pinned descriptor-batch descriptors are missing from RDKit: "
            + ", ".join(missing)
        )


@dataclass(frozen=True)
class DescriptorRow:
    """One canonical descriptor row for a valid input molecule."""

    molecule_id: str
    canonical_smiles: str
    values: tuple[float, ...]


class DescriptorStats:
    """Row counters collected while computing a descriptor batch."""

    def __init__(self) -> None:
        self.scanned = 0
        self.invalid = 0
        self.emitted = 0

    def as_metrics(self) -> dict[str, int]:
        return {
            "rows_scanned": self.scanned,
            "invalid_rows": self.invalid,
            "rows_emitted": self.emitted,
        }


def iter_descriptor_rows(
    input_path: Path,
    *,
    skip_invalid: bool = True,
) -> tuple[Iterator[DescriptorRow], DescriptorStats]:
    """Yield canonical descriptor rows in input order with streaming stats."""
    calculator = descriptor_calculator()
    stats = DescriptorStats()

    def generate() -> Iterator[DescriptorRow]:
        for row in iter_rows(input_path):
            stats.scanned += 1
            smiles = row.get("canonical_smiles", "")
            molecule = Chem.MolFromSmiles(smiles)
            if molecule is None:
                stats.invalid += 1
                if not skip_invalid:
                    raise ValueError(
                        f"row {stats.scanned} has an invalid canonical_smiles"
                    )
                continue
            canonical = Chem.MolToSmiles(molecule, canonical=True)
            values = tuple(
                float(value) for value in calculator.CalcDescriptors(molecule)
            )
            stats.emitted += 1
            yield DescriptorRow(row.get("chembl_id", ""), canonical, values)

    return generate(), stats


def write_descriptor_rows(
    output_path: Path,
    rows: Sequence[DescriptorRow],
) -> None:
    """Write a canonical one-row-per-input descriptor CSV with one header."""
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(
            destination, fieldnames=list(DESCRIPTOR_COLUMNS), lineterminator="\n"
        )
        writer.writeheader()
        for row in rows:
            writer.writerow(
                {
                    "chembl_id": row.molecule_id,
                    "canonical_smiles": row.canonical_smiles,
                    **{
                        name: f"{value:.6f}"
                        for name, value in zip(DESCRIPTOR_NAMES, row.values)
                    },
                }
            )


def compute_descriptor_batch(
    input_path: Path,
    output_path: Path,
    *,
    skip_invalid: bool = True,
) -> dict[str, int]:
    """Single-process reference: read the whole input and write the CSV."""
    rows, stats = iter_descriptor_rows(input_path, skip_invalid=skip_invalid)
    materialized = list(rows)
    write_descriptor_rows(output_path, materialized)
    return stats.as_metrics()
