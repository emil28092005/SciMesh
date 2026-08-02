"""Contract tests for immutable SDK identities, artifacts, workflows, and manifests."""

from __future__ import annotations

import json
from dataclasses import FrozenInstanceError, replace
from typing import Any, Mapping
from uuid import NAMESPACE_URL, uuid5

import pytest

from scimesh.sdk import (
    AcceleratorMode,
    ArtifactCollection,
    ArtifactEdge,
    ArtifactItem,
    ArtifactRef,
    ArtifactSchema,
    Cardinality,
    CheckpointPolicy,
    CollectionKind,
    CompatibilityError,
    ComponentRef,
    DeterminismProfile,
    EnvironmentSpec,
    ExecutionProfile,
    ExpansionManifest,
    FailureCategory,
    FailureReport,
    FeatureRequirement,
    GangSpec,
    JobRequest,
    LoopSpec,
    NetworkPolicy,
    PackageSpec,
    PortRef,
    PortSpec,
    ProcessModel,
    Provenance,
    ResourceAllocation,
    ResourceInventory,
    ResourceRequirements,
    RetryPolicy,
    RuntimeCapabilities,
    SchemaRef,
    SideEffectSpec,
    StageKind,
    StageSpec,
    StreamSpec,
    TaskSpec,
    TrustMode,
    VerifierSpec,
    VersionRange,
    WorkflowSpec,
    WorkloadId,
    WorkloadLimits,
    WorkloadManifest,
    negotiate_manifest,
)


PACKAGE_DIGEST = "sha256:" + "a" * 64
ENVIRONMENT_DIGEST = "sha256:" + "b" * 64


def artifact_schema(*, max_bytes: int = 1_024) -> ArtifactSchema:
    return ArtifactSchema(
        ref=SchemaRef("molecule-table", 1),
        media_type="application/json",
        encoding="utf-8",
        max_bytes=max_bytes,
        validator=ComponentRef("json-document", 1),
        max_records=100,
        max_dimensions=(100, 8),
        canonicalizer="canonical-json-v1",
    )


def artifact(seed: str, *, schema: SchemaRef | None = None, size_bytes: int = 12) -> ArtifactRef:
    digest = (seed.encode("utf-8").hex() * 64)[:64]
    return ArtifactRef(
        artifact_id=str(uuid5(NAMESPACE_URL, seed)),
        sha256=digest,
        schema=schema or SchemaRef("molecule-table", 1),
        media_type="application/json",
        size_bytes=size_bytes,
        records=1,
        dimensions=(1, 2),
    )


def workload_manifest(
    *,
    parameters_schema: Mapping[str, Any] | None = None,
    required_features: tuple[FeatureRequirement, ...] | None = None,
    optional_features: tuple[FeatureRequirement, ...] | None = None,
) -> WorkloadManifest:
    port = PortSpec(artifact_schema())
    resources = ResourceRequirements(
        profile="cpu-small-v1",
        cpu_cores=1,
        memory_mb=128,
        scratch_mb=64,
        max_duration_seconds=60,
    )
    stage = StageSpec(
        stage_id="compute",
        kind=StageKind.MAP,
        entry_point="tests.sdk_fixture:run@v1",
        needs=(),
        inputs={"dataset": port},
        outputs={"result": port},
        parameter_names=("limit",),
        resources=resources,
        execution=ExecutionProfile(
            profile="single-cpu-v1",
            network=NetworkPolicy.TRUSTED,
            timeout_seconds=60,
        ),
        retry=RetryPolicy(),
        verifier=ComponentRef("exact-artifact", 1),
        cacheable=True,
    )
    workflow = WorkflowSpec(
        workflow_id="single-stage-v1",
        inputs={"dataset": port},
        stages=(stage,),
        edges=(ArtifactEdge(PortRef("dataset"), PortRef("dataset", "compute")),),
        outputs={"result": PortRef("result", "compute")},
        max_tasks=8,
        max_output_bytes=1_024,
    )
    schema = parameters_schema or {
        "type": "object",
        "additionalProperties": False,
        "properties": {"limit": {"type": "integer", "minimum": 1}},
    }
    return WorkloadManifest(
        sdk_api=VersionRange(">=1.0,<2.0"),
        protocol=VersionRange(">=1,<2"),
        workload=WorkloadId("demo-workload", "1.2.3"),
        description="A deterministic SDK contract fixture.",
        package=PackageSpec("scimesh-demo", PACKAGE_DIGEST),
        environment=EnvironmentSpec(
            "python-process",
            ENVIRONMENT_DIGEST,
            {"python": {"implementation": "cpython", "version": [3, 10]}},
        ),
        parameters_schema=schema,
        workflow=workflow,
        inputs={"dataset": port},
        outputs={"result": port},
        determinism=DeterminismProfile.BYTE_EXACT,
        trust_modes=(TrustMode.TRUSTED,),
        verifier=VerifierSpec(ComponentRef("exact-artifact", 1), {}),
        limits=WorkloadLimits(max_input_bytes=1_024, max_tasks=8, max_output_bytes=1_024),
        capabilities=("demo-workload",),
        conformance_profiles=("core-batch-v1",),
        required_features=required_features
        if required_features is not None
        else (FeatureRequirement("exact-verifier", VersionRange(">=1,<2")),),
        optional_features=optional_features or (),
    )


