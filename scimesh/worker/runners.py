"""SDK-based local workload execution for claimed coordinator tasks.

The runner is a v1-wire bridge: the coordinator still claims flat tasks and
the worker still uploads one partial CSV, but execution goes through the
SDK-built workload's own Runner handler with a real ``TaskSpec``,
provenance, resource reservation, and a content-addressed local store. No
legacy distributed-protocol code is involved.
"""

from __future__ import annotations

import hashlib
from datetime import datetime, timezone
from pathlib import Path
from typing import Mapping, Protocol
from uuid import NAMESPACE_URL, uuid5

from scimesh.sdk.artifacts import (
    ArtifactCollection,
    ArtifactRef,
    OutputManifest,
    Provenance,
)
from scimesh.sdk.conformance import (
    CancellationFlag,
    LocalArtifactStore,
    LocalTaskContext,
    ScopedArtifactSink,
)
from scimesh.sdk._validation import canonical_json
from scimesh.sdk.manifest import TrustMode
from scimesh.sdk.plans import TaskSpec
from scimesh.sdk.registry import WorkloadDefinition
from scimesh.sdk.resources import ResourceAllocation, ResourcePool
from scimesh.sdk.runtime import (
    NegotiatedWorkload,
    RuntimeCapabilities,
    negotiate_manifest,
)
from scimesh.sdk.workflow import StageKind
from scimesh.workloads.library import default_sdk_runtime
from scimesh.workloads.search import similarity_search_sdk_definition

from .models import ClaimedTask, ProducedArtifact, RunResult

#: Parameters the worker may hand to a map task. ``max_rows`` is a plan-time
#: option applied before sharding and is intentionally rejected here.
_RUNNER_PARAMETERS = frozenset(
    {"query_smiles", "top_k", "threshold", "threshold_direction", "progress_every"}
)


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class Runner(Protocol):
    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult: ...


