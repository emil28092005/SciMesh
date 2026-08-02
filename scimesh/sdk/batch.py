"""High-level core-batch-v1 authoring scaffold for map/reduce workloads.

``MapReduceWorkload`` is the SDK's primary authoring surface for the
``core-batch-v1`` profile: a subclass declares its identity, parameter
schema, artifact ports, and three scientific hooks (partitioning, per-shard
computation, partial merging), and the base class assembles the immutable
manifest, the map/reduce stages, the workflow DAG, the digest-pinned
planner/runner/reducer handlers, and the exact-artifact verifier.

The base class deliberately supports only the static byte-exact map/reduce
shape with a single external input and one output port. Workloads that need
a different DAG (for example block-pair tasks with two map inputs) override
the ``map_stage_inputs``, ``plan_tasks``, ``parse_partial_key``, and
``validate_partial_keys`` hooks; anything outside the model must use the
lower-level SDK value objects directly.
"""

from __future__ import annotations

import hashlib
import shutil
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

from .artifacts import (
    ArtifactCollection,
    ArtifactRef,
    Cardinality,
    CollectionKind,
    OutputManifest,
    PortSpec,
)
from .execution import (
    CheckpointPolicy,
    ExecutionProfile,
    NetworkPolicy,
    RetryPolicy,
)
from .identity import ComponentRef, VersionRange, WorkloadId
from .manifest import (
    DeterminismProfile,
    EnvironmentSpec,
    PackageSpec,
    TrustMode,
    VerifierSpec,
    WorkloadLimits,
    WorkloadManifest,
)
from .plans import JobRequest, TaskSpec, ValidatedJob, WorkflowPlan
from .protocols import PlanningContext, ReduceContext, TaskContext
from .registry import WorkloadDefinition
from .resources import ResourceRequirements
from .ui import UIElement
from .verification import ExactArtifactVerifier
from .workflow import ArtifactEdge, PortRef, StageKind, StageSpec, WorkflowSpec

_EXACT_ARTIFACT = ComponentRef("exact-artifact", 1)
_EXACT_VERIFIER = ExactArtifactVerifier()


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _default_entry_point(module: str, kind: str) -> str:
    return f"{module}:{kind}@v1"


def concatenate_partial_tables(
    partial_paths: Sequence[Path],
    output_path: Path,
) -> dict[str, int]:
    """Concatenate partial CSV/TSV tables with exactly one shared header.

    Every partial must start with the same header line; the first partial is
    copied verbatim and later partials contribute only their data rows, so the
    merged file is byte-identical to a single-process run over the same rows.
    """
    if not partial_paths:
        raise ValueError("reducer requires at least one partial")
    output_path.parent.mkdir(parents=True, exist_ok=True)
    header: str | None = None
    rows_emitted = 0
    with output_path.open("w", encoding="utf-8", newline="") as destination:
        for index, partial in enumerate(partial_paths):
            with partial.open("r", encoding="utf-8", newline="") as source:
                for line_index, line in enumerate(source):
                    if line_index == 0:
                        if header is None:
                            header = line
                            destination.write(line)
                        elif line != header:
                            raise ValueError("partial tables have inconsistent headers")
                        continue
                    destination.write(line)
                    rows_emitted += 1
    return {"partial_count": len(partial_paths), "rows_emitted": rows_emitted}


