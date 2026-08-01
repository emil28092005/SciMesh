# SciMesh Workload SDK v1

SciMesh now ships a public Python SDK under `scimesh.sdk`. The implemented
authoring profile is **`core-batch-v1`**: installed and digest-pinned workload
definitions, strict JSON manifests, typed artifact ports and collections, a
static map/reduce workflow, CPU/memory/scratch eligibility, atomic local
resource reservation, exact/canonical/numeric verifier primitives, and a
compatibility adapter for the existing `DistributedWorkload` protocol. Its
local executor is deliberately a trusted, in-process conformance harness; the
production subprocess/lease sandbox remains a coordinator/Worker milestone.

The full target contract remains in
[`scimesh-sdk-contract.md`](scimesh-sdk-contract.md). Dynamic expansion,
streaming, accelerators, gang execution, and side effects have typed bounded
declarations, but the current coordinator/Worker runtime does not advertise
their features. Compatibility negotiation therefore rejects those workflows
before planner code runs.

## SDK versus workloads

`scimesh.sdk` is the framework only: strict manifests, plans, artifacts,
registry, verifiers, and the local conformance executor. It contains no
scientific workload code. Workloads are user Python scripts and packages that
import the SDK and live outside it. The built-in SciMesh workloads are under
`scimesh/workloads/`:

- `scimesh/workloads/search/` — SDK-built `similarity-search@1.0.0`;
- `scimesh/workloads/graph/` — SDK-built `similarity-graph@1.0.0`;
- `scimesh/workloads/descriptors/` — SDK-built `descriptor-batch@1.0.0`;
- `scimesh/workloads/molwt_filter/` — SDK-built `molwt-filter@1.0.0`, the
  minimal authoring example: it only declares identity, parameters, ports,
  and the `compute_shard` hook, using the scaffold's default sharding and
  concatenation;
- `scimesh/workloads/library.py` — the built-in library wiring: a default
  registry containing all three definitions and a runtime advertising their
  capabilities;
- the plain `scimesh/workloads/*.py` modules remain the local CLI scientific
  cores and their `Workload` registry.

Each SDK-built workload is a small package with `core.py` (scientific code)
and `definition.py` (manifest plus planner/runner/reducer handlers). A future
external workload library can follow the same shape: its own distribution, one
`scimesh.workloads` entry point per workload version, and an administrator
allowlist.

## What authors import

The stable authoring surface is exported from `scimesh.sdk`:

- `WorkloadManifest`, `WorkloadId`, `VersionRange`, `PackageSpec`, and
  `EnvironmentSpec` pin identity and compatibility;
- `ArtifactSchema`, `PortSpec`, `ArtifactRef`, and `ArtifactCollection` define
  immutable data boundaries without transport URLs or local paths;
- `WorkflowSpec`, `StageSpec`, `ArtifactEdge`, `TaskSpec`, and `WorkflowPlan`
  define a typed acyclic plan and pin package/manifest digests plus trust mode;
- `ResourceRequirements` and `ExecutionProfile` separate per-task resources
  from Agent `max_concurrency`;
- `Planner`, `Runner`, `Reducer`, and `Verifier` are the package handler
  protocols;
- `OutputManifest` and `Provenance` describe sealed durable results;
- `WorkloadRegistry` resolves an exact name, version, package digest, runtime,
  environment, and feature set. It never selects an implicit latest version;
- `MapReduceWorkload` is the primary authoring scaffold for `core-batch-v1`:
  a subclass declares its identity, parameter schema, artifact ports, and
  three scientific hooks (partition, compute, merge), and the SDK assembles
  the manifest, map/reduce stages, workflow, digest-pinned handlers, and the
  exact-artifact verifier. See "Authoring a workload" below.

Persisted manifests, requests, plans, tasks, expansions, outputs, candidates,
decisions, and failures are frozen, recursively immutable, JSON-safe,
canonically serialized, and strict about unknown fields; their enclosing wire
contracts carry schema versions.
Artifact identities contain a coordinator-owned UUID, schema, checksum, media
type, and bounds; a scientific handler never persists a filesystem path.

