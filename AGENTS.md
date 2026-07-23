# Repository Guidelines

## Project Structure & Module Organization

SciMesh is a Python package for molecular-similarity workloads. Source lives in
`scimesh/`: `chemistry/` reads data and makes fingerprints, `workloads/`
contains commands, and `core/` provides the workload protocol and registry.
The worker daemon in `scimesh/worker/` is a coordinator client, not a database
client. Tests are in `tests/`; specifications in `docs/`; roadmap: `PLAN.md`.

For distributed work, read `.agents/`, `docs/api-contract.md`,
and `STATUS.md`. Use one CTX task per pull request; local workloads are the
scientific reference.

## Build, Test, and Development Commands

Create a virtual environment, install the package with development tools, and
run the suite:

```bash
python -m venv .venv
source .venv/bin/activate
pip install -e '.[dev]'
pytest
```

Use `pytest tests/test_similarity_graph.py` for one module. Exercise the CLI
with `scimesh help` or `scimesh similarity-search --help`. Run `python -m build`
only when packaging is needed; install `build` first if necessary.

## Coding Style & Naming Conventions

Target Python 3.10+; type public APIs and exchanged data. Use four spaces,
`snake_case` for modules,
functions, and variables, `PascalCase` for classes, and descriptive test names
such as `test_graph_is_deterministic_across_block_sizes`. Keep CLI parsing in
workload modules and register new workloads through `scimesh/core/registry.py`;
do not add workload-specific logic to the main CLI.

Prefer few dependencies. RDKit is the chemistry dependency.
For worker/coordinator work, keep network payloads explicit and multi-line;
never make the worker access PostgreSQL directly.

## Testing Guidelines

Use pytest and add a regression test for every defect. Similarity code must be
checked against a small brute-force or fully sorted reference. Graph results
must be deterministic, have no self-loops or duplicate pairs, and remain
stable for different block sizes. Worker changes need success and failure
tests: checksum mismatch, lease failure, upload failure, and safe reporting.
Run the full `pytest` suite before committing.

## Commit & Pull Request Guidelines

Use short imperative commit subjects, for example `Add graph threshold mode` or
`Fix worker result and lease contracts`. Keep one logical change per commit.
In a pull request, state the problem, behaviour changed, tests run, and any API
or documentation changes. Link the relevant `CTX-*` item in `PLAN.md` for
distributed work. Do not commit datasets, generated CSV/PNG files, `.venv/`,
tokens, or local worker artifacts.

## Security & Protocol Rules

Upload worker results through the coordinator before posting completion; never
submit `file://` or `worker://` result URIs. Send failures to `/failure`, not
`/result`. Do not log bearer tokens, raw tracebacks, or private local paths.
