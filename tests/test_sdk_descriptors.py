"""Tests for the SDK-native descriptor-batch reference workload."""

from __future__ import annotations

import csv
from dataclasses import replace
from pathlib import Path

import pytest

from scimesh.sdk import (
    AllowedPackage,
    ArtifactCollection,
    CandidateOutput,
    CandidateOutputs,
    DeterminismProfile,
    ExactArtifactVerifier,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    LocalPlanningContext,
    TrustMode,
    VerificationBinding,
    VerificationStatus,
    VerifyContext,
    WorkloadRegistry,
    assert_manifest_round_trip,
)
from scimesh.workloads.descriptors import (
    DESCRIPTOR_COLUMNS,
    descriptor_batch_sdk_definition,
    compute_descriptor_batch,
)
from scimesh.workloads.library import default_sdk_runtime


def _write_tiny_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\textra\n"
        "ALCOHOL\tCCO\talcohol\n"
        "ALKANE\tCCCC\talkane\n"
        "AMINE\tCCN\tamine\n"
        "BROKEN\tnot-a-smiles\tinvalid\n"
        "HEXANE\tCCCCCC\thexane\n",
        encoding="utf-8",
    )


def _registered_descriptor_batch(shard_rows: int = 2):
    workload = descriptor_batch_sdk_definition(shard_rows=shard_rows)
    registry = WorkloadRegistry()
    registry.register(workload.definition(), enabled=True)
    runtime = default_sdk_runtime()
    definition, negotiated = registry.require(
        workload.manifest.workload.name,
        workload.manifest.workload.version,
        workload.manifest.package.digest,
        runtime=runtime,
    )
    return registry, runtime, workload, definition, negotiated


def _request_for(
    dataset: Path,
    artifact_store: LocalArtifactStore,
    workload,
    *,
    skip_invalid: bool = True,
) -> JobRequest:
    input_port = workload.manifest.inputs["input"]
    dataset_artifact = artifact_store.import_file(
        dataset,
        declaration=input_port.schema,
    )
    return JobRequest(
        workload=workload.manifest.workload,
        parameters={"skip_invalid": skip_invalid},
        inputs={"input": ArtifactCollection.single(dataset_artifact)},
    )


def test_descriptor_batch_manifest_is_registered_and_negotiable() -> None:
    _, runtime, workload, definition, negotiated = _registered_descriptor_batch()
    manifest = definition.manifest

    assert manifest.workload.name == "descriptor-batch"
    assert manifest.workload.version == "1.0.0"
    assert manifest.determinism is DeterminismProfile.BYTE_EXACT
    assert manifest.verifier.verifier.canonical == "exact-artifact@1"
    assert set(mode.value for mode in manifest.trust_modes) == {
        "trusted",
        "untrusted_quorum",
    }
    assert manifest.conformance_profiles == ("core-batch-v1",)
    assert manifest.capabilities == ("descriptor-batch",)
    assert [stage.kind.value for stage in manifest.workflow.stages] == ["map", "reduce"]
    assert set(definition.runners) == {manifest.workflow.stages[0].entry_point}
    assert set(definition.reducers) == {manifest.workflow.stages[1].entry_point}
    assert negotiated is not None
    assert negotiated.manifest == manifest
    assert_manifest_round_trip(manifest)
    assert runtime is not None


def test_local_sdk_executor_matches_descriptor_batch_reference(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    manifest = workload.manifest
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, workload)

    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, definition.manifest.package.digest)
    result_artifact = result.outputs["result"].items[0].artifact

    reference_path = tmp_path / "reference.csv"
    reference_metrics = compute_descriptor_batch(
        dataset, reference_path, skip_invalid=True
    )

    assert result.task_key == "reduce/final"
    assert dict(result.metrics) == {
        "partial_count": 3,
        "rows_emitted": reference_metrics["rows_emitted"],
    }
    assert (
        artifact_store.materialize(result_artifact).read_bytes()
        == reference_path.read_bytes()
    )

    with artifact_store.materialize(result_artifact).open(
        encoding="utf-8", newline=""
    ) as source:
        rows = list(csv.reader(source))
    assert rows[0] == list(DESCRIPTOR_COLUMNS)
    assert [row[0] for row in rows[1:]] == ["ALCOHOL", "ALKANE", "AMINE", "HEXANE"]
    assert len(rows[1:]) == reference_metrics["rows_emitted"]
    assert any(len(row) == len(DESCRIPTOR_COLUMNS) for row in rows[1:])


