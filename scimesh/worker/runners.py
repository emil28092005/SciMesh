"""SDK-based local workload execution for claimed coordinator tasks.

The runner is a v1-wire bridge: the coordinator still claims flat tasks and
the worker still uploads one partial CSV, but execution goes through the
SDK-built workload's own Runner handler with a real ``TaskSpec``,
provenance, resource reservation, and a content-addressed local store.

The runner is workload-generic: it loads definitions by name (from an
explicit mapping, built-in defaults, or installed-package discovery through
an administrator allowlist) and executes any workload whose map stage has a
single ``input`` port and a single ``partial`` output. Anything else fails
closed with a clear message, so adding a workload never requires touching
worker code.
"""

from __future__ import annotations

import hashlib
import platform
from datetime import datetime, timezone
from pathlib import Path
from typing import Mapping, Protocol
from uuid import NAMESPACE_URL, uuid5

from scimesh.sdk.artifacts import (
    ArtifactCollection,
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
from scimesh.sdk.identity import SDK_API_VERSION
from scimesh.sdk.manifest import TrustMode
from scimesh.sdk.plans import TaskSpec
from scimesh.sdk.registry import WorkloadDefinition, WorkloadRegistry
from scimesh.sdk.resources import ResourceAllocation, ResourceInventory, ResourcePool
from scimesh.sdk.runtime import (
    NegotiatedWorkload,
    RuntimeCapabilities,
    negotiate_manifest,
)
from scimesh.sdk.workflow import StageKind

from .config import WorkerConfig
from .models import ClaimedTask, ProducedArtifact, RunResult

def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class Runner(Protocol):
    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult: ...


def _inventory_for(
    definitions: Mapping[str, WorkloadDefinition],
    *,
    cpu_cores: int,
    memory_mb: int,
) -> ResourceInventory:
    return ResourceInventory(
        cpu_cores=cpu_cores,
        memory_mb=memory_mb,
        scratch_mb=memory_mb,
        architecture=platform.machine().lower() or "unknown",
        environment_digests=tuple(
            dict.fromkeys(
                definition.manifest.environment.digest
                for definition in definitions.values()
            )
        ),
    )


def _runtime_for(
    definitions: Mapping[str, WorkloadDefinition], inventory: ResourceInventory
) -> RuntimeCapabilities:
    return RuntimeCapabilities(
        sdk_api_version=SDK_API_VERSION,
        protocol_version="1.0.0",
        profiles=("core-batch-v1",),
        features={"artifact-collections": "1.0.0", "exact-verifier": "1.0.0"},
        workload_capabilities=tuple(sorted(definitions)),
        inventory=inventory,
    )


class SciMeshRunner:
    """Execute claimed coordinator tasks through SDK-built workloads."""

    def __init__(
        self,
        definitions: Mapping[str, WorkloadDefinition] | None = None,
        *,
        inventory: ResourceInventory | None = None,
        runtime: RuntimeCapabilities | None = None,
    ) -> None:
        self._definitions = dict(definitions or {})
        if "similarity-search" not in self._definitions:
            from scimesh.workloads.search import similarity_search_sdk_definition

            self._definitions["similarity-search"] = (
                similarity_search_sdk_definition().definition()
            )
        self._inventory = inventory or _inventory_for(
            self._definitions,
            cpu_cores=1,
            memory_mb=1024,
        )
        self._runtime = runtime or _runtime_for(self._definitions, self._inventory)
        self._pool = ResourcePool(self._runtime.inventory, max_concurrency=1)

    @classmethod
    def for_worker(cls, config: WorkerConfig) -> "SciMeshRunner":
        """Build a runner for one worker: discover allowlisted workloads or use built-ins."""
        definitions: dict[str, WorkloadDefinition] = {}
        if config.workload_allowlist:
            registry = WorkloadRegistry()
            registry.discover_installed(config.workload_allowlist)
            for description in registry.descriptions():
                definition, _ = registry.require(
                    description.workload.name,
                    description.workload.version,
                    description.package_digest,
                )
                definitions[description.workload.name] = definition
            if not definitions:
                raise ValueError("workload_allowlist discovered no workloads")
        else:
            from scimesh.workloads.search import similarity_search_sdk_definition

            definitions["similarity-search"] = (
                similarity_search_sdk_definition().definition()
            )
        inventory = _inventory_for(
            definitions,
            cpu_cores=config.cpu_count,
            memory_mb=config.memory_mb or 1024,
        )
        return cls(definitions=definitions, inventory=inventory)

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
        if set(map_stage.inputs) != {"input"} or len(map_stage.outputs) != 1:
            raise ValueError(
                f"workload {workload} is not executable through the v1 single-input contract"
            )
        assert map_stage.verifier is not None
        input_path = task_dir / "input"
        if not input_path.is_file():
            raise ValueError("claimed task input is missing")
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
            parameters=task.parameters,
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
