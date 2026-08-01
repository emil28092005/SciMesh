# SciMesh Workload SDK contract

**Status:** contract `0.1`. The Python `core-batch-v1` foundation is implemented
in `scimesh.sdk`; dynamic, streaming, accelerator, gang, side-effect, and
coordinator protocol-v2 behavior remains a normative target. Normative words
**MUST**, **MUST NOT**, **SHOULD**, and **MAY** apply to an implementation only
when it advertises the affected profile or feature.

This document defines the compatibility boundary for approved SciMesh workload
packages. The sequencing and unresolved product decisions remain in
[`scimesh-sdk-roadmap.md`](scimesh-sdk-roadmap.md). The production coordinator
wire compatibility profile remains
[`ctx-07-distributed-workload-protocol.md`](ctx-07-distributed-workload-protocol.md).

## 1. Scope and invariants

The SDK must express current molecular workloads and future batch, iterative,
streaming, optimization, simulation, ML, image, engineering, and accelerator
workloads through installed, allowlisted packages. "Universal" means that a
workload can declare its dataflow, resources, execution semantics, and result
validation; it does not mean users may submit arbitrary executable code.

Every implementation MUST preserve these invariants:

1. The coordinator owns Jobs, workflow state, Tasks, Attempts, Leases, resource
   assignments, and durable Artifact metadata.
2. A Worker Agent executes only an installed package and entry point whose
   digest is enabled by an administrator.
3. Scientific code never receives PostgreSQL credentials, worker identity
   credentials, or unrestricted coordinator credentials.
4. Every input and output crosses a typed artifact port. Worker-local paths are
   attempt-scoped implementation details and never become durable references.
5. A Task reports success only after all declared outputs are durable and its
   verifier policy has produced an admissible decision.
6. Resource allocation is explicit and lease-fenced. A worker MUST NOT execute
   a Task without reserving its complete declared resource set.
7. Unknown fields, unsupported versions, undeclared outputs, non-finite numeric
   values, and limit violations fail closed.
8. No verifier, reducer, or trust policy may silently downgrade to a weaker
   mode.

## 2. Version and identity model

The following versions are independent and MUST be recorded in each Job and
output provenance manifest:

| Identifier | Meaning |
| --- | --- |
| `sdk_api_version` | Python authoring API compatibility. |
| `protocol_version` | Coordinator/Worker wire contract. |
| `workload.name` + `workload.version` | Scientific behavior and planner contract. |
| `manifest_schema_version` | Shape of the workload manifest. |
| `workflow_schema_version` | Shape of workflow/stage/task plans. |
| `artifact_schema.name` + `version` | Logical data format. |
| `verifier.name` + `version` | Result-acceptance semantics. |
| `environment.digest` | Exact executable environment or image. |

Versions use explicit compatibility ranges. The coordinator enables a workload
only when there is a non-empty intersection among coordinator protocol, Worker
runtime, SDK API, workload package, and verifier versions. A missing or unknown
version is never interpreted as "latest". Jobs remain pinned to the resolved
versions even after an administrator upgrades the installed package.

### 2.1 Feature negotiation and conformance profiles

Universality does not require every deployment to enable every execution mode.
The protocol negotiates explicit feature IDs; a workload declares required and
optional features, and coordinator, Worker, and verifier runtimes advertise
supported versions. Planning fails before Job creation when a required feature
is absent.

| Profile | Required feature set |
| --- | --- |
| `core-batch-v1` | Typed parameters/artifacts, static map/reduce DAG, subprocess runner, exact verifier, CPU/memory/scratch reservations. |
| `dynamic-v1` | Transactional expansion manifests and bounded loop controllers. |
| `stream-v1` | Partition offsets, windows, checkpoints, backpressure, and declared delivery guarantees. |
| `accelerator-v1` | Generic device inventory, fenced allocation, visibility/isolation, and device failure codes. |
| `gang-v1` | Atomic multi-Agent reservation, rendezvous, group lease, and fail-all semantics. |
| `side-effect-v1` | Idempotency/audit/compensation and scoped external credentials. |

Example feature IDs include `artifact-collections@1`, `dynamic-expansion@1`,
`bounded-loops@1`, `stream-checkpoints@1`, `gpu-exclusive@1`, `gpu-mig@1`,
`gang-leases@1`, and `numeric-verifier@1`. Optional features may select a
manifest-declared fallback such as CPU execution; they MUST NOT change output
schema or verification semantics unless the fallback is a separately versioned
workflow variant.

## 3. Workload package and manifest

An SDK workload is an installed Python distribution containing:

- one or more versioned workload manifests;
- explicit Python entry points for planner, runners, reducers, and verifiers;
- artifact and parameter schemas;
- pinned environment/image metadata;
- golden fixtures and conformance tests;
- provenance and license metadata.

Discovery MUST use configured package entry points and an administrator
allowlist. It MUST NOT import modules from job parameters, uploaded archives, or
user-provided filesystem paths.

Illustrative manifest shape:

```yaml
manifest_schema_version: 1
sdk_api: ">=1.0,<2.0"
protocol: ">=2,<3"
workload:
  name: descriptor-batch
  version: 1.0.0
  description: Pinned RDKit 2D descriptors
package:
  distribution: scimesh-descriptors
  digest: sha256:...
  signature: cosign-or-project-signature-reference
environment:
  kind: oci
  digest: sha256:...
parameters_schema: schemas/parameters-v1.json
workflow: workflows/default-v1.yaml
inputs:
  molecules: {schema: molecule-table@1, cardinality: one}
outputs:
  descriptors: {schema: descriptor-table@1, cardinality: one}
determinism: byte_exact
trust_modes: [trusted, verified, untrusted_quorum]
verifier: {name: exact-artifact, version: 1}
limits:
  max_input_bytes: 10737418240
  max_tasks: 10000
  max_output_bytes: 10737418240
capabilities: [descriptor-batch]
```

The manifest MUST declare canonical hyphenated workload names, parameter
schema, external ports, workflow, determinism, supported trust modes, verifier,
resource bounds, output-growth bounds, environment, and capabilities.

## 4. Workflow model

### 4.1 Workflow graph

`WorkflowSpec` defines versioned stages and artifact edges. Its persisted form
is a DAG. Iteration is represented by a bounded controller that materializes a
new DAG segment for each iteration; persisted task dependencies never contain a
cycle.

```yaml
workflow_schema_version: 1
id: default
inputs: [molecules]
stages:
  - id: partition
    kind: plan
    runner: descriptor.partition:v1
  - id: calculate
    kind: map
    needs: [partition]
    runner: descriptor.calculate:v1
  - id: combine
    kind: reduce
    needs: [calculate]
    reducer: descriptor.combine:v1
  - id: verify
    kind: verify
    needs: [combine]
outputs: [descriptors]
failure_policy: fail_fast
```

Stage IDs are stable within a workflow version. Valid stage kinds are initially
`plan`, `map`, `reduce`, `verify`, `loop-controller`, `stream`, `service`, and
`side-effect`. Runtimes MAY add kinds only through a new workflow schema
version.

### 4.2 Stage contract

A `StageSpec` MUST declare:

- stable ID, kind, entry-point identity, and dependencies;
- named input/output ports and their artifact schemas/cardinality;
- parameter projection from immutable Job parameters;
- resource and execution profiles;
- retry, timeout, checkpoint, cancellation, and failure policies;
- fan-out/fan-in bounds and ordering semantics;
- verifier and trust requirements where stage outputs affect acceptance;
- cacheability, side effects, and network/secrets policy.

Dynamic fan-out requires an accepted `ExpansionManifest` containing stable child
keys, bounded child count, TaskSpecs, artifact bindings, and a digest. The
coordinator validates and persists the entire expansion transactionally. A
retry producing a different expansion digest is a conflict, not a replacement.

### 4.3 Bounded iteration

`LoopSpec` supports training epochs, optimization, adaptive sampling, MD
segments, and convergence algorithms:

```yaml
loop:
  state_schema: optimizer-state@1
  max_iterations: 100
  max_wall_seconds: 86400
  body_workflow: optimize-step@2
  continue_when: verifier-entry-point-reference
  checkpoint_every: 5
  on_limit: fail   # fail | accept-best | return-inconclusive
```

The loop controller is trusted orchestration code from the workload package.
It consumes immutable prior-state and evaluation artifacts and emits an
immutable next-iteration expansion. It MUST NOT mutate completed Tasks or reuse
an Attempt directory. Termination is bounded by iterations, wall time, cost,
and output growth. The condition and best-result selection are versioned and
auditable.

### 4.4 Streaming profile

Streaming is an explicit profile rather than an indefinitely running batch
Task. A `StreamSpec` declares source identity, partitioning, offset/checkpoint
schema, event-time or processing-time windows, watermark behavior, backpressure
limit, delivery guarantee, idle/terminal condition, and output compaction.

Supported guarantees are `at_least_once` initially and, only where the source
and sink support transactional offsets, `exactly_once`. A checkpoint commits
source offsets only after corresponding output artifacts are durable. Stream
processors MUST be restartable from a sealed checkpoint artifact. An unbounded
stream produces versioned window/result artifacts and does not hold one Task
lease forever.

