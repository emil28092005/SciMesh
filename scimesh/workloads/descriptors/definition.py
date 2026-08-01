"""SDK-built ``descriptor-batch`` workload definition and handlers.

This module is the first non-adapter reference workload built directly on the
``core-batch-v1`` profile: an explicit immutable manifest, a static map/reduce
workflow, a row-bounded planner, pinned descriptor computation, deterministic
shard concatenation, and the exact-artifact verifier. It is the intended
first ``untrusted_quorum`` candidate: ``byte_exact`` determinism with whole
file SHA-256 agreement from distinct owners.
"""

from __future__ import annotations

import hashlib
import shutil
from pathlib import Path
from typing import Any, Mapping, Sequence

from ...sdk.artifacts import (
    ArtifactCollection,
    ArtifactItem,
    ArtifactRef,
    ArtifactSchema,
    Cardinality,
    CollectionKind,
    OutputManifest,
    PortSpec,
)
from ..environment import current_environment_digest, current_scimesh_package_digest
from ...sdk.execution import (
    CheckpointPolicy,
    ExecutionProfile,
    NetworkPolicy,
    RetryPolicy,
)
from ...sdk.identity import ComponentRef, SchemaRef, VersionRange, WorkloadId
from ...sdk.manifest import (
    DeterminismProfile,
    EnvironmentSpec,
    PackageSpec,
    TrustMode,
    VerifierSpec,
    WorkloadLimits,
    WorkloadManifest,
)
from ...sdk.plans import JobRequest, TaskSpec, ValidatedJob, WorkflowPlan
from ...sdk.protocols import PlanningContext, ReduceContext, TaskContext
from ...sdk.registry import WorkloadDefinition
from ...sdk.resources import ResourceRequirements
from ...sdk.verification import ExactArtifactVerifier
from ...sdk.workflow import ArtifactEdge, PortRef, StageKind, StageSpec, WorkflowSpec
from .core import (
    DESCRIPTOR_COLUMNS,
    compute_descriptor_batch,
    concatenate_descriptor_shards,
    validate_descriptor_names,
    write_descriptor_shards,
)

MAP_ENTRY_POINT = "scimesh.workloads.descriptors.definition:map_descriptors@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.descriptors.definition:reduce_descriptors@v1"

_DESCRIPTOR_PARAMETERS = ("skip_invalid",)


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _parameters_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "skip_invalid": {
                "type": "boolean",
                "default": True,
                "description": "Skip rows with invalid SMILES instead of failing",
            },
        },
    }


def _input_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("molecule-table", 1),
        "text/tab-separated-values",
        "utf-8",
        max_bytes=10 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "required_columns": ["canonical_smiles", "chembl_id"],
        },
        max_records=100_000_000,
        canonicalizer="scimesh-tsv-v1",
    )


def _descriptor_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("descriptor-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": list(DESCRIPTOR_COLUMNS),
        },
        max_records=100_000_000,
        canonicalizer="descriptor-table-v1",
    )


