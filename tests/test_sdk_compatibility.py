"""Compatibility tests for the built-in SDK bridge and scientific reference."""

from __future__ import annotations

import csv
from dataclasses import replace
from pathlib import Path
from uuid import uuid4

import pytest

from scimesh.chemistry.dataset import find_molecule_by_id
from scimesh.sdk import (
    ArtifactCollection,
    ArtifactSchema,
    CheckpointPolicy,
    CompatibilityError,
    ComponentRef,
    DeterminismProfile,
    FeatureRequirement,
    GangSpec,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    LocalPlanningContext,
    NetworkPolicy,
    PortRef,
    ProcessModel,
    RetryPolicy,
    SchemaRef,
    StageKind,
    TrustMode,
    VerificationDecision,
    VerificationStatus,
    VersionRange,
    WorkloadDefinition,
    WorkloadRegistry,
    assert_manifest_round_trip,
    default_sdk_registry,
    default_sdk_runtime,
    similarity_search_sdk_adapter,
)
from scimesh.workloads.similarity_search import search_similar, write_search_results


def _write_tiny_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\textra\n"
        "QUERY\tCCO\tquery\n"
        "ALCOHOL\tCCCO\talcohol\n"
        "ALKANE\tCCCC\talkane\n"
        "BROKEN\tnot-a-smiles\tinvalid\n"
        "DUPLICATE\tCCO\tduplicate\n"
        "AMINE\tCCN\tamine\n",
        encoding="utf-8",
    )


def _registered_similarity_search(shard_rows: int = 2):
    registry = default_sdk_registry(shard_rows=shard_rows)
    runtime = default_sdk_runtime()
    descriptions = registry.descriptions()
    assert len(descriptions) == 1
    description = descriptions[0]
    definition, negotiated = registry.require(
        description.workload.name,
        description.workload.version,
        description.package_digest,
        runtime=runtime,
    )
    return registry, runtime, description, definition, negotiated


def _request_for(
    dataset: Path,
    artifact_store: LocalArtifactStore,
    definition: WorkloadDefinition,
) -> JobRequest:
    input_port = definition.manifest.inputs["input"]
    dataset_artifact = artifact_store.import_file(
        dataset,
        declaration=input_port.schema,
    )
    return JobRequest(
        workload=definition.manifest.workload,
        parameters={"query_id": "QUERY", "top_k": 3, "progress_every": 0},
        inputs={"input": ArtifactCollection.single(dataset_artifact)},
    )


def test_builtin_similarity_search_manifest_is_registered_and_negotiable() -> None:
    _, _, description, definition, negotiated = _registered_similarity_search()
    manifest = definition.manifest

    assert description.enabled is True
    assert manifest.workload.name == "similarity-search"
    assert manifest.workload.version == "1.0.0"
    assert manifest.determinism is DeterminismProfile.BYTE_EXACT
    assert manifest.conformance_profiles == ("core-batch-v1",)
    assert [stage.kind for stage in manifest.workflow.stages] == [
        StageKind.MAP,
        StageKind.REDUCE,
    ]
    assert set(definition.runners) == {manifest.workflow.stages[0].entry_point}
    assert set(definition.reducers) == {manifest.workflow.stages[1].entry_point}
    assert negotiated is not None
    assert negotiated.manifest == manifest
    assert_manifest_round_trip(manifest)


def test_local_sdk_executor_matches_similarity_search_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)

    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, description.package_digest)
    result_artifact = result.outputs["result"].items[0].artifact

    reference_path = tmp_path / "reference.csv"
    query = find_molecule_by_id(dataset, "QUERY")
    reference = search_similar(dataset, query, top_k=3, progress_every=0)
    write_search_results(reference_path, reference.matches)

    assert artifact_store.materialize(result_artifact).read_bytes() == reference_path.read_bytes()
    assert result.task_key == "reduce/final"
    assert result.metrics == {"matches_emitted": 3, "partial_count": 3}