### 4.5 Side effects and human interaction

Stages controlling instruments or writing external systems are trusted-only.
They require an idempotency key, declared target, credential scope, audit event,
timeout, and compensation/manual-recovery policy. Retries are disabled unless
the stage proves idempotency. Human approval is modeled as a coordinator state
transition with an authenticated decision record, never as a worker waiting
indefinitely while holding resources.

## 5. Task and artifact contracts

`TaskSpec` is the concrete unit leased to a Worker Agent:

```yaml
schema_version: 1
workload: descriptor-batch@1.2.0
package_digest: sha256:...
manifest_digest: ...
trust_mode: trusted
task_key: calculate/shard-000042
stage_id: calculate
parameters: {...validated JSON...}
inputs:
  molecules:
    collection: ordered
    artifacts: [{artifact_id: uuid, sha256: ..., schema: molecule-table@1}]
expected_outputs:
  descriptors: {schema: descriptor-table@1, cardinality: one}
resources: {profile: cpu-medium@1}
execution: {profile: python-process@1}
```

`task_key` is deterministic within a workflow expansion. The coordinator adds
its durable Task ID, Attempt number, Lease, and generated download/upload URLs.
Plans contain artifact IDs and checksums, never external credentials or local
paths.

### 5.1 Artifact schemas and collections

Each port references an `ArtifactSchema` defining logical type, schema version,
media type, encoding, cardinality, maximum bytes/records/dimensions, streaming
support, canonicalizer, validation entry point, and privacy/retention class.

Collections are `single`, `ordered`, `keyed`, or `set`:

- `ordered` preserves declared order and is included in the collection digest;
- `keyed` requires unique canonical string keys;
- `set` canonicalizes by artifact identity and forbids duplicates;
- nested collections require explicit schema permission and depth limits.

Protocol v1 compatibility uses one immutable composite-manifest artifact to
represent a collection. A later protocol may persist collection edges directly.

### 5.2 Output and provenance manifest

Before completion, a runner uploads an `OutputManifest` listing every declared
output artifact, checksum, schema, size, record/dimension summary, metrics, and
provenance. Provenance includes resolved versions, package/environment and
manifest digests, Worker runtime, allocated resource IDs, parameters digest,
input collection digest, timestamps, random seed where applicable, and
checkpoint lineage.

Unexpected ports, missing required outputs, extra artifacts, schema failures,
or limit violations reject the Attempt. Logs and checkpoints are separate
artifact kinds and never satisfy scientific output ports.

### 5.3 Locality and cache

Artifacts remain coordinator-owned even when cached. Worker Agents MAY maintain
a content-addressed read cache verified by checksum. Cache entries carry size,
last-use, schema, environment sensitivity, and retention class; eviction never
deletes the durable coordinator copy.

Task matching MAY score data locality after eligibility and fairness checks.
It MUST NOT weaken trust, resource, lease, or ownership constraints. Large input
staging occurs before execution timeout starts, with a bounded staging lease.
Cache hits are verified before use; private artifacts are isolated by tenant or
encrypted policy.

## 6. Resource and execution contracts

### 6.1 Resource inventory and requests

A Worker Agent advertises versioned, periodically refreshed inventory:

```yaml
agent:
  cpu:
    logical_cores: 32
    allocatable_cores: 28
    architecture: x86_64
  memory_mb: 131072
  scratch_mb: 1000000
  accelerators:
    - kind: gpu
      vendor: nvidia
      device_uuid: GPU-...
      model: A100
      memory_mb: 81920
      compute_capability: "8.0"
      partitioning: [exclusive, mig]
      topology_group: nvlink-0
  runtime:
    os: linux
    container_runtime: ...
    driver_versions: {...}
    environment_digests: [sha256:...]
```

A `ResourceRequirements` request distinguishes minimums, preferred values, and
hard constraints. Core fields are CPU cores, memory, scratch, accelerator count
and kind, device memory, architecture/capability, exclusivity, topology,
network/interconnect class, environment digest, estimated input/output bytes,
and maximum duration.

Allocation is atomic and lease-fenced. A Task cannot start until the Agent has
confirmed the reservation token. Resources are released only after the process
group exits and attempt cleanup completes. Device IDs and secrets are not part
of scientific parameters.

### 6.2 CPU concurrency

One machine runs one Worker Agent with `max_concurrency` execution slots. Each
Task separately requests `cpu_cores`; the sum of reservations cannot exceed
allocatable capacity. `ExecutionProfile` declares:

- `process_model`: `single`, `process_pool`, `thread_pool`, or `external_runtime`;
- maximum worker processes and threads per process;
- OpenMP/BLAS/native-library thread limits;
- affinity/NUMA preference when required;
- whether nested parallelism is prohibited (default) or explicitly bounded.

CPU-bound Python SHOULD use isolated processes. Threads remain valid for I/O or
native extensions that release the GIL. Independent task heartbeat supervisors
remain outside scientific subprocesses. Draining stops claims first, maintains
active leases, then checkpoints/cancels at the declared deadline.

### 6.3 GPU and accelerator allocation

GPU requests can specify `exclusive_device`, `fractional`, or `partition`
(including MIG-like partitions); runtimes MUST advertise which modes they can
enforce. Requests may require multiple devices in one topology group. The
coordinator performs generic eligibility and gang selection; the Agent owns
device isolation and sets backend-specific visibility variables.

The Agent MUST fence allocation by device UUID/partition ID, validate driver,
runtime and environment compatibility, prevent incompatible sharing, monitor
device health and memory, and terminate the whole process group on lease loss.
OOM, device reset, ECC failure, and unavailable-device errors have distinct
sanitized codes and workload-declared retry policies. Preemptible GPU Tasks need
an explicit compatible checkpoint contract.

The workload owns CPU/GPU implementation, batching, mixed precision, seeds,
algorithm determinism, memory strategy, and scientific parity tests. The
coordinator contains no CUDA calls or domain formulas. A GPU result follows the
same output schema and verifier semantics as any CPU result.

### 6.4 Multi-node and gang execution

`GangSpec` expresses MPI, multi-node training, and tightly coupled simulations:

```yaml
gang:
  replicas: 4
  per_replica_resources: {cpu_cores: 8, gpu_count: 1, memory_mb: 32768}
  topology: {same_fabric: true, min_bandwidth_class: infiniband}
  rendezvous: worker-agent-managed
  failure_mode: fail_all
```

The coordinator atomically reserves all replicas or none, then issues one gang
lease and per-replica fenced assignments. Worker Agents establish a scoped
rendezvous channel without exposing general coordinator credentials. A failed,
expired, or cancelled replica invalidates the gang according to `failure_mode`;
partial success cannot complete the stage. Gang retries use a new Attempt and
new rendezvous credentials.

## 7. Verification and trust

Verifier decisions are `accepted`, `rejected`, or `inconclusive`. Only
`accepted` can satisfy a stage. Evidence is a bounded, sanitized artifact tied
to input/output and verifier digests.

| Verifier | Intended semantics |
| --- | --- |
| `ExactArtifactVerifier` | Whole-file or declared collection digest equality. |
| `CanonicalRecordVerifier` | Parse, validate, normalize, order, and serialize through a versioned canonicalizer. |
| `NumericToleranceVerifier` | Structured element/aggregate comparison with declared absolute, relative, ULP, NaN, and shape policies. |
| `StatisticalVerifier` | Repeated seeded evidence and versioned statistical acceptance criteria. |
| `DomainSpecificVerifier` | Workload-owned invariants, constraints, objective bounds, or reference checks. |
| `TrustedWorkerPolicy` | Accept only from allowed trust/environment attestations; still validate schema and bounds. |

The current whole-artifact SHA quorum maps only to `ExactArtifactVerifier`.
Canonical, numeric, stochastic, optimization, and side-effecting workloads MUST
declare an appropriate verifier/trust combination. Reducers consume only
accepted partial outputs and MUST detect missing, duplicate, conflicting, or
inconclusive inputs.

Quorum candidates MUST be coordinator-authenticated envelopes with unique
Attempt/candidate identity and an owner identity. A verifier counts at most one
vote per owner. It also receives a coordinator-owned binding for workload,
task, package/manifest/environment, parameters, and input-collection digests;
outputs from another job or code pin are invalid even when their result bytes
match.

## 8. Failure, retry, cancellation, and checkpoint semantics

Every failure has a stable sanitized code, category (`input`, `scientific`,
`resource`, `infrastructure`, `lease`, `verification`, or `policy`), retryability,
and optional bounded evidence reference. Raw tracebacks and private paths remain
local.

- Retries create a new Attempt directory and resource lease; they never mutate
  prior artifacts.
- Idempotent completion accepts the identical output manifest for the same
  Attempt; conflicting manifests are rejected.
- Speculative execution, when enabled, creates multiple Attempts but commits at
  most one accepted result and cancels the rest.
