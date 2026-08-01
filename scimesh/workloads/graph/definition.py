"""SDK-built ``similarity-graph`` workload definition and handlers.

Built directly on the ``core-batch-v1`` profile: molecules are parsed once
into deterministic row-ordered blocks, every block pair ``(i, j)`` with
``i <= j`` becomes one map task, and the reducer enforces the CTX-10
pair-coverage invariant (every unordered molecule pair compared exactly once)
before emitting a deterministically sorted edge list that is byte-identical
to the local brute-force reference.
"""

from __future__ import annotations

import hashlib
import shutil
from pathlib import Path
from typing import Any, Mapping

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
from ..environment import current_environment_digest, current_scimesh_package_digest
from .core import (
    block_pair_from_key,
    check_pair_coverage,
    compute_block_edges,
    merge_edge_partials,
    parse_molecule_blocks,
    read_block_rows,
    write_block_tsv,
    write_edge_csv,
)

MAP_ENTRY_POINT = "scimesh.workloads.graph.definition:map_graph@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.graph.definition:reduce_graph@v1"

_MAP_PARAMETERS = ("left_block", "right_block", "threshold", "threshold_direction")
_REDUCE_PARAMETERS = ("threshold", "threshold_direction", "block_size", "max_rows")


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
            "threshold": {"type": "number", "minimum": 0, "maximum": 1},
            "threshold_direction": {"enum": ["greater", "less"]},
            "block_size": {"type": "integer", "minimum": 1},
            "max_rows": {"type": "integer", "minimum": 1},
        },
        "required": ["threshold"],
    }


def _molecule_schema() -> ArtifactSchema:
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


def _edge_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("similarity-edge-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["source_id", "target_id", "similarity"],
        },
        max_records=1_000_000_000,
        canonicalizer="similarity-edge-table-v1",
    )


