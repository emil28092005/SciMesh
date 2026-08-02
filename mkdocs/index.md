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

The fastest path for a scientist: install the platform with one command and
start it. Everything — the coordinator, its databases, the userservice, and
local workers — is embedded in a single binary; no PostgreSQL, no Docker, no
Python setup.

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash
coordinator serve --open

# Windows (PowerShell)
powershell -ExecutionPolicy Bypass -c "irm https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.ps1 | iex"
coordinator serve --open
```

The first start prints the admin login (also stored under `~/.scimesh`), and
`--open` opens the UI in the browser. `coordinator serve --workers 2`
spawns two local workers; `SCIMESH_PIP_PACKAGE` points the managed venv at
your scimesh wheel so scientific workloads can run.

For development, install the Python SDK and run workloads locally:

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e '.[dev]'
scimesh workload list
scimesh workload run molwt-filter \
  --input molecules.tsv \
  --params '{"min_molwt": 40.0}' \
  -o filtered.csv
```

Start the full demo (PostgreSQL, coordinator, UI, two workers):

```bash
make demo-ui
# open http://localhost:18080/ui  (root@scimesh.local / rootpassword)
```

## Prebuilt binaries

Every `v*` tag pushes a GitHub Release with static binaries for
`coordinator` and `worker-agent` on linux/darwin/windows × amd64/arm64
(plus SHA-256 checksums), the installer scripts above, and the `coordinator`
image on GHCR. Download and run:

```bash
curl -L -o coordinator https://github.com/emil28092005/SciMesh/releases/latest/download/coordinator-linux-amd64
chmod +x coordinator
```

- **worker-agent** is installed separately and joins an existing coordinator:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash -s worker
  export COORDINATOR_URL=http://COORDINATOR_HOST:8080
  export WORKER_AUTH_TOKEN=<worker token from the coordinator>
  export WORK_DIR=~/scimesh-worker
  worker-agent
  ```

  It spawns `python -m scimesh.worker.task`, so the machine needs Python 3
  with the `scimesh` package (`pip install scimesh`, or let the managed venv
  do it via `SCIMESH_PIP_PACKAGE`). For a `coordinator serve` instance, the
  worker token is in `~/.scimesh/worker.token`. On Windows set
  `SCIMESH_COMPONENT=worker` for `install.ps1`.
- **coordinator** needs no external services at all in its default mode:
  `coordinator serve` embeds SQLite (both databases), the userservice, and
  local workers. The `SCIMESH_DB=postgres` engine remains for cluster
  deployments (`DATABASE_URL`, `COORDINATOR_STORAGE_DIR`, `JWT_SECRET`); the
  binary applies its embedded schema migrations itself on startup
  (`AUTO_MIGRATE=false` opts out), and `coordinator setup` provisions a
  PostgreSQL deployment interactively. The UI login uses the userservice
  (`USERSERVICE_URL`) — embedded by `serve`, or the standalone `users/`
  service otherwise.
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
