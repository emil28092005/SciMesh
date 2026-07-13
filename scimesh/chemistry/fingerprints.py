"""Morgan fingerprint utilities shared by molecular workloads."""

from __future__ import annotations

from functools import lru_cache
from typing import Any

from rdkit import Chem
from rdkit.Chem import rdFingerprintGenerator


FP_RADIUS = 2
FP_SIZE = 2048


@lru_cache(maxsize=1)
def morgan_generator() -> Any:
    """Return the standard SciMesh Morgan fingerprint generator."""
    return rdFingerprintGenerator.GetMorganGenerator(radius=FP_RADIUS, fpSize=FP_SIZE)


def fingerprint(molecule: Chem.Mol) -> Any:
    """Build a Morgan radius-2, 2048-bit fingerprint."""
    return morgan_generator().GetFingerprint(molecule)
