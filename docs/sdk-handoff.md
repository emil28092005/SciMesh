# Workload SDK handoff

**Audience:** the engineer/AI continuing SciMesh Workload SDK implementation.
**Date:** 2026-08-01. **Baseline:** uncommitted working tree on `main` (`11e9333`
plus the CTX-16 SDK changes); `python -m pytest -q` reports **225 passed**.

Read first, in this order: `AGENTS.md` (binding repo rules),
`docs/scimesh-sdk-roadmap.md` (delivery order — it governs, this file does not),
`docs/scimesh-sdk-contract.md` (normative target semantics),
`docs/workload-sdk.md` (author guide for what exists), and the CTX-16 entry in
`PLAN.md`.

## What is already done (do not redo)

**Legacy removal (2026-08-01):** the CTX-07 `DistributedWorkload` protocol
package (`scimesh/distributed/`), the SDK compatibility adapter
(`scimesh/sdk/compat/`), and `library.similarity_search_sdk_adapter` were
removed. The worker now executes the SDK-built workloads directly:
`scimesh/worker/runners.py` builds a `TaskSpec` with the workload's pins,
negotiates, reserves resources, runs the workload's own Runner through
`LocalTaskContext` (store-backed catalog/sink), and uploads the sealed
partial over the unchanged v1 wire. `run_search_shard` + the full-precision
partial writer moved to `scimesh/workloads/search/core.py`; the partial
format is unchanged, so the Go reducer and UI keep working. The runner
resolves `query_id` per task and rejects plan-time `max_rows`.

**Default hooks + molwt-filter (2026-08-01):** `MapReduceWorkload` now provides default `partition_input` (row-bounded, header-preserving sharding for delimited inputs, `shard_rows` class attr) and default `reduce_partials` (`concatenate_partial_tables`, one header, byte-identical). A new built-in `molwt-filter@1.0.0` (`scimesh/workloads/molwt_filter/`) demonstrates the minimal authoring surface: only `compute_shard` is workload code. descriptor-batch dropped its now-redundant partition/reduce overrides.

**MkDocs site (2026-08-02):** the standalone documentation site lives in `mkdocs/` (`docs_dir: mkdocs`) and does not use the project's `docs/` directory. It contains guides (`mkdocs/sdk/`: overview, authoring-workloads, cli, worker-integration), the full auto-generated API reference for all `scimesh.sdk` modules (`mkdocs/api/`, mkdocstrings `::: scimesh.sdk.<module>` — set `show_if_no_docstring: true`), and the writing rules (`mkdocs/approach.md`). `make docs` builds it; the UI serves it at `/ui/docs/`. All public SDK members now carry Google-style docstrings.

**Go worker agent prototype (2026-08-02):** `coordinator/internal/agent/` (config, models, client, sanitize, taskrunner, daemon) + `coordinator/cmd/worker-agent`, built with `make agent`. It mirrors the Python worker's v1 lifecycle; per-task SDK execution happens in a Python subprocess (`scimesh/worker/task.py`: exits 0 on success, 3 permanent, 1 retryable). Default `TASK_RUNNER` is `python -m scimesh.worker.task` — set it to the venv python in source checkouts. Verified E2E against the demo coordinator. Open items: JWT refresh, resource slots/limits, attempt-dir cleanup, protocol-v2 features.

**Authoring scaffold (2026-08-01):** `scimesh/sdk/batch.py` adds
`MapReduceWorkload` — the primary authoring surface for `core-batch-v1`. A
subclass declares identity/parameters/ports and three scientific hooks
(`partition_input`, `compute_shard`, `reduce_partials`); the SDK assembles the
manifest, map/reduce stages, workflow, digest-pinned handlers, and the
exact-artifact verifier. Overridable hooks: `domain_validate`,
`resolved_parameters`, `resolved_parameters_for_plan`, `plan_tasks`,
`parse_partial_key`/`validate_partial_keys`, `map_stage_inputs` (multi-input
map stages share the external input schema). All three built-in workloads are
refactored onto it. Generic `scimesh workload list|run` CLI added (no
workload-specific logic). The worker loads workloads generically:
`SCIMESH_WORKLOAD_ALLOWLIST` (JSON `{distribution, name, version, digest}`,
discovery via entry points) or built-in fallback; `SCIMESH_CAPABILITIES`
overrides advertised capabilities; workloads with multi-input map stages are
rejected by the v1 bridge. `query_id` resolution moved into the search
workload's `run_search_shard`; the worker passes task parameters through and
the workload validates them.

