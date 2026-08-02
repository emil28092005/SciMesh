# Authoring workloads

A workload is a **user script** that imports the SDK. For the standard
`core-batch-v1` shape — one input dataset, deterministic shards, one merged
result — subclass [`MapReduceWorkload`](../api/sdk-batch.md) and implement
the scientific hooks. The SDK assembles everything else: the immutable
manifest, the map/reduce stages, the workflow DAG, the digest-pinned
planner/runner/reducer handlers, and the exact-artifact verifier.

## The minimal workload

This is the complete `molwt-filter` workload (the built-in minimal example):
it filters molecules by exact RDKit molecular weight and needs only **one**
scientific hook, because the scaffold's default sharding and concatenation
already cover partitioning and reduction.

```python
from pathlib import Path
from typing import Any, Mapping

from scimesh.sdk import (
    ArtifactSchema,
    ComponentRef,
    MapReduceWorkload,
    PortSpec,
    SchemaRef,
    WorkloadId,
)
from scimesh.sdk.registry import WorkloadDefinition


class MolwtFilterWorkload(MapReduceWorkload):
    workload_id = WorkloadId("molwt-filter", "1.0.0")
    description = (
        "Filter molecules by exact RDKit molecular weight, one canonical "
        "CSV row per kept input molecule, in deterministic input order."
    )
    parameters_schema = {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "min_molwt": {"type": "number", "minimum": 0},
            "max_molwt": {"type": "number", "minimum": 0},
            "skip_invalid": {"type": "boolean", "default": True},
        },
    }
    input_port = PortSpec(ArtifactSchema(
        SchemaRef("molecule-table", 1),
        "text/tab-separated-values",
        "utf-8",
        max_bytes=10 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={"required_columns": ["canonical_smiles", "chembl_id"]},
        max_records=100_000_000,
        canonicalizer="scimesh-tsv-v1",
    ))
    partial_port = output_port = PortSpec(ArtifactSchema(
        SchemaRef("molwt-filtered-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={"columns": ["chembl_id", "canonical_smiles", "molwt"]},
        max_records=100_000_000,
        canonicalizer="molwt-filtered-table-v1",
    ))
    map_parameter_names = ("min_molwt", "max_molwt", "skip_invalid")

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        if parameters.get("min_molwt") is None and parameters.get("max_molwt") is None:
            raise ValueError("at least one of min_molwt or max_molwt is required")

    def compute_shard(self, inputs, parameters, output_path):
        # `inputs` maps every map-stage input port to a materialized file;
        # here there is one port: "input".
        return filter_molecules_by_molwt(
            inputs["input"],
            output_path,
            min_molwt=parameters.get("min_molwt"),
            max_molwt=parameters.get("max_molwt"),
            skip_invalid=parameters.get("skip_invalid", True),
        )
```

Because the scaffold provides default `partition_input` (row-bounded shards
that keep the header, `shard_rows` rows each) and default `reduce_partials`
(concatenation with one header), no further code is required.

## Required class attributes

| Attribute | Meaning |
| --- | --- |
| `workload_id` | `WorkloadId("name", "1.0.0")` — the immutable identity |
| `description` | Shown in `scimesh workload list`, the UI library, and the catalog export |
| `parameters_schema` | Strict JSON object schema (`additionalProperties: false`); the registry validates jobs against it before the planner runs |
| `input_port` | External input port (`PortSpec`) |
| `partial_port` | One map output artifact (`PortSpec`) |
| `output_port` | Final result artifact (`PortSpec`) |

## Optional class attributes

| Attribute | Default | Meaning |
| --- | --- | --- |
| `map_stage_inputs` | `{"input": input_port}` | Map-stage input ports; extra ports must share the external input schema |
| `map_parameter_names` | `()` | Parameter projection for map tasks |
| `reduce_parameter_names` | `map_parameter_names` | Parameter projection for the reducer |
| `capabilities` | `(workload_id.name,)` | Advertised capabilities |
| `trust_modes` | `(trusted, untrusted_quorum)` | Declared trust modes |
| `workflow_id` | `"<name>-map-reduce-v1"` | Workflow identity |
| `limits` | derived from port bounds | `WorkloadLimits` |
| `resources` / `execution` | CPU-1 core defaults | Per-task resource and execution profile |
| `shard_rows` | `1000` | Rows per shard for the default `partition_input` |
| `map_entry_point` / `reduce_entry_point` | derived from the module | Handler keys (can stay default) |

## Scientific hooks

Override only what your workload needs:

| Hook | Default | Purpose |
| --- | --- | --- |
| `domain_validate(parameters)` | none | Extra job-parameter validation (the JSON schema already ran) |
| `resolved_parameters(request)` | `dict(request.parameters)` | Values persisted into the plan |
| `resolved_parameters_for_plan(job, input_path, resolved)` | unchanged | Plan-time enrichment (e.g. one-time query resolution) |
| `partition_input(input_path, parameters, workspace)` | row-bounded sharding | Deterministic shard files, one per map task |
| `plan_tasks(shard_paths, resolved, job, negotiated, map_stage, context)` | one task per shard | Custom task construction |
| `task_parameters(resolved)` | filtered projection | Map-task parameters |
| `compute_shard(inputs, parameters, output_path)` | **required** | One map task; returns metrics |
| `parse_partial_key(key)` / `validate_partial_keys(parsed)` | `map.<8-digit>`, contiguous | Partial-key policy for the reducer |
| `reduce_partials(partial_paths, parameters, output_path)` | header-preserving concatenation | Deterministic merge |

