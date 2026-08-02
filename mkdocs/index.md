# SciMesh

SciMesh is a local-first platform for scientific computation on molecular
datasets. It turns a scientific run into independent tasks, dispatches them
to Python workers, and deterministically combines the partial results into a
checksum-protected final artifact.

The two halves of the project:

- **The Workload SDK (`scimesh.sdk`)** — a strict Python framework for
  authoring scientific workloads. Workloads are ordinary user scripts built
  on the SDK; they run locally, in the conformance harness, and on claimed
  coordinator tasks without touching any other part of the program.
- **The coordinator and worker** — a Go/PostgreSQL coordinator with an
  operator UI and Python worker agents that execute SDK-built workloads over
  an HTTP contract.

## What is implemented

- **SDK-built workloads**: `similarity-search` (exact top-k Tanimoto search),
  `similarity-graph` (exact sparse similarity graph with pair-coverage),
  `descriptor-batch` (pinned RDKit 2D descriptors), and `molwt-filter`
  (molecular-weight filter — the minimal authoring example).
- **`MapReduceWorkload`**: the primary authoring scaffold. A subclass
  declares identity, parameters, ports, and scientific hooks; the SDK
  assembles the manifest, map/reduce stages, the digest-pinned
  planner/runner/reducer, and the exact-artifact verifier.
- **A local conformance runtime** (`LocalCoreBatchExecutor`): a trusted,
  in-process harness that validates scientific parity, sealed outputs,
  provenance, and limits.
- **A distributed worker** that executes the same SDK workload handlers on
  tasks claimed from the coordinator, with digest-pinned `TaskSpec`s,
  resource reservation, and allowlist-driven workload discovery.
- **An operator UI** served by the coordinator: the control room, a workload
  library page, a workload-agnostic "new computation" form whose controls come
  from each workload's own `UIElement` declarations, and this documentation
  site at `/ui/docs/`.

## Quick start

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e '.[dev]'
```

List the installed SDK workloads and run one locally:

```bash
scimesh workload list
scimesh workload run molwt-filter \
  --input molecules.tsv \
  --params '{"min_molwt": 40.0}' \
  -o filtered.csv
```

Run the local scientific CLI workloads:

```bash
scimesh help
```

Start the full demo (PostgreSQL, coordinator, UI, two workers):

```bash
make demo-ui
# open http://localhost:18080/ui  (root@scimesh.local / rootpassword)
```

## Prebuilt binaries

Every `v*` tag pushes a GitHub Release with static binaries for
`coordinator` and `worker-agent` on linux/darwin/windows × amd64/arm64
(plus SHA-256 checksums) and the `coordinator` image on GHCR. Download and
run:

```bash
curl -L -o coordinator https://github.com/emil28092005/SciMesh/releases/latest/download/coordinator-linux-amd64
chmod +x coordinator
```

- **worker-agent** runs anywhere with Python: it spawns
  `python -m scimesh.worker.task`, so the machine needs the `scimesh`
  package in a venv (`pip install scimesh`) and the usual environment:
  `COORDINATOR_URL`, `WORKER_AUTH_TOKEN`, `WORK_DIR`.
- **coordinator** needs PostgreSQL running (`DATABASE_URL`,
  `COORDINATOR_STORAGE_DIR`, `JWT_SECRET`); the binary applies its embedded
  schema migrations itself on startup (`AUTO_MIGRATE=false` opts out), so no
  separate migration step is needed. The UI login additionally requires
  `USERSERVICE_URL`.
  `coordinator --version` / `worker-agent --version` print the build tag.

Build and serve this documentation site:

```bash
make docs
make docs-serve      # http://localhost:8000
```

## Where to go next

- [SDK overview](sdk/overview.md) — what the SDK is and is not.
- [Authoring workloads](sdk/authoring-workloads.md) — write your first
  workload with `MapReduceWorkload`.
- [Workload CLI](sdk/cli.md) — list, run, and export workloads from the
  command line.
- [Worker integration](sdk/worker-integration.md) — how the distributed
  worker executes SDK workloads.
- [API reference](api/index.md) — the complete `scimesh.sdk` API, generated
  from docstrings.
- [Documentation approach](approach.md) — the rules this site is written by.
