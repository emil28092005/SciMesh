# SciMesh

Minimal local search for ChEMBL molecules similar to gefitinib (`CHEMBL939`).

The script finds `CHEMBL939` in the TSV file and uses its `canonical_smiles` as the reference. It then makes a second streaming pass through the file, generates Morgan fingerprints (`radius=2`, `fpSize=2048`), and ranks the remaining valid SMILES by Tanimoto similarity. Invalid SMILES and `CHEMBL939` itself are skipped. Only the best 20 results (or the value passed to `--top`) are kept in memory.

## Installation

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

RDKit can also be installed through conda-forge:

```bash
conda install -c conda-forge rdkit
```

## Usage

```bash
python scimesh.py chembl_37_chemreps.txt -o gefitinib_similarities.csv
```

By default, the script writes a CSV with `rank,chembl_id,canonical_smiles,similarity` columns and prints the same top 20 results to the terminal. To choose a different number of results:

```bash
python scimesh.py chembl_37_chemreps.txt --top 50 -o top_50.csv
```

During the search, status is written to `stderr` every 100,000 rows: number of processed rows, current and average rates, elapsed time, and the number of invalid SMILES skipped. The interval can be changed or disabled:

```bash
python scimesh.py chembl_37_chemreps.txt --progress-every 500000
python scimesh.py chembl_37_chemreps.txt --progress-every 0
```

## Quick test on part of the database

The `--max-rows` option limits the second pass to the first `N` TSV rows. `CHEMBL939` is still found in its own streaming pass first, so the reference stays the same. The resulting CSV is the top 20 only within the processed subset, not the full database.

```bash
python scimesh.py chembl_37_chemreps.txt --max-rows 10000 -o test_results.csv
```

## Structure images

Pass a directory to `--images-dir` to create `CHEMBL939_gefitinib.png` for gefitinib and `top_candidates.png` with a grid of top candidates. Candidate images show rank, ChEMBL ID, and Tanimoto similarity.

```bash
python scimesh.py chembl_37_chemreps.txt --images-dir structures
```

The `--image-columns` option controls the number of structures per grid row (default: `4`).