CTX-16 "Workload SDK foundation" is complete and tested. `scimesh/sdk/`
implements the `core-batch-v1` profile:

- Immutable, JSON-strict value objects: `identity.py`, `artifacts.py`,
  `workflow.py`, `manifest.py`, `plans.py`, `execution.py`, `resources.py`.
- Fail-closed compatibility negotiation: `runtime.py` (`negotiate_manifest`)
  plus request-level checks in `registry.py`.
- Installed-package registry with administrator allowlist, exact version +
  `sha256:` digest pinning, entry-point discovery with digest measured before
  and after import: `registry.py`, `integrity.py`.
- Verifier primitives `ExactArtifactVerifier`, `CanonicalRecordVerifier`,
  `NumericToleranceVerifier` with bounded sanitized evidence: `verification.py`.
- Local conformance harness: `LocalArtifactStore`, `LocalCoreBatchExecutor`,
  `ResourcePool` (atomic all-or-nothing reservation): `conformance.py`.
- SDK-built workloads living outside the SDK: `scimesh/workloads/search/`,
  `scimesh/workloads/graph/`, `scimesh/workloads/descriptors/` (each `core.py`
  + `definition.py`), composed by `scimesh/workloads/library.py`
  (`default_sdk_registry`, `default_sdk_runtime`); entry points for all three
  declared in `pyproject.toml`.
- Tests: `tests/test_sdk_{models,resources,verification,compatibility,registry}.py`
  including fail-closed rejection coverage for every advanced profile
  declaration (gang, GPU modes, pools, checkpoints, retries, secrets, streams,
  loops, side effects), plus `tests/test_sdk_{search,graph,descriptors}.py`
  and the worker bridge tests in `tests/test_worker_daemon.py`.
  `tests/test_distributed*.py` were removed with the protocol.

## What remains, in delivery order

1. ~~**`descriptor-batch` reference workload**~~ — **done** (2026-08-01):
   `scimesh/workloads/descriptors/` (`core.py` + `definition.py`) is the first
   SDK-built workload. Pinned 81-name RDKit 2D descriptor set (validated at
   definition build time), canonical one-row-per-input CSV with `%.6f` floats,
   deterministic row-bounded shards, shard-index concatenation with one header,
   byte-identical local/distributed output, `skip_invalid` explicit policy, and
   `untrusted_quorum` + exact-artifact@1 declared in the manifest. Tests:
   `tests/test_sdk_descriptors.py`.
2. ~~**SDK-built `similarity-search` and `similarity-graph`**~~ — **done**
   (2026-08-01). Both local workloads are SDK-built packages outside the SDK:
   `scimesh/workloads/search/` and `scimesh/workloads/graph/` (each `core.py` +
   `definition.py`, manifest + planner/runner/reducer, byte_exact +
   exact-artifact@1, trusted + untrusted_quorum). Search resolves the query at
   plan time and merges partials with the reference heap (byte-identical to the
   CLI). Graph plans one task per block pair `(i,j)` with `i <= j`, reducer
   enforces pair-coverage and duplicate-pair rejection, output byte-identical
   to the local brute-force reference for both directions and any block size.
   Tests: `tests/test_sdk_search.py`, `tests/test_sdk_graph.py`.
   **Architecture note:** `scimesh.sdk/` is the framework ONLY (no workload
   code); workloads are user scripts/packages under `scimesh/workloads/` that
   import the SDK. Keep new workloads out of the SDK package.
2. **Distributed `similarity-graph`** (CTX-10, roadmap step 1). The coordinator
   currently rejects `similarity-graph` uploads; it needs cross-shard block-pair
   planning and duplicate-safe reduction. STATUS.md names this the next
   recommended assignment overall.