def runtime_capabilities(**changes: object) -> RuntimeCapabilities:
    values: dict[str, object] = {
        "sdk_api_version": "1.0.0",
        "protocol_version": "1.0.0",
        "profiles": ("core-batch-v1",),
        "features": {"exact-verifier": "1.0.0"},
        "workload_capabilities": ("demo-workload",),
        "inventory": ResourceInventory(
            cpu_cores=2,
            memory_mb=1_024,
            scratch_mb=1_024,
            architecture="x86-64",
            environment_digests=(ENVIRONMENT_DIGEST,),
        ),
    }
    values.update(changes)
    return RuntimeCapabilities(**values)  # type: ignore[arg-type]


def test_identity_values_use_explicit_versions_and_strict_round_trips() -> None:
    version_range = VersionRange(">=1.0,<2.0")
    workload = WorkloadId("demo-workload", "1.2.3")
    schema = SchemaRef("molecule-table", 2)
    component = ComponentRef("exact-artifact", 1)
    feature = FeatureRequirement("gpu-exclusive", VersionRange(">=1,<2"), "cpu-fallback")

    assert VersionRange.from_dict(version_range.to_dict()) == version_range
    assert WorkloadId.from_dict(workload.to_dict()) == workload
    assert SchemaRef.from_dict(schema.to_dict()) == schema
    assert ComponentRef.from_dict(component.to_dict()) == component
    assert FeatureRequirement.from_dict(feature.to_dict()) == feature
    assert version_range.contains("1.9.9")
    assert not version_range.contains("2.0.0")

    with pytest.raises(ValueError, match="must use =="):
        VersionRange("1.0")
    with pytest.raises(ValueError, match="semantic version"):
        WorkloadId("demo-workload", "latest")
    with pytest.raises(ValueError, match="unknown future"):
        WorkloadId.from_dict({"name": "demo-workload", "version": "1.0.0", "future": True})


def test_version_range_whitespace_is_canonical_and_semver_prerelease_is_strict() -> None:
    assert VersionRange(" >= 1.0 , < 2 ").expression == ">=1.0,<2"
    with pytest.raises(ValueError, match="prerelease"):
        WorkloadId("demo-workload", "1.0.0-01")


def test_artifact_models_and_manifest_have_canonical_strict_round_trips() -> None:
    schema = artifact_schema()
    reference = artifact("round-trip")
    collection = ArtifactCollection.single(reference)
    manifest = workload_manifest()

    assert ArtifactSchema.from_dict(schema.to_dict()) == schema
    assert ArtifactRef.from_dict(reference.to_dict()) == reference
    assert ArtifactCollection.from_dict(collection.to_dict()) == collection
    assert WorkflowSpec.from_dict(manifest.workflow.to_dict()) == manifest.workflow

    encoded = manifest.to_json()
    decoded = WorkloadManifest.from_json(encoded)
    assert decoded == manifest
    assert decoded.to_json() == encoded
    assert decoded.digest == manifest.digest
    assert json.loads(encoded)["manifest_schema_version"] == 1

    payload = manifest.to_dict()
    payload["future_semantics"] = {"enabled": True}
    with pytest.raises(ValueError, match="unknown future_semantics"):
        WorkloadManifest.from_dict(payload)


def test_json_backed_values_are_deeply_immutable_and_detached_from_callers() -> None:
    parameter_schema: dict[str, Any] = {
        "type": "object",
        "additionalProperties": False,
        "properties": {"limit": {"type": "integer", "minimum": 1}},
    }
    manifest = workload_manifest(parameters_schema=parameter_schema)

    parameter_schema["properties"]["limit"]["minimum"] = -100
    assert manifest.parameters_schema["properties"]["limit"]["minimum"] == 1
    with pytest.raises(TypeError):
        manifest.parameters_schema["properties"]["limit"]["minimum"] = 0
    with pytest.raises(TypeError):
        manifest.environment.metadata["python"]["version"][0] = 2
    with pytest.raises(TypeError):
        manifest.inputs["another"] = manifest.inputs["dataset"]  # type: ignore[index]
    with pytest.raises(FrozenInstanceError):
        manifest.description = "changed"  # type: ignore[misc]


