"""Contract tests for SDK verifier decisions and built-in verifiers."""

from __future__ import annotations

import hashlib
import json
import math
from collections.abc import Callable
from dataclasses import replace
from uuid import NAMESPACE_URL, uuid5

import pytest

from scimesh.sdk import (
    ArtifactCollection,
    ArtifactRef,
    ArtifactSchema,
    CandidateOutput,
    CandidateOutputs,
    CanonicalRecordVerifier,
    ComponentRef,
    ExactArtifactVerifier,
    NumericTolerance,
    NumericToleranceVerifier,
    OutputManifest,
    PortSpec,
    Provenance,
    SchemaRef,
    TrustMode,
    VerificationDecision,
    VerificationBinding,
    VerificationStatus,
    VerifyContext,
    WorkloadId,
)


def _sha256(seed: str) -> str:
    return hashlib.sha256(seed.encode("utf-8")).hexdigest()


OUTPUT_SCHEMA = ArtifactSchema(
    ref=SchemaRef("verification-result", 1),
    media_type="application/json",
    encoding="utf-8",
    max_bytes=1_024,
    validator=ComponentRef("json-document", 1),
)
OUTPUT_PORT = PortSpec(OUTPUT_SCHEMA)
AUTHENTICATION_KEY = b"sdk-verification-test-key-000001"
JOB_ID = str(uuid5(NAMESPACE_URL, "verification-job"))
TASK_ID = str(uuid5(NAMESPACE_URL, "verification-task"))
EXECUTION_CONTRACT_DIGEST = _sha256("execution-contract")


def _artifact(seed: str, *, size_bytes: int = 16) -> ArtifactRef:
    return ArtifactRef(
        artifact_id=str(uuid5(NAMESPACE_URL, f"artifact:{seed}")),
        sha256=_sha256(seed),
        schema=OUTPUT_SCHEMA.ref,
        media_type=OUTPUT_SCHEMA.media_type,
        size_bytes=size_bytes,
    )


def _provenance(attempt: str) -> Provenance:
    return Provenance(
        workload=WorkloadId("verification-fixture", "1.0.0"),
        sdk_api_version="1.0.0",
        protocol_version="1.0",
        manifest_schema_version=1,
        workflow_schema_version=1,
        verifier=ComponentRef("exact-artifact", 1),
        artifact_schemas=(OUTPUT_SCHEMA.ref,),
        package_digest=f"sha256:{_sha256('package')}",
        manifest_digest=_sha256("manifest"),
        environment_digest=f"sha256:{_sha256('environment')}",
        worker_runtime={"attempt": attempt},
        allocated_resource_ids=(f"cpu-{attempt}",),
        parameters_digest=_sha256("parameters"),
        input_collection_digest=_sha256("inputs"),
        execution_contract_digest=EXECUTION_CONTRACT_DIGEST,
        selected_features={"exact-verifier": "1.0.0"},
        optional_fallbacks={},
        job_id=JOB_ID,
        task_id=TASK_ID,
        started_at="2026-08-01T10:00:00Z",
        finished_at="2026-08-01T10:00:01Z",
    )


def _manifest(
    output_seed: str,
    attempt: str,
    *,
    port_name: str = "result",
    size_bytes: int = 16,
) -> OutputManifest:
    return OutputManifest(
        task_key="verify/0",
        outputs={port_name: ArtifactCollection.single(_artifact(output_seed, size_bytes=size_bytes))},
        metrics={"elapsed_seconds": 1.0},
        provenance=_provenance(attempt),
    )


def _candidate(
    output_seed: str,
    attempt: str,
    *,
    owner: str | None,
    candidate_id: str | None = None,
    port_name: str = "result",
    size_bytes: int = 16,
    authenticated: bool = True,
) -> CandidateOutput:
    resolved_candidate_id = candidate_id or str(
        uuid5(NAMESPACE_URL, f"candidate:{attempt}")
    )
    resolved_owner_id = None if owner is None else str(uuid5(NAMESPACE_URL, f"owner:{owner}"))
    manifest = _manifest(
        output_seed,
        attempt,
        port_name=port_name,
        size_bytes=size_bytes,
    )
    if resolved_owner_id is not None and authenticated:
        return CandidateOutput.from_coordinator_record(
            resolved_candidate_id,
            resolved_owner_id,
            manifest,
            AUTHENTICATION_KEY,
        )
    return CandidateOutput(resolved_candidate_id, resolved_owner_id, manifest)


