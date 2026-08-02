"""Installed-package manifest and cross-component compatibility contract."""

from __future__ import annotations

import json
import re
from collections.abc import Mapping
from dataclasses import dataclass
from enum import Enum
from types import MappingProxyType
from typing import Any

from ._validation import (
    canonical_json,
    enum_value,
    freeze_json_mapping,
    require_exact_keys,
    require_identifier,
    require_positive_int,
    require_schema_version,
    require_sha256,
    require_string,
    thaw_json,
)
from .artifacts import PortSpec
from .identity import (
    MANIFEST_SCHEMA_VERSION,
    ComponentRef,
    FeatureRequirement,
    VersionRange,
    WorkloadId,
)
from .schema import validate_schema_definition
from .ui import UIElement, ui_elements_from_list
from .workflow import StageKind, WorkflowSpec


class DeterminismProfile(str, Enum):
    """How a workload's output is guaranteed to repeat.

    ``BYTE_EXACT`` is the only profile eligible for ``untrusted_quorum`` in
    v1; the other profiles require future verifier or trust policies.
    """

    BYTE_EXACT = "byte_exact"
    CANONICAL_EXACT = "canonical_exact"
    NUMERIC_TOLERANCE = "numeric_tolerance"
    SEEDED_STOCHASTIC = "seeded_stochastic"
    SEARCH_OR_OPTIMIZATION = "search_or_optimization"
    SIDE_EFFECTING = "side_effecting"


class TrustMode(str, Enum):
    """Who may execute a workload and what acceptance requires.

    ``TRUSTED`` accepts a single execution; ``VERIFIED`` requires a
    coordinator-owned binding; ``UNTRUSTED_QUORUM`` requires distinct owners
    to produce identical whole-artifact SHA-256 digests.
    """

    TRUSTED = "trusted"
    VERIFIED = "verified"
    UNTRUSTED_QUORUM = "untrusted_quorum"