## Try the built-in SDK workloads

This example runs the SDK-built `similarity-search` without starting
PostgreSQL or the coordinator:

```python
from pathlib import Path

from scimesh.sdk import (
    ArtifactCollection,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
)
from scimesh.workloads.library import (
    default_sdk_registry,
    default_sdk_runtime,
    similarity_search_sdk_definition,
)

root = Path("sdk-run")
store = LocalArtifactStore(root / "artifacts")
workload = similarity_search_sdk_definition(shard_rows=1_000)

dataset = store.import_file(
    Path("chembl_37_chemreps.txt"),
    declaration=workload.manifest.inputs["input"].schema,
)
request = JobRequest(
    workload=workload.manifest.workload,
    parameters={"query_smiles": "CCO", "top_k": 20},
    inputs={"input": ArtifactCollection.single(dataset)},
)

result = LocalCoreBatchExecutor(
    default_sdk_registry(shard_rows=1_000),
    default_sdk_runtime(),
    store,
    root / "attempts",
).execute(request, workload.manifest.package.digest)

result_ref = result.outputs["result"].items[0].artifact
print(store.materialize(result_ref))
```

`LocalCoreBatchExecutor` is a correctness/conformance runtime, not a substitute
for coordinator leases or multi-machine scheduling. It accepts only
`TrustMode.TRUSTED`, `NetworkPolicy.TRUSTED`, single-process/single-threaded CPU
map/reduce stages without secrets, checkpoints, retries, gangs, or
accelerators. It does not claim network, timeout, process, or credential
isolation. Unsupported declarations are rejected before a handler runs. The
harness runs the SDK-built workload handlers themselves, and their parity
against the single-process references is covered by automated tests.

## The descriptor-batch reference workload

`descriptor-batch@1.0.0` is the first SDK-native reference workload: it is
built directly on the manifest/planner/runner/reducer contracts, and it is
the intended first `untrusted_quorum` candidate
(`byte_exact` plus `exact-artifact@1`). Its scientific contract is pinned:

- one output CSV row per valid input molecule, in input order, with RDKit
  canonical SMILES recomputed by RDKit;
- an explicit 81-name pinned RDKit 2D descriptor set (see
  `scimesh/workloads/descriptors/core.py`), validated against the installed RDKit at
  definition build time;
- `%.6f` float formatting, `utf-8` CSV with one header, and row-bounded
  deterministic shards;
- `skip_invalid` is the only parameter (default `true`): invalid SMILES rows
  are counted and skipped, or fail the run when `false`;
- the reducer concatenates shard partials by shard index with exactly one
  header, so the distributed output is byte-identical to the single-process
  reference for the same input rows.

```python
from pathlib import Path

from scimesh.sdk import (
    ArtifactCollection,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    WorkloadRegistry,
)
from scimesh.workloads.descriptors import descriptor_batch_sdk_definition
from scimesh.workloads.library import default_sdk_runtime

root = Path("descriptor-run")
store = LocalArtifactStore(root / "artifacts")
workload = descriptor_batch_sdk_definition(shard_rows=1_000)

dataset = store.import_file(
    Path("chembl_37_chemreps.txt"),
    declaration=workload.manifest.inputs["input"].schema,
)
request = JobRequest(
    workload=workload.manifest.workload,
    parameters={"skip_invalid": True},
    inputs={"input": ArtifactCollection.single(dataset)},
)
registry = WorkloadRegistry()
registry.register(workload.definition(), enabled=True)

result = LocalCoreBatchExecutor(
    registry,
    default_sdk_runtime(),
    store,
    root / "attempts",
).execute(request, workload.manifest.package.digest)

result_ref = result.outputs["result"].items[0].artifact
print(store.materialize(result_ref))
```

The descriptor-batch entry point `descriptor-batch@1.0.0` is declared in
`pyproject.toml`; discovery loads it only when an administrator supplies a
matching `AllowedPackage` allowlist entry. Its manifest declares both
`trusted` and `untrusted_quorum` trust modes and the exact-artifact verifier,
so the same definition can later run under coordinator quorum once protocol-v2
leases exist.

