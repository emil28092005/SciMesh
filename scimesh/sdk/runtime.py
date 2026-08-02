"""Fail-closed SDK/profile/feature/resource compatibility negotiation."""

from __future__ import annotations

from dataclasses import dataclass
from types import MappingProxyType
from typing import Mapping

from ._validation import (
    require_identifier,
    require_string,
    validate_version_range,
    version_in_range,
)
from .identity import SDK_API_VERSION
from .execution import NetworkPolicy, ProcessModel
from .manifest import TrustMode, WorkloadManifest
from .resources import AcceleratorMode, ResourceInventory
from .workflow import StageKind


class CompatibilityError(ValueError):
    """A fail-closed negotiation failure with a stable machine-readable code."""

    def __init__(self, code: str, message: str) -> None:
        self.code = require_identifier(code, "compatibility error code")
        super().__init__(message)


@dataclass(frozen=True, slots=True)
class RuntimeCapabilities:
    """What a runtime advertises: SDK/protocol versions, profiles, features,
    workload capabilities, inventory, and enforceable trust modes."""

    sdk_api_version: str
    protocol_version: str
    profiles: tuple[str, ...]
    features: Mapping[str, str]
    workload_capabilities: tuple[str, ...]
    inventory: ResourceInventory
    trust_modes: tuple[TrustMode, ...] = (TrustMode.TRUSTED,)

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "sdk_api_version",
            require_string(self.sdk_api_version, "sdk_api_version"),
        )
        object.__setattr__(
            self,
            "protocol_version",
            require_string(self.protocol_version, "protocol_version"),
        )
        # Parsing as an equality range provides the same numeric release rules
        # used by manifest ranges without accepting an implicit/latest value.
        validate_version_range(f"=={self.sdk_api_version}", "sdk_api_version")
        validate_version_range(f"=={self.protocol_version}", "protocol_version")
        profiles = tuple(
            require_identifier(value, "runtime profile") for value in self.profiles
        )
        if len(profiles) != len(set(profiles)):
            raise ValueError("runtime profiles must be unique")
        object.__setattr__(self, "profiles", profiles)
        if not isinstance(self.features, Mapping):
            raise ValueError("runtime features must be an object")
        features: dict[str, str] = {}
        for name, version in self.features.items():
            canonical = require_identifier(name, "runtime feature")
            text = require_string(version, "runtime feature version", max_length=32)
            validate_version_range(f"=={text}", "runtime feature version")
            features[canonical] = text
        object.__setattr__(self, "features", MappingProxyType(features))
        capabilities = tuple(
            require_identifier(value, "workload capability")
            for value in self.workload_capabilities
        )
        if len(capabilities) != len(set(capabilities)):
            raise ValueError("workload_capabilities must be unique")
        object.__setattr__(self, "workload_capabilities", capabilities)
        if not isinstance(self.inventory, ResourceInventory):
            raise ValueError("runtime inventory must be a ResourceInventory")
        try:
            trust_modes = tuple(TrustMode(value) for value in self.trust_modes)
        except (TypeError, ValueError) as error:
            raise ValueError(
                "runtime trust_modes contain an unsupported value"
            ) from error
        if not trust_modes or len(trust_modes) != len(set(trust_modes)):
            raise ValueError("runtime trust_modes must be non-empty and unique")
        object.__setattr__(self, "trust_modes", trust_modes)


@dataclass(frozen=True, slots=True)
class NegotiatedWorkload:
    """The result of successful negotiation: selected features, fallbacks,
    and the exact manifest a plan must pin."""

    manifest: WorkloadManifest
    optional_fallbacks: Mapping[str, str]
    sdk_api_version: str
    protocol_version: str
    selected_features: Mapping[str, str]

    def __post_init__(self) -> None:
        if not isinstance(self.manifest, WorkloadManifest):
            raise ValueError("negotiated manifest must be a WorkloadManifest")
        object.__setattr__(
            self, "optional_fallbacks", MappingProxyType(dict(self.optional_fallbacks))
        )
        object.__setattr__(
            self,
            "sdk_api_version",
            require_string(
                self.sdk_api_version, "negotiated sdk_api_version", max_length=32
            ),
        )
        object.__setattr__(
            self,
            "protocol_version",
            require_string(
                self.protocol_version, "negotiated protocol_version", max_length=32
            ),
        )
        validate_version_range(
            f"=={self.sdk_api_version}", "negotiated sdk_api_version"
        )
        validate_version_range(
            f"=={self.protocol_version}", "negotiated protocol_version"
        )
        selected: dict[str, str] = {}
        for name, version in self.selected_features.items():
            selected[require_identifier(name, "negotiated feature")] = require_string(
                version,
                "negotiated feature version",
                max_length=32,
            )
            validate_version_range(
                f"=={selected[name]}",
                "negotiated feature version",
            )
        object.__setattr__(self, "selected_features", MappingProxyType(selected))