def test_legacy_adapter_planning_is_deterministic_ordered_and_path_free(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, description, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, definition)
    input_artifact = request.inputs["input"].items[0].artifact

    first = registry.plan(
        request,
        description.package_digest,
        runtime,
        LocalPlanningContext(
            artifact_store,
            artifact_store,
            tmp_path / "first-plan",
            allowed_artifacts=(input_artifact,),
        ),
    )
    second = registry.plan(
        request,
        description.package_digest,
        runtime,
        LocalPlanningContext(
            artifact_store,
            artifact_store,
            tmp_path / "second-plan",
            allowed_artifacts=(input_artifact,),
        ),
    )

    assert first.to_json() == second.to_json()
    assert first.digest == second.digest
    assert first.package_digest == definition.manifest.package.digest
    assert first.manifest_digest == definition.manifest.digest
    assert first.trust_mode is request.trust_mode
    assert JobRequest.from_json(request.to_json()) == request
    assert [task.task_key for task in first.tasks] == [
        "map/00000000",
        "map/00000001",
        "map/00000002",
    ]
    assert all(task.stage_id == "map" for task in first.tasks)
    assert all(task.package_digest == first.package_digest for task in first.tasks)
    assert all(task.manifest_digest == first.manifest_digest for task in first.tasks)
    assert all(task.trust_mode is first.trust_mode for task in first.tasks)
    assert all("query_id" not in task.parameters for task in first.tasks)
    assert all(task.parameters["query_smiles"] == "CCO" for task in first.tasks)

    shard_ids: list[list[str]] = []
    for task in first.tasks:
        artifact = task.inputs["input"].items[0].artifact
        with artifact_store.materialize(artifact).open(encoding="utf-8", newline="") as source:
            shard_ids.append(
                [row["chembl_id"] for row in csv.DictReader(source, delimiter="\t")]
            )
        assert set(artifact.to_dict()) == {
            "artifact_id",
            "sha256",
            "schema",
            "media_type",
            "size_bytes",
            "records",
            "dimensions",
        }
    assert shard_ids == [
        ["QUERY", "ALCOHOL"],
        ["ALKANE", "BROKEN"],
        ["DUPLICATE", "AMINE"],
    ]

    wire_payload = first.to_json()
    assert str(tmp_path) not in wire_payload
    assert "file://" not in wire_payload
    assert "worker://" not in wire_payload
    assert "workspace" not in wire_payload


def test_local_context_sink_cannot_seal_files_outside_the_attempt(
    tmp_path: Path,
) -> None:
    _, _, _, definition, _ = _registered_similarity_search()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    workspace = tmp_path / "attempt"
    context = LocalPlanningContext(artifact_store, artifact_store, workspace)
    outside = tmp_path / "private.txt"
    outside.write_text("private", encoding="utf-8")
    schema = definition.manifest.inputs["input"].schema

    with pytest.raises(ValueError, match="inside its workspace"):
        context.sink.seal(outside, declaration=schema)

    workspace.mkdir(parents=True, exist_ok=True)
    link = workspace / "result"
    link.symlink_to(outside)
    with pytest.raises(ValueError, match="real workspace directories"):
        context.sink.seal(link, declaration=schema)


def test_local_store_rejects_malformed_content_before_publishing(
    tmp_path: Path,
) -> None:
    malformed = tmp_path / "malformed.json"
    malformed.write_text('{"unfinished":', encoding="utf-8")
    declaration = ArtifactSchema(
        SchemaRef("json-result", 1),
        "application/json",
        "utf-8",
        max_bytes=1_024,
        validator=ComponentRef("json-document", 1),
    )
    store = LocalArtifactStore(tmp_path / "artifacts")

    with pytest.raises(ValueError, match="not a valid bounded document"):
        store.import_file(malformed, declaration=declaration)
    assert tuple(path for path in store.root.iterdir() if not path.name.startswith(".seal-")) == ()