def test_descriptor_batch_planning_is_deterministic_ordered_and_path_free(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, workload)
    input_artifact = request.inputs["input"].items[0].artifact

    first = registry.plan(
        request,
        definition.manifest.package.digest,
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
        definition.manifest.package.digest,
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
    assert [task.task_key for task in first.tasks] == [
        "map/00000000",
        "map/00000001",
        "map/00000002",
    ]
    assert all(task.stage_id == "map" for task in first.tasks)
    assert all(task.parameters == {"skip_invalid": True} for task in first.tasks)
    assert all(task.package_digest == first.package_digest for task in first.tasks)
    assert all(task.manifest_digest == first.manifest_digest for task in first.tasks)

    shard_ids: list[list[str]] = []
    for task in first.tasks:
        artifact = task.inputs["input"].items[0].artifact
        with artifact_store.materialize(artifact).open(
            encoding="utf-8", newline=""
        ) as source:
            shard_ids.append(
                [row["chembl_id"] for row in csv.DictReader(source, delimiter="\t")]
            )
    assert shard_ids == [
        ["ALCOHOL", "ALKANE"],
        ["AMINE", "BROKEN"],
        ["HEXANE"],
    ]

    wire_payload = first.to_json()
    assert str(tmp_path) not in wire_payload
    assert "file://" not in wire_payload
    assert "worker://" not in wire_payload
    assert "workspace" not in wire_payload


def test_descriptor_batch_skip_invalid_policy_is_explicit(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, workload, skip_invalid=False)

    with pytest.raises(ValueError, match="invalid canonical_smiles"):
        LocalCoreBatchExecutor(
            registry, runtime, artifact_store, tmp_path / "work"
        ).execute(
            request,
            definition.manifest.package.digest,
        )


def test_descriptor_batch_rejects_unknown_or_mistyped_parameters(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    base = _request_for(dataset, artifact_store, workload)

    for bad_parameters, message in (
        ({"skip_invalid": True, "bogus": 1}, "unknown field"),
        ({"skip_invalid": "yes"}, "type mismatch"),
    ):
        request = replace(base, parameters=bad_parameters)
        with pytest.raises(ValueError, match=message):
            registry.plan(
                request,
                definition.manifest.package.digest,
                runtime,
                LocalPlanningContext(
                    artifact_store,
                    artifact_store,
                    tmp_path / "bad-plan",
                    allowed_artifacts=(base.inputs["input"].items[0].artifact,),
                ),
            )


def _binding_from(provenance, trust_mode: TrustMode) -> VerificationBinding:
    return VerificationBinding(
        workload=provenance.workload,
        task_key="reduce/final",
        package_digest=provenance.package_digest,
        manifest_digest=provenance.manifest_digest,
        environment_digest=provenance.environment_digest,
        parameters_digest=provenance.parameters_digest,
        input_collection_digest=provenance.input_collection_digest,
        execution_contract_digest=provenance.execution_contract_digest,
        selected_features=provenance.selected_features,
        optional_fallbacks=provenance.optional_fallbacks,
        job_id=provenance.job_id,
        task_id=provenance.task_id,
        verifier=provenance.verifier,
        sdk_api_version=provenance.sdk_api_version,
        protocol_version=provenance.protocol_version,
        manifest_schema_version=provenance.manifest_schema_version,
        workflow_schema_version=provenance.workflow_schema_version,
        artifact_schemas=provenance.artifact_schemas,
        trust_mode=trust_mode,
    )


def _candidate_for(
    manifest,
    candidate_id: str,
    owner_id: str,
    authentication_key: bytes,
) -> CandidateOutput:
    return CandidateOutput.from_coordinator_record(
        candidate_id,
        owner_id,
        manifest,
        authentication_key,
    )


def test_descriptor_batch_accepts_two_owner_quorum_on_identical_outputs(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, workload)

    final = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, definition.manifest.package.digest)

    provenance = replace(
        final.provenance,
        trust_mode="untrusted_quorum",
        worker_runtime={"kind": "worker-one"},
    )
    first = replace(final, provenance=provenance)
    second = replace(
        final,
        provenance=replace(
            provenance,
            worker_runtime={"kind": "worker-two"},
        ),
    )
    binding = _binding_from(provenance, TrustMode.UNTRUSTED_QUORUM)
    assert binding.matches(first)
    assert binding.matches(second)

    key = b"coordinator-authentication-key-32-bytes"
    decision = ExactArtifactVerifier().verify(
        VerifyContext(
            expected_outputs=definition.manifest.outputs,
            max_output_bytes=definition.manifest.limits.max_output_bytes,
            minimum_matches=2,
            binding=binding,
            trust_mode=TrustMode.UNTRUSTED_QUORUM,
        ),
        CandidateOutputs(
            (
                _candidate_for(first, "candidate-one", "owner-one", key),
                _candidate_for(second, "candidate-two", "owner-two", key),
            )
        ),
    )

    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.reason_code == "quorum-match"
    assert decision.accepted_digest == first.digest
    assert decision.evidence["matched"] == 2
    assert decision.evidence["distinct_digests"] == 1


def test_descriptor_batch_quorum_rejects_conflicting_worker_outputs(
    tmp_path: Path,
) -> None:
    dataset = tmp_path / "molecules.tsv"
    _write_tiny_dataset(dataset)
    registry, runtime, workload, definition, _ = _registered_descriptor_batch()
    artifact_store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request_for(dataset, artifact_store, workload)

    final = LocalCoreBatchExecutor(
        registry,
        runtime,
        artifact_store,
        tmp_path / "sdk-work",
    ).execute(request, definition.manifest.package.digest)
    provenance = replace(
        final.provenance,
        trust_mode="untrusted_quorum",
        worker_runtime={"kind": "worker-one"},
    )
    good = replace(final, provenance=provenance)
    forged_output = replace(
        good.outputs["result"],
        items=(
            replace(
                good.outputs["result"].items[0],
                artifact=replace(
                    good.outputs["result"].items[0].artifact,
                    sha256="a" * 64,
                ),
            ),
        ),
    )
    bad = replace(good, outputs={"result": forged_output})
    assert bad.digest != good.digest
    binding = _binding_from(provenance, TrustMode.UNTRUSTED_QUORUM)
    assert binding.matches(bad)

    key = b"coordinator-authentication-key-32-bytes"
    decision = ExactArtifactVerifier().verify(
        VerifyContext(
            expected_outputs=definition.manifest.outputs,
            max_output_bytes=definition.manifest.limits.max_output_bytes,
            minimum_matches=2,
            binding=binding,
            trust_mode=TrustMode.UNTRUSTED_QUORUM,
        ),
        CandidateOutputs(
            (
                _candidate_for(good, "candidate-good-one", "owner-one", key),
                _candidate_for(good, "candidate-good-two", "owner-two", key),
                _candidate_for(bad, "candidate-bad-one", "owner-three", key),
                _candidate_for(bad, "candidate-bad-two", "owner-four", key),
            )
        ),
    )

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "conflicting-quorums"
    assert decision.evidence["largest_group"] == 2
    assert decision.evidence["distinct_digests"] == 2
    assert decision.accepted_digest is None


def test_descriptor_batch_discovery_imports_an_allowlisted_installed_entry_point(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from importlib import metadata

    from scimesh.sdk.registry import WorkloadRegistry as RegistryClass

    definition = descriptor_batch_sdk_definition().definition()
    loaded: list[str] = []

    class EntryPoint:
        name = "descriptor-batch@1.0.0"
        dist = metadata.distribution("scimesh")
        value = "scimesh.workloads.descriptors:workload_definition"

        @property
        def module(self) -> str:
            return self.value.partition(":")[0]

        def load(self):
            loaded.append(self.name)
            return lambda: definition

    class EntryPoints:
        def __init__(self, values: tuple) -> None:
            self._values = values

        def __iter__(self):
            return iter(self._values)

        def select(self, *, group: str):
            assert group == RegistryClass.ENTRY_POINT_GROUP
            return self

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: EntryPoints((EntryPoint(),)),
    )
    monkeypatch.setattr(
        "scimesh.sdk.registry.installed_distribution_digest",
        lambda _distribution: definition.manifest.package.digest,
    )
    registry = WorkloadRegistry()
    registry.discover_installed(
        (
            AllowedPackage(
                "scimesh",
                definition.manifest.workload,
                definition.manifest.package.digest,
            ),
        )
    )

    assert loaded == ["descriptor-batch@1.0.0"]
    description = registry.descriptions()[0]
    assert description.workload.name == "descriptor-batch"
    assert description.enabled
