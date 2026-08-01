"""SDK-built ``similarity-search`` workload definition and handlers.

A direct ``core-batch-v1`` definition (not the legacy adapter): the planner
resolves the query once, shards deterministically, each map task computes the
local top-k with the reference implementation, and the reducer merges the
sorted partials with the same bounded heap and tie-breakers as the local CLI.
The manifest declares ``byte_exact`` with the exact-artifact verifier and both
``trusted`` and ``untrusted_quorum`` trust modes.
"""

from __future__ import annotations

import hashlib
import shutil
from pathlib import Path
from typing import Any, Mapping

from rdkit import Chem

from scimesh.chemistry.dataset import find_molecule_by_id, parse_smiles
from scimesh.chemistry.fingerprints import FP_RADIUS, FP_SIZE

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
from .core import merge_search_partials, run_search_shard, write_search_shards

MAP_ENTRY_POINT = "scimesh.workloads.search.definition:map_search@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.search.definition:reduce_search@v1"

_MAP_PARAMETERS = (
    "query_smiles",
    "top_k",
    "threshold",
    "threshold_direction",
    "progress_every",
)
_REDUCE_PARAMETERS = _MAP_PARAMETERS + ("query_source", "fingerprint")


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
            "query_id": {"type": "string", "minLength": 1, "maxLength": 200},
            "query_smiles": {"type": "string", "minLength": 1, "maxLength": 200},
            "top_k": {"type": "integer", "minimum": 1},
            "threshold": {"type": "number", "minimum": 0, "maximum": 1},
            "threshold_direction": {"enum": ["greater", "less"]},
            "max_rows": {"type": "integer", "minimum": 1},
            "progress_every": {"type": "integer", "minimum": 0},
        },
        "oneOf": [
            {"required": ["query_id"], "not": {"required": ["query_smiles"]}},
            {"required": ["query_smiles"], "not": {"required": ["query_id"]}},
        ],
    }


def _dataset_schema() -> ArtifactSchema:
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


def _search_table_schema(ref: SchemaRef, canonicalizer: str) -> ArtifactSchema:
    return ArtifactSchema(
        ref,
        "text/csv",
        "utf-8",
        max_bytes=1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["rank", "chembl_id", "canonical_smiles", "similarity"],
        },
        max_records=100_000,
        canonicalizer=canonicalizer,
    )