def test_delimited_validator_rejects_headerless_data_and_enforces_record_limit(
    tmp_path: Path,
) -> None:
    declaration = ArtifactSchema(
        SchemaRef("bounded-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=1_024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={"columns": ["value"]},
        max_records=1,
    )
    store = LocalArtifactStore(tmp_path / "artifacts")
    headerless = tmp_path / "headerless.csv"
    headerless.write_text("1\n2\n", encoding="utf-8")
    oversized = tmp_path / "oversized.csv"
    oversized.write_text("value\n1\n2\n", encoding="utf-8")

    with pytest.raises(ValueError, match="header does not match"):
        store.import_file(headerless, declaration=declaration)
    with pytest.raises(ValueError, match="record limit"):
        store.import_file(oversized, declaration=declaration)


def test_custom_artifact_inspector_is_bound_to_schema_and_validator_identity(
    tmp_path: Path,
) -> None:
    schema_ref = SchemaRef("matrix-result", 1)
    validator = ComponentRef("matrix-inspector", 1)
    declaration = ArtifactSchema(
        schema_ref,
        "application/x-matrix",
        None,
        max_bytes=1_024,
        validator=validator,
        validator_configuration={"layout": "row-major"},
        max_records=1,
        max_dimensions=(2, 2),
    )
    source = tmp_path / "matrix.bin"
    source.write_bytes(b"matrix")
    wrong = LocalArtifactStore(
        tmp_path / "wrong-store",
        inspectors={
            schema_ref.canonical: (
                ComponentRef("other-inspector", 1),
                lambda _path, _configuration: (1, (2, 2)),
            )
        },
    )
    with pytest.raises(ValueError, match="no matching registered validator"):
        wrong.import_file(source, declaration=declaration)

    def inspect(_path: Path, configuration):
        assert dict(configuration) == {"layout": "row-major"}
        return 1, (2, 2)

    store = LocalArtifactStore(
        tmp_path / "store",
        inspectors={schema_ref.canonical: (validator, inspect)},
    )
    artifact = store.import_file(source, declaration=declaration)
    assert artifact.records == 1
    assert artifact.dimensions == (2, 2)


@pytest.mark.parametrize(
    ("forgery", "message"),
    (
        ("artifact", "artifacts sealed by its attempt"),
        ("provenance", "provenance does not match"),
    ),
)
def test_local_executor_rejects_handler_forged_outputs(
    tmp_path: Path,
    forgery: str,
    message: str,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, description, original, _ = _registered_similarity_search()
    map_stage = next(stage for stage in original.manifest.workflow.stages if stage.kind is StageKind.MAP)
    inner = original.runners[map_stage.entry_point]

    class ForgingRunner:
        def run(self, context):
            result = inner.run(context)
            if forgery == "provenance":
                forged = replace(
                    result.provenance,
                    worker_runtime={"kind": "forged-runtime"},
                )
                return replace(result, provenance=forged)
            original_ref = result.outputs["partial"].items[0].artifact
            forged_ref = replace(original_ref, artifact_id=str(uuid4()))
            return replace(
                result,
                outputs={"partial": ArtifactCollection.single(forged_ref)},
            )

    definition = WorkloadDefinition(
        original.manifest,
        original.planner,
        {map_stage.entry_point: ForgingRunner()},
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)

    with pytest.raises(ValueError, match=message):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            description.package_digest,
        )


def test_local_executor_rejects_profiles_that_claim_network_isolation(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, description, original, _ = _registered_similarity_search()
    stages = tuple(
        replace(stage, execution=replace(stage.execution, network=NetworkPolicy.NONE))
        for stage in original.manifest.workflow.stages
    )
    workflow = replace(original.manifest.workflow, stages=stages)
    manifest = replace(original.manifest, workflow=workflow)
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)

    with pytest.raises(CompatibilityError) as raised:
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            description.package_digest,
        )
    assert raised.value.code == "feature-undeclared"


def test_local_executor_rejects_aliased_terminal_outputs_before_planning(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, description, original, _ = _registered_similarity_search()
    reducer = next(
        stage for stage in original.manifest.workflow.stages if stage.kind is StageKind.REDUCE
    )
    internal_name = next(iter(reducer.outputs))
    workflow = replace(
        original.manifest.workflow,
        outputs={"aliased": PortRef(internal_name, reducer.stage_id)},
    )
    manifest = replace(
        original.manifest,
        workflow=workflow,
        outputs={"aliased": reducer.outputs[internal_name]},
    )
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)

    with pytest.raises(ValueError, match="identity-mapped reducer outputs"):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            description.package_digest,
        )


def test_local_executor_rejects_non_trusted_trust_modes(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, _, original, _ = _registered_similarity_search()
    stages = tuple(
        replace(stage, trust_modes=("trusted", "verified"))
        for stage in original.manifest.workflow.stages
    )
    manifest = replace(
        original.manifest,
        workflow=replace(original.manifest.workflow, stages=stages),
        trust_modes=(TrustMode.TRUSTED, TrustMode.VERIFIED),
    )
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = replace(
        _request_for(dataset, store, definition),
        trust_mode=TrustMode.VERIFIED,
    )
    runtime = replace(runtime, trust_modes=(TrustMode.TRUSTED, TrustMode.VERIFIED))

    with pytest.raises(ValueError, match="supports only trusted workloads"):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            manifest.package.digest,
        )


