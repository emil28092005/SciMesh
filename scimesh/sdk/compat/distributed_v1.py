"""Compatibility adapter for the CTX-07 ``DistributedWorkload`` protocol."""

from __future__ import annotations

import hashlib
import shutil
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence

from scimesh.distributed.models import (
    ArtifactReference as LegacyArtifactReference,
    CompletedPartial,
    FinalResult,
)
from scimesh.distributed.workload import DistributedWorkload

from ..artifacts import (
    ArtifactCollection,
    ArtifactItem,
    ArtifactRef,
    Cardinality,
    CollectionKind,
    OutputManifest,
    PortSpec,
)
from ..execution import CheckpointPolicy, ExecutionProfile, NetworkPolicy, RetryPolicy
from ..identity import ComponentRef, SchemaRef, VersionRange, WorkloadId
from ..manifest import (
    DeterminismProfile,
    EnvironmentSpec,
    PackageSpec,
    TrustMode,
    VerifierSpec,
    WorkloadLimits,
    WorkloadManifest,
)
from ..plans import JobRequest, TaskSpec, ValidatedJob, WorkflowPlan
from ..protocols import PlanningContext, ReduceContext, TaskContext
from ..registry import WorkloadDefinition
from ..resources import ResourceRequirements
from ..verification import ExactArtifactVerifier
from ..workflow import ArtifactEdge, PortRef, StageKind, StageSpec, WorkflowSpec


ShardRunner = Callable[[Path, Mapping[str, object], Path], Mapping[str, int | float]]


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


