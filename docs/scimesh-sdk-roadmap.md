# SciMesh Workload SDK roadmap

**Status:** future design and sequencing document. No SDK package, commands, or
general verifier abstraction described here is implemented yet.

## Purpose and boundaries

The SDK should let a scientific developer add an allowlisted workload without
learning coordinator internals or writing SQL, while keeping one scientific
implementation usable locally and in distributed execution:

```text
scientific implementation -> local adapter -> planner/tasks -> reducer -> verifier
```

It must not execute arbitrary code or shell commands supplied by a coordinator.
SDK v1 is not a public marketplace, generic container/job runner, cross-language
SDK, automatic correctness-proof system, or immediate route to stochastic ML or
GPU workloads.

The current foundation is the Python `DistributedWorkload` protocol and registry
under `scimesh/distributed/`, the Worker Agent under `scimesh/worker/`, and the
coordinator API contract. See [CTX-07](ctx-07-distributed-workload-protocol.md),
[worker-building guide](building-workers.md), and [API contract](api-contract.md).

## Proposed public concepts

| Concept | Responsibility |
| --- | --- |
| `WorkloadDefinition` / `WorkloadManifest` | Name, versions, schemas, execution and verification metadata. |
| `ParameterSchema`, `InputSpec`, `OutputSpec`, `ArtifactRef` | Typed public inputs and durable artifact shapes. |
| `TaskPlan`, `Planner`, `Runner`, `Reducer` | Validate, split, execute, and deterministically combine work. |
| `Verifier` | Accept or reject result evidence; never silently downgrade checks. |
| `ResourceRequirements`, `ExecutionProfile`, `ReproducibilityProfile` | Bounded resource needs and pinned execution assumptions. |

A manifest should include workload/version and SDK compatibility versions,
description, parameter/input/output schemas, planner/runner/reducer/verifier
types, determinism and trust profiles, resource/output limits, worker
capabilities, and environment or image digest. Compatibility must be explicit
among SDK, coordinator protocol, worker runtime, workload, output schema, and
verifier versions.

## Contracts

**Planner:** validates before durable Job/Task creation; produces versioned,
JSON-serializable plans that refer only to durable artifacts; gives stable task
order and expected resources/outputs; fails transactionally without a partial
task graph.

**Runner:** receives typed parameters and owned artifact references; runs only
allowlisted SDK code; writes to its attempt directory; produces output manifest,
metrics, and sanitized failures; respects cancellation/lease loss when platform
support exists; never uses `shell=True` or unnecessarily exposes credentials.

**Reducer:** consumes only accepted partial artifacts in stable order; is
idempotent or coordinator-state protected; creates a versioned final manifest;
defines missing, duplicate, and malformed-shard failures.

**Verifier:** is versioned with the workload and states one of byte-exact,
canonical, numeric, domain-specific, or trust-policy comparison. It processes
structured manifests and bounded streams where practical, records sanitized
evidence, and rejects inconsistent results.

Current untrusted quorum is only `ExactArtifactVerifier`: distinct owners must
produce whole files with identical SHA-256. Future modes are
`CanonicalRecordVerifier`, `NumericToleranceVerifier`,
`DomainSpecificVerifier`, and `TrustedWorkerPolicy`. Canonical mode requires a
specified parser/schema/order/encoding/serialization; numeric mode compares
structured values, not CSV text.

## Resources and reproducibility

The extensible requirement model is `cpu_cores`, `memory_mb`, `scratch_mb`,
`gpu_count`, `gpu_memory_mb`, `accelerator_kind`, `exclusive_device`, and
`estimated_output_bytes`. It must not imply one Task equals one CPU core.

Untrusted byte-exact workloads require a pinned image/environment digest,
runtime and dependency versions, fixed locale/timezone/UTF-8/newlines/dialect,
explicit invalid-row policy and algorithm options, canonical ordering, stable
archive metadata, golden fixtures, two independently provisioned workers, and
local/distributed plus retry/completion-order parity tests.

## Compatibility evolution

The stable release retains the current one-input/one-result task contract.
First, a composite manifest artifact may reference multiple logical inputs;
later, ordered input and output artifact collections can become first-class.
The transition must be versioned and retain old workload compatibility.

Discovery should use an installed Python package, manifest, pinned environment
metadata, explicit entry points, and golden fixtures. It must be allowlisted;
never scan or execute user-provided module paths.

## Delivery sequence

1. Finish distributed `similarity-graph` and reliability/cross-language CI.
2. Stabilize manifest, schema, planner/runner/reducer interfaces, exact verifier,
   compatibility metadata, and an author guide.
3. Deliver `descriptor-batch` as the reference workload: pinned RDKit 2D
   descriptors; canonical one-row-per-input CSV; shard-index concatenation with
   one header; byte-identical local/distributed output and two-worker quorum.
4. Add standardization, SMARTS screening, fingerprint export, fixed-template
   reaction enumeration, then reaction validation/descriptors.
5. Generalize composite artifacts, process slots, resource requests, and richer
   verifier policies.
6. Only then consider pinned, trusted/domain-verified numeric, ML, docking, QM,
   MD, and GPU workloads.

Future developer tooling may include `scimesh workload init`, `validate`,
`test-local`, `test-distributed`, `golden`, and `package`; these commands do not
exist today. A template should generate a manifest, schemas, planner, runner,
reducer, verifier, unit tests, golden fixture, two-worker integration test, and
documentation.

## Open decisions

- Is the SDK part of `scimesh` or a separately versioned Python distribution?
- What stable bridge connects Go orchestration to Python planners/reducers?
- Where do future verifiers execute, and how are environments attested?
- Which trust modes may run each verifier profile?
- Who may install/enable workloads in multi-user deployments?
- How are composite I/O, version negotiation, output-growth limits, and
  numeric-tolerance access governed without breaking the existing API?