def test_collection_kinds_have_distinct_ordering_key_and_duplicate_semantics() -> None:
    first = ArtifactItem(artifact("first"))
    second = ArtifactItem(artifact("second"))

    ordered = ArtifactCollection(CollectionKind.ORDERED, (second, first))
    assert ordered.items == (second, first)
    assert ordered.digest != ArtifactCollection(CollectionKind.ORDERED, (first, second)).digest

    keyed = ArtifactCollection(
        CollectionKind.KEYED,
        (ArtifactItem(second.artifact, "z-key"), ArtifactItem(first.artifact, "a-key")),
    )
    assert [item.key for item in keyed.items] == ["a-key", "z-key"]
    with pytest.raises(ValueError, match="keys must be unique"):
        ArtifactCollection(
            CollectionKind.KEYED,
            (ArtifactItem(first.artifact, "same"), ArtifactItem(second.artifact, "same")),
        )

    set_forward = ArtifactCollection(CollectionKind.SET, (second, first))
    set_reverse = ArtifactCollection(CollectionKind.SET, (first, second))
    assert set_forward.items == set_reverse.items
    assert set_forward.digest == set_reverse.digest
    duplicate_content = ArtifactItem(
        ArtifactRef(
            artifact_id=str(uuid5(NAMESPACE_URL, "other-identity")),
            sha256=first.artifact.sha256,
            schema=first.artifact.schema,
            media_type=first.artifact.media_type,
            size_bytes=first.artifact.size_bytes,
            records=first.artifact.records,
            dimensions=first.artifact.dimensions,
        )
    )
    with pytest.raises(ValueError, match="duplicate artifacts"):
        ArtifactCollection(CollectionKind.SET, (first, duplicate_content))


def test_port_cardinality_and_artifact_bounds_are_enforced() -> None:
    schema = artifact_schema(max_bytes=16)
    one = PortSpec(schema)
    optional = PortSpec(schema, Cardinality.OPTIONAL)
    many = PortSpec(schema, Cardinality.MANY, CollectionKind.ORDERED)
    valid = artifact("valid", size_bytes=16)

    one.validate_collection(ArtifactCollection.single(valid))
    optional.validate_collection(ArtifactCollection.single(None))
    many.validate_collection(
        ArtifactCollection(CollectionKind.ORDERED, (ArtifactItem(valid),))
    )

    with pytest.raises(ValueError, match="exactly one"):
        one.validate_collection(ArtifactCollection.single(None))
    with pytest.raises(ValueError, match="at least one"):
        many.validate_collection(ArtifactCollection(CollectionKind.ORDERED, ()))
    with pytest.raises(ValueError, match="byte limit"):
        one.validate_collection(ArtifactCollection.single(artifact("large", size_bytes=17)))
    wrong_schema = artifact("wrong", schema=SchemaRef("other-table", 1))
    with pytest.raises(ValueError, match="wrong schema"):
        one.validate_collection(ArtifactCollection.single(wrong_schema))


def test_workflow_graph_validation_fails_closed_for_unbound_or_inconsistent_dependencies() -> None:
    payload = workload_manifest().workflow.to_dict()
    payload["edges"] = []
    with pytest.raises(ValueError, match="unbound inputs"):
        WorkflowSpec.from_dict(payload)

    payload = workload_manifest().workflow.to_dict()
    payload["stages"][0]["needs"] = ["undeclared-stage"]  # type: ignore[index]
    with pytest.raises(ValueError, match="needs do not match"):
        WorkflowSpec.from_dict(payload)