## The SDK-built similarity workloads

`similarity-search@1.0.0` and `similarity-graph@1.0.0` are SDK-built workloads
under `scimesh/workloads/search/` and `scimesh/workloads/graph/`; both reuse
the local scientific cores from `scimesh/workloads/similarity_search.py` and
`similarity_graph.py` and declare `byte_exact` + `exact-artifact@1`:

- the search workload resolves `query_id` exactly once at plan time, shards
  the input deterministically, computes a local top-k per shard with the
  reference heap, and merges the sorted partials with the same tie-breakers,
  so the final CSV is byte-identical to the single-process CLI output;
- the graph workload parses molecules once into deterministic row-ordered
  blocks, plans one map task per block pair `(i, j)` with `i <= j`, and its
  reducer enforces the pair-coverage invariant (every unordered molecule pair
  compared exactly once, no duplicates) before emitting the same
  deterministically sorted edge list as the local brute-force reference, for
  either threshold direction and any block size;
- the v1 worker executes SDK-built workloads directly:
  `scimesh/worker/runners.py` is a workload-generic wire bridge that builds a
  `TaskSpec` with the workload's own pins, negotiates against a runtime
  derived from the loaded definitions, reserves resources, seals the partial
  through a content-addressed store, and uploads the resulting CSV over the
  unchanged coordinator contract. The worker loads workloads from
  `SCIMESH_WORKLOAD_ALLOWLIST` (a JSON array of
  `{distribution, name, version, digest}` entries matched against installed
  `scimesh.workloads` entry points) or falls back to the built-in
  `similarity-search`; advertised capabilities come from
  `SCIMESH_CAPABILITIES`. Workloads whose map stage needs more than one input
  port are rejected with a clear message until the coordinator contract
  supports them.

## Authoring a workload

A workload is a user script that imports the SDK. For the standard
`core-batch-v1` shape (one input dataset, shards, partials, one merged result)
subclass `MapReduceWorkload` and implement the three scientific hooks; the
framework provides everything else:

```python
from pathlib import Path
from typing import Any, Mapping, Sequence

from scimesh.sdk import (
    ArtifactSchema,
    ComponentRef,
    MapReduceWorkload,
    PortSpec,
    SchemaRef,
    WorkloadId,
)

class CountRowsWorkload(MapReduceWorkload):
    workload_id = WorkloadId("count-rows", "1.0.0")
    description = "Count TSV data rows per shard and concatenate the counts."
    parameters_schema = {
        "type": "object",
        "additionalProperties": False,
        "properties": {"prefix": {"type": "string", "minLength": 1, "maxLength": 50}},
    }
    input_port = PortSpec(ArtifactSchema(
        SchemaRef("molecule-table", 1), "text/tab-separated-values", "utf-8",
        max_bytes=10**9, validator=ComponentRef("delimited-table", 1),
        validator_configuration={"required_columns": ["canonical_smiles", "chembl_id"]},
    ))
    partial_port = output_port = PortSpec(ArtifactSchema(
        SchemaRef("count-table", 1), "text/csv", "utf-8",
        max_bytes=10**9, validator=ComponentRef("delimited-table", 1),
        validator_configuration={"columns": ["id", "rows"]},
    ))
    map_parameter_names = ("prefix",)

    def partition_input(self, input_path, parameters, workspace):  # -> list[Path]
        ...  # deterministic shard files, one per map task

    def compute_shard(self, inputs, parameters, output_path):  # -> Mapping[str, int|float]
        ...  # one map task; inputs maps each map port to a materialized file

    def reduce_partials(self, partial_paths, parameters, output_path):  # -> Mapping[str, int|float]
        ...  # deterministic merge of the accepted partials
```

