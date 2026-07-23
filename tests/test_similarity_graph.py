from __future__ import annotations

from pathlib import Path

import pytest
from rdkit import DataStructs

from scimesh.chemistry.dataset import DatasetStats, iter_valid_molecules
from scimesh.chemistry.fingerprints import fingerprint
from scimesh.workloads.similarity_graph import (
    SimilarityEdge,
    build_similarity_graph,
    write_graph_edges,
)


def _brute_force_edges(dataset: Path, threshold: float) -> list[SimilarityEdge]:
    records = list(iter_valid_molecules(dataset, DatasetStats()))
    edges = []
    for left_index, left in enumerate(records):
        for right in records[left_index + 1 :]:
            similarity = DataStructs.TanimotoSimilarity(
                fingerprint(left.molecule), fingerprint(right.molecule)
            )
            if similarity >= threshold:
                edges.append(SimilarityEdge(left.molecule_id, right.molecule_id, similarity))
    return sorted(edges, key=lambda edge: (edge.source_id, edge.target_id, -edge.similarity))


def test_graph_matches_brute_force_and_has_unique_non_self_edges(
    small_dataset: Path,
) -> None:
    threshold = 0.15
    result = build_similarity_graph(small_dataset, threshold, block_size=2)

    assert result.edges == _brute_force_edges(small_dataset, threshold)
    assert result.checked_pairs == 10
    edge_pairs = [(edge.source_id, edge.target_id) for edge in result.edges]
    assert all(source != target for source, target in edge_pairs)
    assert len(edge_pairs) == len(set(edge_pairs))
    assert result.stats.invalid == 1


def test_graph_is_block_size_independent_and_deterministic(
    small_dataset: Path, tmp_path: Path
) -> None:
    first = build_similarity_graph(small_dataset, threshold=0.15, block_size=1)
    second = build_similarity_graph(small_dataset, threshold=0.15, block_size=3)
    repeated = build_similarity_graph(small_dataset, threshold=0.15, block_size=3)

    assert first.edges == second.edges == repeated.edges
    first_path = tmp_path / "first.csv"
    second_path = tmp_path / "second.csv"
    write_graph_edges(first_path, first.edges)
    write_graph_edges(second_path, repeated.edges)
    assert first_path.read_bytes() == second_path.read_bytes()


def test_graph_supports_less_than_threshold_direction(small_dataset: Path) -> None:
    result = build_similarity_graph(
        small_dataset, threshold=0.15, block_size=2, threshold_direction="less"
    )

    assert all(edge.similarity <= 0.15 for edge in result.edges)


def test_graph_rejects_duplicate_identifiers(tmp_path: Path) -> None:
    dataset = tmp_path / "duplicate_ids.tsv"
    dataset.write_text(
        "chembl_id\tcanonical_smiles\nDUP\tCCO\nDUP\tCCC\n", encoding="utf-8"
    )

    with pytest.raises(ValueError, match="duplicate chembl_id"):
        build_similarity_graph(dataset, threshold=0.1, block_size=1)


def test_graph_writer_creates_missing_output_directory(tmp_path: Path) -> None:
    output = tmp_path / "nested" / "edges.csv"
    write_graph_edges(output, [])
    assert output.read_text(encoding="utf-8").startswith("source_id,target_id")
