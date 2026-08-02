"""Versioned verifier decisions and core exact/numeric implementations."""

from __future__ import annotations

import json
import hashlib
import hmac
import math
import re
import struct
from decimal import Decimal
from dataclasses import dataclass, field
from enum import Enum
from types import MappingProxyType
from typing import Any, Callable, Iterable, Mapping, Sequence, cast

from ._validation import (
    enum_value,
    canonical_json,
    freeze_json_mapping,
    require_exact_keys,
    require_identifier,
    require_positive_int,
    require_sha256,
    require_string,
    require_task_key,
    parse_release,
    thaw_json,
)
from .artifacts import OutputManifest, PortSpec
from .identity import ComponentRef, SchemaRef, WorkloadId
from .manifest import TrustMode


class VerificationStatus(str, Enum):
    """The outcome of verification.

    Only ``ACCEPTED`` satisfies a stage; ``INCONCLUSIVE`` must never be
    treated as success by a reducer default.
    """

    ACCEPTED = "accepted"
    REJECTED = "rejected"
    INCONCLUSIVE = "inconclusive"


@dataclass(frozen=True, slots=True)
class VerificationDecision:
    """An immutable verifier outcome with bounded sanitized evidence.

    ``accepted_digest`` is set only for accepted decisions; evidence is
    limited to 16 KiB and rejects local paths and transport URLs.
    """

    status: VerificationStatus
    verifier: ComponentRef
    reason_code: str
    evidence: Mapping[str, Any]
    accepted_digest: str | None = None

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "status",
            enum_value(VerificationStatus, self.status, "verification.status"),
        )
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("verification verifier must be a ComponentRef")
        object.__setattr__(
            self,
            "reason_code",
            require_identifier(self.reason_code, "verification.reason_code"),
        )
        evidence = freeze_json_mapping(
            self.evidence, "verification.evidence", forbid_locations=True
        )
        if (
            len(
                json.dumps(thaw_json(evidence), sort_keys=True, allow_nan=False).encode(
                    "utf-8"
                )
            )
            > 16_384
        ):
            raise ValueError("verification evidence exceeds 16 KiB")
        object.__setattr__(self, "evidence", evidence)
        if self.accepted_digest is not None:
            object.__setattr__(
                self,
                "accepted_digest",
                require_sha256(self.accepted_digest, "accepted_digest"),
            )
        if self.status is VerificationStatus.ACCEPTED and self.accepted_digest is None:
            raise ValueError("accepted verification requires an accepted_digest")
        if (
            self.status is not VerificationStatus.ACCEPTED
            and self.accepted_digest is not None
        ):
            raise ValueError("only accepted verification may carry an accepted_digest")

    def to_dict(self) -> dict[str, object]:
        return {
            "status": self.status.value,
            "verifier": self.verifier.canonical,
            "reason_code": self.reason_code,
            "evidence": thaw_json(self.evidence),
            "accepted_digest": self.accepted_digest,
        }

    @classmethod
    def from_dict(cls, value: object) -> "VerificationDecision":
        if not isinstance(value, Mapping):
            raise ValueError("verification decision must be an object")
        fields = {"status", "verifier", "reason_code", "evidence", "accepted_digest"}
        require_exact_keys(value, fields, "verification decision")
        return cls(
            status=value["status"],  # type: ignore[arg-type]
            verifier=ComponentRef.from_dict(value["verifier"]),
            reason_code=value["reason_code"],  # type: ignore[arg-type]
            evidence=value["evidence"],  # type: ignore[arg-type]
            accepted_digest=value["accepted_digest"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class VerificationBinding:
    """Coordinator-owned scientific identity shared by accepted attempts."""

    workload: WorkloadId
    task_key: str
    package_digest: str
    manifest_digest: str
    environment_digest: str
    parameters_digest: str
    input_collection_digest: str
    execution_contract_digest: str
    selected_features: Mapping[str, str]
    optional_fallbacks: Mapping[str, str]
    job_id: str
    task_id: str
    verifier: ComponentRef
    sdk_api_version: str
    protocol_version: str
    manifest_schema_version: int
    workflow_schema_version: int
    artifact_schemas: tuple[SchemaRef, ...]
    trust_mode: TrustMode

    def __post_init__(self) -> None:
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("verification binding workload must be a WorkloadId")
        object.__setattr__(
            self,
            "task_key",
            require_task_key(self.task_key, "verification task_key"),
        )
        object.__setattr__(
            self,
            "package_digest",
            require_sha256(
                self.package_digest, "verification package_digest", prefixed=True
            ),
        )
        object.__setattr__(
            self,
            "manifest_digest",
            require_sha256(self.manifest_digest, "verification manifest_digest"),
        )
        object.__setattr__(
            self,
            "environment_digest",
            require_sha256(
                self.environment_digest,
                "verification environment_digest",
                prefixed=True,
            ),
        )
        object.__setattr__(
            self,
            "parameters_digest",
            require_sha256(self.parameters_digest, "verification parameters_digest"),
        )
        object.__setattr__(
            self,
            "input_collection_digest",
            require_sha256(
                self.input_collection_digest,
                "verification input_collection_digest",
            ),
        )
        object.__setattr__(
            self,
            "execution_contract_digest",
            require_sha256(
                self.execution_contract_digest,
                "verification execution_contract_digest",
            ),
        )
        selected_features = freeze_json_mapping(
            self.selected_features,
            "verification selected_features",
        )
        optional_fallbacks = freeze_json_mapping(
            self.optional_fallbacks,
            "verification optional_fallbacks",
        )
        for name, version in selected_features.items():
            require_identifier(name, "verification selected feature")
            require_string(
                version, "verification selected feature version", max_length=32
            )
            parse_release(version, "verification selected feature version")
        for name, fallback in optional_fallbacks.items():
            require_identifier(name, "verification fallback feature")
            require_identifier(fallback, "verification fallback")
        if set(selected_features).intersection(optional_fallbacks):
            raise ValueError("verification feature cannot be selected and fallbacked")
        object.__setattr__(self, "selected_features", selected_features)
        object.__setattr__(self, "optional_fallbacks", optional_fallbacks)
        from ._validation import require_uuid

        object.__setattr__(
            self, "job_id", require_uuid(self.job_id, "verification job_id")
        )
        object.__setattr__(
            self, "task_id", require_uuid(self.task_id, "verification task_id")
        )
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("verification binding verifier must be a ComponentRef")
        object.__setattr__(
            self,
            "sdk_api_version",
            require_string(
                self.sdk_api_version, "verification sdk_api_version", max_length=32
            ),
        )
        object.__setattr__(
            self,
            "protocol_version",
            require_string(
                self.protocol_version, "verification protocol_version", max_length=32
            ),
        )
        parse_release(self.sdk_api_version, "verification sdk_api_version")
        parse_release(self.protocol_version, "verification protocol_version")
        object.__setattr__(
            self,
            "manifest_schema_version",
            require_positive_int(
                self.manifest_schema_version,
                "verification manifest_schema_version",
            ),
        )
        object.__setattr__(
            self,
            "workflow_schema_version",
            require_positive_int(
                self.workflow_schema_version,
                "verification workflow_schema_version",
            ),
        )
        schemas = tuple(self.artifact_schemas)
        if not schemas or any(not isinstance(schema, SchemaRef) for schema in schemas):
            raise ValueError(
                "verification artifact_schemas must contain schema identities"
            )
        if len(schemas) != len(set(schemas)) or schemas != tuple(
            sorted(schemas, key=lambda schema: schema.canonical)
        ):
            raise ValueError(
                "verification artifact_schemas must be unique and canonical"
            )
        object.__setattr__(self, "artifact_schemas", schemas)
        try:
            trust_mode = TrustMode(self.trust_mode)
        except (TypeError, ValueError) as error:
            raise ValueError("verification trust_mode is unsupported") from error
        object.__setattr__(self, "trust_mode", trust_mode)

    def matches(self, manifest: OutputManifest) -> bool:
        if not isinstance(manifest, OutputManifest):
            return False
        provenance = manifest.provenance
        return (
            manifest.task_key == self.task_key
            and provenance.workload == self.workload
            and provenance.package_digest == self.package_digest
            and provenance.manifest_digest == self.manifest_digest
            and provenance.environment_digest == self.environment_digest
            and provenance.parameters_digest == self.parameters_digest
            and provenance.input_collection_digest == self.input_collection_digest
            and provenance.execution_contract_digest == self.execution_contract_digest
            and provenance.selected_features == self.selected_features
            and provenance.optional_fallbacks == self.optional_fallbacks
            and provenance.job_id == self.job_id
            and provenance.task_id == self.task_id
            and provenance.verifier == self.verifier
            and provenance.sdk_api_version == self.sdk_api_version
            and provenance.protocol_version == self.protocol_version
            and provenance.manifest_schema_version == self.manifest_schema_version
            and provenance.workflow_schema_version == self.workflow_schema_version
            and provenance.artifact_schemas == self.artifact_schemas
            and provenance.trust_mode == self.trust_mode.value
        )

    def to_dict(self) -> dict[str, object]:
        return {
            "workload": self.workload.to_dict(),
            "task_key": self.task_key,
            "package_digest": self.package_digest,
            "manifest_digest": self.manifest_digest,
            "environment_digest": self.environment_digest,
            "parameters_digest": self.parameters_digest,
            "input_collection_digest": self.input_collection_digest,
            "execution_contract_digest": self.execution_contract_digest,
            "selected_features": thaw_json(self.selected_features),
            "optional_fallbacks": thaw_json(self.optional_fallbacks),
            "job_id": self.job_id,
            "task_id": self.task_id,
            "verifier": self.verifier.canonical,
            "sdk_api_version": self.sdk_api_version,
            "protocol_version": self.protocol_version,
            "manifest_schema_version": self.manifest_schema_version,
            "workflow_schema_version": self.workflow_schema_version,
            "artifact_schemas": [schema.canonical for schema in self.artifact_schemas],
            "trust_mode": self.trust_mode.value,
        }

    @classmethod
    def from_dict(cls, value: object) -> "VerificationBinding":
        if not isinstance(value, Mapping):
            raise ValueError("verification binding must be an object")
        fields = {
            "workload",
            "task_key",
            "package_digest",
            "manifest_digest",
            "environment_digest",
            "parameters_digest",
            "input_collection_digest",
            "execution_contract_digest",
            "selected_features",
            "optional_fallbacks",
            "job_id",
            "task_id",
            "verifier",
            "sdk_api_version",
            "protocol_version",
            "manifest_schema_version",
            "workflow_schema_version",
            "artifact_schemas",
            "trust_mode",
        }
        require_exact_keys(value, fields, "verification binding")
        schemas = value["artifact_schemas"]
        if not isinstance(schemas, list):
            raise ValueError("verification artifact_schemas must be an array")
        return cls(
            workload=WorkloadId.from_dict(value["workload"]),
            task_key=value["task_key"],  # type: ignore[arg-type]
            package_digest=value["package_digest"],  # type: ignore[arg-type]
            manifest_digest=value["manifest_digest"],  # type: ignore[arg-type]
            environment_digest=value["environment_digest"],  # type: ignore[arg-type]
            parameters_digest=value["parameters_digest"],  # type: ignore[arg-type]
            input_collection_digest=value["input_collection_digest"],  # type: ignore[arg-type]
            execution_contract_digest=value["execution_contract_digest"],  # type: ignore[arg-type]
            selected_features=value["selected_features"],  # type: ignore[arg-type]
            optional_fallbacks=value["optional_fallbacks"],  # type: ignore[arg-type]
            job_id=value["job_id"],  # type: ignore[arg-type]
            task_id=value["task_id"],  # type: ignore[arg-type]
            verifier=ComponentRef.from_dict(value["verifier"]),
            sdk_api_version=value["sdk_api_version"],  # type: ignore[arg-type]
            protocol_version=value["protocol_version"],  # type: ignore[arg-type]
            manifest_schema_version=value["manifest_schema_version"],  # type: ignore[arg-type]
            workflow_schema_version=value["workflow_schema_version"],  # type: ignore[arg-type]
            artifact_schemas=tuple(SchemaRef.from_dict(item) for item in schemas),
            trust_mode=value["trust_mode"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class VerifyContext:
    """Coordinator-owned verification inputs: expected outputs, byte budget,
    quorum size, an optional reference, and the binding for non-trusted modes.

    Multi-vote contexts automatically require distinct authenticated owners.
    """

    expected_outputs: Mapping[str, PortSpec]
    max_output_bytes: int
    minimum_matches: int = 1
    reference: OutputManifest | None = None
    require_distinct_owners: bool = False
    binding: VerificationBinding | None = None
    trust_mode: TrustMode = TrustMode.TRUSTED

    def __post_init__(self) -> None:
        if not isinstance(self.expected_outputs, Mapping) or not self.expected_outputs:
            raise ValueError("expected_outputs must be a non-empty object")
        ports: dict[str, PortSpec] = {}
        for name, port in self.expected_outputs.items():
            canonical = require_identifier(name, "expected output port")
            if not isinstance(port, PortSpec):
                raise ValueError("expected_outputs values must be PortSpec values")
            ports[canonical] = port
        object.__setattr__(self, "expected_outputs", MappingProxyType(ports))
        object.__setattr__(
            self,
            "max_output_bytes",
            require_positive_int(self.max_output_bytes, "max_output_bytes"),
        )
        object.__setattr__(
            self,
            "minimum_matches",
            require_positive_int(self.minimum_matches, "minimum_matches"),
        )
        if not isinstance(self.require_distinct_owners, bool):
            raise ValueError("require_distinct_owners must be a boolean")
        # A multi-vote quorum is never allowed to fall back to anonymous
        # candidate counting. Single-candidate trusted verification remains
        # convenient, while every quorum must carry coordinator-owned owners.
        if self.minimum_matches > 1:
            object.__setattr__(self, "require_distinct_owners", True)
        if self.binding is not None and not isinstance(
            self.binding, VerificationBinding
        ):
            raise ValueError("binding must be a VerificationBinding")
        try:
            trust_mode = TrustMode(self.trust_mode)
        except (TypeError, ValueError) as error:
            raise ValueError("verification trust_mode is unsupported") from error
        object.__setattr__(self, "trust_mode", trust_mode)
        if self.binding is not None and self.binding.trust_mode is not trust_mode:
            raise ValueError(
                "verification context trust mode does not match its binding"
            )
        if trust_mode is not TrustMode.TRUSTED and self.binding is None:
            raise ValueError("non-trusted verification requires a coordinator binding")
        if trust_mode is TrustMode.UNTRUSTED_QUORUM:
            if self.minimum_matches < 2:
                raise ValueError(
                    "untrusted quorum requires at least two matching owners"
                )
            object.__setattr__(self, "require_distinct_owners", True)
        if self.require_distinct_owners and self.binding is None:
            raise ValueError("multi-owner verification requires a coordinator binding")
        if self.reference is not None:
            if not isinstance(self.reference, OutputManifest):
                raise ValueError("reference must be an OutputManifest")
            self.reference.validate_against(
                self.expected_outputs, max_output_bytes=self.max_output_bytes
            )
            if self.binding is not None and not self.binding.matches(self.reference):
                raise ValueError(
                    "reference output does not match the coordinator binding"
                )


_CANDIDATE_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")


def _candidate_identity(value: object, field: str) -> str:
    text = require_string(value, field, max_length=128)
    if not _CANDIDATE_ID_PATTERN.fullmatch(text):
        raise ValueError(f"{field} must be an opaque coordinator identity")
    return text


@dataclass(frozen=True, slots=True)
class CandidateOutput:
    """Coordinator-authenticated identity envelope for one output attempt.

    The SDK validates the envelope shape, but the coordinator is responsible
    for constructing it from authenticated attempt and owner records. Worker
    payloads must never be allowed to choose these identity fields directly.
    ``owner_id`` may be omitted only for a trusted single-candidate path.
    """

    candidate_id: str
    owner_id: str | None
    manifest: OutputManifest
    authentication_tag: str | None = None
    _coordinator_authenticated: bool = field(default=False, init=False, repr=False)

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "candidate_id",
            _candidate_identity(self.candidate_id, "candidate_id"),
        )
        if self.owner_id is not None:
            object.__setattr__(
                self,
                "owner_id",
                _candidate_identity(self.owner_id, "owner_id"),
            )
        if not isinstance(self.manifest, OutputManifest):
            raise ValueError("candidate manifest must be an OutputManifest")
        if self.authentication_tag is not None:
            if self.owner_id is None:
                raise ValueError("authenticated candidate requires an owner_id")
            object.__setattr__(
                self,
                "authentication_tag",
                require_sha256(self.authentication_tag, "candidate authentication_tag"),
            )

    def _authentication_payload(self) -> bytes:
        return canonical_json(
            {
                "candidate_id": self.candidate_id,
                "owner_id": self.owner_id,
                "manifest": self.manifest.to_dict(),
            }
        ).encode("utf-8")

    def authenticated_by(self, key: bytes) -> bool:
        if (
            self.authentication_tag is None
            or not isinstance(key, bytes)
            or len(key) < 32
        ):
            return False
        expected = hmac.new(
            key, self._authentication_payload(), hashlib.sha256
        ).hexdigest()
        return hmac.compare_digest(self.authentication_tag, expected)

    @classmethod
    def from_coordinator_record(
        cls,
        candidate_id: str,
        owner_id: str,
        manifest: OutputManifest,
        authentication_key: bytes,
    ) -> "CandidateOutput":
        """Create an envelope from authenticated coordinator-owned records.

        Worker wire payloads must use :meth:`from_dict`, which deliberately
        cannot confer this process-local authority marker.
        """
        if not isinstance(authentication_key, bytes) or len(authentication_key) < 32:
            raise ValueError(
                "candidate authentication key must contain at least 32 bytes"
            )
        unsigned = cls(candidate_id, owner_id, manifest)
        tag = hmac.new(
            authentication_key,
            unsigned._authentication_payload(),
            hashlib.sha256,
        ).hexdigest()
        value = cls(candidate_id, owner_id, manifest, tag)
        object.__setattr__(value, "_coordinator_authenticated", True)
        return value

    @property
    def coordinator_authenticated(self) -> bool:
        return self._coordinator_authenticated

    def to_dict(self) -> dict[str, object]:
        return {
            "candidate_id": self.candidate_id,
            "owner_id": self.owner_id,
            "manifest": self.manifest.to_dict(),
            "authentication_tag": self.authentication_tag,
        }

    @classmethod
    def from_dict(cls, value: object) -> "CandidateOutput":
        if not isinstance(value, Mapping):
            raise ValueError("candidate output must be an object")
        require_exact_keys(
            value,
            {"candidate_id", "owner_id", "manifest", "authentication_tag"},
            "candidate output",
        )
        return cls(
            candidate_id=value["candidate_id"],  # type: ignore[arg-type]
            owner_id=value["owner_id"],  # type: ignore[arg-type]
            manifest=OutputManifest.from_dict(value["manifest"]),
            authentication_tag=value["authentication_tag"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True, init=False)
class CandidateOutputs:
    """Immutable candidate set with replay-safe coordinator identities.

    For source compatibility, ``CandidateOutputs((manifest,))`` creates one
    anonymous synthetic envelope for trusted local verification. Raw manifests
    cannot be combined or used to form a quorum; distributed callers provide
    explicit :class:`CandidateOutput` envelopes instead.
    """

    candidates: tuple[CandidateOutput, ...]

    def __init__(
        self,
        manifests: Sequence[CandidateOutput | OutputManifest] = (),
        *,
        candidates: Sequence[CandidateOutput | OutputManifest] | None = None,
    ) -> None:
        if candidates is not None:
            if manifests:
                raise ValueError("provide candidate values only once")
            manifests = candidates
        values = tuple(manifests)
        raw = tuple(value for value in values if isinstance(value, OutputManifest))
        if raw:
            if len(values) != 1:
                raise ValueError(
                    "raw OutputManifest compatibility is limited to one trusted candidate"
                )
            manifest = raw[0]
            normalized = (
                CandidateOutput(
                    candidate_id=f"trusted-{manifest.manifest_digest}",
                    owner_id=None,
                    manifest=manifest,
                ),
            )
        else:
            if any(not isinstance(value, CandidateOutput) for value in values):
                raise ValueError("candidates must contain CandidateOutput values")
            normalized = cast(tuple[CandidateOutput, ...], values)
        candidate_ids = [value.candidate_id for value in normalized]
        if len(candidate_ids) != len(set(candidate_ids)):
            raise ValueError("candidate_id values must be unique")
        object.__setattr__(self, "candidates", normalized)

    @property
    def manifests(self) -> tuple[OutputManifest, ...]:
        """Compatibility view for trusted code that only needs manifests."""
        return tuple(candidate.manifest for candidate in self.candidates)

    def to_dict(self) -> dict[str, object]:
        return {"candidates": [candidate.to_dict() for candidate in self.candidates]}

    @classmethod
    def from_dict(cls, value: object) -> "CandidateOutputs":
        if not isinstance(value, Mapping):
            raise ValueError("candidate outputs must be an object")
        require_exact_keys(value, {"candidates"}, "candidate outputs")
        candidates = value["candidates"]
        if not isinstance(candidates, list):
            raise ValueError("candidate outputs candidates must be an array")
        return cls(
            candidates=tuple(CandidateOutput.from_dict(item) for item in candidates)
        )

    @classmethod
    def from_authenticated_dict(
        cls,
        value: object,
        authentication_key: bytes,
    ) -> "CandidateOutputs":
        """Decode an internal coordinator envelope and verify every MAC.

        The signing key remains in the SDK-owned transport adapter and is never
        placed in :class:`VerifyContext` or exposed to package verifier code.
        """
        decoded = cls.from_dict(value)
        if not isinstance(authentication_key, bytes) or len(authentication_key) < 32:
            raise ValueError(
                "candidate authentication key must contain at least 32 bytes"
            )
        for candidate in decoded.candidates:
            if not candidate.authenticated_by(authentication_key):
                raise ValueError("candidate envelope authentication failed")
            object.__setattr__(candidate, "_coordinator_authenticated", True)
        return decoded


def _authentication_failure(
    context: VerifyContext,
    candidates: CandidateOutputs,
    identity: ComponentRef,
) -> VerificationDecision | None:
    if (
        context.trust_mode is TrustMode.TRUSTED
        and context.minimum_matches == 1
        and not context.require_distinct_owners
    ):
        return None
    invalid = sum(
        candidate.owner_id is None or not candidate.coordinator_authenticated
        for candidate in candidates.candidates
    )
    if invalid:
        return VerificationDecision(
            VerificationStatus.REJECTED,
            identity,
            "coordinator-authentication-required",
            {
                "candidate_count": len(candidates.candidates),
                "unauthenticated_count": invalid,
            },
        )
    return None


def _verify_loaded_candidates(
    context: VerifyContext,
    candidates: CandidateOutputs,
    identity: ComponentRef,
    compare: Callable[[OutputManifest, OutputManifest], VerificationDecision],
) -> VerificationDecision:
    """Apply a package-owned loader/comparator without trusting vote replay."""
    if not isinstance(context, VerifyContext) or not isinstance(
        candidates, CandidateOutputs
    ):
        raise ValueError("verifier requires VerifyContext and CandidateOutputs")
    authentication_failure = _authentication_failure(context, candidates, identity)
    if authentication_failure is not None:
        return authentication_failure
    if context.reference is None:
        return VerificationDecision(
            VerificationStatus.INCONCLUSIVE,
            identity,
            "reference-required",
            {"candidate_count": len(candidates.candidates)},
        )
    if context.require_distinct_owners and any(
        candidate.owner_id is None for candidate in candidates.candidates
    ):
        return VerificationDecision(
            VerificationStatus.REJECTED,
            identity,
            "owner-identity-required",
            {"candidate_count": len(candidates.candidates)},
        )
    matched = 0
    compared = 0
    invalid = 0
    seen_owners: set[str] = set()
    for candidate in sorted(candidates.candidates, key=lambda item: item.candidate_id):
        if candidate.owner_id is not None:
            if candidate.owner_id in seen_owners:
                continue
            seen_owners.add(candidate.owner_id)
        try:
            if context.binding is not None and not context.binding.matches(
                candidate.manifest
            ):
                raise ValueError("candidate does not match the coordinator binding")
            candidate.manifest.validate_against(
                context.expected_outputs,
                max_output_bytes=context.max_output_bytes,
            )
            decision = compare(context.reference, candidate.manifest)
        except Exception:
            invalid += 1
            continue
        compared += 1
        if decision.status is VerificationStatus.ACCEPTED:
            matched += 1
    evidence = {
        "matched": matched,
        "required": context.minimum_matches,
        "compared": compared,
        "invalid_count": invalid,
    }
    if matched >= context.minimum_matches:
        return VerificationDecision(
            VerificationStatus.ACCEPTED,
            identity,
            "reference-match",
            evidence,
            context.reference.digest,
        )
    if compared >= context.minimum_matches:
        return VerificationDecision(
            VerificationStatus.REJECTED,
            identity,
            "reference-mismatch",
            evidence,
        )
    return VerificationDecision(
        VerificationStatus.INCONCLUSIVE,
        identity,
        "insufficient-evidence",
        evidence,
    )


class ExactArtifactVerifier:
    """Whole-artifact SHA-256 acceptance for byte-exact workloads.

    Compares logical port/collection/schema/content digests while ignoring
    coordinator UUIDs, timestamps, and worker identity; counts at most one
    vote per owner and accepts only a declared reference match or an
    unambiguous quorum.
    """

    identity = ComponentRef("exact-artifact", 1)
    configuration: Mapping[str, object] = MappingProxyType({})

    def verify(
        self,
        context: VerifyContext,
        candidates: CandidateOutputs,
    ) -> VerificationDecision:
        if not isinstance(context, VerifyContext) or not isinstance(
            candidates, CandidateOutputs
        ):
            raise ValueError(
                "exact verifier requires VerifyContext and CandidateOutputs"
            )
        authentication_failure = _authentication_failure(
            context, candidates, self.identity
        )
        if authentication_failure is not None:
            return authentication_failure
        if context.require_distinct_owners:
            missing_owner_count = sum(
                candidate.owner_id is None for candidate in candidates.candidates
            )
            if missing_owner_count:
                return VerificationDecision(
                    VerificationStatus.REJECTED,
                    self.identity,
                    "owner-identity-required",
                    {
                        "candidate_count": len(candidates.candidates),
                        "missing_owner_count": missing_owner_count,
                    },
                )

        valid: list[CandidateOutput] = []
        invalid = 0
        for candidate in candidates.candidates:
            try:
                if context.binding is not None and not context.binding.matches(
                    candidate.manifest
                ):
                    raise ValueError("candidate does not match the coordinator binding")
                candidate.manifest.validate_against(
                    context.expected_outputs,
                    max_output_bytes=context.max_output_bytes,
                )
            except ValueError:
                invalid += 1
            else:
                valid.append(candidate)
        if not valid:
            return VerificationDecision(
                VerificationStatus.REJECTED
                if candidates.candidates
                else VerificationStatus.INCONCLUSIVE,
                self.identity,
                "no-valid-candidates" if candidates.candidates else "no-candidates",
                {
                    "candidate_count": len(candidates.candidates),
                    "invalid_count": invalid,
                },
            )
        owner_digests: dict[str, set[str]] = {}
        for candidate in valid:
            if candidate.owner_id is not None:
                owner_digests.setdefault(candidate.owner_id, set()).add(
                    candidate.manifest.digest
                )
        equivocating_owner_count = sum(
            len(digests) > 1 for digests in owner_digests.values()
        )
        if equivocating_owner_count:
            # Do not echo owner IDs or conflicting digests into durable
            # evidence. Counts are sufficient for policy and audit routing.
            return VerificationDecision(
                VerificationStatus.REJECTED,
                self.identity,
                "owner-equivocation",
                {
                    "candidate_count": len(candidates.candidates),
                    "equivocating_owner_count": equivocating_owner_count,
                },
            )
        counts: dict[str, int] = {}
        seen_owners: set[str] = set()
        duplicate_owner_candidates = 0
        for candidate in sorted(valid, key=lambda value: value.candidate_id):
            if candidate.owner_id is not None:
                if candidate.owner_id in seen_owners:
                    duplicate_owner_candidates += 1
                    continue
                seen_owners.add(candidate.owner_id)
            digest = candidate.manifest.digest
            counts[digest] = counts.get(digest, 0) + 1

        def with_duplicate_evidence(evidence: dict[str, int]) -> dict[str, int]:
            if duplicate_owner_candidates:
                evidence["duplicate_owner_candidates"] = duplicate_owner_candidates
            return evidence

        if context.reference is not None:
            expected_digest = context.reference.digest
            matched = counts.get(expected_digest, 0)
            if matched >= context.minimum_matches:
                return VerificationDecision(
                    VerificationStatus.ACCEPTED,
                    self.identity,
                    "reference-match",
                    with_duplicate_evidence(
                        {
                            "matched": matched,
                            "required": context.minimum_matches,
                            "invalid_count": invalid,
                        }
                    ),
                    expected_digest,
                )
            status = (
                VerificationStatus.REJECTED
                if sum(counts.values()) >= context.minimum_matches
                else VerificationStatus.INCONCLUSIVE
            )
            return VerificationDecision(
                status,
                self.identity,
                "reference-mismatch"
                if status is VerificationStatus.REJECTED
                else "insufficient-evidence",
                with_duplicate_evidence(
                    {
                        "matched": matched,
                        "required": context.minimum_matches,
                        "invalid_count": invalid,
                    }
                ),
            )
        ordered = sorted(counts.items(), key=lambda item: (-item[1], item[0]))
        digest, matches = ordered[0]
        tied = len(ordered) > 1 and ordered[1][1] == matches
        if matches >= context.minimum_matches and not tied:
            return VerificationDecision(
                VerificationStatus.ACCEPTED,
                self.identity,
                "quorum-match",
                with_duplicate_evidence(
                    {
                        "matched": matches,
                        "required": context.minimum_matches,
                        "distinct_digests": len(counts),
                        "invalid_count": invalid,
                    }
                ),
                digest,
            )
        if tied and matches >= context.minimum_matches:
            status = VerificationStatus.REJECTED
            reason = "conflicting-quorums"
        else:
            status = VerificationStatus.INCONCLUSIVE
            reason = "insufficient-evidence"
        return VerificationDecision(
            status,
            self.identity,
            reason,
            with_duplicate_evidence(
                {
                    "largest_group": matches,
                    "required": context.minimum_matches,
                    "distinct_digests": len(counts),
                    "invalid_count": invalid,
                }
            ),
        )


def _float_ulp_distance(left: float, right: float) -> int:
    """Return IEEE-754 binary64 representable steps between two finite values."""
    sign = 0x8000000000000000
    magnitude = sign - 1

    def ordered(value: float) -> int:
        bits = struct.unpack(">Q", struct.pack(">d", value))[0]
        return sign - (bits & magnitude) if bits & sign else sign + bits

    return abs(ordered(left) - ordered(right))


def _numeric_digest_value(value: object, depth: int = 0) -> object:
    if depth > 64:
        raise ValueError("numeric value nesting exceeds 64 levels")
    if isinstance(value, Mapping):
        if any(not isinstance(key, str) for key in value):
            raise ValueError("numeric objects must use JSON string keys")
        return {
            key: _numeric_digest_value(child, depth + 1) for key, child in value.items()
        }
    if isinstance(value, (list, tuple)):
        return [_numeric_digest_value(child, depth + 1) for child in value]
    if isinstance(value, float) and math.isnan(value):
        return {"$number": "nan"}
    return value


def _numeric_value_within_limit(value: object, max_elements: int) -> bool:
    """Bound a loaded numeric shape before comparison or digest allocation."""
    pending = [value]
    visited = 0
    while pending:
        current = pending.pop()
        visited += 1
        if visited > max_elements:
            return False
        if isinstance(current, Mapping):
            if len(current) > max_elements - visited:
                return False
            pending.extend(current.values())
        elif isinstance(current, (list, tuple)):
            if len(current) > max_elements - visited:
                return False
            pending.extend(current)
    return True


def _decimal_evidence(value: Decimal) -> int | float | str:
    """Return bounded, JSON-safe numeric evidence without range exceptions."""
    if value == value.to_integral_value() and abs(value).adjusted() <= 18:
        return int(value)
    try:
        converted = float(value)
    except (OverflowError, ValueError):
        converted = math.inf
    if math.isfinite(converted):
        return converted
    text = format(value, ".16E")
    return text if len(text) <= 64 else text[:64]


@dataclass(frozen=True, slots=True)
class NumericTolerance:
    """Bounded numeric comparison policy: absolute/relative/ULP tolerances,
    NaN policy, and a maximum element count for structured values."""

    absolute: float = 0.0
    relative: float = 0.0
    max_ulps: int = 0
    nan_policy: str = "reject"
    max_elements: int = 1_000_000

    def __post_init__(self) -> None:
        for field in ("absolute", "relative"):
            value = getattr(self, field)
            if (
                isinstance(value, bool)
                or not isinstance(value, (int, float))
                or value < 0
            ):
                raise ValueError(
                    f"numeric tolerance {field} must be a finite non-negative number"
                )
            try:
                converted = float(value)
            except (OverflowError, ValueError) as error:
                raise ValueError(
                    f"numeric tolerance {field} must be a finite non-negative number"
                ) from error
            if not math.isfinite(converted):
                raise ValueError(
                    f"numeric tolerance {field} must be a finite non-negative number"
                )
            object.__setattr__(self, field, converted)
        if (
            isinstance(self.max_ulps, bool)
            or not isinstance(self.max_ulps, int)
            or self.max_ulps < 0
        ):
            raise ValueError(
                "numeric tolerance max_ulps must be a non-negative integer"
            )
        if self.nan_policy not in {"reject", "equal"}:
            raise ValueError("numeric tolerance nan_policy must be reject or equal")
        object.__setattr__(
            self,
            "max_elements",
            require_positive_int(self.max_elements, "numeric tolerance max_elements"),
        )


class NumericToleranceVerifier:
    """Reference-based structured numeric comparison verifier.

    Requires a package-owned ``value_loader`` to turn artifacts into bounded
    structured values; without one, verification returns ``inconclusive``
    rather than accepting bytes it did not parse.
    """

    identity = ComponentRef("numeric-tolerance", 1)

    def __init__(
        self,
        tolerance: NumericTolerance,
        value_loader: Callable[[OutputManifest], object] | None = None,
    ) -> None:
        if not isinstance(tolerance, NumericTolerance):
            raise ValueError("tolerance must be NumericTolerance")
        if value_loader is not None and not callable(value_loader):
            raise ValueError("value_loader must be callable")
        self.tolerance = tolerance
        self._value_loader = value_loader

    @property
    def configuration(self) -> Mapping[str, object]:
        return MappingProxyType(
            {
                "absolute": self.tolerance.absolute,
                "relative": self.tolerance.relative,
                "max_ulps": self.tolerance.max_ulps,
                "nan_policy": self.tolerance.nan_policy,
                "max_elements": self.tolerance.max_elements,
            }
        )

    def verify(
        self,
        context: VerifyContext,
        candidates: CandidateOutputs,
    ) -> VerificationDecision:
        if not isinstance(context, VerifyContext) or not isinstance(
            candidates, CandidateOutputs
        ):
            raise ValueError(
                "numeric verifier requires VerifyContext and CandidateOutputs"
            )
        if self._value_loader is None:
            return VerificationDecision(
                VerificationStatus.INCONCLUSIVE,
                self.identity,
                "loader-unavailable",
                {"candidate_count": len(candidates.candidates)},
            )

        def compare(
            reference: OutputManifest, candidate: OutputManifest
        ) -> VerificationDecision:
            assert self._value_loader is not None
            return self.verify_values(
                self._value_loader(reference),
                self._value_loader(candidate),
            )

        return _verify_loaded_candidates(context, candidates, self.identity, compare)

    def verify_values(self, expected: object, actual: object) -> VerificationDecision:
        if not _numeric_value_within_limit(
            expected,
            self.tolerance.max_elements,
        ) or not _numeric_value_within_limit(actual, self.tolerance.max_elements):
            return VerificationDecision(
                VerificationStatus.REJECTED,
                self.identity,
                "element-limit",
                {"max_elements": self.tolerance.max_elements},
            )
        mismatch = self._compare(expected, actual, "$", 0)
        if mismatch is None:
            digest = hashlib.sha256(
                canonical_json(_numeric_digest_value(actual)).encode("utf-8")
            ).hexdigest()
            return VerificationDecision(
                VerificationStatus.ACCEPTED,
                self.identity,
                "within-tolerance",
                {
                    "absolute": self.tolerance.absolute,
                    "relative": self.tolerance.relative,
                    "max_ulps": self.tolerance.max_ulps,
                },
                digest,
            )
        return VerificationDecision(
            VerificationStatus.REJECTED,
            self.identity,
            mismatch[0],
            {"location": mismatch[1], **mismatch[2]},
        )

    def _compare(
        self,
        expected: object,
        actual: object,
        location: str,
        depth: int,
    ) -> tuple[str, str, dict[str, object]] | None:
        if depth > 64:
            return "nesting-limit", location, {}
        if isinstance(expected, bool) or isinstance(actual, bool):
            return None if expected is actual else ("value-mismatch", location, {})
        if isinstance(expected, (int, float)) and isinstance(actual, (int, float)):
            if isinstance(expected, int) and isinstance(actual, int):
                if max(abs(expected).bit_length(), abs(actual).bit_length()) > 1024:
                    return "numeric-range", location, {}
                difference_int = abs(actual - expected)
                allowed_decimal = max(
                    Decimal(str(self.tolerance.absolute)),
                    Decimal(str(self.tolerance.relative))
                    * Decimal(max(abs(expected), abs(actual))),
                )
                if Decimal(difference_int) <= allowed_decimal:
                    return None
                return (
                    "numeric-mismatch",
                    location,
                    {
                        "absolute_error": difference_int,
                        "allowed_error": _decimal_evidence(allowed_decimal),
                        "ulp_distance": 0,
                    },
                )
            if (
                isinstance(expected, int)
                and abs(expected).bit_length() > 1024
                or isinstance(actual, int)
                and abs(actual).bit_length() > 1024
            ):
                return "numeric-range", location, {}
            left = float(expected) if isinstance(expected, int) else expected
            right = float(actual) if isinstance(actual, int) else actual
            if math.isnan(left) or math.isnan(right):
                if (
                    self.tolerance.nan_policy == "equal"
                    and math.isnan(left)
                    and math.isnan(right)
                ):
                    return None
                return "nan-policy", location, {}
            if not math.isfinite(left) or not math.isfinite(right):
                return "non-finite", location, {}
            expected_decimal = (
                Decimal(expected)
                if isinstance(expected, int)
                else Decimal.from_float(expected)
            )
            actual_decimal = (
                Decimal(actual)
                if isinstance(actual, int)
                else Decimal.from_float(actual)
            )
            difference_decimal = abs(actual_decimal - expected_decimal)
            allowed_decimal = max(
                Decimal(str(self.tolerance.absolute)),
                Decimal(str(self.tolerance.relative))
                * max(abs(expected_decimal), abs(actual_decimal)),
            )
            ulp_distance = (
                _float_ulp_distance(expected, actual)
                if isinstance(expected, float) and isinstance(actual, float)
                else None
            )
            if difference_decimal <= allowed_decimal or (
                ulp_distance is not None and ulp_distance <= self.tolerance.max_ulps
            ):
                return None
            return (
                "numeric-mismatch",
                location,
                {
                    "absolute_error": _decimal_evidence(difference_decimal),
                    "allowed_error": _decimal_evidence(allowed_decimal),
                    "ulp_distance": ulp_distance if ulp_distance is not None else 0,
                },
            )
        if isinstance(expected, Mapping) and isinstance(actual, Mapping):
            if any(not isinstance(key, str) for key in expected) or any(
                not isinstance(key, str) for key in actual
            ):
                return "type-mismatch", location, {}
            if set(expected) != set(actual):
                return (
                    "shape-mismatch",
                    location,
                    {
                        "missing_keys": sorted(
                            str(key) for key in set(expected) - set(actual)
                        )[:32],
                        "extra_keys": sorted(
                            str(key) for key in set(actual) - set(expected)
                        )[:32],
                    },
                )
            for key in sorted(expected, key=str):
                mismatch = self._compare(
                    expected[key], actual[key], f"{location}.{key}", depth + 1
                )
                if mismatch is not None:
                    return mismatch
            return None
        if isinstance(expected, (list, tuple)) and isinstance(actual, (list, tuple)):
            if len(expected) != len(actual):
                return (
                    "shape-mismatch",
                    location,
                    {"expected_length": len(expected), "actual_length": len(actual)},
                )
            for index, (left, right) in enumerate(zip(expected, actual)):
                mismatch = self._compare(left, right, f"{location}[{index}]", depth + 1)
                if mismatch is not None:
                    return mismatch
            return None
        if type(expected) is not type(actual):
            return (
                "type-mismatch",
                location,
                {
                    "expected_type": type(expected).__name__,
                    "actual_type": type(actual).__name__,
                },
            )
        return None if expected == actual else ("value-mismatch", location, {})


class CanonicalRecordVerifier:
    """Package-owned record canonicalizer with bounded core comparison logic."""

    identity = ComponentRef("canonical-record", 1)

    def __init__(
        self,
        canonicalizer: Callable[[object], bytes],
        *,
        max_records: int = 1_000_000,
        record_loader: Callable[[OutputManifest], Iterable[object]] | None = None,
    ) -> None:
        if not callable(canonicalizer):
            raise ValueError("canonicalizer must be callable")
        self._canonicalizer = canonicalizer
        self._max_records = require_positive_int(max_records, "max_records")
        if record_loader is not None and not callable(record_loader):
            raise ValueError("record_loader must be callable")
        self._record_loader = record_loader

    @property
    def configuration(self) -> Mapping[str, object]:
        return MappingProxyType({"max_records": self._max_records})

    def verify(
        self,
        context: VerifyContext,
        candidates: CandidateOutputs,
    ) -> VerificationDecision:
        if not isinstance(context, VerifyContext) or not isinstance(
            candidates, CandidateOutputs
        ):
            raise ValueError(
                "canonical verifier requires VerifyContext and CandidateOutputs"
            )
        if self._record_loader is None:
            return VerificationDecision(
                VerificationStatus.INCONCLUSIVE,
                self.identity,
                "loader-unavailable",
                {"candidate_count": len(candidates.candidates)},
            )

        def compare(
            reference: OutputManifest, candidate: OutputManifest
        ) -> VerificationDecision:
            assert self._record_loader is not None
            return self.verify_records(
                self._record_loader(reference),
                self._record_loader(candidate),
            )

        return _verify_loaded_candidates(context, candidates, self.identity, compare)

    def verify_records(
        self, expected: Iterable[object], actual: Iterable[object]
    ) -> VerificationDecision:
        expected_digest = hashlib.sha256()
        actual_digest = hashlib.sha256()
        counts = [0, 0]
        try:
            for index, (stream, digest) in enumerate(
                ((expected, expected_digest), (actual, actual_digest))
            ):
                for record in stream:
                    counts[index] += 1
                    if counts[index] > self._max_records:
                        return VerificationDecision(
                            VerificationStatus.REJECTED,
                            self.identity,
                            "record-limit",
                            {"max_records": self._max_records},
                        )
                    encoded = self._canonicalizer(record)
                    if not isinstance(encoded, bytes):
                        raise ValueError("canonicalizer must return bytes")
                    digest.update(len(encoded).to_bytes(8, "big"))
                    digest.update(encoded)
        except Exception:
            return VerificationDecision(
                VerificationStatus.REJECTED,
                self.identity,
                "canonicalization-failed",
                {},
            )
        if counts[0] != counts[1] or expected_digest.digest() != actual_digest.digest():
            return VerificationDecision(
                VerificationStatus.REJECTED,
                self.identity,
                "canonical-mismatch",
                {"expected_records": counts[0], "actual_records": counts[1]},
            )
        digest = expected_digest.hexdigest()
        return VerificationDecision(
            VerificationStatus.ACCEPTED,
            self.identity,
            "canonical-match",
            {"records": counts[0]},
            digest,
        )