def test_manifest_negotiation_accepts_only_explicit_compatible_capabilities() -> None:
    manifest = workload_manifest(
        optional_features=(
            FeatureRequirement("gpu-fastpath", VersionRange(">=1,<2"), "cpu-fallback"),
        )
    )

    negotiated = negotiate_manifest(manifest, runtime_capabilities())
    assert negotiated.optional_fallbacks == {"gpu-fastpath": "cpu-fallback"}

    incompatible_cases = (
        (runtime_capabilities(sdk_api_version="2.0.0"), "runtime-sdk-mismatch"),
        (runtime_capabilities(protocol_version="2.0.0"), "protocol-mismatch"),
        (runtime_capabilities(profiles=()), "profile-unavailable"),
        (runtime_capabilities(workload_capabilities=()), "workload-unavailable"),
        (
            runtime_capabilities(
                inventory=replace(
                    runtime_capabilities().inventory,
                    environment_digests=("sha256:" + "c" * 64,),
                )
            ),
            "environment-unavailable",
        ),
        (runtime_capabilities(features={}), "feature-unavailable"),
    )
    for runtime, code in incompatible_cases:
        with pytest.raises(CompatibilityError) as raised:
            negotiate_manifest(manifest, runtime)
        assert raised.value.code == code

    optional_without_fallback = replace(
        manifest,
        optional_features=(FeatureRequirement("gpu-fastpath", VersionRange(">=1,<2")),),
    )
    with pytest.raises(CompatibilityError) as raised:
        negotiate_manifest(optional_without_fallback, runtime_capabilities())
    assert raised.value.code == "optional-feature-unavailable"


@pytest.mark.parametrize(
    "unsafe",
    (
        "failed reading file:/tmp/result.json",
        "invalid payload data:text/plain,secret",
        "invalid payload data:",
        "lookup failed for urn:uuid:1234",
        "lookup failed for urn:",
        "failed at /home/worker/result.json",
        "failed at ../private/result.json",
        r"failed at C:\\worker\\result.json",
        "failed reading run-123/tasks/map/result.csv",
        "path=attempts/job-123/private.txt",
        "upload=https%253A%252F%252Fworker.invalid%252Fresult%253Ftoken%253Dsecret",
    ),
)
def test_failure_report_rejects_uri_schemes_and_local_paths(unsafe: str) -> None:
    with pytest.raises(ValueError, match="URI or local path"):
        FailureReport(
            code="attempt-failed",
            category=FailureCategory.INFRASTRUCTURE,
            retryable=False,
            message=unsafe,
            evidence={},
        )


def test_failure_report_has_a_strict_wire_round_trip() -> None:
    report = FailureReport(
        code="attempt-failed",
        category=FailureCategory.INFRASTRUCTURE,
        retryable=True,
        message="temporary execution failure",
        evidence={"attempt": 2},
    )

    assert FailureReport.from_json(report.to_json()) == report


def test_location_filter_does_not_reject_stereochemical_smiles() -> None:
    request = JobRequest(
        WorkloadId("demo-workload", "1.2.3"),
        {"query_smiles": "F/C=C/F"},
        {},
    )

    assert request.parameters["query_smiles"] == "F/C=C/F"


def _provenance_with_resource_ids(resource_ids: tuple[str, ...]) -> Provenance:
    return Provenance(
        workload=WorkloadId("demo-workload", "1.2.3"),
        sdk_api_version="1.0.0",
        protocol_version="1.0.0",
        manifest_schema_version=1,
        workflow_schema_version=1,
        verifier=ComponentRef("exact-artifact", 1),
        artifact_schemas=(SchemaRef("molecule-table", 1),),
        package_digest=PACKAGE_DIGEST,
        manifest_digest="e" * 64,
        environment_digest=ENVIRONMENT_DIGEST,
        worker_runtime={"kind": "test-runtime"},
        allocated_resource_ids=resource_ids,
        parameters_digest="c" * 64,
        input_collection_digest="d" * 64,
        execution_contract_digest="f" * 64,
        selected_features={"exact-verifier": "1.0.0"},
        optional_fallbacks={},
        job_id=str(uuid5(NAMESPACE_URL, "provenance-job")),
        task_id=str(uuid5(NAMESPACE_URL, "provenance-task")),
        started_at="2026-08-01T10:00:00Z",
        finished_at="2026-08-01T10:00:01Z",
    )


def test_provenance_accepts_uuid_and_gpu_like_opaque_resource_ids() -> None:
    resource_ids = (
        str(uuid5(NAMESPACE_URL, "allocation")),
        "GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        "MIG-GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-gi0-ci0",
    )

    assert _provenance_with_resource_ids(resource_ids).allocated_resource_ids == resource_ids


@pytest.mark.parametrize(
    "unsafe",
    (
        "file:/dev/nvidia0",
        "data:text/plain,gpu-0",
        "data:",
        "urn:scimesh:gpu:0",
        "urn:",
        "/dev/nvidia0",
        "../gpu-0",
        "gpu/0",
        r"gpu\\0",
        "gpu 0",
        "gpu,0",
    ),
)
def test_provenance_rejects_locator_like_resource_ids(unsafe: str) -> None:
    with pytest.raises(ValueError, match="opaque single resource identifier"):
        _provenance_with_resource_ids((unsafe,))