def _context(
    *,
    minimum_matches: int = 1,
    reference: OutputManifest | None = None,
    require_distinct_owners: bool = False,
    trust_mode: TrustMode = TrustMode.TRUSTED,
) -> VerifyContext:
    provenance = _provenance("binding")
    return VerifyContext(
        expected_outputs={"result": OUTPUT_PORT},
        max_output_bytes=1_024,
        minimum_matches=minimum_matches,
        reference=reference,
        require_distinct_owners=require_distinct_owners,
        binding=VerificationBinding(
            workload=provenance.workload,
            task_key="verify/0",
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
        ),
        trust_mode=trust_mode,
    )


def test_verification_decision_is_strict_immutable_and_round_trips() -> None:
    source = {"summary": {"counts": [1, 2]}}
    decision = VerificationDecision(
        VerificationStatus.REJECTED,
        ComponentRef("test-verifier", 1),
        "comparison-failed",
        source,
    )

    source["summary"]["counts"].append(3)  # type: ignore[index, union-attr]

    assert decision.evidence["summary"]["counts"] == (1, 2)
    assert VerificationDecision.from_dict(decision.to_dict()) == decision
    with pytest.raises(TypeError):
        decision.evidence["new"] = True  # type: ignore[index]
    with pytest.raises(TypeError):
        decision.evidence["summary"]["new"] = True  # type: ignore[index]


@pytest.mark.parametrize(
    ("status", "accepted_digest"),
    [
        (VerificationStatus.ACCEPTED, None),
        (VerificationStatus.REJECTED, "a" * 64),
        (VerificationStatus.INCONCLUSIVE, "a" * 64),
    ],
)
def test_verification_decision_enforces_digest_status_invariant(
    status: VerificationStatus,
    accepted_digest: str | None,
) -> None:
    with pytest.raises(ValueError, match="accepted_digest|accepted verification"):
        VerificationDecision(
            status,
            ComponentRef("test-verifier", 1),
            "test-result",
            {},
            accepted_digest,
        )


@pytest.mark.parametrize(
    "unsafe",
    [
        "/home/worker/private.log",
        "https://worker.invalid/evidence",
        "run-123/tasks/map/result.csv",
        "path=attempts/job-123/private.txt",
        "https%253A%252F%252Fworker.invalid%252Fevidence%253Ftoken%253Dsecret",
    ],
)
def test_verification_decision_rejects_private_locations(unsafe: str) -> None:
    with pytest.raises(ValueError, match="URI or local path"):
        VerificationDecision(
            VerificationStatus.REJECTED,
            ComponentRef("test-verifier", 1),
            "unsafe-evidence",
            {"detail": unsafe},
        )


def test_verification_decision_bounds_evidence_and_rejects_unknown_fields() -> None:
    with pytest.raises(ValueError, match="exceeds 16 KiB"):
        VerificationDecision(
            VerificationStatus.REJECTED,
            ComponentRef("test-verifier", 1),
            "oversized-evidence",
            {"detail": "x" * 17_000},
        )

    payload = VerificationDecision(
        VerificationStatus.REJECTED,
        ComponentRef("test-verifier", 1),
        "test-result",
        {},
    ).to_dict()
    payload["unexpected"] = True
    with pytest.raises(ValueError, match="unknown unexpected"):
        VerificationDecision.from_dict(payload)


