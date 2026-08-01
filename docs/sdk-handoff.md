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
- Legacy adapter exposing distributed `similarity-search` through the SDK
  without changing its wire schema: `compat/distributed_v1.py`, `builtins.py`;
  entry point `similarity-search@1.0.0` is declared in `pyproject.toml`.
- Tests: `tests/test_sdk_{models,resources,verification,compatibility,registry}.py`
  including fail-closed rejection coverage for every advanced profile
  declaration (gang, GPU modes, pools, checkpoints, retries, secrets, streams,
  loops, side effects).

## What remains, in delivery order

1. **`descriptor-batch` reference workload** (roadmap step 3 — the recommended
   next task; it is pure Python and needs no coordinator changes). Pinned RDKit
   2D descriptors, canonical one-row-per-input CSV, shard-index concatenation
   with one header, byte-identical local/distributed output, two-worker quorum.
   Build it as an SDK-native package (manifest + planner/runner/reducer/
   verifier handlers), not through the legacy adapter; reuse the
   `similarity-search` adapter (`scimesh/sdk/compat/distributed_v1.py`) and
   `builtins.py` as the structural template, and the
   `tests/test_sdk_compatibility.py` fixtures as the test template. This is the
   intended first `untrusted_quorum` candidate (byte_exact + exact-artifact@1).
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