The base class then provides `validate`, `plan`, `run`, `reduce`, and
`definition()`. For workloads whose map output is a simple filtered or
transformed table, the scaffold's defaults already cover partitioning
(row-bounded shards that keep the header) and reduction (concatenation with
one header), so only `compute_shard` has to be written — that is exactly what
the built-in `molwt-filter` workload does. The registry, negotiation, resource
reservation, verification, and the local conformance executor treat the
result like any other workload:

```python
from scimesh.sdk import (
    ArtifactCollection,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    WorkloadRegistry,
)
from scimesh.workloads.library import default_sdk_runtime

workload = CountRowsWorkload(package_digest=..., environment_digest=...)
registry = WorkloadRegistry()
registry.register(workload.definition(), enabled=True)

store = LocalArtifactStore(Path("artifacts"))
artifact = store.import_file(Path("tiny.tsv"), declaration=workload.manifest.inputs["input"].schema)
request = JobRequest(workload=workload.manifest.workload, parameters={"prefix": "x"},
                     inputs={"input": ArtifactCollection.single(artifact)})
result = LocalCoreBatchExecutor(registry, default_sdk_runtime(), store, Path("work")) \
    .execute(request, workload.manifest.package.digest)
```

Hooks you can override beyond the three scientific ones:

- `domain_validate(parameters)` — extra job-parameter validation (the JSON
  schema already ran);
- `resolved_parameters(request)` / `resolved_parameters_for_plan(job, input_path, resolved)`
  — values persisted into the plan (for example one-time query resolution);
- `plan_tasks(...)` — custom task construction (the graph workload uses this
  to plan one task per block pair with two block inputs);
- `parse_partial_key(key)` / `validate_partial_keys(parsed)` — partial-key
  policy (default: `map.<eight-digit-index>`, contiguous; the graph workload
  parses `map.<i>x<j>` and enforces the pair-coverage invariant);
- `map_stage_inputs` — a map stage with more than one input port (each extra
  port must share the external input schema).

Anything outside this model uses the lower-level SDK value objects directly.
Authoring rules: keep the scientific core callable without a coordinator,
inline a strict JSON parameter schema, declare artifact schemas with bounds,
return only sink-sealed artifacts, and select a verifier compatible with
determinism and trust.

To run a workload from the command line without writing any program code:

```bash
scimesh workload list
scimesh workload run count-rows --input tiny.tsv --params '{"prefix": "x"}' -o result.csv
```

`scimesh workload` is a generic SDK tool; it contains no workload-specific
logic, so new workloads do not require changes to the CLI or any other part of
the program.

## Package shape and registration

A workload distribution provides one explicit entry point per workload
version. The built-in workloads are part of the `scimesh` distribution:

```toml
[project.entry-points."scimesh.workloads"]
"similarity-search@1.0.0" = "scimesh.workloads.search:workload_definition"
"similarity-graph@1.0.0" = "scimesh.workloads.graph:workload_definition"
"descriptor-batch@1.0.0" = "scimesh.workloads.descriptors:workload_definition"
```

The factory returns a `WorkloadDefinition` containing its manifest and handler
objects. An administrator supplies an `AllowedPackage` with the same
distribution, exact `WorkloadId`, and `sha256:` package digest. Discovery
filters installed metadata before importing an entry point and fails
transactionally if an allowlisted definition is missing or mismatched. Job
parameters cannot name a module, entry point, package path, or executable.
The measured digest covers package payload files and installed entry-point
declarations and is checked before and after loading. It is a content pin, not
a signature or image attestation; production discovery should run in a fresh
trusted control-plane process so a pre-populated Python module cache is not an
integrity boundary.

Direct registration is useful for tests and embedded deployments:

```python
registry = WorkloadRegistry()
registry.register(definition, enabled=False)
registry.enable(
    definition.manifest.workload.name,
    definition.manifest.workload.version,
    definition.manifest.package.digest,
)
```

Both version and digest are required when resolving or planning. Upgrading an
installed definition does not change the identity of an existing Job.

## Authoring rules

1. Keep the scientific core callable without a coordinator.
2. Inline a strict JSON parameter schema with `type: object` and
   `additionalProperties: false`; the planner still performs domain validation.