class SciMeshRunner:
    """Execute claimed coordinator tasks through the SDK-built workloads."""

    def __init__(
        self,
        definitions: Mapping[str, WorkloadDefinition] | None = None,
        runtime: RuntimeCapabilities | None = None,
    ) -> None:
        self._definitions = dict(definitions or {})
        if "similarity-search" not in self._definitions:
            self._definitions["similarity-search"] = (
                similarity_search_sdk_definition().definition()
            )
        self._runtime = runtime or default_sdk_runtime()
        self._pool = ResourcePool(self._runtime.inventory, max_concurrency=1)

    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
        task_dir = task_dir.resolve()
        workload = task.workload.replace("_", "-")
        definition = self._definitions.get(workload)
        if definition is None:
            raise ValueError(f"unsupported workload: {task.workload}")
        manifest = definition.manifest
        negotiated = negotiate_manifest(manifest, self._runtime)
        map_stage = next(
            stage for stage in manifest.workflow.stages if stage.kind is StageKind.MAP
        )
        assert map_stage.verifier is not None
        input_path = task_dir / "input"
        if not input_path.is_file():
            raise ValueError("claimed task input is missing")
        parameters = self._resolve_parameters(task, input_path)
        store = LocalArtifactStore(task_dir / "sdk-store")
        input_ref = store.import_file(
            input_path,
            declaration=manifest.inputs["input"].schema,
        )
        spec = TaskSpec(
            workload=manifest.workload,
            package_digest=manifest.package.digest,
            manifest_digest=manifest.digest,
            trust_mode=TrustMode.TRUSTED,
            sdk_api_version=negotiated.sdk_api_version,
            protocol_version=negotiated.protocol_version,
            manifest_schema_version=manifest.manifest_schema_version,
            workflow_schema_version=manifest.workflow.schema_version,
            environment_digest=manifest.environment.digest,
            verifier=map_stage.verifier,
            selected_features=negotiated.selected_features,
            optional_fallbacks=negotiated.optional_fallbacks,
            task_key="map/00000000",
            stage_id=map_stage.stage_id,
            parameters=parameters,
            inputs={"input": ArtifactCollection.single(input_ref)},
            expected_outputs=map_stage.outputs,
            resources=map_stage.resources,
            execution=map_stage.execution,
        ).validate_stage(map_stage)
        allocation = self._pool.reserve(task.task_id, spec.resources)
        try:
            provenance = self._provenance(
                definition, negotiated, spec, task, allocation
            )
            context = LocalTaskContext(
                spec,
                store,
                store,
                task_dir,
                CancellationFlag(),
                provenance,
                spec.inputs,
                transaction=None,
            )
            output = definition.runners[map_stage.entry_point].run(context)
            self._validate_output(
                output,
                spec,
                context,
                store,
                provenance,
                max_output_bytes=manifest.limits.max_output_bytes,
            )
            partial_ref = output.outputs["partial"].items[0].artifact
            partial_path = store.materialize(partial_ref)
            return RunResult(
                (ProducedArtifact(partial_path, "text/csv"),),
                dict(output.metrics),
            )
        finally:
            self._pool.release(allocation.allocation_id)

    @staticmethod
    def _resolve_parameters(task: ClaimedTask, input_path: Path) -> dict[str, object]:
        """Resolve ``query_id`` once per task and reject plan-time options."""
        parameters = dict(task.parameters)
        query_id = parameters.get("query_id")
        query_smiles = parameters.get("query_smiles")
        if isinstance(query_id, str) and not isinstance(query_smiles, str):
            from scimesh.chemistry.dataset import find_molecule_by_id
            from rdkit import Chem

            record = find_molecule_by_id(input_path, query_id)
            parameters["query_smiles"] = Chem.MolToSmiles(
                record.molecule, canonical=True
            )
            del parameters["query_id"]
        unknown = set(parameters) - _RUNNER_PARAMETERS
        if unknown:
            raise ValueError(
                "unsupported runner parameters: " + ", ".join(sorted(unknown))
            )
        return parameters

    def _provenance(
        self,
        definition: WorkloadDefinition,
        negotiated: NegotiatedWorkload,
        spec: TaskSpec,
        task: ClaimedTask,
        allocation: ResourceAllocation,
    ) -> Provenance:
        manifest = definition.manifest
        started_at = _utc_now()
        return Provenance(
            workload=manifest.workload,
            sdk_api_version=spec.sdk_api_version,
            protocol_version=spec.protocol_version,
            manifest_schema_version=spec.manifest_schema_version,
            workflow_schema_version=spec.workflow_schema_version,
            verifier=spec.verifier,
            artifact_schemas=tuple(
                sorted(
                    {
                        item.artifact.schema
                        for collection in spec.inputs.values()
                        for item in collection.items
                    }.union(port.schema.ref for port in spec.expected_outputs.values()),
                    key=lambda value: value.canonical,
                )
            ),
            package_digest=spec.package_digest,
            manifest_digest=spec.manifest_digest,
            environment_digest=spec.environment_digest,
            worker_runtime={"kind": "worker-agent-v1"},
            allocated_resource_ids=(allocation.allocation_id,),
            parameters_digest=hashlib.sha256(
                canonical_json(spec.parameters).encode("utf-8")
            ).hexdigest(),
            input_collection_digest=spec.inputs["input"].digest,
            execution_contract_digest=spec.digest,
            selected_features=spec.selected_features,
            optional_fallbacks=spec.optional_fallbacks,
            job_id=str(uuid5(NAMESPACE_URL, f"scimesh:job:{task.task_id}")),
            task_id=str(uuid5(NAMESPACE_URL, f"scimesh:task:{task.task_id}")),
            started_at=started_at,
            finished_at=started_at,
            trust_mode=TrustMode.TRUSTED.value,
        )

    @staticmethod
    def _validate_output(
        output: object,
        spec: TaskSpec,
        context: LocalTaskContext,
        store: LocalArtifactStore,
        provenance: Provenance,
        *,
        max_output_bytes: int,
    ) -> None:
        if not isinstance(output, OutputManifest):
            raise ValueError("SDK workload must return an OutputManifest")
        if output.task_key != spec.task_key:
            raise ValueError(
                "SDK workload output task_key does not match its trusted task"
            )
        if output.provenance != provenance:
            raise ValueError(
                "SDK workload output provenance does not match its context"
            )
        output.validate_against(
            spec.expected_outputs,
            max_output_bytes=max_output_bytes,
        )
        if output.provenance != provenance:
            raise ValueError(
                "SDK workload output provenance does not match its context"
            )
        output.validate_against(
            spec.expected_outputs,
            max_output_bytes=spec.resources.max_duration_seconds,  # replaced below
        )
        sink = context.sink
        if not isinstance(sink, ScopedArtifactSink):
            raise ValueError("SDK execution requires a scoped artifact sink")
        declared = {
            item.artifact.artifact_id: item.artifact
            for collection in output.outputs.values()
            for item in collection.items
        }
        issued = {artifact.artifact_id: artifact for artifact in sink.sealed_references}
        if issued != declared:
            raise ValueError(
                "SDK workload outputs must declare exactly the artifacts sealed by its attempt"
            )
        for collection in output.outputs.values():
            for item in collection.items:
                store.require(item.artifact)