def test_candidate_output_envelope_is_strict_and_round_trips() -> None:
    candidate = _candidate("output", "attempt-one", owner="owner-one")

    decoded_candidate = CandidateOutput.from_dict(candidate.to_dict())
    assert decoded_candidate.to_dict() == candidate.to_dict()
    assert not decoded_candidate.coordinator_authenticated
    candidates = CandidateOutputs(candidates=(candidate,))
    decoded = CandidateOutputs.from_dict(candidates.to_dict())
    assert not decoded.candidates[0].coordinator_authenticated
    assert CandidateOutputs.from_authenticated_dict(
        candidates.to_dict(), AUTHENTICATION_KEY
    ) == candidates
    with pytest.raises(ValueError, match="authentication failed"):
        CandidateOutputs.from_authenticated_dict(candidates.to_dict(), b"x" * 32)
    assert candidates.manifests == (candidate.manifest,)

    with pytest.raises(ValueError, match="opaque coordinator identity"):
        CandidateOutput("../worker-path", candidate.owner_id, candidate.manifest)


def test_trusted_single_manifest_compatibility_uses_an_anonymous_envelope() -> None:
    manifest = _manifest("output", "trusted")
    candidates = CandidateOutputs((manifest,))

    assert candidates.manifests == (manifest,)
    assert candidates.candidates[0].owner_id is None
    decision = ExactArtifactVerifier().verify(_context(), candidates)
    assert decision.status is VerificationStatus.ACCEPTED


def test_raw_manifests_cannot_form_a_quorum() -> None:
    with pytest.raises(ValueError, match="one trusted candidate"):
        CandidateOutputs(
            (
                _manifest("output", "trusted-one"),
                _manifest("output", "trusted-two"),
            )
        )


def test_multi_vote_context_automatically_requires_distinct_owners() -> None:
    assert _context(minimum_matches=2).require_distinct_owners
    assert _context(require_distinct_owners=True).require_distinct_owners

    with pytest.raises(ValueError, match="boolean"):
        _context(require_distinct_owners=1)  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="coordinator binding"):
        VerifyContext(
            expected_outputs={"result": OUTPUT_PORT},
            max_output_bytes=1_024,
            minimum_matches=2,
        )

    with pytest.raises(ValueError, match="at least two"):
        _context(trust_mode=TrustMode.UNTRUSTED_QUORUM)


def test_exact_verifier_reports_no_candidates_as_inconclusive() -> None:
    decision = ExactArtifactVerifier().verify(_context(), CandidateOutputs(()))

    assert decision.status is VerificationStatus.INCONCLUSIVE
    assert decision.reason_code == "no-candidates"
    assert decision.evidence == {"candidate_count": 0, "invalid_count": 0}
    assert decision.accepted_digest is None


def test_exact_verifier_rejects_candidates_that_violate_output_contract() -> None:
    invalid = _manifest("same-output", "invalid", port_name="undeclared")

    decision = ExactArtifactVerifier().verify(_context(), CandidateOutputs((invalid,)))

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "no-valid-candidates"
    assert decision.evidence == {"candidate_count": 1, "invalid_count": 1}


def test_exact_verifier_rejects_candidate_from_another_scientific_binding() -> None:
    candidate = _candidate("same-output", "other-job", owner="owner-one")
    forged_provenance = replace(
        candidate.manifest.provenance,
        parameters_digest=_sha256("different-parameters"),
    )
    forged = replace(
        candidate,
        manifest=replace(candidate.manifest, provenance=forged_provenance),
    )

    decision = ExactArtifactVerifier().verify(_context(), CandidateOutputs((forged,)))

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "no-valid-candidates"


def test_exact_verifier_accepts_unique_quorum_and_ignores_invalid_candidates() -> None:
    first = _candidate("same-output", "one", owner="owner-one")
    second = _candidate("same-output", "two", owner="owner-two")
    minority = _candidate("different-output", "three", owner="owner-three")
    invalid = _candidate(
        "same-output",
        "four",
        owner="owner-four",
        port_name="undeclared",
    )

    decision = ExactArtifactVerifier().verify(
        _context(minimum_matches=2),
        CandidateOutputs((first, second, minority, invalid)),
    )

    assert first.manifest.digest == second.manifest.digest
    assert first.manifest.manifest_digest != second.manifest.manifest_digest
    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.reason_code == "quorum-match"
    assert decision.accepted_digest == first.manifest.digest
    assert decision.evidence == {
        "matched": 2,
        "required": 2,
        "distinct_digests": 2,
        "invalid_count": 1,
    }