@dataclass(frozen=True, slots=True)
class PackageSpec:
    """Identity pin of the installed distribution providing the workload.

    ``digest`` is the measured content pin (``sha256:`` prefix) that
    discovery compares before importing an entry point.
    """

    distribution: str
    digest: str
    signature: str | None = None

    def __post_init__(self) -> None:
        distribution = require_string(
            self.distribution, "package.distribution", max_length=128
        ).lower()
        if not re.fullmatch(r"[a-z0-9]+(?:[-_.][a-z0-9]+)*", distribution):
            raise ValueError(
                "package.distribution must be a canonical Python distribution name"
            )
        object.__setattr__(self, "distribution", distribution.replace("_", "-"))
        object.__setattr__(
            self, "digest", require_sha256(self.digest, "package.digest", prefixed=True)
        )
        if self.signature is not None:
            object.__setattr__(
                self,
                "signature",
                require_string(self.signature, "package.signature", max_length=512),
            )

    def to_dict(self) -> dict[str, object]:
        return {
            "distribution": self.distribution,
            "digest": self.digest,
            "signature": self.signature,
        }

    @classmethod
    def from_dict(cls, value: object) -> PackageSpec:
        if not isinstance(value, Mapping):
            raise ValueError("package specification must be an object")
        require_exact_keys(
            value, {"distribution", "digest", "signature"}, "package specification"
        )
        return cls(
            distribution=value["distribution"],  # type: ignore[arg-type]
            digest=value["digest"],  # type: ignore[arg-type]
            signature=value["signature"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class EnvironmentSpec:
    """Pinned execution environment (kind, digest, metadata).

    Negotiation fails unless the runtime inventory advertises this exact
    environment digest.
    """

    kind: str
    digest: str
    metadata: Mapping[str, Any]

    def __post_init__(self) -> None:
        object.__setattr__(
            self, "kind", require_identifier(self.kind, "environment.kind")
        )
        object.__setattr__(
            self,
            "digest",
            require_sha256(self.digest, "environment.digest", prefixed=True),
        )
        object.__setattr__(
            self, "metadata", freeze_json_mapping(self.metadata, "environment.metadata")
        )

    def to_dict(self) -> dict[str, object]:
        return {
            "kind": self.kind,
            "digest": self.digest,
            "metadata": thaw_json(self.metadata),
        }

    @classmethod
    def from_dict(cls, value: object) -> EnvironmentSpec:
        if not isinstance(value, Mapping):
            raise ValueError("environment specification must be an object")
        require_exact_keys(
            value, {"kind", "digest", "metadata"}, "environment specification"
        )
        return cls(
            kind=value["kind"],  # type: ignore[arg-type]
            digest=value["digest"],  # type: ignore[arg-type]
            metadata=value["metadata"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class VerifierSpec:
    """The manifest acceptance verifier and its bounded configuration.

    The verifier must be installed in the definition and its handler
    configuration must match this declaration exactly.
    """

    verifier: ComponentRef
    configuration: Mapping[str, Any]

    def __post_init__(self) -> None:
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("verifier must be a ComponentRef")
        object.__setattr__(
            self,
            "configuration",
            freeze_json_mapping(self.configuration, "verifier.configuration"),
        )

    def to_dict(self) -> dict[str, object]:
        return {
            "verifier": self.verifier.canonical,
            "configuration": thaw_json(self.configuration),
        }

    @classmethod
    def from_dict(cls, value: object) -> VerifierSpec:
        if not isinstance(value, Mapping):
            raise ValueError("verifier specification must be an object")
        require_exact_keys(
            value, {"verifier", "configuration"}, "verifier specification"
        )
        return cls(
            verifier=ComponentRef.from_dict(value["verifier"]),
            configuration=value["configuration"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class WorkloadLimits:
    """Hard resource and size bounds enforced by planning and execution.

    Covers input bytes, task count, output bytes, parameter bytes, and the
    total artifact count for one job.
    """

    max_input_bytes: int
    max_tasks: int
    max_output_bytes: int
    max_parameter_bytes: int = 65_536
    max_artifacts: int = 100_000

    def __post_init__(self) -> None:
        for field in (
            "max_input_bytes",
            "max_tasks",
            "max_output_bytes",
            "max_parameter_bytes",
            "max_artifacts",
        ):
            object.__setattr__(
                self,
                field,
                require_positive_int(getattr(self, field), f"limits.{field}"),
            )

    def to_dict(self) -> dict[str, int]:
        return {
            "max_input_bytes": self.max_input_bytes,
            "max_tasks": self.max_tasks,
            "max_output_bytes": self.max_output_bytes,
            "max_parameter_bytes": self.max_parameter_bytes,
            "max_artifacts": self.max_artifacts,
        }

    @classmethod
    def from_dict(cls, value: object) -> WorkloadLimits:
        if not isinstance(value, Mapping):
            raise ValueError("workload limits must be an object")
        fields = {
            "max_input_bytes",
            "max_tasks",
            "max_output_bytes",
            "max_parameter_bytes",
            "max_artifacts",
        }
        require_exact_keys(value, fields, "workload limits")
        return cls(**value)  # type: ignore[arg-type]


def _ports(
    value: Mapping[str, PortSpec], field: str, *, allow_empty: bool = False
) -> Mapping[str, PortSpec]:
    if not isinstance(value, Mapping) or (not value and not allow_empty):
        qualifier = "an object" if allow_empty else "a non-empty object"
        raise ValueError(f"{field} must be {qualifier}")
    result: dict[str, PortSpec] = {}
    for name, port in value.items():
        canonical = require_identifier(name, f"{field} port")
        if not isinstance(port, PortSpec):
            raise ValueError(f"{field} values must be PortSpec values")
        result[canonical] = port
    return MappingProxyType(result)


_REDUCTION_MODES = ("top-k", "ordered-concat")


def _reduction(value: object) -> str:
    if value not in _REDUCTION_MODES:
        raise ValueError(f"reduction must be one of: {', '.join(_REDUCTION_MODES)}")
    return value  # type: ignore[return-value]


@dataclass(frozen=True, slots=True)
class WorkloadManifest:
    """The installed workload's complete, immutable declaration.

    Pins identity, SDK/protocol compatibility ranges, package and environment
    digests, the strict parameter schema, the workflow DAG, external ports,
    determinism, trust modes, the acceptance verifier, limits, capabilities,
    and conformance profiles. ``digest`` is the canonical JSON content pin
    carried by every plan and task.
    """

    sdk_api: VersionRange
    protocol: VersionRange
    workload: WorkloadId
    description: str
    package: PackageSpec
    environment: EnvironmentSpec
    parameters_schema: Mapping[str, Any]
    workflow: WorkflowSpec
    inputs: Mapping[str, PortSpec]
    outputs: Mapping[str, PortSpec]
    determinism: DeterminismProfile
    trust_modes: tuple[TrustMode, ...]
    verifier: VerifierSpec
    limits: WorkloadLimits
    capabilities: tuple[str, ...]
    conformance_profiles: tuple[str, ...]
    required_features: tuple[FeatureRequirement, ...] = ()
    optional_features: tuple[FeatureRequirement, ...] = ()
    ui_elements: tuple[UIElement, ...] = ()
    reduction: str = "ordered-concat"
    upload_ready: bool = True
    manifest_schema_version: int = MANIFEST_SCHEMA_VERSION

    def __post_init__(self) -> None:
        require_schema_version(
            self.manifest_schema_version,
            MANIFEST_SCHEMA_VERSION,
            "manifest_schema_version",
        )
        if not isinstance(self.sdk_api, VersionRange) or not isinstance(
            self.protocol, VersionRange
        ):
            raise ValueError(
                "sdk_api and protocol must be explicit VersionRange values"
            )
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("workload must be a WorkloadId")
        object.__setattr__(
            self,
            "description",
            require_string(self.description, "description", max_length=512),
        )
        if not isinstance(self.package, PackageSpec) or not isinstance(
            self.environment, EnvironmentSpec
        ):
            raise ValueError(
                "manifest package and environment declarations are required"
            )
        schema = freeze_json_mapping(self.parameters_schema, "parameters_schema")
        if (
            schema.get("type") != "object"
            or schema.get("additionalProperties") is not False
        ):
            raise ValueError(
                "parameters_schema must be an object schema with additionalProperties=false"
            )
        properties = schema.get("properties")
        if not isinstance(properties, Mapping):
            raise ValueError("parameters_schema.properties must be an object")
        if len(canonical_json(schema).encode("utf-8")) > 1_048_576:
            raise ValueError("parameters_schema exceeds 1 MiB")
        validate_schema_definition(schema)
        object.__setattr__(self, "parameters_schema", schema)
        properties = dict(schema["properties"])
        ui_elements = ui_elements_from_list(self.ui_elements, "ui_elements")
        for element in ui_elements:
            if element.field not in properties:
                raise ValueError(
                    f"ui element {element.field!r} is not a declared parameter"
                )
        object.__setattr__(self, "ui_elements", ui_elements)
        object.__setattr__(self, "reduction", _reduction(self.reduction))
        if not isinstance(self.upload_ready, bool):
            raise ValueError("upload_ready must be a boolean")
        if not isinstance(self.workflow, WorkflowSpec):
            raise ValueError("workflow must be a WorkflowSpec")
        object.__setattr__(
            self, "inputs", _ports(self.inputs, "manifest.inputs", allow_empty=True)
        )
        object.__setattr__(self, "outputs", _ports(self.outputs, "manifest.outputs"))
        if dict(self.inputs) != dict(self.workflow.inputs):
            raise ValueError("manifest inputs must match workflow inputs")
        if dict(self.outputs) != dict(self.workflow.output_ports()):
            raise ValueError("manifest outputs must match workflow outputs")
        object.__setattr__(
            self,
            "determinism",
            enum_value(DeterminismProfile, self.determinism, "determinism"),
        )
        modes = tuple(
            enum_value(TrustMode, mode, "trust_mode") for mode in self.trust_modes
        )
        if not modes or len(modes) != len(set(modes)):
            raise ValueError("trust_modes must be non-empty and unique")
        object.__setattr__(self, "trust_modes", modes)
        manifest_mode_values = {mode.value for mode in modes}
        terminal_stage_ids = {
            reference.stage_id
            for reference in self.workflow.outputs.values()
            if reference.stage_id is not None
        }
        for stage in self.workflow.stages:
            if not set(stage.trust_modes).issubset(manifest_mode_values):
                raise ValueError(
                    "stage trust modes must be a subset of manifest trust_modes"
                )
            if stage.verifier is None:
                raise ValueError(
                    "every output-producing stage requires an acceptance verifier"
                )
            resource_sets = (stage.resources,) + (
                (stage.gang.per_replica_resources,) if stage.gang is not None else ()
            )
            if any(
                resources.environment_digest not in {None, self.environment.digest}
                for resources in resource_sets
            ):
                raise ValueError(
                    "stage resource environment must match the manifest environment pin"
                )
        if not isinstance(self.verifier, VerifierSpec):
            raise ValueError("verifier must be a VerifierSpec")
        for stage in self.workflow.stages:
            if (
                stage.stage_id in terminal_stage_ids
                and stage.verifier != self.verifier.verifier
            ):
                raise ValueError(
                    "terminal stage verifier must match the manifest acceptance verifier"
                )
        if not isinstance(self.limits, WorkloadLimits):
            raise ValueError("limits must be WorkloadLimits")
        if self.workflow.max_tasks > self.limits.max_tasks:
            raise ValueError("workflow max_tasks exceeds the workload limit")
        if self.workflow.max_output_bytes > self.limits.max_output_bytes:
            raise ValueError("workflow max_output_bytes exceeds the workload limit")
        capabilities = tuple(
            require_identifier(value, "capability") for value in self.capabilities
        )
        if not capabilities or len(capabilities) != len(set(capabilities)):
            raise ValueError("capabilities must be non-empty and unique")
        if self.workload.name not in capabilities:
            raise ValueError("capabilities must include the canonical workload name")
        object.__setattr__(self, "capabilities", capabilities)
        profiles = tuple(
            require_identifier(value, "conformance_profile")
            for value in self.conformance_profiles
        )
        if "core-batch-v1" not in profiles or len(profiles) != len(set(profiles)):
            raise ValueError("conformance_profiles must uniquely include core-batch-v1")
        object.__setattr__(self, "conformance_profiles", profiles)
        required = tuple(self.required_features)
        optional = tuple(self.optional_features)
        if any(
            not isinstance(item, FeatureRequirement) for item in required + optional
        ):
            raise ValueError("features must contain FeatureRequirement values")
        names = [item.name for item in required + optional]
        if len(names) != len(set(names)):
            raise ValueError("required and optional feature names must be unique")
        object.__setattr__(self, "required_features", required)
        object.__setattr__(self, "optional_features", optional)
        self._validate_acceptance_policy()

    def _validate_acceptance_policy(self) -> None:
        verifier = self.verifier.verifier
        exact = verifier == ComponentRef("exact-artifact", 1)
        canonical = verifier == ComponentRef("canonical-record", 1)
        numeric = verifier == ComponentRef("numeric-tolerance", 1)
        if self.determinism is DeterminismProfile.BYTE_EXACT and not exact:
            raise ValueError("byte_exact workloads require exact-artifact verifier")
        if self.determinism is DeterminismProfile.CANONICAL_EXACT and not canonical:
            raise ValueError(
                "canonical_exact workloads require canonical-record verifier"
            )
        if self.determinism is DeterminismProfile.NUMERIC_TOLERANCE and not numeric:
            raise ValueError(
                "numeric_tolerance workloads require numeric-tolerance verifier"
            )
        if TrustMode.UNTRUSTED_QUORUM in self.trust_modes:
            if self.determinism is not DeterminismProfile.BYTE_EXACT or not exact:
                raise ValueError(
                    "untrusted_quorum v1 requires byte_exact and exact-artifact"
                )
            if any(
                stage.kind is StageKind.SIDE_EFFECT for stage in self.workflow.stages
            ):
                raise ValueError("side-effect stages cannot use untrusted quorum")
        if self.determinism is DeterminismProfile.SIDE_EFFECTING:
            if self.trust_modes != (TrustMode.TRUSTED,):
                raise ValueError("side_effecting workloads must be trusted-only")
            if not any(
                stage.kind is StageKind.SIDE_EFFECT for stage in self.workflow.stages
            ):
                raise ValueError("side_effecting workload requires a side-effect stage")

    @property
    def digest(self) -> str:
        import hashlib

        return hashlib.sha256(self.to_json().encode("utf-8")).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "manifest_schema_version": self.manifest_schema_version,
            "sdk_api": self.sdk_api.expression,
            "protocol": self.protocol.expression,
            "workload": self.workload.to_dict(),
            "description": self.description,
            "package": self.package.to_dict(),
            "environment": self.environment.to_dict(),
            "parameters_schema": thaw_json(self.parameters_schema),
            "workflow": self.workflow.to_dict(),
            "inputs": {name: port.to_dict() for name, port in self.inputs.items()},
            "outputs": {name: port.to_dict() for name, port in self.outputs.items()},
            "determinism": self.determinism.value,
            "trust_modes": [mode.value for mode in self.trust_modes],
            "verifier": self.verifier.to_dict(),
            "limits": self.limits.to_dict(),
            "capabilities": list(self.capabilities),
            "conformance_profiles": list(self.conformance_profiles),
            "required_features": [item.to_dict() for item in self.required_features],
            "optional_features": [item.to_dict() for item in self.optional_features],
            "ui_elements": [element.to_dict() for element in self.ui_elements],
            "reduction": self.reduction,
            "upload_ready": self.upload_ready,
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> WorkloadManifest:
        if not isinstance(value, Mapping):
            raise ValueError("workload manifest must be an object")
        fields = {
            "manifest_schema_version",
            "sdk_api",
            "protocol",
            "workload",
            "description",
            "package",
            "environment",
            "parameters_schema",
            "workflow",
            "inputs",
            "outputs",
            "determinism",
            "trust_modes",
            "verifier",
            "limits",
            "capabilities",
            "conformance_profiles",
            "required_features",
            "optional_features",
            "ui_elements",
            "reduction",
            "upload_ready",
        }
        require_exact_keys(value, fields, "workload manifest")
        inputs, outputs = value["inputs"], value["outputs"]
        arrays = (
            value["trust_modes"],
            value["capabilities"],
            value["conformance_profiles"],
            value["required_features"],
            value["optional_features"],
            value["ui_elements"],
        )
        if not isinstance(inputs, Mapping) or not isinstance(outputs, Mapping):
            raise ValueError("manifest inputs and outputs must be objects")
        if any(not isinstance(item, list) for item in arrays):
            raise ValueError(
                "manifest trust, capability, profile, and feature fields must be arrays"
            )
        return cls(
            manifest_schema_version=value["manifest_schema_version"],  # type: ignore[arg-type]
            sdk_api=VersionRange.from_dict(value["sdk_api"]),
            protocol=VersionRange.from_dict(value["protocol"]),
            workload=WorkloadId.from_dict(value["workload"]),
            description=value["description"],  # type: ignore[arg-type]
            package=PackageSpec.from_dict(value["package"]),
            environment=EnvironmentSpec.from_dict(value["environment"]),
            parameters_schema=value["parameters_schema"],  # type: ignore[arg-type]
            workflow=WorkflowSpec.from_dict(value["workflow"]),
            inputs={name: PortSpec.from_dict(port) for name, port in inputs.items()},
            outputs={name: PortSpec.from_dict(port) for name, port in outputs.items()},
            determinism=value["determinism"],  # type: ignore[arg-type]
            trust_modes=tuple(value["trust_modes"]),  # type: ignore[arg-type]
            verifier=VerifierSpec.from_dict(value["verifier"]),
            limits=WorkloadLimits.from_dict(value["limits"]),
            capabilities=tuple(value["capabilities"]),  # type: ignore[arg-type]
            conformance_profiles=tuple(value["conformance_profiles"]),  # type: ignore[arg-type]
            required_features=tuple(
                FeatureRequirement.from_dict(item)
                for item in value["required_features"]  # type: ignore[union-attr]
            ),
            optional_features=tuple(
                FeatureRequirement.from_dict(item)
                for item in value["optional_features"]  # type: ignore[union-attr]
            ),
            ui_elements=ui_elements_from_list(
                tuple(value["ui_elements"]),
                "ui_elements",  # type: ignore[arg-type]
            ),
            reduction=value["reduction"],  # type: ignore[arg-type]
            upload_ready=value["upload_ready"],  # type: ignore[arg-type]
        )

    @classmethod
    def from_json(cls, value: str) -> WorkloadManifest:
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("workload manifest must be valid JSON") from error
        return cls.from_dict(decoded)