3. **Coordinator/Worker protocol v2** (needs CTX-10, then CTX-13 in-worker CPU
   parallelism and CTX-14 GPU execution; Go + Python). The protocol-v1
   coordinator persists only flat one-input/one-result tasks: no resource
   requirements, stage edges, package versions, device allocations, or gang
   leases. Until a versioned rollout lands, SDK declarations for those features
   must stay fail-closed — do not silently "enable" them.
4. **More chemistry workloads** (roadmap step 4): standardization, SMARTS
   screening, fingerprint export, fixed-template reaction enumeration, then
   reaction validation/descriptors.
5. **Composite artifacts and richer verifier policies** (roadmap step 5):
   first-class ordered/keyed `ArtifactCollection` edges instead of composite
   manifest artifacts; decide where verifiers execute (open decision in the
   roadmap).
6. **Authoring CLI** (future tooling, does not exist today): `scimesh workload
   init`, `validate`, `test-local`, `test-distributed`, `golden`, `package`.
   Per AGENTS.md, keep CLI parsing in workload modules and register through
   `scimesh/core/registry.py`; no workload-specific logic in the main CLI.
7. **Open decisions** (listed at the end of the roadmap): SDK distribution
   split, Go↔Python planner bridge, verifier execution/attestation, trust-mode
   governance, multi-user enablement. Do not pick one unilaterally — surface it.

## Known traps (cost the previous session real time)

- **Architecture boundary:** `scimesh.sdk/` is the framework only and must
  never import `scimesh.workloads` (SDK depends on nothing workload-specific).
  Workload packages live under `scimesh/workloads/` (each `core.py` +
  `definition.py`), and built-in wiring lives in `scimesh/workloads/library.py`.
  The digest helpers are in `scimesh/workloads/environment.py`; the SDK keeps
  only the generic `installed_distribution_digest` in `scimesh/sdk/integrity.py`.
- The legacy adapter pins its own manifest (`adapter.manifest`). If a test
  changes limits/workflow on the manifest, the adapter's copy must be replaced
  too, or `registry.plan` fails with "planner plan does not carry the selected
  immutable workload pin".
- `WorkloadDefinition` validation: a PLAN stage's `entry_point` must equal
  `planner.entry_point`; every non-REDUCE stage's `entry_point` must be a key
  in `runners` (REDUCE → `reducers`); verifier handlers are keyed by
  `ComponentRef.canonical` and must expose a matching `.identity`.
- Negotiation requires each triggering property's feature to be declared
  separately: e.g. `PROCESS_POOL` needs `process-pools` **and** `multi-process`
  for `max_processes > 1`. Runtime must also advertise every declared required
  feature, or negotiation fails with `feature-unavailable`.
- `feature-fallback-disallowed` in `scimesh/sdk/registry.py` is currently
  unreachable via `registry.plan` (the `feature-unavailable` check fires first
  for any runtime that produced a fallback). Behavior is still fail-closed;
  decide whether to reorder or delete the branch.
- The local executor is deliberately trusted/in-process: it rejects anything
  but `TrustMode.TRUSTED`, `NetworkPolicy.TRUSTED`, single-threaded CPU
  map/reduce without retries/gangs/accelerators/secrets/checkpoints. That is a
  contract, not a bug — test rejections, don't "fix" them.
- `JobRequest` parameters and failure/evidence payloads reject local paths and
  URIs by design; keep new payloads location-free.
- There is a stray nested clone `SciMesh/` in the repo root (same repo at an
  older commit). Ignore it and never `git add` it; consider deleting it.
- The full ChEMBL extract `chembl_37_chemreps.txt` (~2.9M rows) makes the
  single-threaded local executor run for many minutes; tests must use small
  TSV fixtures (see `_write_tiny_dataset`).

## Working agreement

- Verify with `source .venv/bin/activate && python -m pytest -q`; the baseline
  is 225 passing tests and it must stay green. Add a regression test for every
  behavioral change; similarity code needs a brute-force/sorted reference and
  determinism across block sizes.
- Legacy `similarity-search` wire schema, worker alias boundary, and scientific
  output must not change. Worker code never talks to PostgreSQL directly;
  results go through the coordinator; failures go to `/failure`, never as
  `file://`/`worker://` result URIs.
- Do not commit datasets, generated CSV/PNG, tokens, or local worker artifacts.
- One CTX task per pull request; link the CTX item from `PLAN.md`.