class DescriptorBatchWorkload:
    """Manifest-backed planner, runner, and reducer for descriptor-batch.

    The class follows the legacy adapter's structural pattern (one object
    registered under each stage entry point) while remaining fully SDK-built:
    sharding is explicit and deterministic, every artifact is sealed through
    the bridge-owned sink, and no filesystem path ever enters a plan or task.
    """

    def __init__(
        self,
        *,
        shard_rows: int,
        package_digest: str,
        environment_digest: str,
    ) -> None:
        if (
            isinstance(shard_rows, bool)
            or not isinstance(shard_rows, int)
            or shard_rows < 1
        ):
            raise ValueError("shard_rows must be a positive integer")
        validate_descriptor_names()
        self.entry_point = MAP_ENTRY_POINT
        self.shard_rows = shard_rows
        self.input_port = PortSpec(_input_schema())
        self.partial_port = PortSpec(_descriptor_schema())
        self.output_port = PortSpec(_descriptor_schema())
        resources = ResourceRequirements(
            profile="descriptor-cpu-v1",
            cpu_cores=1,
            memory_mb=1024,
            scratch_mb=1024,
            max_duration_seconds=3600,
        )
        execution = ExecutionProfile(
            profile="descriptor-python-process-v1",
            network=NetworkPolicy.TRUSTED,
            timeout_seconds=3600,
            checkpoint=CheckpointPolicy(),
        )
        limits = WorkloadLimits(
            max_input_bytes=self.input_port.schema.max_bytes,
            max_tasks=10_000,
            max_output_bytes=self.output_port.schema.max_bytes,
        )
        trust_modes = ("trusted", "untrusted_quorum")
        map_stage = StageSpec(
            stage_id="map",
            kind=StageKind.MAP,
            entry_point=MAP_ENTRY_POINT,
            needs=(),
            inputs={"input": self.input_port},
            outputs={"partial": self.partial_port},
            parameter_names=_DESCRIPTOR_PARAMETERS,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
            trust_modes=trust_modes,
            max_fan_out=limits.max_tasks,
            cacheable=True,
        )
        reduce_input = PortSpec(
            schema=self.partial_port.schema,
            cardinality=Cardinality.MANY,
            collection=CollectionKind.KEYED,
        )
        reduce_stage = StageSpec(
            stage_id="reduce",
            kind=StageKind.REDUCE,
            entry_point=REDUCE_ENTRY_POINT,
            needs=("map",),
            inputs={"partials": reduce_input},
            outputs={"result": self.output_port},
            parameter_names=_DESCRIPTOR_PARAMETERS,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
            trust_modes=trust_modes,
            max_fan_out=1,
            cacheable=True,
        )
        workflow = WorkflowSpec(
            workflow_id="descriptor-map-reduce-v1",
            inputs={"input": self.input_port},
            stages=(map_stage, reduce_stage),
            edges=(
                ArtifactEdge(PortRef("input"), PortRef("input", "map")),
                ArtifactEdge(PortRef("partial", "map"), PortRef("partials", "reduce")),
            ),
            outputs={"result": PortRef("result", "reduce")},
            max_tasks=limits.max_tasks,
            max_output_bytes=limits.max_output_bytes,
        )
        self.manifest = WorkloadManifest(
            sdk_api=VersionRange(">=1.0,<2.0"),
            protocol=VersionRange(">=1,<2"),
            workload=WorkloadId("descriptor-batch", "1.0.0"),
            description=(
                "Compute a pinned set of RDKit 2D descriptors, one canonical "
                "CSV row per input molecule, in deterministic input order."
            ),
            package=PackageSpec("scimesh", package_digest),
            environment=EnvironmentSpec(
                "python-process",
                environment_digest,
                {"adapter": "sdk-native"},
            ),
            parameters_schema=_parameters_schema(),
            workflow=workflow,
            inputs={"input": self.input_port},
            outputs={"result": self.output_port},
            determinism=DeterminismProfile.BYTE_EXACT,
            trust_modes=(TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM),
            verifier=VerifierSpec(ComponentRef("exact-artifact", 1), {}),
            limits=limits,
            capabilities=("descriptor-batch",),
            conformance_profiles=("core-batch-v1",),
        )
        self._exact_verifier = ExactArtifactVerifier()

    def definition(self) -> WorkloadDefinition:
        return WorkloadDefinition(
            manifest=self.manifest,
            planner=self,
            runners={MAP_ENTRY_POINT: self},
            reducers={REDUCE_ENTRY_POINT: self},
            verifiers={self._exact_verifier.identity.canonical: self._exact_verifier},
        )

    @staticmethod
    def _skip_invalid(parameters: Mapping[str, Any]) -> bool:
        value = parameters.get("skip_invalid", True)
        if not isinstance(value, bool):
            raise ValueError("skip_invalid must be a boolean")
        return value

    def validate(self, request: JobRequest) -> ValidatedJob:
        if request.workload != self.manifest.workload:
            raise ValueError("descriptor-batch received a request for another workload")
        self._skip_invalid(request.parameters)
        return ValidatedJob(request, request.parameters)

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan:
        if not isinstance(job, ValidatedJob):
            raise ValueError("job must be a ValidatedJob")
        collection = job.request.inputs.get("input")
        if collection is None:
            raise ValueError("descriptor-batch requires the input port")
        self.input_port.validate_collection(collection, "job input")
        input_artifact = collection.items[0].artifact
        input_path = context.catalog.materialize(input_artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        shard_paths = write_descriptor_shards(
            input_path,
            workspace,
            self.shard_rows,
        )
        negotiated = context.negotiated
        map_stage = self.manifest.workflow.stages[0]
        assert map_stage.verifier is not None
        tasks: list[TaskSpec] = []
        for index, path in enumerate(shard_paths):
            sealed = context.sink.seal(
                path,
                declaration=self.input_port.schema,
            )
            tasks.append(
                TaskSpec(
                    workload=self.manifest.workload,
                    package_digest=self.manifest.package.digest,
                    manifest_digest=self.manifest.digest,
                    trust_mode=job.request.trust_mode,
                    sdk_api_version=negotiated.sdk_api_version,
                    protocol_version=negotiated.protocol_version,
                    manifest_schema_version=self.manifest.manifest_schema_version,
                    workflow_schema_version=self.manifest.workflow.schema_version,
                    environment_digest=self.manifest.environment.digest,
                    verifier=map_stage.verifier,
                    selected_features=negotiated.selected_features,
                    optional_fallbacks=negotiated.optional_fallbacks,
                    task_key=f"map/{index:08d}",
                    stage_id="map",
                    parameters=job.resolved_parameters,
                    inputs={"input": ArtifactCollection.single(sealed)},
                    expected_outputs={"partial": self.partial_port},
                    resources=map_stage.resources,
                    execution=map_stage.execution,
                )
            )
        return WorkflowPlan(
            workload=self.manifest.workload,
            package_digest=self.manifest.package.digest,
            manifest_digest=self.manifest.digest,
            trust_mode=job.request.trust_mode,
            sdk_api_version=negotiated.sdk_api_version,
            protocol_version=negotiated.protocol_version,
            manifest_schema_version=self.manifest.manifest_schema_version,
            workflow_schema_version=self.manifest.workflow.schema_version,
            environment_digest=self.manifest.environment.digest,
            verifier=self.manifest.verifier.verifier,
            selected_features=negotiated.selected_features,
            optional_fallbacks=negotiated.optional_fallbacks,
            workflow_id=self.manifest.workflow.workflow_id,
            resolved_parameters=job.resolved_parameters,
            tasks=tuple(tasks),
        )

    def run(self, context: TaskContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.task.inputs.get("input")
        if collection is None:
            raise ValueError("descriptor map task requires one input collection")
        self.input_port.validate_collection(collection, "descriptor map input")
        source = context.catalog.materialize(collection.items[0].artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        input_path = workspace / "input"
        output_path = workspace / "result.csv"
        if source.resolve() != input_path.resolve():
            shutil.copyfile(source, input_path)
        metrics = compute_descriptor_batch(
            input_path,
            output_path,
            skip_invalid=self._skip_invalid(context.task.parameters),
        )
        context.cancellation.raise_if_cancelled()
        sealed = context.sink.seal(
            output_path,
            declaration=self.partial_port.schema,
        )
        return OutputManifest(
            context.task.task_key,
            {"partial": ArtifactCollection.single(sealed)},
            metrics,
            context.provenance,
        ).validate_against(
            context.task.expected_outputs,
            max_output_bytes=self.manifest.limits.max_output_bytes,
        )

    def reduce(self, context: ReduceContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.accepted_inputs.get("partials")
        if (
            collection is None
            or collection.kind is not CollectionKind.KEYED
            or not collection.items
        ):
            raise ValueError(
                "descriptor reducer requires a non-empty keyed partial collection"
            )
        self.manifest.workflow.stages[1].inputs["partials"].validate_collection(
            collection,
            "descriptor reducer partials",
        )
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        indexed_items: list[tuple[int, ArtifactItem]] = []
        for item in collection.items:
            key = item.key or ""
            prefix = "map."
            raw_index = key[len(prefix) :] if key.startswith(prefix) else ""
            if len(raw_index) != 8 or not raw_index.isdigit():
                raise ValueError(
                    "descriptor partial key must use map.<eight-digit-index>"
                )
            indexed_items.append((int(raw_index), item))
        expected_keys = context.task.expected_input_keys.get("partials")
        if expected_keys is None or {item.key for item in collection.items} != set(
            expected_keys
        ):
            raise ValueError(
                "descriptor partial keys do not match the coordinator expected set"
            )
        if sorted(index for index, _ in indexed_items) != list(
            range(len(indexed_items))
        ):
            raise ValueError("descriptor partial keys must be complete and contiguous")
        partial_paths: list[Path] = []
        for index, item in sorted(indexed_items):
            artifact: ArtifactRef = item.artifact
            source = context.catalog.materialize(artifact)
            target = workspace / artifact.artifact_id
            if source.resolve() != target.resolve():
                shutil.copyfile(source, target)
            if _sha256_file(target) != artifact.sha256:
                raise ValueError("materialized partial checksum does not match")
            partial_paths.append(target)
        result_path = workspace / "result.csv"
        metrics = concatenate_descriptor_shards(partial_paths, result_path)
        context.cancellation.raise_if_cancelled()
        sealed = context.sink.seal(
            result_path,
            declaration=self.output_port.schema,
        )
        return OutputManifest(
            context.task.task_key,
            {"result": ArtifactCollection.single(sealed)},
            metrics,
            context.provenance,
        ).validate_against(
            context.task.expected_outputs,
            max_output_bytes=self.manifest.limits.max_output_bytes,
        )


def descriptor_batch_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> DescriptorBatchWorkload:
    """Build the default local descriptor-batch definition for tests."""
    return DescriptorBatchWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the default descriptor-batch definition."""
    return descriptor_batch_sdk_definition().definition()