def test_expansion_is_bound_to_coordinator_parent_and_remaining_budget() -> None:
    port = PortSpec(artifact_schema())
    resources = ResourceRequirements(
        profile="cpu-small-v1",
        cpu_cores=1,
        memory_mb=128,
        scratch_mb=64,
        max_duration_seconds=60,
    )
    execution = ExecutionProfile(
        profile="single-cpu-v1",
        network=NetworkPolicy.TRUSTED,
        timeout_seconds=60,
    )
    verifier = ComponentRef("exact-artifact", 1)
    planner_stage = StageSpec(
        stage_id="planner",
        kind=StageKind.PLAN,
        entry_point="tests.sdk_fixture:plan@v1",
        needs=(),
        inputs={"dataset": port},
        outputs={"planned": port},
        parameter_names=("limit",),
        resources=resources,
        execution=execution,
        retry=RetryPolicy(),
        verifier=verifier,
    )
    child_stage = StageSpec(
        stage_id="compute",
        kind=StageKind.MAP,
        entry_point="tests.sdk_fixture:run@v1",
        needs=("planner",),
        inputs={"dataset": port},
        outputs={"result": port},
        parameter_names=("limit",),
        resources=resources,
        execution=execution,
        retry=RetryPolicy(),
        verifier=verifier,
        max_fan_out=2,
    )
    workflow = WorkflowSpec(
        workflow_id="dynamic-v1",
        inputs={"dataset": port},
        stages=(planner_stage, child_stage),
        edges=(
            ArtifactEdge(PortRef("dataset"), PortRef("dataset", "planner")),
            ArtifactEdge(PortRef("planned", "planner"), PortRef("dataset", "compute")),
        ),
        outputs={"result": PortRef("result", "compute")},
        max_tasks=4,
        max_output_bytes=1_024,
    )
    common: dict[str, object] = {
        "workload": WorkloadId("dynamic-demo", "1.0.0"),
        "package_digest": PACKAGE_DIGEST,
        "manifest_digest": "e" * 64,
        "trust_mode": TrustMode.TRUSTED,
        "sdk_api_version": "1.0.0",
        "protocol_version": "1.0.0",
        "manifest_schema_version": 1,
        "workflow_schema_version": 1,
        "environment_digest": ENVIRONMENT_DIGEST,
        "verifier": verifier,
        "selected_features": {"dynamic-expansion": "1.0.0"},
        "optional_fallbacks": {},
        "parameters": {"limit": 1},
        "resources": resources,
        "execution": execution,
    }
    source = ArtifactCollection.single(artifact("dynamic-source"))
    planned = ArtifactCollection.single(artifact("dynamic-planned"))
    parent = TaskSpec(
        **common,  # type: ignore[arg-type]
        task_key="root/planner",
        stage_id="planner",
        inputs={"dataset": source},
        expected_outputs={"planned": port},
    )
    child = TaskSpec(
        **common,  # type: ignore[arg-type]
        task_key="root/planner/000",
        stage_id="compute",
        inputs={"dataset": planned},
        expected_outputs={"result": port},
    )
    job_id = str(uuid5(NAMESPACE_URL, "dynamic-job"))
    parent_task_id = str(uuid5(NAMESPACE_URL, "dynamic-parent-task"))
    expansion = ExpansionManifest(
        job_id=job_id,
        parent_task_id=parent_task_id,
        parent_task_key=parent.task_key,
        parent_execution_contract_digest=parent.digest,
        tasks=(child,),
        max_children=2,
    )
    authorized_inputs = {"compute": {"dataset": planned}}

    assert ExpansionManifest.from_json(expansion.to_json()) == expansion
    assert expansion.validate_against(
        parent,
        workflow,
        job_id=job_id,
        parent_task_id=parent_task_id,
        declared_max_children=2,
        remaining_tasks=2,
        authorized_inputs=authorized_inputs,
        existing_stage_task_counts={},
    ) is expansion
    with pytest.raises(ValueError, match="another job"):
        expansion.validate_against(
            parent,
            workflow,
            job_id=str(uuid5(NAMESPACE_URL, "other-job")),
            parent_task_id=parent_task_id,
            declared_max_children=2,
            remaining_tasks=2,
            authorized_inputs=authorized_inputs,
            existing_stage_task_counts={},
        )
    with pytest.raises(ValueError, match="execution contract"):
        expansion.validate_against(
            replace(parent, parameters={"limit": 2}),
            workflow,
            job_id=job_id,
            parent_task_id=parent_task_id,
            declared_max_children=2,
            remaining_tasks=2,
            authorized_inputs=authorized_inputs,
            existing_stage_task_counts={},
        )
    with pytest.raises(ValueError, match="child task budget"):
        replace(expansion, max_children=3).validate_against(
            parent,
            workflow,
            job_id=job_id,
            parent_task_id=parent_task_id,
            declared_max_children=2,
            remaining_tasks=2,
            authorized_inputs=authorized_inputs,
            existing_stage_task_counts={},
        )
    with pytest.raises(ValueError, match="child task budget"):
        expansion.validate_against(
            parent,
            workflow,
            job_id=job_id,
            parent_task_id=parent_task_id,
            declared_max_children=2,
            remaining_tasks=0,
            authorized_inputs=authorized_inputs,
            existing_stage_task_counts={},
        )
    with pytest.raises(ValueError, match="not coordinator-authorized"):
        expansion.validate_against(
            parent,
            workflow,
            job_id=job_id,
            parent_task_id=parent_task_id,
            declared_max_children=2,
            remaining_tasks=2,
            authorized_inputs={},
            existing_stage_task_counts={},
        )