class MapReduceWorkload:
    """Base class for static byte-exact map/reduce workloads.

    Required class attributes:

    - ``workload_id``: ``WorkloadId`` identity (name + version);
    - ``parameters_schema``: strict JSON object schema (the registry validates
      ``additionalProperties: false`` for the top level);
    - ``input_port``, ``partial_port``, ``output_port``: typed ``PortSpec``
      values for the external input, one map partial, and the final result.

    Scientific hooks (override as needed):

    - ``domain_validate(parameters)``: extra job-parameter validation;
    - ``resolved_parameters(request)``: values carried into the plan;
    - ``partition_input(input_path, parameters, workspace)``: deterministic
      shard splitting; returns one TSV/CSV file per map task. The default
      splits a delimited table into ``shard_rows``-bounded shards that keep
      the header, using the input schema's media type to pick the delimiter;
    - ``plan_tasks(shard_paths, resolved, job, negotiated, map_stage, context)``:
      task construction (default: one task per shard, ``map/<index>``);
    - ``compute_shard(inputs, parameters, output_path)``: one map task;
      ``inputs`` maps every ``map_stage_inputs`` port to a materialized file;
    - ``parse_partial_key(key)`` and ``validate_partial_keys(parsed)``:
      partial-key policy for the reducer;
    - ``reduce_partials(partial_paths, parameters, output_path)``: the
      deterministic merge. The default concatenates partial tables with one
      header and counts the emitted data rows.

    Optional class attributes: ``map_stage_inputs`` (default one ``input``
    port), ``map_parameter_names``, ``reduce_parameter_names``, ``capabilities``,
    ``trust_modes``, ``workflow_id``, ``limits``, ``resources``, ``execution``,
    ``map_entry_point``, ``reduce_entry_point``, ``shard_rows`` (default 1000,
    used only by the default ``partition_input``), ``ui_elements`` (tuple of
    ``UIElement`` declarations that shape the operator "new job" form in the
    coordinator UI; each ``field`` must name a ``parameters_schema`` property).
    """

    workload_id: WorkloadId
    description: str = ""
    parameters_schema: Mapping[str, Any] = {}
    input_port: PortSpec
    partial_port: PortSpec
    output_port: PortSpec

    map_stage_inputs: Mapping[str, PortSpec] | None = None
    map_parameter_names: tuple[str, ...] = ()
    reduce_parameter_names: tuple[str, ...] = ()
    capabilities: tuple[str, ...] | None = None
    trust_modes: tuple[TrustMode, ...] = (TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM)
    workflow_id: str | None = None
    limits: WorkloadLimits | None = None
    resources: ResourceRequirements | None = None
    execution: ExecutionProfile | None = None
    map_entry_point: str | None = None
    reduce_entry_point: str | None = None
    shard_rows: int = 1_000
    ui_elements: tuple[UIElement, ...] = ()
    reduction: str = "ordered-concat"
    upload_ready: bool = True

    def __init__(
        self,
        *,
        package_digest: str,
        environment_digest: str,
    ) -> None:
        workload_id = self.workload_id
        if not isinstance(workload_id, WorkloadId):
            raise ValueError("workload_id must be a WorkloadId")
        if not isinstance(self.input_port, PortSpec):
            raise ValueError("input_port must be a PortSpec")
        if not isinstance(self.partial_port, PortSpec):
            raise ValueError("partial_port must be a PortSpec")
        if not isinstance(self.output_port, PortSpec):
            raise ValueError("output_port must be a PortSpec")
        if not self.parameters_schema:
            raise ValueError("parameters_schema must be provided")
        module = type(self).__module__
        self.map_entry_point = self.map_entry_point or _default_entry_point(
            module, "map"
        )
        self.reduce_entry_point = self.reduce_entry_point or _default_entry_point(
            module, "reduce"
        )
        self.entry_point = self.map_entry_point
        self.map_stage_inputs = dict(
            self.map_stage_inputs or {"input": self.input_port}
        )
        if set(self.map_stage_inputs) != {"input"} and any(
            port.schema != self.input_port.schema
            for port in self.map_stage_inputs.values()
        ):
            raise ValueError(
                "additional map inputs must share the external input schema"
            )
        self.map_parameter_names = tuple(self.map_parameter_names)
        self.reduce_parameter_names = tuple(
            self.reduce_parameter_names or self.map_parameter_names
        )
        self.capabilities = tuple(self.capabilities or (workload_id.name,))
        if workload_id.name not in self.capabilities:
            raise ValueError("capabilities must include the workload name")
        name = workload_id.name
        resources = self.resources or ResourceRequirements(
            profile=f"{name}-cpu-v1",
            cpu_cores=1,
            memory_mb=1024,
            scratch_mb=1024,
            max_duration_seconds=3600,
        )
        execution = self.execution or ExecutionProfile(
            profile=f"{name}-python-process-v1",
            network=NetworkPolicy.TRUSTED,
            timeout_seconds=3600,
            checkpoint=CheckpointPolicy(),
        )
        limits = self.limits or WorkloadLimits(
            max_input_bytes=self.input_port.schema.max_bytes,
            max_tasks=10_000,
            max_output_bytes=self.output_port.schema.max_bytes,
        )
        trust_values = tuple(mode.value for mode in self.trust_modes)
        map_stage = StageSpec(
            stage_id="map",
            kind=StageKind.MAP,
            entry_point=self.map_entry_point,
            needs=(),
            inputs=self.map_stage_inputs,
            outputs={"partial": self.partial_port},
            parameter_names=self.map_parameter_names,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=_EXACT_ARTIFACT,
            trust_modes=trust_values,
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
            entry_point=self.reduce_entry_point,
            needs=("map",),
            inputs={"partials": reduce_input},
            outputs={"result": self.output_port},
            parameter_names=self.reduce_parameter_names,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=_EXACT_ARTIFACT,
            trust_modes=trust_values,
            max_fan_out=1,
            cacheable=True,
        )
        edges = [
            ArtifactEdge(PortRef("input"), PortRef(port, "map"))
            for port in self.map_stage_inputs
        ] + [
            ArtifactEdge(PortRef("partial", "map"), PortRef("partials", "reduce")),
        ]
        workflow = WorkflowSpec(
            workflow_id=self.workflow_id or f"{name}-map-reduce-v1",
            inputs={"input": self.input_port},
            stages=(map_stage, reduce_stage),
            edges=tuple(edges),
            outputs={"result": PortRef("result", "reduce")},
            max_tasks=limits.max_tasks,
            max_output_bytes=limits.max_output_bytes,
        )
        self.manifest = WorkloadManifest(
            sdk_api=VersionRange(">=1.0,<2.0"),
            protocol=VersionRange(">=1,<2"),
            workload=workload_id,
            description=self.description,
            package=PackageSpec("scimesh", package_digest),
            environment=EnvironmentSpec(
                "python-process",
                environment_digest,
                {"adapter": "sdk-native"},
            ),
            parameters_schema=self.parameters_schema,
            workflow=workflow,
            inputs={"input": self.input_port},
            outputs={"result": self.output_port},
            determinism=DeterminismProfile.BYTE_EXACT,
            trust_modes=self.trust_modes,
            verifier=VerifierSpec(_EXACT_ARTIFACT, {}),
            limits=limits,
            capabilities=self.capabilities,
            conformance_profiles=("core-batch-v1",),
            ui_elements=tuple(self.ui_elements),
            reduction=self.reduction,
            upload_ready=self.upload_ready,
        )
        self._exact_verifier = _EXACT_VERIFIER
        self._limits = limits

    # ------------------------------------------------------------------
    # Public assembly
    # ------------------------------------------------------------------

    def definition(self) -> WorkloadDefinition:
        map_entry_point = self.map_entry_point
        reduce_entry_point = self.reduce_entry_point
        assert map_entry_point is not None and reduce_entry_point is not None
        return WorkloadDefinition(
            manifest=self.manifest,
            planner=self,
            runners={map_entry_point: self},
            reducers={reduce_entry_point: self},
            verifiers={_EXACT_ARTIFACT.canonical: self._exact_verifier},
        )

    # ------------------------------------------------------------------
    # Scientific hooks
    # ------------------------------------------------------------------

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        """Extra job-parameter validation beyond the JSON schema."""

    def resolved_parameters(self, request: JobRequest) -> dict[str, Any]:
        """Values persisted as the plan's resolved parameters."""
        return dict(request.parameters)

    def partition_input(
        self,
        input_path: Path,
        parameters: Mapping[str, Any],
        workspace: Path,
    ) -> list[Path]:
        """Split the materialized input into deterministic shard files.

        The default implementation shards a delimited table by rows: every
        shard keeps the header and holds at most ``self.shard_rows`` data
        rows, in input order. The delimiter follows the input schema's media
        type. Workloads that partition differently (block pairs, sampling)
        override this hook.
        """
        import csv

        if (
            isinstance(self.shard_rows, bool)
            or not isinstance(self.shard_rows, int)
            or self.shard_rows < 1
        ):
            raise ValueError("shard_rows must be a positive integer")
        media_type = self.input_port.schema.media_type
        if media_type == "text/tab-separated-values":
            delimiter = "\t"
        elif media_type == "text/csv":
            delimiter = ","
        else:
            raise ValueError(
                "default sharding requires a delimited input media type: " + media_type
            )
        workspace.mkdir(parents=True, exist_ok=True)
        paths: list[Path] = []
        destination = None
        writer = None
        rows_in_shard = 0
        try:
            with input_path.open("r", encoding="utf-8", newline="") as source:
                reader = csv.DictReader(source, delimiter=delimiter)
                fieldnames = tuple(reader.fieldnames or ())
                if not fieldnames:
                    raise ValueError("dataset has no header row")
                for row in reader:
                    if destination is None or rows_in_shard == self.shard_rows:
                        if destination is not None:
                            destination.close()
                        current = workspace / f"shard-{len(paths)}.tsv"
                        destination = current.open("w", encoding="utf-8", newline="")
                        writer = csv.DictWriter(
                            destination,
                            fieldnames=list(fieldnames),
                            delimiter=delimiter,
                            lineterminator="\n",
                        )
                        writer.writeheader()
                        paths.append(current)
                        rows_in_shard = 0
                    assert writer is not None
                    writer.writerow(row)
                    rows_in_shard += 1
        finally:
            if destination is not None:
                destination.close()
        if not paths:
            raise ValueError("dataset has no data rows")
        return paths

    def plan_tasks(
        self,
        shard_paths: Sequence[Path],
        resolved: Mapping[str, Any],
        job: ValidatedJob,
        negotiated: Any,
        map_stage: StageSpec,
        context: PlanningContext,
    ) -> list[TaskSpec]:
        """Build one map TaskSpec per planned shard."""
        task_parameters = self.task_parameters(resolved)
        tasks: list[TaskSpec] = []
        for index, path in enumerate(shard_paths):
            sealed = context.sink.seal(
                path,
                declaration=self.input_port.schema,
            )
            tasks.append(
                self.task_spec(
                    map_stage,
                    job,
                    negotiated,
                    f"map/{index:08d}",
                    task_parameters,
                    {"input": ArtifactCollection.single(sealed)},
                )
            )
        return tasks

    def task_parameters(self, resolved: Mapping[str, Any]) -> dict[str, Any]:
        """Project resolved parameters onto the map stage projection."""
        return {
            key: value
            for key, value in resolved.items()
            if key in set(self.map_parameter_names)
        }

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        """Compute one map task and write its partial CSV."""
        raise NotImplementedError("compute_shard must be implemented")

    def parse_partial_key(self, key: str) -> Any:
        """Parse one ``map.<key>`` partial key into an orderable identity."""
        prefix = "map."
        if not key.startswith(prefix):
            raise ValueError("partial key must use map.<eight-digit-index>")
        raw = key[len(prefix) :]
        if len(raw) != 8 or not raw.isdigit():
            raise ValueError("partial key must use map.<eight-digit-index>")
        return int(raw)

    def validate_partial_keys(self, parsed: Sequence[Any]) -> None:
        """Enforce the reducer's partial-set invariant (default: contiguous)."""
        indices = sorted(int(value) for value in parsed)
        if indices != list(range(len(indices))):
            raise ValueError("partial keys must be complete and contiguous")

    def reduce_partials(
        self,
        partial_paths: Sequence[Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        """Merge materialized partials into one deterministic final CSV.

        The default implementation concatenates partial tables in key order
        with exactly one header: the first partial is copied verbatim and
        every later partial contributes only its data rows. Workloads that
        merge (top-k, edge sets) override this hook.
        """
        return concatenate_partial_tables(partial_paths, output_path)

    # ------------------------------------------------------------------
    # Framework handlers
    # ------------------------------------------------------------------

    def validate(self, request: JobRequest) -> ValidatedJob:
        if request.workload != self.manifest.workload:
            raise ValueError("workload received a request for another workload")
        self.domain_validate(request.parameters)
        return ValidatedJob(request, self.resolved_parameters(request))

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan:
        if not isinstance(job, ValidatedJob):
            raise ValueError("job must be a ValidatedJob")
        collection = job.request.inputs.get("input")
        if collection is None:
            raise ValueError("workload requires the input port")
        self.input_port.validate_collection(collection, "job input")
        input_path = context.catalog.materialize(collection.items[0].artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        resolved = dict(job.resolved_parameters)
        resolved = self.resolved_parameters_for_plan(job, input_path, resolved)
        shard_paths = self.partition_input(input_path, resolved, workspace)
        negotiated = context.negotiated
        map_stage = self.manifest.workflow.stages[0]
        tasks = self.plan_tasks(
            shard_paths, resolved, job, negotiated, map_stage, context
        )
        return self.workflow_plan(job, negotiated, resolved, tasks)

    def resolved_parameters_for_plan(
        self,
        job: ValidatedJob,
        input_path: Path,
        resolved: dict[str, Any],
    ) -> dict[str, Any]:
        """Hook to enrich resolved parameters at plan time (query resolution)."""
        return resolved

    def workflow_plan(
        self,
        job: ValidatedJob,
        negotiated: Any,
        resolved: Mapping[str, Any],
        tasks: Sequence[TaskSpec],
    ) -> WorkflowPlan:
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
            resolved_parameters=dict(resolved),
            tasks=tuple(tasks),
        )

    def task_spec(
        self,
        map_stage: StageSpec,
        job: ValidatedJob,
        negotiated: Any,
        task_key: str,
        parameters: Mapping[str, Any],
        inputs: Mapping[str, ArtifactCollection],
    ) -> TaskSpec:
        assert map_stage.verifier is not None
        return TaskSpec(
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
            task_key=task_key,
            stage_id=map_stage.stage_id,
            parameters=parameters,
            inputs=inputs,
            expected_outputs=map_stage.outputs,
            resources=map_stage.resources,
            execution=map_stage.execution,
        ).validate_stage(map_stage)

    def run(self, context: TaskContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        assert self.map_stage_inputs is not None
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        inputs: dict[str, Path] = {}
        for name, port in self.map_stage_inputs.items():
            collection = context.task.inputs.get(name)
            if collection is None:
                raise ValueError(f"map task requires the {name} input")
            port.validate_collection(collection, f"map input {name}")
            item = next(iter(collection.items), None)
            if item is None:
                raise ValueError(f"map task input {name} is empty")
            inputs[name] = context.catalog.materialize(item.artifact)
        output_path = workspace / "result.csv"
        metrics = self.compute_shard(
            inputs,
            context.task.parameters,
            output_path,
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
            max_output_bytes=self._limits.max_output_bytes,
        )

    def reduce(self, context: ReduceContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.accepted_inputs.get("partials")
        if collection is None:
            raise ValueError("reducer requires a non-empty keyed partial collection")
        if collection.kind is not CollectionKind.KEYED or not collection.items:
            raise ValueError("reducer requires a non-empty keyed partial collection")
        self.manifest.workflow.stages[1].inputs["partials"].validate_collection(
            collection,
            "reducer partials",
        )
        parsed = [self.parse_partial_key(item.key or "") for item in collection.items]
        self.validate_partial_keys(parsed)
        expected_keys = context.task.expected_input_keys.get("partials")
        if expected_keys is None or {item.key for item in collection.items} != set(
            expected_keys
        ):
            raise ValueError("partial keys do not match the coordinator expected set")
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        partial_paths: list[Path] = []
        for index, item in sorted(
            enumerate(collection.items),
            key=lambda entry: entry[1].key or "",
        ):
            artifact: ArtifactRef = item.artifact
            source = context.catalog.materialize(artifact)
            target = workspace / artifact.artifact_id
            if source.resolve() != target.resolve():
                shutil.copyfile(source, target)
            if _sha256_file(target) != artifact.sha256:
                raise ValueError("materialized partial checksum does not match")
            partial_paths.append(target)
        result_path = workspace / "result.csv"
        metrics = self.reduce_partials(
            partial_paths,
            context.task.parameters,
            result_path,
        )
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
            max_output_bytes=self._limits.max_output_bytes,
        )