def test_exact_verifier_rejects_conflicting_quorums() -> None:
    candidates = CandidateOutputs(
        (
            _candidate("group-a", "a-one", owner="a-one"),
            _candidate("group-a", "a-two", owner="a-two"),
            _candidate("group-b", "b-one", owner="b-one"),
            _candidate("group-b", "b-two", owner="b-two"),
        )
    )

    decision = ExactArtifactVerifier().verify(_context(minimum_matches=2), candidates)

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "conflicting-quorums"
    assert decision.accepted_digest is None
    assert decision.evidence["largest_group"] == 2
    assert decision.evidence["distinct_digests"] == 2


def test_exact_verifier_distinguishes_insufficient_evidence_from_reference_mismatch() -> None:
    reference = _manifest("reference", "reference")
    one_mismatch = CandidateOutputs((_candidate("other", "one", owner="owner-one"),))
    two_mismatches = CandidateOutputs(
        (
            _candidate("other-a", "two", owner="owner-two"),
            _candidate("other-b", "three", owner="owner-three"),
        )
    )

    insufficient = ExactArtifactVerifier().verify(
        _context(minimum_matches=2, reference=reference),
        one_mismatch,
    )
    rejected = ExactArtifactVerifier().verify(
        _context(minimum_matches=2, reference=reference),
        two_mismatches,
    )

    assert (insufficient.status, insufficient.reason_code) == (
        VerificationStatus.INCONCLUSIVE,
        "insufficient-evidence",
    )
    assert (rejected.status, rejected.reason_code) == (
        VerificationStatus.REJECTED,
        "reference-mismatch",
    )


def test_exact_verifier_accepts_declared_reference_quorum() -> None:
    reference = _manifest("reference", "reference")
    candidates = CandidateOutputs(
        (
            _candidate("reference", "worker-one", owner="owner-one"),
            _candidate("reference", "worker-two", owner="owner-two"),
            _candidate("other", "worker-three", owner="owner-three"),
        )
    )

    decision = ExactArtifactVerifier().verify(
        _context(minimum_matches=2, reference=reference),
        candidates,
    )

    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.reason_code == "reference-match"
    assert decision.accepted_digest == reference.digest
    assert decision.evidence == {"matched": 2, "required": 2, "invalid_count": 0}


def test_candidate_outputs_rejects_duplicate_candidate_ids() -> None:
    candidate = _candidate("output", "one", owner="owner-one")
    replay = _candidate(
        "different-output",
        "two",
        owner="owner-two",
        candidate_id=candidate.candidate_id,
    )

    with pytest.raises(ValueError, match="candidate_id values must be unique"):
        CandidateOutputs((candidate, replay))


def test_exact_quorum_rejects_candidates_without_authenticated_owners() -> None:
    candidates = CandidateOutputs(
        (
            _candidate("output", "one", owner=None),
            _candidate("output", "two", owner="owner-two"),
        )
    )

    decision = ExactArtifactVerifier().verify(
        _context(minimum_matches=2),
        candidates,
    )

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "coordinator-authentication-required"
    assert decision.evidence == {"candidate_count": 2, "unauthenticated_count": 1}


def test_exact_verifier_counts_at_most_one_vote_per_owner() -> None:
    candidates = CandidateOutputs(
        (
            _candidate("output", "one", owner="same-owner"),
            _candidate("output", "two", owner="same-owner"),
        )
    )

    decision = ExactArtifactVerifier().verify(
        _context(minimum_matches=2),
        candidates,
    )

    assert decision.status is VerificationStatus.INCONCLUSIVE
    assert decision.reason_code == "insufficient-evidence"
    assert decision.evidence == {
        "largest_group": 1,
        "required": 2,
        "distinct_digests": 1,
        "invalid_count": 0,
        "duplicate_owner_candidates": 1,
    }