def _advanced_execution_manifest(
    original: WorkloadDefinition,
    case: str,
) -> tuple[tuple[str, ...], object]:
    """Declare one negotiable advanced profile the local executor cannot enforce."""
    stages = original.manifest.workflow.stages
    if case == "process-pool":
        features = ("process-pools", "multi-process")
        changed = tuple(
            replace(
                stage,
                resources=replace(stage.resources, cpu_cores=2),
                execution=replace(
                    stage.execution,
                    process_model=ProcessModel.PROCESS_POOL,
                    max_processes=2,
                ),
            )
            for stage in stages
        )
    elif case == "checkpoints":
        features = ("checkpoints",)
        changed = tuple(
            replace(
                stage,
                execution=replace(
                    stage.execution,
                    checkpoint=CheckpointPolicy(
                        enabled=True,
                        schema=SchemaRef("task-state", 1),
                        compatibility_version=1,
                    ),
                ),
            )
            for stage in stages
        )
    elif case == "retries":
        features = ("retries",)
        changed = tuple(
            replace(stage, retry=RetryPolicy(max_attempts=2))
            for stage in stages
        )
    elif case == "secrets":
        features = ("secret-injection",)
        changed = tuple(
            replace(
                stage,
                execution=replace(stage.execution, secret_handles=("db-credential",)),
            )
            for stage in stages
        )
    elif case == "gang":
        features = ("gang-leases",)
        changed = tuple(
            replace(
                stage,
                gang=GangSpec(replicas=2, per_replica_resources=stage.resources),
            )
            for stage in stages
        )
    elif case == "network-isolation":
        features = ("network-isolation",)
        changed = tuple(
            replace(
                stage,
                execution=replace(stage.execution, network=NetworkPolicy.NONE),
            )
            for stage in stages
        )
    else:
        assert case == "service-stage"
        features = ("services",)
        changed = (replace(stages[0], kind=StageKind.SERVICE),) + stages[1:]
    manifest = replace(
        original.manifest,
        workflow=replace(original.manifest.workflow, stages=changed),
        required_features=original.manifest.required_features
        + tuple(
            FeatureRequirement(feature, VersionRange(">=1,<2")) for feature in features
        ),
    )
    return features, manifest


@pytest.mark.parametrize(
    ("case", "message"),
    (
        ("process-pool", "one non-nested host thread"),
        ("checkpoints", "cannot enforce this stage profile"),
        ("retries", "does not implement retries"),
        ("secrets", "cannot enforce this stage profile"),
        ("gang", "cannot enforce this stage profile"),
        ("network-isolation", "cannot enforce a restricted network policy"),
        ("service-stage", "does not implement advanced stages"),
    ),
)
def test_local_executor_rejects_profiles_it_cannot_enforce(
    tmp_path: Path,
    case: str,
    message: str,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, _, original, _ = _registered_similarity_search()
    features, manifest = _advanced_execution_manifest(original, case)
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)
    runtime = replace(
        runtime,
        features={**runtime.features, **{feature: "1.0.0" for feature in features}},
    )

    with pytest.raises(ValueError, match=message):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            manifest.package.digest,
        )


def test_local_executor_fails_when_the_declared_verifier_rejects(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    _, runtime, _, original, _ = _registered_similarity_search()

    class RejectingVerifier:
        identity = ComponentRef("exact-artifact", 1)

        def verify(self, context, candidates):
            return VerificationDecision(
                VerificationStatus.REJECTED,
                self.identity,
                "forced-rejection",
                {},
            )

    definition = WorkloadDefinition(
        original.manifest,
        original.planner,
        original.runners,
        original.reducers,
        {ComponentRef("exact-artifact", 1).canonical: RejectingVerifier()},
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)

    with pytest.raises(ValueError, match="did not pass its declared verifier"):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            original.manifest.package.digest,
        )


def test_local_executor_enforces_the_declared_output_byte_budget(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    runtime = default_sdk_runtime()
    adapter = similarity_search_sdk_adapter(shard_rows=2)
    # The planner pins its own manifest into every task, so the budget cut must
    # be applied to the adapter's manifest for plan and definition to agree.
    adapter.manifest = replace(
        adapter.manifest,
        workflow=replace(adapter.manifest.workflow, max_output_bytes=256),
        limits=replace(adapter.manifest.limits, max_output_bytes=256),
    )
    definition = adapter.definition()
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, store, definition)

    with pytest.raises(ValueError, match="bytes exceed their sink limit"):
        LocalCoreBatchExecutor(registry, runtime, store, tmp_path / "work").execute(
            request,
            definition.manifest.package.digest,
        )
