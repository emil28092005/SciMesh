# SciMesh Workload SDK roadmap

**Status:** active delivery roadmap. The Python `scimesh.sdk` package now
implements the `core-batch-v1` foundation, verifier primitives, resource
eligibility/local allocation, installed-package registry, local conformance
runtime, and legacy similarity-search adapter. Coordinator-backed generalized
DAG execution, Worker concurrency, accelerators, streaming, gang execution,
and authoring CLI commands remain future phases.

The normative future API, workflow, execution, resource, security, and failure
semantics are specified in the design-draft
[`scimesh-sdk-contract.md`](scimesh-sdk-contract.md). This roadmap controls
delivery order and does not override that contract.

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

The detailed schemas and invariants are defined in the SDK contract; this table
is the roadmap-level responsibility map.

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

Current untrusted quorum is only `ExactArtifactVerifier`: coordinator-created
candidate envelopes from distinct owners must share the exact workload, task,
package/manifest/environment, parameters, and input binding and produce whole
files with identical SHA-256. Future modes are
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

## General workload model: a versioned artifact workflow

Map/reduce is the first execution shape, not the limit of the SDK. The target
abstraction is an acyclic **workflow graph**: typed artifact ports connect
versioned stages, and a stage may fan out, fan in, or run once per job. This
allows the same SDK to express scientific ETL, simulations, parameter sweeps,
multi-step pipelines, model inference, image/video analysis, and the current
molecular workloads without placing scientific logic in the coordinator.

```text
Job inputs -> validate -> plan -> [map/partition stages] -> [join/reduce stages]
                                      |                         |
                                  accepted artifacts -----------+-> verify -> final manifest
```

The coordinator persists the graph, task attempts, leases, and artifact
ownership. The SDK declares stage behavior; it does not receive database access
or arbitrary commands. The initial `DistributedWorkload` protocol maps to a
single input, many map tasks, one reducer, and one final artifact. It remains a
supported compatibility profile rather than being replaced abruptly.

### Workflow and stage contracts

| Concept | Target responsibility |
| --- | --- |
| `WorkflowSpec` | Versioned DAG, external input ports, terminal outputs, global limits, and failure policy. |
| `StageSpec` | Stable stage ID, kind (`map`, `reduce`, `service`, `verify`), input/output port schemas, retry and resource policy. |
| `TaskSpec` | One concrete deterministic unit: stage ID, ordered artifact bindings, parameters, execution profile, and expected output manifest. |
| `ArtifactSchema` | Logical media type, schema version, cardinality, size bound, canonicalization rules, and privacy/retention class. |
| `ArtifactCollection` | Ordered, named, or keyed artifact set; used for shards, paired inputs, model bundles, and multiple outputs. |
| `OutputManifest` | Every output's artifact reference, schema/version/digest, metrics, provenance, and verifier evidence. |
| `FailurePolicy` | Retryable versus terminal errors, timeout, cancellation, partial-output disposal, and compensating cleanup rules. |

A stage is a pure artifact transformation wherever possible. Interactive,
long-running, or external-side-effect stages must declare that fact explicitly
and are initially trusted-only. A workflow cannot form cycles, read a
worker-local path from another stage, mutate a sealed input artifact, or produce
undeclared output ports. Dynamic fan-out is permitted only through a bounded,
versioned manifest emitted by an accepted planning stage; the coordinator must
enforce declared task, artifact, scratch, and output limits.

### Artifact and data-shape generality

The SDK must support more than CSV while retaining streamability and audit
trails. An `ArtifactSchema` can describe tabular records, scientific arrays,
images, meshes, molecular structures, model weights, archives, JSON manifests,
binary checkpoints, or opaque domain formats. It always declares how a consumer
validates structure and bounds bytes/records/dimensions before loading it.

Collections solve multi-input/multi-output work without an immediate database
rewrite. A task can initially receive one composite manifest artifact whose
entries name ordered or keyed logical inputs; it can return a composite output
manifest. Later protocol versions may persist first-class collection edges. The
collection manifest itself is immutable, coordinator-owned, schema-versioned,
and hash-addressed, so the old one-input/one-result API remains compatible.

## Execution model: Worker Agent, slots, and isolation

The Worker Agent is a resource manager, not a scientific runtime. One physical
machine registers one Agent. The Agent advertises a finite inventory and creates
isolated **execution slots**; each leased Task owns exactly one slot until it
finishes, loses its lease, or is cancelled.

```text
machine -> Worker Agent -> CPU / GPU / memory / scratch slot -> task subprocess -> attempt directory
```

`ExecutionProfile` declares whether a task uses a single process, a bounded
process pool, a distributed runtime, or an accelerator backend. It also carries
environment image/digest, entry-point identity, timeout, network policy,
scratch/output bounds, checkpoint policy, and determinism declaration. The
Agent—not a workload—sets environment variables, process groups, filesystem
roots, credentials, resource limits, and lifecycle signals.

