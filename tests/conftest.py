from __future__ import annotations

from pathlib import Path

import pytest


@pytest.fixture
def small_dataset(tmp_path: Path) -> Path:
    path = tmp_path / "molecules.tsv"
    path.write_text(
        "chembl_id\tcanonical_smiles\n"
        "QUERY\tCCO\n"
        "ALCOHOL\tCCCO\n"
        "AMINE\tCCN\n"
        "BENZENE\tc1ccccc1\n"
        "BROKEN\tnot-a-smiles\n"
        "DUPLICATE\tCCO\n",
        encoding="utf-8",
    )
    return path