def test_workflow_graph_rejects_cyclic_dependencies() -> None:
    port = PortSpec(artifact_schema())
    resources = ResourceRequirements(
        profile="cpu-small-v1",
        cpu_cores=1,
        memory_mb=128,
        scratch_mb=64,
        max_duration_seconds=60,
    )
    execution = ExecutionProfile(
        profile="single-cpu-v1",
        network=NetworkPolicy.TRUSTED,
        timeout_seconds=60,
    )

    def stage(stage_id: str, needs: tuple[str, ...]) -> StageSpec:
        return StageSpec(
            stage_id=stage_id,
            kind=StageKind.MAP,
            entry_point=f"tests.sdk_fixture:{stage_id}@v1",
            needs=needs,
            inputs={"incoming": port},
            outputs={"outgoing": port},
            parameter_names=(),
            resources=resources,
            execution=execution,
            retry=RetryPolicy(),
            verifier=ComponentRef("exact-artifact", 1),
        )

    with pytest.raises(ValueError, match="must be acyclic"):
        WorkflowSpec(
            workflow_id="cyclic-v1",
            inputs={"dataset": port},
            stages=(stage("first", ("second",)), stage("second", ("first",))),
            edges=(
                ArtifactEdge(PortRef("outgoing", "second"), PortRef("incoming", "first")),
                ArtifactEdge(PortRef("outgoing", "first"), PortRef("incoming", "second")),
            ),
            outputs={"result": PortRef("outgoing", "first")},
        )