class SimilaritySearchSDKWorkload:
    """Manifest-backed planner, runner, and reducer for similarity-search."""

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
        self.entry_point = MAP_ENTRY_POINT
        self.shard_rows = shard_rows
        self.input_port = PortSpec(_dataset_schema())
        self.partial_port = PortSpec(
            _search_table_schema(
                SchemaRef("similarity-search-partial", 1), "scimesh-search-partial-v1"
            )
        )
        self.output_port = PortSpec(
            _search_table_schema(
                SchemaRef("similarity-search-result", 1), "scimesh-search-result-v1"
            )
        )
        resources = ResourceRequirements(
            profile="search-cpu-v1",
            cpu_cores=1,
            memory_mb=1024,
            scratch_mb=1024,
            max_duration_seconds=3600,
        )
        execution = ExecutionProfile(
            profile="search-python-process-v1",
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
            workflow_id="search-map-reduce-v1",
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
            workload=WorkloadId("similarity-search", "1.0.0"),
            description=(
                "Exact top-k Tanimoto molecular similarity search over "
                "deterministic TSV shards with a bounded merge."
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
            capabilities=("similarity-search",),
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
    def _string(value: object, name: str) -> str:
        if not isinstance(value, str) or not value.strip() or len(value) > 200:
            raise ValueError(f"{name} must be a non-empty string")
        return value

    @staticmethod
    def _positive_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError(f"{name} must be a positive integer")
        return value

    @staticmethod
    def _nonnegative_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise ValueError(f"{name} must be a non-negative integer")
        return value

    @staticmethod
    def _unit_interval(value: object, name: str) -> float:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)

    def validate(self, request: JobRequest) -> ValidatedJob:
        if request.workload != self.manifest.workload:
            raise ValueError(
                "similarity-search received a request for another workload"
            )
        parameters = request.parameters
        unknown = set(parameters) - {
            "query_id",
            "query_smiles",
            "top_k",
            "threshold",
            "threshold_direction",
            "max_rows",
            "progress_every",
        }
        if unknown:
            raise ValueError(
                "unsupported similarity-search parameters: "
                + ", ".join(sorted(unknown))
            )
        query_id = parameters.get("query_id")
        query_smiles = parameters.get("query_smiles")
        if (query_id is None) == (query_smiles is None):
            raise ValueError("exactly one of query_id or query_smiles is required")
        if query_id is not None:
            self._string(query_id, "query_id")
        if query_smiles is not None:
            self._string(query_smiles, "query_smiles")
        self._positive_int(parameters.get("top_k", 20), "top_k")
        if "max_rows" in parameters:
            self._positive_int(parameters["max_rows"], "max_rows")
        if "progress_every" in parameters:
            self._nonnegative_int(parameters["progress_every"], "progress_every")
        if "threshold" in parameters:
            self._unit_interval(parameters["threshold"], "threshold")
        if "threshold_direction" in parameters and parameters[
            "threshold_direction"
        ] not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        return ValidatedJob(request, self._resolved_parameters(request))

    def _resolved_parameters(self, request: JobRequest) -> dict[str, object]:
        parameters = request.parameters
        query_id = parameters.get("query_id")
        if isinstance(query_id, str):
            query_source: dict[str, str] = {"kind": "chembl_id", "value": query_id}
        else:
            query_source = {
                "kind": "smiles",
                "value": self._string(parameters.get("query_smiles"), "query_smiles"),
            }
        resolved: dict[str, object] = {
            "query_source": query_source,
            "top_k": self._positive_int(parameters.get("top_k", 20), "top_k"),
            "threshold_direction": parameters.get("threshold_direction", "greater"),
            "fingerprint": {
                "algorithm": "morgan",
                "radius": FP_RADIUS,
                "fp_size": FP_SIZE,
            },
        }
        if "threshold" in parameters:
            resolved["threshold"] = self._unit_interval(
                parameters["threshold"], "threshold"
            )
        if "max_rows" in parameters:
            resolved["max_rows"] = self._positive_int(
                parameters["max_rows"], "max_rows"
            )
        if "progress_every" in parameters:
            resolved["progress_every"] = self._nonnegative_int(
                parameters["progress_every"], "progress_every"
            )
        return resolved

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan:
        if not isinstance(job, ValidatedJob):
            raise ValueError("job must be a ValidatedJob")
        collection = job.request.inputs.get("input")
        if collection is None:
            raise ValueError("similarity-search requires the input port")
        self.input_port.validate_collection(collection, "job input")
        input_artifact = collection.items[0].artifact
        input_path = context.catalog.materialize(input_artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        resolved = dict(job.resolved_parameters)
        query_smiles = self._resolve_query(input_path, job.request.parameters)
        resolved["query_smiles"] = query_smiles
        max_rows = resolved.get("max_rows")
        shard_paths = write_search_shards(
            input_path,
            workspace,
            self.shard_rows,
            int(max_rows) if isinstance(max_rows, int) else None,
        )
        task_parameters = {
            key: value for key, value in resolved.items() if key in set(_MAP_PARAMETERS)
        }
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
                    parameters=task_parameters,
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
            resolved_parameters=resolved,
            tasks=tuple(tasks),
        )

    @staticmethod
    def _resolve_query(input_path: Path, parameters: Mapping[str, object]) -> str:
        query_id = parameters.get("query_id")
        if isinstance(query_id, str):
            record = find_molecule_by_id(input_path, query_id)
            return Chem.MolToSmiles(record.molecule, canonical=True)
        supplied = parameters["query_smiles"]
        assert isinstance(supplied, str)
        molecule = parse_smiles(supplied)
        if molecule is None:
            raise ValueError("query_smiles is invalid")
        return Chem.MolToSmiles(molecule, canonical=True)

    def run(self, context: TaskContext) -> OutputManifest:
        context.cancellation.raise_if_cancelled()
        collection = context.task.inputs.get("input")
        if collection is None:
            raise ValueError("search map task requires one input collection")
        self.input_port.validate_collection(collection, "search map input")
        source = context.catalog.materialize(collection.items[0].artifact)
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        input_path = workspace / "input"
        output_path = workspace / "result.csv"
        if source.resolve() != input_path.resolve():
            shutil.copyfile(source, input_path)
        metrics = run_search_shard(
            input_path,
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
                "search reducer requires a non-empty keyed partial collection"
            )
        self.manifest.workflow.stages[1].inputs["partials"].validate_collection(
            collection,
            "search reducer partials",
        )
        workspace = context.workspace
        workspace.mkdir(parents=True, exist_ok=True)
        indexed_items: list[tuple[int, ArtifactItem]] = []
        for item in collection.items:
            key = item.key or ""
            prefix = "map."
            raw_index = key[len(prefix) :] if key.startswith(prefix) else ""
            if len(raw_index) != 8 or not raw_index.isdigit():
                raise ValueError("search partial key must use map.<eight-digit-index>")
            indexed_items.append((int(raw_index), item))
        expected_keys = context.task.expected_input_keys.get("partials")
        if expected_keys is None or {item.key for item in collection.items} != set(
            expected_keys
        ):
            raise ValueError(
                "search partial keys do not match the coordinator expected set"
            )
        if sorted(index for index, _ in indexed_items) != list(
            range(len(indexed_items))
        ):
            raise ValueError("search partial keys must be complete and contiguous")
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
        metrics = merge_search_partials(
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
            max_output_bytes=self.manifest.limits.max_output_bytes,
        )


def similarity_search_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> SimilaritySearchSDKWorkload:
    """Build the SDK-built similarity-search definition for tests."""
    return SimilaritySearchSDKWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the SDK-built similarity-search."""
    return similarity_search_sdk_definition().definition()
