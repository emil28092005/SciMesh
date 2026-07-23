from __future__ import annotations

from pathlib import Path

from rdkit import Chem, DataStructs

from scimesh.chemistry.dataset import DatasetStats, find_molecule_by_id, iter_valid_molecules
from scimesh.chemistry.fingerprints import fingerprint
from scimesh.workloads.similarity_search import (
    SimilarityMatch,
    search_similar,
    write_search_results,
)


def test_search_matches_full_sorting_and_skips_query_and_invalid(
    small_dataset: Path,
) -> None:
    query = find_molecule_by_id(small_dataset, "QUERY")
    result = search_similar(small_dataset, query, top_k=2)

    query_smiles = Chem.MolToSmiles(query.molecule, canonical=True)
    expected = []
    for record in iter_valid_molecules(small_dataset, DatasetStats()):
        if record.molecule_id == query.molecule_id:
            continue
        if Chem.MolToSmiles(record.molecule, canonical=True) == query_smiles:
            continue
        expected.append(
            SimilarityMatch(
                DataStructs.TanimotoSimilarity(
                    fingerprint(query.molecule), fingerprint(record.molecule)
                ),
                record.molecule_id,
                record.smiles,
            )
        )

    assert result.matches == sorted(expected, key=SimilarityMatch.sort_key)[:2]
    assert "QUERY" not in {match.molecule_id for match in result.matches}
    assert "DUPLICATE" not in {match.molecule_id for match in result.matches}
    assert result.stats.invalid == 1
    assert result.stats.valid == 5


def test_search_can_rank_and_filter_least_similar_molecules(
    small_dataset: Path,
) -> None:
    query = find_molecule_by_id(small_dataset, "QUERY")
    result = search_similar(
        small_dataset,
        query,
        top_k=20,
        threshold=0.2,
        threshold_direction="less",
    )

    assert result.matches
    assert all(match.similarity <= 0.2 for match in result.matches)
    assert result.matches == sorted(
        result.matches, key=lambda match: match.sort_key("less")
    )


def test_search_writer_creates_missing_output_directory(tmp_path: Path) -> None:
    output = tmp_path / "nested" / "results.csv"
    write_search_results(output, [])
    assert output.read_text(encoding="utf-8").startswith("rank,chembl_id")