class LegacyDistributedWorkloadAdapter:
    """Expose a legacy map/reduce workload through the SDK core-batch profile.

    The adapter preserves the old wire schema. Local files are materialized and
    sealed only through bridge-owned contexts, and no path is included in a
    ``TaskSpec`` or ``WorkflowPlan``.
    """

    MAP_ENTRY_POINT = "scimesh.sdk.compat.distributed_v1:run_legacy@v1"
    REDUCE_ENTRY_POINT = "scimesh.sdk.compat.distributed_v1:reduce_legacy@v1"

    def __init__(
        self,
        workload: DistributedWorkload,
        shard_runner: ShardRunner,
        *,
        version: str,
        package_digest: str,
        environment_digest: str,
        parameters_schema: Mapping[str, Any],
        input_port: PortSpec,
        partial_port: PortSpec,
        output_port: PortSpec,
        resolved_parameter_names: Sequence[str] = (),
        shard_rows: int = 10_000,
        resources: ResourceRequirements | None = None,
        execution: ExecutionProfile | None = None,
        limits: WorkloadLimits | None = None,
    ) -> None:
        if not isinstance(workload.name, str) or not isinstance(workload.description, str):
            raise ValueError("legacy workload must expose name and description")
        if not callable(shard_runner):
            raise ValueError("shard_runner must be callable")
        if isinstance(shard_rows, bool) or not isinstance(shard_rows, int) or shard_rows < 1:
            raise ValueError("shard_rows must be a positive integer")
        self.workload = workload
        self.shard_runner = shard_runner
        self.shard_rows = shard_rows
        self.input_port = input_port
        self.partial_port = partial_port
        self.output_port = output_port
        resources = resources or ResourceRequirements(
            profile="legacy-cpu-v1",
            cpu_cores=1,
            memory_mb=1024,
            scratch_mb=1024,
            max_duration_seconds=3600,
        )
        execution = execution or ExecutionProfile(
            profile="legacy-python-process-v1",
            network=NetworkPolicy.TRUSTED,
            timeout_seconds=3600,
            checkpoint=CheckpointPolicy(),
        )
        limits = limits or WorkloadLimits(
            max_input_bytes=input_port.schema.max_bytes,
            max_tasks=10_000,
            max_output_bytes=output_port.schema.max_bytes,
        )
        parameter_names = tuple(sorted(parameters_schema.get("properties", {})))
        reduce_parameter_names = tuple(sorted(set(parameter_names).union(resolved_parameter_names)))
        map_stage = StageSpec(
            stage_id="map",
            kind=StageKind.MAP,
            entry_point=self.MAP_ENTRY_POINT,
            needs=(),
            inputs={"input": input_port},
            outputs={"partial": partial_port},
            parameter_names=parameter_names,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
            trust_modes=("trusted",),
            max_fan_out=limits.max_tasks,
            cacheable=True,
        )
        reduce_input = PortSpec(
            schema=partial_port.schema,
            cardinality=Cardinality.MANY,
            collection=CollectionKind.KEYED,
        )
        reduce_stage = StageSpec(
            stage_id="reduce",
            kind=StageKind.REDUCE,
            entry_point=self.REDUCE_ENTRY_POINT,
            needs=("map",),
            inputs={"partials": reduce_input},
            outputs={"result": output_port},
            parameter_names=reduce_parameter_names,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
            trust_modes=("trusted",),
            cacheable=True,
        )
        workflow = WorkflowSpec(
            workflow_id="map-reduce-v1",
            inputs={"input": input_port},
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
            workload=WorkloadId(workload.name, version),
            description=workload.description,
            package=PackageSpec("scimesh", package_digest),
            environment=EnvironmentSpec("python-process", environment_digest, {"adapter": "distributed-v1"}),
            parameters_schema=parameters_schema,
            workflow=workflow,
            inputs={"input": input_port},
            outputs={"result": output_port},
            determinism=DeterminismProfile.BYTE_EXACT,
            trust_modes=(TrustMode.TRUSTED,),
            verifier=VerifierSpec(ComponentRef("exact-artifact", 1), {}),
            limits=limits,
            capabilities=(workload.name,),
            conformance_profiles=("core-batch-v1",),
        )
        self._exact_verifier = ExactArtifactVerifier()

    def definition(self) -> WorkloadDefinition:
        return WorkloadDefinition(
            manifest=self.manifest,
            planner=self,
            runners={self.MAP_ENTRY_POINT: self},
            reducers={self.REDUCE_ENTRY_POINT: self},
            verifiers={self._exact_verifier.identity.canonical: self._exact_verifier},
        )

    def validate(self, request: JobRequest) -> ValidatedJob:
        if request.workload != self.manifest.workload:
            raise ValueError("legacy adapter received a request for another workload")
        self.workload.validate_job(request.parameters)
        return ValidatedJob(request, request.parameters)

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan:
        if not isinstance(job, ValidatedJob):
            raise ValueError("job must be a ValidatedJob")
        collection = job.request.inputs.get("input")
        if collection is None:
            raise ValueError("legacy adapter requires the input port")
        self.input_port.validate_collection(collection, "job input")
        input_artifact = collection.items[0].artifact
        input_path = context.catalog.materialize(input_artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        legacy = self.workload.plan(
            input_path,
            input_artifact.artifact_id,
            job.request.parameters,
            self.shard_rows,
            workspace,
        )
        if legacy.workload != self.workload.name:
            raise ValueError("legacy planner returned a plan for another workload")
        tasks: list[TaskSpec] = []
        negotiated = context.negotiated
        map_stage = self.manifest.workflow.stages[0]
        used_paths: set[Path] = set()
        for planned in legacy.tasks:
            path = self._find_planned_file(workspace, planned.input_artifact.sha256, used_paths)
            sealed = context.sink.seal(
                path,
                declaration=self.input_port.schema,
            )
            if sealed.sha256 != planned.input_artifact.sha256:
                raise ValueError("artifact sink returned a checksum that differs from the legacy plan")
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
                    task_key=f"map/{planned.chunk_index:08d}",
                    stage_id="map",
                    parameters=planned.parameters,
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
            resolved_parameters=legacy.resolved_parameters,
            tasks=tuple(tasks),
        )

    @staticmethod
    def _find_planned_file(workspace: Path, expected_sha256: str, used: set[Path]) -> Path:
        for candidate in sorted(workspace.rglob("*")):
            if candidate in used or not candidate.is_file() or candidate.is_symlink():
                continue
            if _sha256_file(candidate) == expected_sha256:
                used.add(candidate)
                return candidate
        raise ValueError("legacy planner did not materialize its planned artifact")

    def run(self, context: TaskContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.task.inputs.get("input")
        if collection is None:
            raise ValueError("legacy map task requires one input collection")
        self.input_port.validate_collection(collection, "legacy map input")
        source = context.catalog.materialize(collection.items[0].artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        input_path = workspace / "input"
        output_path = workspace / "result"
        if source.resolve() != input_path.resolve():
            shutil.copyfile(source, input_path)
        metrics = self.shard_runner(input_path, context.task.parameters, output_path)
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
        ).validate_against(context.task.expected_outputs, max_output_bytes=self.manifest.limits.max_output_bytes)

    def reduce(self, context: ReduceContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.accepted_inputs.get("partials")
        if collection is None or collection.kind is not CollectionKind.KEYED or not collection.items:
            raise ValueError("legacy reducer requires a non-empty keyed partial collection")
        self.manifest.workflow.stages[1].inputs["partials"].validate_collection(
            collection,
            "legacy reducer partials",
        )
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        partials: list[CompletedPartial] = []
        indexed_items: list[tuple[int, ArtifactItem]] = []
        for item in collection.items:
            key = item.key or ""
            prefix = "map."
            raw_index = key[len(prefix):] if key.startswith(prefix) else ""
            if len(raw_index) != 8 or not raw_index.isdigit():
                raise ValueError("legacy partial key must use map.<eight-digit-index>")
            indexed_items.append((int(raw_index), item))
        indices = [index for index, _ in indexed_items]
        expected_keys = context.task.expected_input_keys.get("partials")
        if expected_keys is None or {item.key for item in collection.items} != set(expected_keys):
            raise ValueError("legacy partial keys do not match the coordinator expected set")
        if sorted(indices) != list(range(len(indexed_items))):
            raise ValueError("legacy partial keys must be complete and contiguous")
        for index, item in sorted(indexed_items):
            artifact = item.artifact
            source = context.catalog.materialize(artifact)
            target = workspace / artifact.artifact_id
            if source.resolve() != target.resolve():
                shutil.copyfile(source, target)
            if _sha256_file(target) != artifact.sha256:
                raise ValueError("materialized partial checksum does not match")
            partials.append(
                CompletedPartial(
                    index,
                    LegacyArtifactReference(
                        artifact.artifact_id,
                        artifact.sha256,
                        artifact.media_type,
                    ),
                    {},
                )
            )
        result = self.workload.reduce(partials, context.task.parameters, workspace)
        if not isinstance(result, FinalResult):
            raise ValueError("legacy reducer must return a FinalResult")
        path = self._find_planned_file(workspace, result.artifact.sha256, set())
        sealed = context.sink.seal(
            path,
            declaration=self.output_port.schema,
        )
        if sealed.sha256 != result.artifact.sha256:
            raise ValueError("artifact sink returned a checksum that differs from the legacy result")
        return OutputManifest(
            context.task.task_key,
            {"result": ArtifactCollection.single(sealed)},
            result.metrics,
            context.provenance,
        ).validate_against(context.task.expected_outputs, max_output_bytes=self.manifest.limits.max_output_bytes)
