# SciMesh

SciMesh is a local-first platform for scientific computation on molecular
datasets. It turns a scientific run into independent tasks, dispatches them
to worker agents, and deterministically combines the partial results into a
checksum-protected final artifact.

- **The Workload SDK (`scimesh.sdk`)** — a strict Python framework for
  authoring scientific workloads: `similarity-search` (exact top-k Tanimoto),
  `similarity-graph` (exact sparse graph), `descriptor-batch`, and
  `molwt-filter`. Workloads are ordinary user scripts built on the SDK; they
  run locally, in the conformance harness, and on claimed coordinator tasks
  without touching any other part of the program.
- **The coordinator and worker agents** — a Go/PostgreSQL coordinator with an
  operator UI and Go worker agents that execute SDK workloads in a Python
  subprocess. The UI is workload-agnostic: the "New computation" form offers
  every workload from the embedded SDK library, and each workload declares its
  own form controls (`UIElement`) through the SDK.

The ChEMBL TSV database is intentionally not included in this repository.
Download it separately and pass its path to the commands below. The expected
columns are `chembl_id` and `canonical_smiles`. See
[`STATUS.md`](STATUS.md) and [`PLAN.md`](PLAN.md).

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

## Releases

Every `v*` tag pushes a GitHub Release with static binaries for `coordinator`
and `worker-agent` on linux/darwin/windows × amd64/arm64 (plus SHA-256
checksums), the installer scripts, and the `coordinator` image on GHCR:

```bash
docker pull ghcr.io/emil28092005/SciMesh/coordinator:latest
```

For scientists: one command downloads the right binary and prints the start
instructions:

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash
coordinator serve --open

# Windows (PowerShell)
powershell -ExecutionPolicy Bypass -c "irm https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.ps1 | iex"
coordinator serve --open
```

`coordinator serve` is the single-binary mode: it embeds SQLite (coordinator +
userservice databases), the userservice itself, and local worker agents
(`--workers N`, default 1). On first start it generates secrets and the admin
password under `~/.scimesh`, prints the login, and opens the UI. No
PostgreSQL, no Docker, no environment variables. The scientific runtime is a
managed venv (`~/.scimesh/venv`); point `SCIMESH_PIP_PACKAGE` at your scimesh
wheel to install it automatically.

Manual download and run of a release binary:

```bash
curl -L -o coordinator https://github.com/emil28092005/SciMesh/releases/latest/download/coordinator-linux-amd64
chmod +x coordinator
./coordinator --version
```

Cluster deployments keep the PostgreSQL engine (`SCIMESH_DB=postgres` with
`DATABASE_URL`, or `coordinator setup` to provision it) and the standalone
userservice (`users/`). `coordinator agent` runs a worker agent from the same
binary.

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

To inspect the coordinator, Web UI, and distributed pipeline by hand, install
development dependencies once and start the isolated demo from the repository
root:

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

Open `http://localhost:18080/ui` and sign in with username
`root@scimesh.local` and password `rootpassword`. The command starts
PostgreSQL, the coordinator, and two Go worker agents (built by `make agent`;
each executes the SDK workload in a Python subprocess). The **New computation**
form offers every upload-ready workload from the installed library — the
controls come from each workload's own SDK declarations. Upload a small ChEMBL
TSV, then use the job page to follow shard progress, inspect bounded
**Preview CSV** results, and see a live processing-speed chart in shards per
minute. The **Workloads** page shows the installed SDK workload library
(descriptions, parameters, and artifact schemas) from the embedded catalog;
regenerate it with `make workloads-export` (or `scimesh workload export`)
whenever workloads change. To change the worker count, run
`make demo-ui WORKERS=3`; stop everything with `make demo-down`.

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

The coordinator and worker agent are Go modules under `coordinator/` and `users/`:

```bash
cd coordinator && make coordinator agent && go test ./...
```

`make check` runs the full gate: vet, lint, race tests, the PostgreSQL
integration suite, and the two-worker end-to-end smoke script.

## Workload SDK

`scimesh.sdk` is the framework only: strict and immutable workload manifests,
typed artifact ports, static map/reduce plans, resource eligibility and local
reservations, exact/canonical/numeric verifier primitives, installed-package
allowlisting, and a local conformance executor. It contains no scientific
workload code. Workloads are user scripts built on the SDK: the built-in
`similarity-search`, `similarity-graph`, `descriptor-batch`, and
`molwt-filter` live in `scimesh/workloads/` (each a small package with
`core.py` + `definition.py`), composed by `scimesh/workloads/library.py` and
registered through `scimesh.workloads` entry points. The Worker Agent executes
those SDK-built workloads directly (see `scimesh/worker/runners.py`), so the
same scientific handlers run locally, in conformance, and on claimed
coordinator tasks. `scimesh workload list` and `scimesh workload run` run any
SDK workload from the command line; `scimesh workload export` writes the
coordinator's embedded workload catalog, and `scimesh workload allowlist`
prints the JSON for `SCIMESH_WORKLOAD_ALLOWLIST`.

Workloads can also declare how they should appear in the coordinator UI:
a tuple of `UIElement`s (`scimesh.sdk.UIElement`) shapes the "New computation"
form — widget, label, help, defaults, and ordering — plus the coordinator-side
reduction mode (`reduction`: `top-k` or `ordered-concat`) and whether a single
uploaded dataset can drive the workload (`upload_ready`). The strict parameter
schema stays the authoritative validation contract.

See the
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
