# SciMesh

SciMesh is a scientific-workload framework for molecular datasets. Its public CLI
currently runs exact similarity search and sparse similarity-graph construction
locally in one Python process; it creates no dense similarity matrix. A Python
Worker client and the planned Go/PostgreSQL coordinator contract are tracked in
the repository, but distributed execution is not available yet; see
[`STATUS.md`](STATUS.md).

The ChEMBL TSV database is intentionally not included in this repository. Download it separately and pass its path to the commands below. The expected columns are `chembl_id` and `canonical_smiles`.

## Installation

SciMesh requires Python 3.10+ and RDKit.

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e .
```

RDKit can alternatively be installed from conda-forge:

```bash
conda install -c conda-forge rdkit
pip install -e .
```

## Quick start

Run the built-in help command for copy-paste examples of both workloads:

```bash
scimesh help
```

It includes environment setup, output-directory creation, similarity search by
ChEMBL ID or SMILES, and similarity-graph construction. Use the standard help
for the complete option reference:

```bash
scimesh similarity-search --help
scimesh similarity-graph --help
```

## Similarity search

`similarity-search` finds the top-k molecules most similar to a query. The query is supplied either by ChEMBL ID or by SMILES. It uses Morgan fingerprints with `radius=2` and `fpSize=2048`, Tanimoto similarity, streaming TSV reads, and a bounded heap. Invalid SMILES and the query molecule are skipped.

```bash
scimesh similarity-search chembl_37_chemreps.txt \
  --query-id CHEMBL939 \
  --top-k 20 \
  --output results.csv
```

Use a SMILES query when it is not identified by ChEMBL ID:

```bash
scimesh similarity-search chembl_37_chemreps.txt \
  --query-smiles 'COc1cc2ncnc(Nc3ccc(F)c(Cl)c3)c2cc1OCCCN1CCOCC1' \
  --top-k 20 \
  --output results.csv
```

The output CSV contains `rank,chembl_id,canonical_smiles,similarity`. Search progress and valid/invalid-SMILES statistics are written to the terminal. `--max-rows` limits the candidate scan for small tests, while `--progress-every 0` disables progress reports.

To find the least similar molecules, use `--threshold-direction less`. This ranks
results from the lowest similarity upward; `--threshold` optionally limits them
to values less than or equal to a cutoff:

```bash
scimesh similarity-search chembl_37_chemreps.txt \
  --query-id CHEMBL939 \
  --threshold-direction less \
  --threshold 0.1 \
  --top-k 20 \
  --output least_similar.csv
```

To render the query and retained candidates:

```bash
scimesh similarity-search chembl_37_chemreps.txt \
  --query-id CHEMBL939 \
  --images-dir structures
```

This writes `query.png` and `top_candidates.png` into `structures`.

## Similarity graph

`similarity-graph` constructs an exact sparse undirected graph. Every valid molecule is a vertex; an edge is emitted when Tanimoto similarity satisfies the selected threshold direction (`>=` by default, or `<=` with `--threshold-direction less`). Each fingerprint is calculated once. Comparisons are processed block by block, each pair is tested once (`i < j`), and no dense N×N matrix is created or stored.

```bash
scimesh similarity-graph chembl_37_chemreps.txt \
  --max-rows 10000 \
  --threshold 0.7 \
  --block-size 1000 \
  --output similarity_graph.csv
```

The deterministic edge-list CSV has `source_id,target_id,similarity` columns. The command reports valid molecules, checked pairs, emitted edges, rate, and elapsed time. `--block-size` changes only how comparisons are grouped, not the result.

## Development

```bash
pip install -e '.[dev]'
pytest
```

The package separates common dataset parsing and fingerprints from independent workloads. Add future workloads through the workload registry without changing the main CLI.