class SimilarityGraphSDKWorkload:
    """Manifest-backed planner, runner, and reducer for similarity-graph."""

    def __init__(
        self,
        *,
        package_digest: str,
        environment_digest: str,
    ) -> None:
        self.entry_point = MAP_ENTRY_POINT
        self.input_port = PortSpec(_molecule_schema())
        self.block_port = PortSpec(_molecule_schema())
        self.partial_port = PortSpec(_edge_schema())
        self.output_port = PortSpec(_edge_schema())
        resources = ResourceRequirements(
            profile="graph-cpu-v1",
            cpu_cores=1,
            memory_mb=1024,
            scratch_mb=1024,
            max_duration_seconds=3600,
        )
        execution = ExecutionProfile(
            profile="graph-python-process-v1",
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
            inputs={"left": self.block_port, "right": self.block_port},
            outputs={"partial": self.partial_port},
            parameter_names=_MAP_PARAMETERS,
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
            parameter_names=_REDUCE_PARAMETERS,
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
            trust_modes=trust_modes,
            max_fan_out=1,
            cacheable=True,
        )
        workflow = WorkflowSpec(
            workflow_id="graph-block-pairs-v1",
            inputs={"input": self.input_port},
            stages=(map_stage, reduce_stage),
            edges=(
                ArtifactEdge(PortRef("input"), PortRef("left", "map")),
                ArtifactEdge(PortRef("input"), PortRef("right", "map")),
                ArtifactEdge(PortRef("partial", "map"), PortRef("partials", "reduce")),
            ),
            outputs={"result": PortRef("result", "reduce")},
            max_tasks=limits.max_tasks,
            max_output_bytes=limits.max_output_bytes,
        )
        self.manifest = WorkloadManifest(
            sdk_api=VersionRange(">=1.0,<2.0"),
            protocol=VersionRange(">=1,<2"),
            workload=WorkloadId("similarity-graph", "1.0.0"),
            description=(
                "Exact sparse Tanimoto similarity graph over deterministic "
                "block pairs with a duplicate-safe, coverage-checked merge."
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
            capabilities=("similarity-graph",),
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
    def _unit_interval(value: object, name: str) -> float:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)

    @staticmethod
    def _positive_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError(f"{name} must be a positive integer")
        return value

    def validate(self, request: JobRequest) -> ValidatedJob:
        if request.workload != self.manifest.workload:
            raise ValueError("similarity-graph received a request for another workload")
        parameters = request.parameters
        unknown = set(parameters) - {
            "threshold",
            "threshold_direction",
            "block_size",
            "max_rows",
        }
        if unknown:
            raise ValueError(
                "unsupported similarity-graph parameters: " + ", ".join(sorted(unknown))
            )
        threshold = parameters.get("threshold")
        if threshold is None:
            raise ValueError("threshold is required")
        self._unit_interval(threshold, "threshold")
        if "threshold_direction" in parameters and parameters[
            "threshold_direction"
        ] not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        if "block_size" in parameters:
            self._positive_int(parameters["block_size"], "block_size")
        if "max_rows" in parameters:
            self._positive_int(parameters["max_rows"], "max_rows")
        return ValidatedJob(request, request.parameters)

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan:
        if not isinstance(job, ValidatedJob):
            raise ValueError("job must be a ValidatedJob")
        collection = job.request.inputs.get("input")
        if collection is None:
            raise ValueError("similarity-graph requires the input port")
        self.input_port.validate_collection(collection, "job input")
        input_artifact = collection.items[0].artifact
        input_path = context.catalog.materialize(input_artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        parameters = job.resolved_parameters
        threshold = self._unit_interval(parameters.get("threshold"), "threshold")
        direction = parameters.get("threshold_direction", "greater")
        if direction not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        block_size = int(parameters.get("block_size", 1_000))
        max_rows = parameters.get("max_rows")
        blocks, stats = parse_molecule_blocks(
            input_path,
            block_size,
            int(max_rows) if isinstance(max_rows, int) else None,
        )
        task_parameters = {
            "threshold": threshold,
            "threshold_direction": direction,
        }
        negotiated = context.negotiated
        map_stage = self.manifest.workflow.stages[0]
        assert map_stage.verifier is not None
        block_refs: list[ArtifactRef] = []
        for index, block in enumerate(blocks):
            path = workspace / f"block-{index:04d}.tsv"
            write_block_tsv(block, path)
            block_refs.append(
                context.sink.seal(
                    path,
                    declaration=self.block_port.schema,
                )
            )
        tasks: list[TaskSpec] = []
        for left in range(len(blocks)):
            for right in range(left, len(blocks)):
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
                        task_key=f"map/{left:04d}x{right:04d}",
                        stage_id="map",
                        parameters={
                            **task_parameters,
                            "left_block": left,
                            "right_block": right,
                        },
                        inputs={
                            "left": ArtifactCollection.single(block_refs[left]),
                            "right": ArtifactCollection.single(block_refs[right]),
                        },
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
            resolved_parameters=dict(parameters),
            tasks=tuple(tasks),
        )

    def run(self, context: TaskContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        parameters = context.task.parameters
        left_block = parameters.get("left_block")
        right_block = parameters.get("right_block")
        if (
            isinstance(left_block, bool)
            or not isinstance(left_block, int)
            or isinstance(right_block, bool)
            or not isinstance(right_block, int)
        ):
            raise ValueError("graph map task requires block indices")
        diagonal = left_block == right_block
        left_collection = context.task.inputs.get("left")
        right_collection = context.task.inputs.get("right")
        if left_collection is None or right_collection is None:
            raise ValueError("graph map task requires left and right block inputs")
        self.block_port.validate_collection(left_collection, "graph map left input")
        self.block_port.validate_collection(right_collection, "graph map right input")
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        left_path = context.catalog.materialize(left_collection.items[0].artifact)
        right_path = context.catalog.materialize(right_collection.items[0].artifact)
        left_rows = read_block_rows(left_path)
        right_rows = (
            left_rows
            if diagonal and left_path.resolve() == right_path.resolve()
            else read_block_rows(right_path)
        )
        threshold = self._unit_interval(parameters.get("threshold"), "threshold")
        direction = parameters.get("threshold_direction", "greater")
        if direction not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        checked_pairs = (
            len(left_rows) * (len(left_rows) - 1) // 2
            if diagonal
            else len(left_rows) * len(right_rows)
        )
        edges = compute_block_edges(
            left_rows,
            right_rows,
            threshold,
            direction,
        )
        output_path = workspace / "result.csv"
        write_edge_csv(output_path, edges)
        context.cancellation.raise_if_cancelled()
        sealed = context.sink.seal(
            output_path,
            declaration=self.partial_port.schema,
        )
        return OutputManifest(
            context.task.task_key,
            {"partial": ArtifactCollection.single(sealed)},
            {"checked_pairs": checked_pairs, "edges_emitted": len(edges)},
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
                "graph reducer requires a non-empty keyed partial collection"
            )
        self.manifest.workflow.stages[1].inputs["partials"].validate_collection(
            collection,
            "graph reducer partials",
        )
        pairs = [block_pair_from_key(item.key or "") for item in collection.items]
        check_pair_coverage(pairs)
        expected_keys = context.task.expected_input_keys.get("partials")
        if expected_keys is None or {item.key for item in collection.items} != set(
            expected_keys
        ):
            raise ValueError(
                "graph partial keys do not match the coordinator expected set"
            )
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        partial_paths: list[Path] = []
        for item in sorted(collection.items, key=lambda value: value.key or ""):
            artifact: ArtifactRef = item.artifact
            source = context.catalog.materialize(artifact)
            target = workspace / artifact.artifact_id
            if source.resolve() != target.resolve():
                shutil.copyfile(source, target)
            if _sha256_file(target) != artifact.sha256:
                raise ValueError("materialized partial checksum does not match")
            partial_paths.append(target)
        result_path = workspace / "result.csv"
        metrics = merge_edge_partials(partial_paths, result_path)
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


def similarity_graph_sdk_definition(
    *,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> SimilarityGraphSDKWorkload:
    """Build the SDK-based similarity-graph definition for tests."""
    return SimilarityGraphSDKWorkload(
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the SDK-based similarity-graph."""
    return similarity_graph_sdk_definition().definition()