Hooks must be **deterministic**: identical inputs and parameters must
produce byte-identical partials, in any worker, in any completion order.
Floats should be formatted with a fixed precision (for example `f"{v:.6f}"`),
and output row order must be canonical.

## Running a workload locally

```python
from scimesh.sdk import (
    ArtifactCollection,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    WorkloadRegistry,
)
from scimesh.workloads.library import default_sdk_runtime

workload = MolwtFilterWorkload(
    package_digest=current_scimesh_package_digest(),
    environment_digest=current_environment_digest(),
)
registry = WorkloadRegistry()
registry.register(workload.definition(), enabled=True)

store = LocalArtifactStore(Path("artifacts"))
artifact = store.import_file(
    Path("molecules.tsv"),
    declaration=workload.manifest.inputs["input"].schema,
)
request = JobRequest(
    workload=workload.manifest.workload,
    parameters={"min_molwt": 40.0},
    inputs={"input": ArtifactCollection.single(artifact)},
)
result = LocalCoreBatchExecutor(
    registry, default_sdk_runtime(), store, Path("work"),
).execute(request, workload.manifest.package.digest)

print(store.materialize(result.outputs["result"].items[0].artifact).read_text())
```

`LocalCoreBatchExecutor` runs the full pipeline — negotiation, planning,
map tasks, stage verification, reduce, final verification — in-process. It
is a correctness harness, not an isolation boundary: it accepts only
trusted, single-threaded, trusted-network profiles and rejects everything
else before a handler runs.

## Custom planning: block pairs

Workloads that need more than one input per task override `plan_tasks` and
`map_stage_inputs`. The built-in `similarity-graph` plans one task per block
pair `(i, j)` with `i <= j`:

```python
map_stage_inputs = {"left": block_port, "right": block_port}

def plan_tasks(self, shard_paths, resolved, job, negotiated, map_stage, context):
    block_refs = [
        context.sink.seal(path, declaration=self.input_port.schema)
        for path in shard_paths
    ]
    tasks = []
    for left in range(len(block_refs)):
        for right in range(left, len(block_refs)):
            tasks.append(self.task_spec(
                map_stage, job, negotiated,
                f"map/{left:04d}x{right:04d}",
                {"left_block": left, "right_block": right,
                 "threshold": resolved["threshold"]},
                {"left": ArtifactCollection.single(block_refs[left]),
                 "right": ArtifactCollection.single(block_refs[right])},
            ))
    return tasks
```

Its reducer overrides `parse_partial_key`/`validate_partial_keys` to parse
`map.<i>x<j>` keys and enforce the pair-coverage invariant (every unordered
molecule pair compared exactly once).

## Packaging and discovery

Workloads are installed as part of a Python distribution and declared as
entry points:

```toml
[project.entry-points."scimesh.workloads"]
"my-workload@1.0.0" = "my_package.workload:workload_definition"
```

The factory returns a `WorkloadDefinition` (or a `MapReduceWorkload`
instance with a `definition()` method). An administrator then supplies an
`AllowedPackage(distribution, WorkloadId, "sha256:...")` allowlist entry;
discovery loads the entry point only when the installed package content
matches the pinned digest.

```python
from scimesh.sdk import AllowedPackage, WorkloadId, WorkloadRegistry

registry = WorkloadRegistry()
registry.discover_installed((
    AllowedPackage("my-dist", WorkloadId("my-workload", "1.0.0"), "sha256:" + "a" * 64),
))
```

## Tests and golden parity

Add a regression test for every behavioral change:

- **Byte parity**: run the workload through `LocalCoreBatchExecutor` and
  compare the final artifact bytes with a single-process reference computed
  by the scientific core directly.
- **Determinism**: planning twice must produce identical JSON; results must
  be invariant to shard/block sizes.
- **Fail-closed**: invalid parameters, missing ports, forged outputs, and
  unsupported trust modes must be rejected.
- **Verifier policy**: for `untrusted_quorum`, two distinct owners with
  identical outputs must be accepted, conflicting outputs rejected.

Use small TSV fixtures — never the full ChEMBL extract, which takes minutes
even for one shard.

## Rules

1. Keep the scientific core callable without a coordinator.
2. Inline a strict JSON parameter schema (`additionalProperties: false`);
   the planner still performs domain validation.
3. Give every external and stage port an `ArtifactSchema` with bounds.
4. Return only sink-sealed artifacts in `OutputManifest`; the harness binds
   task key and provenance itself.
5. Select a verifier compatible with determinism and trust: v1 permits
   `untrusted_quorum` only for `byte_exact` plus `exact-artifact@1`.
6. Never put a filesystem path or transport URL into a plan or task.
