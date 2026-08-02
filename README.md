# SciMesh

SciMesh is a scientific-workload framework for molecular datasets. Its public CLI
runs exact similarity search and sparse similarity-graph construction locally in
one Python process; it creates no dense similarity matrix. The Go/PostgreSQL
coordinator and Python worker can run a shard-based `similarity-search`
pipeline locally. After every shard succeeds, the coordinator deterministically
merges its candidates into one final global top-k CSV. See
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

## Manual pipeline demo

To inspect the coordinator, Web UI, and distributed `similarity-search`
pipeline by hand, install development dependencies once and start the isolated
demo from the repository root:

```bash
python3 -m venv .venv
.venv/bin/pip install -e '.[dev]'
make demo-ui
```

The MkDocs documentation site is served inside the UI at `/ui/docs/`
(`make docs` builds it from `mkdocs/`; the demo mounts `site/`
automatically, or set `SCIMESH_DOCS_DIR` for a manual coordinator). The site
covers the complete Workload SDK: guides (`mkdocs/sdk/`), the full
auto-generated API reference for `scimesh.sdk` (`mkdocs/api/`), and the
documentation rules the site is written by (`mkdocs/approach.md`).

Open `http://localhost:18080/ui` and sign in with username `operator` and
password `demo-ui-secret`. The command starts PostgreSQL, the coordinator, and
two local reference workers. Upload a small ChEMBL TSV, then use the job page
to follow shard progress, inspect bounded **Preview CSV** results, and see a
live processing-speed chart in shards per minute. The **Workloads** page shows
the installed SDK workload library (descriptions, parameters, and artifact
schemas) from the embedded catalog; regenerate it with
`make workloads-export` (or `scimesh workload export`) whenever workloads
change. To change the worker count, run `make demo-ui WORKERS=3`; stop
everything with `make demo-down`.

Run `make help` to display these commands in the terminal.

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

## Workload SDK

`scimesh.sdk` is the framework only: strict and immutable workload manifests,
typed artifact ports, static map/reduce plans, resource eligibility and local
reservations, exact/canonical/numeric verifier primitives, installed-package
allowlisting, and a local conformance executor. It contains no scientific
workload code. Workloads are user scripts built on the SDK: the built-in
`similarity-search`, `similarity-graph`, and `descriptor-batch` live in
`scimesh/workloads/` (each a small package with `core.py` + `definition.py`),
composed by `scimesh/workloads/library.py` and registered through
`scimesh.workloads` entry points. The Worker Agent executes those SDK-built
workloads directly (see `scimesh/worker/runners.py`), so the same scientific
handlers run locally, in conformance, and on claimed coordinator tasks.
`scimesh workload list` and `scimesh workload run` run any SDK workload from
the command line. See the
[SDK author guide](docs/workload-sdk.md), [contract](docs/scimesh-sdk-contract.md),
and [delivery roadmap](docs/scimesh-sdk-roadmap.md).

Dynamic workflows, real Worker concurrency, coordinator-backed GPU allocation,
streaming, and gang execution remain fail-closed until their versioned runtime
features are implemented; declaring those profiles does not silently enable
them.

The included `LocalCoreBatchExecutor` is a trusted, single-threaded in-process
conformance harness. It validates scientific parity, sealed outputs, provenance,
and limits, but intentionally refuses profiles that claim network/process
isolation, secrets, accelerators, gangs, checkpoints, or retries; those require
the future enforcing Agent runtime.

## Team

- [Emil](https://github.com/emil28092005) — Project Lead
- [Kristina](https://github.com/kristtma) — Tech Lead
- [Veniamin](https://t.me/Veniamin_Kt) — Scientific Lead
- [Arkhip](https://github.com/hIpa-ussr) — Programmer
- [Reranchik](https://github.com/RERAN4K) — Programmer