### CPU parallelism

`cpu_cores` is a reservation, while `max_concurrency` is the number of slots;
neither is inferred from the other. CPU-bound Python work normally uses a
process pool constrained to the task's allocated cores. A workload must declare
its own internal parallelism and thread-library limits (for example OpenMP,
BLAS, Torch, or RDKit-related native code) so nested pools cannot oversubscribe
the host. The Agent starts independent heartbeat supervision per task and never
claims a task if it cannot reserve all declared resources.

Graceful draining means: stop new claims, continue heartbeat for active
attempts, request checkpoint/cancellation at deadline, then clean only that
attempt directory. Checkpoints are immutable artifacts and may be resumed only
when the workload's manifest explicitly supports checkpoint compatibility; they
are never treated as a completed result.

### Accelerator support

GPU/accelerator capability is generic inventory, not a coordinator-specific
CUDA feature. A future Agent reports device kind/vendor, UUID, compute
capability, memory, driver/runtime/image digest, supported backends, and
allocatable slot count. The coordinator only matches `ResourceRequirements` to
this inventory. The Agent assigns exclusive or shareable devices, sets device
visibility (for example `CUDA_VISIBLE_DEVICES`), reserves memory where the
platform supports it, starts the subprocess, measures usage, and releases the
slot.

The workload implementation chooses CUDA, ROCm, Metal, TPU, FPGA, SIMD, or a
CPU fallback; batches work and manages model/device memory; and declares the
scientific equivalence policy. Reducers and verifiers compare domain outputs,
not device-specific logs or floating-point text. GPU work cannot be enabled for
untrusted quorum merely because it runs: it additionally needs pinned images,
appropriate verifier/trust mode, and CPU/GPU or domain-valid parity evidence.

## Determinism, verification, and scientific validity

`DeterminismProfile` separates reproducibility from correctness:

| Profile | Examples | Minimum acceptance route |
| --- | --- | --- |
| `byte_exact` | canonical descriptors, fingerprints, sorted ETL | Exact artifact SHA-256 from independent owners. |
| `canonical_exact` | format-normalized records, deterministic structures | Versioned parser/canonicalizer then exact records. |
| `numeric_tolerance` | numerical solvers, GPU linear algebra | Structured comparison with absolute/relative/ULP tolerances and invariants. |
| `seeded_stochastic` | conformers, randomized search | Recorded seed, repeated-run policy, statistical/domain verifier. |
| `search_or_optimization` | routing, docking, retrosynthesis | Objective/constraint/domain evidence; often trusted execution. |
| `side_effecting` | instrument control, external database writes | Trusted-only, idempotency key, audit/compensation policy. |

The verifier consumes `OutputManifest` values, declared schemas, and bounded
streams; it returns accept/reject/inconclusive plus evidence. `inconclusive`
must never become success by a reducer default. Verification may be run by a
coordinator adapter, a pinned Python verifier subprocess, or a separate trusted
service—selection remains an open architectural decision. The manifest versions
the verifier configuration, tolerance values, canonicalizer, reference data,
and environment assumptions so historical results remain interpretable.

## Existing-workload migration matrix

| Existing capability | SDK workflow profile | Future adapter path |
| --- | --- | --- |
| Local `similarity-search` | single-process map + bounded top-k reduce | Keep local algorithm; expose a manifest and use current distributed planner/reducer. |
| Distributed `similarity-search` | deterministic shard map -> ordered reduce | Compatibility workload v1; later attach exact verifier and provenance manifest. |
| Local `similarity-graph` | triangular pair-partition map -> edge-set reduce | Preserve pair-coverage invariant as stage verifier. |
| Distributed `similarity-graph` | planned block-pair DAG -> duplicate-safe reduce | First major non-linear partition reference; implement before SDK generalization. |
| `descriptor-batch` | row-partition map -> ordered concatenation | First SDK reference workload and byte-exact quorum candidate. |
| Future ML/docking/QM/MD | parameter sweep, ensemble, or iterative workflow | Use numeric/domain/trusted verifier profile and explicit resource/environment contracts. |

## Authoring and operational lifecycle

An installed workload package should contain a signed or administrator-approved
manifest, Python entry points, schema migrations where required, pinned
environment metadata, golden fixtures, test vectors, and documentation. An
administrator controls enablement; users choose only among enabled manifests and
validated parameter ranges. Workload installation is separate from job
submission, preventing a user from sending code through the normal API.

The eventual author workflow remains deliberate: initialize a template, define
schemas and bounds, implement local scientific core, add planner/runner/reducer/
verifier adapters, generate fixtures, test local parity, test two-worker and
retry behavior, package, review, and enable. Any future CLI names are examples,
not implemented commands.

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