- Lease loss immediately fences upload/completion and terminates execution.
- Cancellation propagates to process groups/gangs and disposes uncommitted
  staging artifacts.
- Retry budgets may be per Task, Stage, Loop, and Job; the strictest exhausted
  budget wins.
- Checkpoint resume requires matching workload, schema, environment, and
  checkpoint compatibility versions. Otherwise execution restarts cleanly.
- Side-effecting retries require an idempotency record or explicit operator
  recovery.

Workflow failure policies are `fail_fast`, `continue_independent`,
`allow_partial` (only with an output schema/verifier that defines partial
results), and `compensate`. A failed reducer/verifier never leaves a Job marked
completed.

## 9. Package security and permissions

Installation and job submission are separate authorities. Only administrators
or managed policy may install, sign, approve, enable, upgrade, or revoke a
workload package. A job references an enabled immutable package digest.

Package policy MUST cover signature trust roots, dependency/image scanning,
license/provenance records, supported platforms, vulnerability/revocation
status, and reproducible build evidence. Upgrade does not rewrite running or
historical Jobs.

Each stage declares least-privilege permissions:

- network: none, coordinator-artifacts-only, allowlisted egress, or trusted;
- filesystem: read-only inputs, attempt scratch, declared outputs;
- secrets: named scoped handles, never raw values in Task parameters;
- subprocess: denied by default except the installed runner/runtime contract;
- devices and host interfaces: only allocated resources.

Worker Agents enforce permissions through the strongest available OS/container
sandbox and report the enforcement profile. A workload requiring unavailable
isolation is ineligible rather than silently unsandboxed.

## 10. SDK interfaces and conformance

The Python API exposes protocols equivalent to:

```python
class Planner(Protocol):
    def validate(self, request: JobRequest) -> ValidatedJob: ...
    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan: ...

class Runner(Protocol):
    def run(self, context: TaskContext) -> OutputManifest: ...

class Reducer(Protocol):
    def reduce(self, context: ReduceContext) -> OutputManifest: ...

class Verifier(Protocol):
    def verify(self, context: VerifyContext, candidates: CandidateOutputs) -> VerificationDecision: ...
```

Concrete public value objects are immutable, typed, JSON-safe, strict about
unknown fields, and canonically serialized inside versioned wire contracts.
Scientific cores SHOULD remain callable without a coordinator so the same
implementation powers local and distributed adapters.

The shipped `LocalCoreBatchExecutor` is a trusted in-process conformance
harness, not the `core-batch-v1` production isolation boundary. It rejects
restricted-network, parallel-process/thread, accelerator, secret, checkpoint,
retry, gang, and advanced-stage declarations. Subprocess isolation, hard
timeouts, leases, and credential enforcement remain requirements for an Agent
runtime that advertises those guarantees.

An SDK conformance suite MUST test manifest/schema validation, deterministic
planning, no local-path/URI leakage, output bounds, local/distributed parity,
retry and completion-order invariance, lease-loss cleanup, verifier behavior,
resource eligibility, and package permission declarations. Profile-specific
suites add two-worker byte equality, numeric tolerance, stochastic evidence,
stream recovery, loop limits, gang failure, or GPU parity as applicable.

## 11. Existing workload compatibility

- Local and distributed `similarity-search` map to a bounded shard-map and
  top-k reducer workflow without duplicating the scientific algorithm.
- Local `similarity-graph` and future CTX-10 distribution map to triangular
  block-pair expansion plus duplicate-safe edge reduction and pair-coverage
  verification.
- Existing `DistributedPlan`, single input artifact, single partial result, and
  `chunk_index` become the SDK compatibility profile `map-reduce-v1`.
- `descriptor-batch` is the first new reference implementation for the full
  manifest, exact verifier, golden fixtures, and local/distributed parity.

No current workload is removed or renamed by adopting the SDK. Migration is an
adapter and manifest exercise first; protocol/database generalization occurs in
versioned later phases.

## 12. Deferred decisions

The roadmap must resolve these before implementation reaches the affected
phase:

1. SDK package ownership and independent release cadence.
2. Go-to-Python planner/reducer/verifier bridge and isolation boundary.
3. Verifier execution placement and environment attestation.
4. Streaming source/sink integrations and exactly-once scope.
5. Multi-node rendezvous, network identity, and gang scheduling persistence.
6. Accelerator sharing/MIG portability and accounting.
7. Workload signing technology, trust roots, and revocation distribution.
8. Tenant quotas, costs, priorities, fairness, and data-retention policy.
9. First-class artifact collections versus composite manifests.
10. Compatibility negotiation and deprecation support windows.