def test_exact_verifier_rejects_owner_equivocation_without_leaking_identity() -> None:
    owner = "equivocating-owner"
    candidates = CandidateOutputs(
        (
            _candidate("output-a", "one", owner=owner),
            _candidate("output-b", "two", owner=owner),
            _candidate("output-a", "three", owner="honest-owner"),
        )
    )

    decision = ExactArtifactVerifier().verify(
        _context(minimum_matches=2),
        candidates,
    )

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "owner-equivocation"
    assert decision.accepted_digest is None
    assert decision.evidence == {
        "candidate_count": 3,
        "equivocating_owner_count": 1,
    }
    assert (candidates.candidates[0].owner_id or "") not in json.dumps(decision.to_dict())


def test_numeric_verifier_accepts_nested_values_with_absolute_and_relative_tolerance() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance(absolute=0.001, relative=0.01))

    decision = verifier.verify_values(
        {"energies": [10.0, 0.05], "converged": True},
        {"energies": [10.05, 0.0505], "converged": True},
    )

    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.reason_code == "within-tolerance"
    assert decision.accepted_digest is not None
    assert decision.evidence == {
        "absolute": 0.001,
        "relative": 0.01,
        "max_ulps": 0,
    }


def test_numeric_verifier_supports_ulp_tolerance() -> None:
    adjacent = math.nextafter(1.0, 2.0)
    verifier = NumericToleranceVerifier(NumericTolerance(max_ulps=1))

    decision = verifier.verify_values(1.0, adjacent)

    assert decision.status is VerificationStatus.ACCEPTED


def test_numeric_verifier_reports_bounded_location_and_error_evidence() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance(absolute=0.1))

    decision = verifier.verify_values(
        {"matrix": [[1.0, 2.0]]},
        {"matrix": [[1.0, 2.5]]},
    )

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "numeric-mismatch"
    assert decision.evidence["location"] == "$.matrix[0][1]"
    assert decision.evidence["absolute_error"] == pytest.approx(0.5)
    assert decision.evidence["allowed_error"] == pytest.approx(0.1)
    assert isinstance(decision.evidence["ulp_distance"], int)


def test_numeric_verifier_rejects_shape_changes_before_value_comparison() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance())

    decision = verifier.verify_values(
        {"energy": 1.0, "iterations": 4},
        {"energy": 1.0, "converged": True},
    )

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "shape-mismatch"
    assert decision.evidence == {
        "location": "$",
        "missing_keys": ("iterations",),
        "extra_keys": ("converged",),
    }


def test_numeric_verifier_applies_declared_nan_policy() -> None:
    reject = NumericToleranceVerifier(NumericTolerance(nan_policy="reject"))
    equal = NumericToleranceVerifier(NumericTolerance(nan_policy="equal"))

    rejected = reject.verify_values(float("nan"), float("nan"))
    accepted = equal.verify_values(float("nan"), float("nan"))

    assert (rejected.status, rejected.reason_code) == (
        VerificationStatus.REJECTED,
        "nan-policy",
    )
    assert accepted.status is VerificationStatus.ACCEPTED


@pytest.mark.parametrize(
    "factory",
    [
        lambda: NumericTolerance(absolute=-0.1),
        lambda: NumericTolerance(relative=float("inf")),
        lambda: NumericTolerance(max_ulps=True),
        lambda: NumericTolerance(nan_policy="propagate"),
        lambda: NumericTolerance(max_elements=0),
    ],
)
def test_numeric_tolerance_rejects_ambiguous_or_non_finite_policy(
    factory: Callable[[], NumericTolerance],
) -> None:
    with pytest.raises(ValueError, match="numeric tolerance"):
        factory()


def test_numeric_verifier_rejects_unrepresentable_integer_without_raising() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance())

    decision = verifier.verify_values(10**10_000, 10**10_000)

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "numeric-range"
    assert decision.evidence["location"] == "$"


