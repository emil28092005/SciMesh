# SDK overview

`scimesh.sdk` is the **framework only**. It contains no scientific workload
code. Workloads are user Python scripts and packages that import the SDK and
live outside it — the built-in SciMesh workloads under `scimesh/workloads/`
are exactly such scripts, and a future workload library can follow the same
shape.

```text
scientific implementation -> SDK manifest/plan -> map tasks -> reduce -> verifier
```

## The `core-batch-v1` profile

The implemented authoring profile is **`core-batch-v1`**: a static
map/reduce workflow with one external input and one final output.

- one input dataset (a delimited table, typically TSV);
- deterministic row-bounded (or otherwise partitioned) shards;
- one **map** task per shard, each producing one partial artifact;
- one **reduce** task merging the accepted partials into the final artifact;
- an acceptance **verifier** for every output-producing stage.

The SDK's strict value objects make the whole contract explicit:

- `WorkloadManifest` pins identity, compatibility ranges, package and
  environment digests, parameter schema, workflow, ports, determinism,
  trust modes, verifier, and limits;
- `WorkflowSpec`/`StageSpec` describe a typed acyclic DAG;
- `TaskSpec`/`WorkflowPlan` carry the exact workload pin (package, manifest,
  environment digests, trust mode, negotiated features);
- `OutputManifest`/`Provenance` describe sealed durable results;
- artifacts are content-addressed (`sha256`), immutable, and free of
  transport URLs and local paths.

## What the SDK provides

| Area | Modules | Purpose |
| --- | --- | --- |
| Identity | `identity`, `schema` | `WorkloadId`, versions, schema refs, bounded JSON parameter schemas |
| Declarations | `manifest`, `workflow`, `artifacts`, `execution`, `resources` | Manifest, DAG stages, typed ports, execution/resource profiles |
| Planning | `plans` | `JobRequest`, `TaskSpec`, `WorkflowPlan` |
| Registry | `registry`, `integrity` | Allowlisted discovery, digest pinning |
| Negotiation | `runtime` | Fail-closed compatibility negotiation |
| Execution | `conformance` | `LocalCoreBatchExecutor`, `LocalArtifactStore` |
| Verification | `verification` | Exact, canonical, and numeric verifier primitives |
| Authoring | `batch` | `MapReduceWorkload` scaffold |

## Security model

- Workload discovery requires an **administrator allowlist**: exact
  distribution, workload name/version, and a `sha256:` package digest.
  Discovery measures the installed package before and after importing the
  entry point and fails transactionally on any mismatch. Job parameters can
  never name a module, entry point, or executable.
- Compatibility negotiation is **fail-closed**: if the runtime does not
  advertise a declared feature (gangs, GPU, streams, checkpoints, secrets,
  retries, process pools, dynamic expansion), the job is rejected before any
  workload code runs. Declaring an advanced profile never silently enables
  it.
- Handlers receive **bridge-owned contexts**: `ArtifactCatalog` for verified
  input materialization and `ArtifactSink` for sealing outputs. They never
  see database credentials, upload URLs, or coordinator tokens.
- The **local conformance executor** is deliberately trusted and in-process.
  It rejects anything but `TrustMode.TRUSTED`, a single non-nested host
  thread, and the trusted network policy — a contract, not a limitation.

## What is not supported yet

The coordinator contract (v1) persists flat one-input/one-result tasks.
Until a versioned protocol rollout lands, the following remain **fail-closed
by design**:

- distributed execution of multi-input map stages (for example the
  block-pair `similarity-graph` tasks) and of workloads beyond the v1
  contract;
- coordinator-backed GPU scheduling, streams, gang leases, checkpoints,
  retries, and secret injection;
- dynamic (plan-stage) expansion.

The verifier primitives `CanonicalRecordVerifier` and
`NumericToleranceVerifier` exist and are tested, but only the
`ExactArtifactVerifier` (whole-file SHA-256) is eligible for
`untrusted_quorum` in v1.

## Next

- [Authoring workloads](authoring-workloads.md) — build a workload with
  `MapReduceWorkload`.
- [API reference](../api/index.md) — the complete SDK surface.