def negotiate_manifest(
    manifest: WorkloadManifest,
    runtime: RuntimeCapabilities,
) -> NegotiatedWorkload:
    """Resolve compatibility before any package handler or planner is invoked."""
    if not isinstance(manifest, WorkloadManifest) or not isinstance(
        runtime, RuntimeCapabilities
    ):
        raise ValueError(
            "negotiation requires WorkloadManifest and RuntimeCapabilities"
        )
    if runtime.sdk_api_version != SDK_API_VERSION:
        raise CompatibilityError(
            "runtime-sdk-mismatch",
            "runtime SDK declaration does not match this SDK implementation",
        )
    if not manifest.sdk_api.contains(runtime.sdk_api_version):
        raise CompatibilityError(
            "sdk-api-mismatch", "runtime SDK API is outside the manifest range"
        )
    if not manifest.protocol.contains(runtime.protocol_version):
        raise CompatibilityError(
            "protocol-mismatch", "runtime protocol is outside the manifest range"
        )
    missing_profiles = sorted(
        set(manifest.conformance_profiles) - set(runtime.profiles)
    )
    if missing_profiles:
        raise CompatibilityError(
            "profile-unavailable",
            "runtime does not support required profiles: "
            + ", ".join(missing_profiles),
        )
    if manifest.workload.name not in runtime.workload_capabilities:
        raise CompatibilityError(
            "workload-unavailable",
            "runtime does not advertise the canonical workload capability",
        )
    if manifest.environment.digest not in runtime.inventory.environment_digests:
        raise CompatibilityError(
            "environment-unavailable", "pinned workload environment is unavailable"
        )
    for feature in manifest.required_features:
        version = runtime.features.get(feature.name)
        if version is None or not feature.versions.contains(version):
            raise CompatibilityError(
                "feature-unavailable",
                f"required feature is unavailable or incompatible: {feature.name}",
            )
    fallbacks: dict[str, str] = {}
    selected_features: dict[str, str] = {}
    for feature in manifest.required_features:
        version = runtime.features.get(feature.name)
        if version is not None and feature.versions.contains(version):
            selected_features[feature.name] = version
    for feature in manifest.optional_features:
        version = runtime.features.get(feature.name)
        if version is None or not feature.versions.contains(version):
            if feature.fallback is None:
                raise CompatibilityError(
                    "optional-feature-unavailable",
                    f"optional feature has no declared fallback: {feature.name}",
                )
            fallbacks[feature.name] = feature.fallback
        else:
            selected_features[feature.name] = version
    required_by_shape: dict[StageKind, str] = {
        StageKind.PLAN: "dynamic-expansion",
        StageKind.LOOP_CONTROLLER: "bounded-loops",
        StageKind.STREAM: "stream-checkpoints",
        StageKind.SERVICE: "services",
        StageKind.SIDE_EFFECT: "side-effect",
    }
    declared_required = {feature.name for feature in manifest.required_features}

    def require_declared(condition: bool, feature: str, message: str) -> None:
        if condition and feature not in declared_required:
            raise CompatibilityError(
                "feature-undeclared", message + f" requires {feature}"
            )

    for stage in manifest.workflow.stages:
        shape_feature = required_by_shape.get(stage.kind)
        if shape_feature is not None and shape_feature not in declared_required:
            raise CompatibilityError(
                "feature-undeclared",
                f"stage {stage.stage_id} requires declared feature {shape_feature}",
            )
        if stage.gang is not None and "gang-leases" not in declared_required:
            raise CompatibilityError(
                "feature-undeclared", "gang execution requires gang-leases"
            )
        execution = stage.execution
        require_declared(
            execution.process_model is ProcessModel.PROCESS_POOL,
            "process-pools",
            f"stage {stage.stage_id} process pool",
        )
        require_declared(
            execution.process_model is ProcessModel.THREAD_POOL,
            "thread-pools",
            f"stage {stage.stage_id} thread pool",
        )
        require_declared(
            execution.process_model is ProcessModel.EXTERNAL_RUNTIME,
            "external-runtimes",
            f"stage {stage.stage_id} external runtime",
        )
        require_declared(
            execution.max_processes > 1,
            "multi-process",
            f"stage {stage.stage_id} multi-process execution",
        )
        require_declared(
            execution.threads_per_process > 1,
            "python-threads",
            f"stage {stage.stage_id} Python threading",
        )
        require_declared(
            execution.native_threads > 1,
            "native-threads",
            f"stage {stage.stage_id} native threading",
        )
        require_declared(
            execution.nested_parallelism,
            "nested-parallelism",
            f"stage {stage.stage_id} nested parallelism",
        )
        network_features = {
            NetworkPolicy.NONE: "network-isolation",
            NetworkPolicy.COORDINATOR_ARTIFACTS_ONLY: "artifact-network-policy",
            NetworkPolicy.ALLOWLISTED_EGRESS: "egress-allowlist",
        }
        network_feature = network_features.get(execution.network)
        if network_feature is not None:
            require_declared(
                True,
                network_feature,
                f"stage {stage.stage_id} network policy",
            )
        require_declared(
            execution.checkpoint.enabled,
            "checkpoints",
            f"stage {stage.stage_id} checkpoint policy",
        )
        require_declared(
            stage.retry.max_attempts > 1,
            "retries",
            f"stage {stage.stage_id} retry policy",
        )
        require_declared(
            bool(execution.secret_handles),
            "secret-injection",
            f"stage {stage.stage_id} secret handles",
        )
        resource_sets = (stage.resources,) + (
            (stage.gang.per_replica_resources,) if stage.gang is not None else ()
        )
        for resources in resource_sets:
            if resources.accelerator_count:
                if resources.accelerator_mode is AcceleratorMode.EXCLUSIVE_DEVICE:
                    feature = "gpu-exclusive"
                elif resources.accelerator_mode is AcceleratorMode.PARTITION:
                    feature = "gpu-mig"
                else:
                    feature = "accelerator-fractional"
                if feature not in declared_required:
                    raise CompatibilityError(
                        "feature-undeclared",
                        f"accelerator stage requires declared feature {feature}",
                    )
            errors = resources.eligibility_errors(runtime.inventory)
            if errors:
                raise CompatibilityError("resource-ineligible", errors[0])
    return NegotiatedWorkload(
        manifest,
        fallbacks,
        runtime.sdk_api_version,
        runtime.protocol_version,
        selected_features,
    )