def test_numeric_verifier_rejects_shapes_above_its_manifest_bound() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance(max_elements=3))

    decision = verifier.verify_values([1, 2, 3], [1, 2, 3])

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "element-limit"
    assert decision.evidence == {"max_elements": 3}
    assert verifier.configuration["max_elements"] == 3


def test_numeric_verifier_does_not_round_mixed_integer_and_float_values() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance())

    decision = verifier.verify_values(2**53 + 1, float(2**53))

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "numeric-mismatch"


def test_numeric_verifier_rejects_non_json_mapping_keys() -> None:
    verifier = NumericToleranceVerifier(NumericTolerance())

    decision = verifier.verify_values({1: 2.0}, {1: 2.0})

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "type-mismatch"


def test_numeric_verifier_implements_manifest_verifier_protocol_with_loader() -> None:
    reference = _manifest("reference", "reference")
    candidate = _manifest("candidate", "candidate")
    values = {
        reference.outputs["result"].items[0].artifact.sha256: {"energy": 1.0},
        candidate.outputs["result"].items[0].artifact.sha256: {"energy": 1.0001},
    }

    def load(manifest: OutputManifest) -> object:
        return values[manifest.outputs["result"].items[0].artifact.sha256]

    verifier = NumericToleranceVerifier(NumericTolerance(absolute=0.001), load)
    decision = verifier.verify(
        _context(reference=reference),
        CandidateOutputs((candidate,)),
    )

    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.accepted_digest == reference.digest


def test_manifest_verifiers_fail_closed_without_artifact_loaders() -> None:
    candidate = CandidateOutputs((_manifest("candidate", "candidate"),))

    numeric = NumericToleranceVerifier(NumericTolerance()).verify(_context(), candidate)
    canonical = CanonicalRecordVerifier(_canonical_json_record).verify(_context(), candidate)

    assert numeric.reason_code == "loader-unavailable"
    assert canonical.reason_code == "loader-unavailable"


def _canonical_json_record(record: object) -> bytes:
    return json.dumps(record, sort_keys=True, separators=(",", ":")).encode("utf-8")


def test_canonical_record_verifier_accepts_normalized_records() -> None:
    verifier = CanonicalRecordVerifier(_canonical_json_record)
    expected = [{"name": "molecule", "score": 0.75}, {"id": 2}]
    actual = [{"score": 0.75, "name": "molecule"}, {"id": 2}]

    decision = verifier.verify_records(expected, actual)

    assert decision.status is VerificationStatus.ACCEPTED
    assert decision.reason_code == "canonical-match"
    assert decision.evidence == {"records": 2}
    assert decision.accepted_digest is not None


def test_canonical_record_verifier_rejects_content_or_count_mismatch() -> None:
    verifier = CanonicalRecordVerifier(_canonical_json_record)

    decision = verifier.verify_records([{"id": 1}, {"id": 2}], [{"id": 1}])

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "canonical-mismatch"
    assert decision.evidence == {"expected_records": 2, "actual_records": 1}


def test_canonical_record_verifier_enforces_record_limit() -> None:
    verifier = CanonicalRecordVerifier(_canonical_json_record, max_records=2)

    decision = verifier.verify_records([1, 2, 3], [1, 2, 3])

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "record-limit"
    assert decision.evidence == {"max_records": 2}


@pytest.mark.parametrize(
    "canonicalizer",
    [
        lambda _record: "not-bytes",
        lambda _record: (_ for _ in ()).throw(ValueError("/private/worker/path")),
    ],
)
def test_canonical_record_verifier_sanitizes_canonicalization_failures(
    canonicalizer: Callable[[object], object],
) -> None:
    verifier = CanonicalRecordVerifier(canonicalizer)  # type: ignore[arg-type]

    decision = verifier.verify_records([{"id": 1}], [{"id": 1}])

    assert decision.status is VerificationStatus.REJECTED
    assert decision.reason_code == "canonicalization-failed"
    assert decision.evidence == {}
    assert decision.accepted_digest is None