3. Give every external and stage port an `ArtifactSchema` with a media type,
   schema version, and byte/record/dimension bounds.
4. Connect stage ports with `ArtifactEdge` values. `WorkflowSpec` checks source
   and target schemas, complete input bindings, declared dependencies, and
   acyclicity.
5. Declare one `ResourceRequirements` and `ExecutionProfile` per stage. A task
   cannot run until its entire request is eligible and atomically reserved.
6. Return only sink-sealed artifacts in `OutputManifest`; the local harness
   binds task key/provenance itself and rejects fabricated references,
   unexpected/missing ports, wrong schema/media type, and cumulative output or
   artifact-limit violations.
7. Select a verifier compatible with determinism and trust. SDK v1 permits
   `untrusted_quorum` only for `byte_exact` plus `exact-artifact@1`.
8. Add golden fixtures, local/distributed parity, retry/completion-order, and
   verifier failure tests before enabling a package.

`ArtifactSink` and `ArtifactCatalog` are bridge-owned protocols. They let
scientific handlers materialize verified inputs and seal outputs without bearer
tokens, database credentials, upload URLs, or durable local paths.

## Verification

The SDK includes:

- `ExactArtifactVerifier`: compares logical port/collection/schema/content
  digests while ignoring coordinator UUIDs, timestamps, metrics, and worker
  identity. Quorum inputs use coordinator-created `CandidateOutput` envelopes,
  count at most one vote per owner, and require a `VerificationBinding` for the
  exact task, inputs, parameters, package, manifest, and environment;
- `CanonicalRecordVerifier`: applies a package-owned bounded canonicalizer and
  compares length-framed canonical records;
- `NumericToleranceVerifier`: recursively checks structure plus explicit
  absolute, relative, ULP, and NaN policy, returning bounded sanitized evidence.

Canonical and numeric objects expose direct bounded comparison methods. To use
them as manifest `Verifier` handlers, the package supplies an artifact-to-record
or artifact-to-structured-value loader; without one, verification returns
`inconclusive` rather than accepting bytes it did not parse.

A decision is `accepted`, `rejected`, or `inconclusive`; only `accepted`
satisfies a stage. Evidence is limited to 16 KiB and cannot contain local paths
or transport URLs.

## Resources and current runtime boundary

`ResourcePool` provides a lock-protected all-or-nothing local reservation for
CPU cores, memory, scratch, and accelerator device/partition IDs, including
whole-device versus partition conflict fencing. It enforces aggregate capacity
and execution-slot count. `ExecutionProfile` produces only
allocation-derived OpenMP/BLAS and device-visibility values; credentials never
belong to scientific parameters.

The current protocol-v1 coordinator stores one input/result per flat task and
does not persist resource requirements, device allocations, stage edges, or
package versions. The production Worker also remains serial. Consequently:

- SDK `core-batch-v1` can be authored, validated, tested, discovered, and run
  through the trusted local conformance harness now;
- existing production `similarity-search` remains on its compatible v1 wire
  path and is not renamed;
- real concurrent claims, GPU scheduling, multi-output DAG execution, dynamic
  loops, streaming, and gang leases require the versioned coordinator/Worker
  changes listed in [`scimesh-sdk-roadmap.md`](scimesh-sdk-roadmap.md);
- merely declaring a GPU or gang request never enables it. Missing runtime
  features or inventory fail before the planner executes.

## Conformance commands

Install development tools and run the SDK suite:

```bash
pip install -e '.[dev]'
pytest tests/test_sdk_models.py \
       tests/test_sdk_resources.py \
       tests/test_sdk_verification.py \
       tests/test_sdk_compatibility.py \
       tests/test_sdk_registry.py \
       tests/test_sdk_descriptors.py \
       tests/test_sdk_search.py \
       tests/test_sdk_graph.py
```

Run `pytest` for the full Worker, local-science, and SDK regression suite. Package authors can reuse `LocalArtifactStore`,
`LocalCoreBatchExecutor`, and `assert_manifest_round_trip` in their own golden
tests.
