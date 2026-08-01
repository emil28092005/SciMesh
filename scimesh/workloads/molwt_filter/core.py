"""Scientific core for the ``molwt-filter`` workload.

Keeps one canonical row per input molecule whose exact RDKit molecular weight
falls inside the requested bounds; rows keep input order, invalid SMILES are
skipped or fail the run, and molecular weights are serialized with fixed
``%.6f`` formatting so the output is byte-identical across workers.
"""

from __future__ import annotations

import csv
from pathlib import Path
from typing import Mapping

from rdkit import Chem
from rdkit.Chem import Descriptors

from scimesh.chemistry.dataset import iter_rows

MOLWT_COLUMNS = ("chembl_id", "canonical_smiles", "molwt")


def _bound(value: object, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be a number")
    return float(value)


def filter_molecules_by_molwt(
    input_path: Path,
    output_path: Path,
    *,
    min_molwt: object,
    max_molwt: object,
    skip_invalid: bool,
) -> dict[str, int]:
    """Write one CSV row per molecule whose MolWt is within [min, max]."""
    minimum = _bound(min_molwt, "min_molwt") if min_molwt is not None else None
    maximum = _bound(max_molwt, "max_molwt") if max_molwt is not None else None
    if minimum is None and maximum is None:
        raise ValueError("at least one of min_molwt or max_molwt is required")
    if minimum is not None and minimum < 0:
        raise ValueError("min_molwt must be non-negative")
    if maximum is not None and maximum < 0:
        raise ValueError("max_molwt must be non-negative")
    if minimum is not None and maximum is not None and minimum > maximum:
        raise ValueError("min_molwt must not exceed max_molwt")
    if not isinstance(skip_invalid, bool):
        raise ValueError("skip_invalid must be a boolean")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    scanned = 0
    invalid = 0
    emitted = 0
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        writer = csv.DictWriter(
            destination,
            fieldnames=list(MOLWT_COLUMNS),
            lineterminator="\n",
        )
        writer.writeheader()
        for row in iter_rows(input_path):
            scanned += 1
            smiles = row.get("canonical_smiles", "")
            molecule = Chem.MolFromSmiles(smiles)
            if molecule is None:
                invalid += 1
                if not skip_invalid:
                    raise ValueError(f"row {scanned} has an invalid canonical_smiles")
                continue
            molwt = Descriptors.MolWt(molecule)
            if minimum is not None and molwt < minimum:
                continue
            if maximum is not None and molwt > maximum:
                continue
            canonical = Chem.MolToSmiles(molecule, canonical=True)
            writer.writerow(
                {
                    "chembl_id": row.get("chembl_id", ""),
                    "canonical_smiles": canonical,
                    "molwt": f"{molwt:.6f}",
                }
            )
            emitted += 1
    return {
        "rows_scanned": scanned,
        "invalid_rows": invalid,
        "rows_emitted": emitted,
    }