def _advanced_stage_changes(case: str) -> dict[str, object]:
    cpu_pair = ResourceRequirements(
        profile="cpu-pair-v1",
        cpu_cores=2,
        memory_mb=128,
        scratch_mb=64,
        max_duration_seconds=60,
    )

    def gpu_resources(mode: AcceleratorMode) -> ResourceRequirements:
        return ResourceRequirements(
            profile="gpu-v1",
            cpu_cores=1,
            memory_mb=128,
            scratch_mb=64,
            accelerator_count=1,
            accelerator_kind="gpu",
            accelerator_mode=mode,
            max_duration_seconds=60,
        )

    cases: dict[str, dict[str, object]] = {
        "plan": {"kind": StageKind.PLAN},
        "loop": {
            "kind": StageKind.LOOP_CONTROLLER,
            "loop": LoopSpec(
                state_schema=SchemaRef("loop-state", 1),
                max_iterations=4,
                max_wall_seconds=60,
                body_workflow="loop-body-v1",
                continue_when=ComponentRef("loop-gate", 1),
                checkpoint_every=2,
            ),
        },
        "stream": {
            "kind": StageKind.STREAM,
            "stream": StreamSpec(
                source="topic-input",
                partitioning="by-key",
                checkpoint_schema=SchemaRef("stream-state", 1),
                window_seconds=10,
                watermark_seconds=5,
                backpressure_limit=16,
                delivery_guarantee="at_least_once",
                max_windows=8,
            ),
        },
        "service": {"kind": StageKind.SERVICE},
        "side-effect": {
            "kind": StageKind.SIDE_EFFECT,
            "cacheable": False,
            "side_effect": SideEffectSpec(
                target="instrument",
                idempotency_key_parameter="limit",
                credential_scope="lab-scope",
                compensation="rollback-run",
            ),
        },
        "gang": {
            "gang": GangSpec(
                replicas=2,
                per_replica_resources=ResourceRequirements(
                    profile="cpu-small-v1",
                    cpu_cores=1,
                    memory_mb=128,
                    scratch_mb=64,
                    max_duration_seconds=60,
                ),
            ),
        },
        "process-pool": {
            "resources": cpu_pair,
            "execution": ExecutionProfile(
                profile="pool-v1",
                process_model=ProcessModel.PROCESS_POOL,
                max_processes=2,
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
            ),
        },
        "thread-pool": {
            "resources": cpu_pair,
            "execution": ExecutionProfile(
                profile="threads-v1",
                process_model=ProcessModel.THREAD_POOL,
                threads_per_process=2,
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
            ),
        },
        "external-runtime": {
            "execution": ExecutionProfile(
                profile="external-v1",
                process_model=ProcessModel.EXTERNAL_RUNTIME,
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
            ),
        },
        "native-threads": {
            "resources": cpu_pair,
            "execution": ExecutionProfile(
                profile="native-v1",
                native_threads=2,
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
            ),
        },
        "nested-parallelism": {
            "execution": ExecutionProfile(
                profile="nested-v1",
                nested_parallelism=True,
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
            ),
        },
        "artifact-network": {
            "execution": ExecutionProfile(
                profile="artifact-net-v1",
                network=NetworkPolicy.COORDINATOR_ARTIFACTS_ONLY,
                timeout_seconds=60,
            ),
        },
        "egress": {
            "execution": ExecutionProfile(
                profile="egress-v1",
                network=NetworkPolicy.ALLOWLISTED_EGRESS,
                allowed_egress=("api.example.org",),
                timeout_seconds=60,
            ),
        },
        "checkpoint": {
            "execution": ExecutionProfile(
                profile="checkpoint-v1",
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
                checkpoint=CheckpointPolicy(
                    enabled=True,
                    schema=SchemaRef("task-state", 1),
                    compatibility_version=1,
                ),
            ),
        },
        "retry": {"retry": RetryPolicy(max_attempts=2)},
        "secrets": {
            "execution": ExecutionProfile(
                profile="secrets-v1",
                network=NetworkPolicy.TRUSTED,
                timeout_seconds=60,
                secret_handles=("db-credential",),
            ),
        },
        "gpu-exclusive": {"resources": gpu_resources(AcceleratorMode.EXCLUSIVE_DEVICE)},
        "gpu-mig": {"resources": gpu_resources(AcceleratorMode.PARTITION)},
        "gpu-fractional": {"resources": gpu_resources(AcceleratorMode.FRACTIONAL)},
    }
    return cases[case]


@pytest.mark.parametrize(
    ("case", "feature"),
    (
        ("plan", "dynamic-expansion"),
        ("loop", "bounded-loops"),
        ("stream", "stream-checkpoints"),
        ("service", "services"),
        ("side-effect", "side-effect"),
        ("gang", "gang-leases"),
        ("process-pool", "process-pools"),
        ("thread-pool", "thread-pools"),
        ("external-runtime", "external-runtimes"),
        ("native-threads", "native-threads"),
        ("nested-parallelism", "nested-parallelism"),
        ("artifact-network", "artifact-network-policy"),
        ("egress", "egress-allowlist"),
        ("checkpoint", "checkpoints"),
        ("retry", "retries"),
        ("secrets", "secret-injection"),
        ("gpu-exclusive", "gpu-exclusive"),
        ("gpu-mig", "gpu-mig"),
        ("gpu-fractional", "accelerator-fractional"),
    ),
)
def test_negotiation_rejects_undeclared_advanced_stage_profiles(
    case: str,
    feature: str,
) -> None:
    manifest = workload_manifest()
    stage = replace(manifest.workflow.stages[0], **_advanced_stage_changes(case))
    manifest = replace(manifest, workflow=replace(manifest.workflow, stages=(stage,)))

    with pytest.raises(CompatibilityError) as raised:
        negotiate_manifest(manifest, runtime_capabilities())
    assert raised.value.code == "feature-undeclared"
    assert feature in str(raised.value)


def test_negotiation_reports_resource_ineligibility() -> None:
    manifest = workload_manifest()
    oversized = ResourceRequirements(
        profile="cpu-large-v1",
        cpu_cores=1,
        memory_mb=8_192,
        scratch_mb=64,
        max_duration_seconds=60,
    )
    stage = replace(manifest.workflow.stages[0], resources=oversized)
    manifest = replace(manifest, workflow=replace(manifest.workflow, stages=(stage,)))

    with pytest.raises(CompatibilityError) as raised:
        negotiate_manifest(manifest, runtime_capabilities())
    assert raised.value.code == "resource-ineligible"
    assert "insufficient-memory" in str(raised.value)


def test_negotiation_rejects_an_sdk_api_outside_the_manifest_range() -> None:
    manifest = replace(workload_manifest(), sdk_api=VersionRange(">=1.1,<2.0"))

    with pytest.raises(CompatibilityError) as raised:
        negotiate_manifest(manifest, runtime_capabilities())
    assert raised.value.code == "sdk-api-mismatch"


def test_manifest_acceptance_policy_binds_verifier_determinism_and_quorum() -> None:
    manifest = workload_manifest()

    quorum = replace(manifest, trust_modes=(TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM))
    assert quorum.trust_modes == (TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM)

    canonical_stage = replace(manifest.workflow.stages[0], verifier=ComponentRef("canonical-record", 1))
    canonical_workflow = replace(manifest.workflow, stages=(canonical_stage,))
    with pytest.raises(ValueError, match="byte_exact workloads require exact-artifact verifier"):
        replace(
            manifest,
            workflow=canonical_workflow,
            verifier=VerifierSpec(ComponentRef("canonical-record", 1), {}),
        )

    canonical_manifest = replace(
        manifest,
        workflow=canonical_workflow,
        determinism=DeterminismProfile.CANONICAL_EXACT,
        verifier=VerifierSpec(ComponentRef("canonical-record", 1), {}),
    )
    with pytest.raises(ValueError, match="untrusted_quorum v1 requires byte_exact and exact-artifact"):
        replace(canonical_manifest, trust_modes=(TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM))


def test_manifest_acceptance_policy_restricts_side_effecting_profiles() -> None:
    manifest = workload_manifest()
    side_effect_stage = replace(
        manifest.workflow.stages[0],
        kind=StageKind.SIDE_EFFECT,
        cacheable=False,
        side_effect=SideEffectSpec(
            target="instrument",
            idempotency_key_parameter="limit",
            credential_scope="lab-scope",
            compensation="rollback-run",
        ),
    )
    side_effect_workflow = replace(manifest.workflow, stages=(side_effect_stage,))

    with pytest.raises(ValueError, match="side-effect stages cannot use untrusted quorum"):
        replace(
            manifest,
            workflow=side_effect_workflow,
            trust_modes=(TrustMode.TRUSTED, TrustMode.UNTRUSTED_QUORUM),
        )
    with pytest.raises(ValueError, match="side_effecting workloads must be trusted-only"):
        replace(
            manifest,
            workflow=side_effect_workflow,
            determinism=DeterminismProfile.SIDE_EFFECTING,
            trust_modes=(TrustMode.TRUSTED, TrustMode.VERIFIED),
        )
    with pytest.raises(ValueError, match="side_effecting workload requires a side-effect stage"):
        replace(manifest, determinism=DeterminismProfile.SIDE_EFFECTING)


def test_allocation_environment_exposes_only_allocation_derived_values() -> None:
    profile = ExecutionProfile(
        profile="native-v1",
        native_threads=8,
        network=NetworkPolicy.TRUSTED,
        timeout_seconds=60,
    )
    allocation = ResourceAllocation(
        allocation_id="allocation-0001",
        owner_id="attempt-0001",
        cpu_cores=2,
        memory_mb=128,
        scratch_mb=64,
        accelerator_ids=("GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",),
    )

    environment = profile.allocation_environment(allocation)

    assert dict(environment) == {
        "OMP_NUM_THREADS": "2",
        "OPENBLAS_NUM_THREADS": "2",
        "MKL_NUM_THREADS": "2",
        "NUMEXPR_NUM_THREADS": "2",
        "VECLIB_MAXIMUM_THREADS": "2",
        "CUDA_VISIBLE_DEVICES": "GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        "ROCR_VISIBLE_DEVICES": "GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
    }
    with pytest.raises(TypeError):
        environment["OMP_NUM_THREADS"] = "1"  # type: ignore[index]

    cpu_only = replace(allocation, accelerator_ids=())
    assert profile.allocation_environment(cpu_only)["CUDA_VISIBLE_DEVICES"] == ""
    with pytest.raises(ValueError, match="must be a ResourceAllocation"):
        profile.allocation_environment("not-an-allocation")  # type: ignore[arg-type]
