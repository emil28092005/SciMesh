"""Explicit, digest-pinned workload package registry and safe discovery."""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from importlib import machinery, util
from importlib import metadata
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import RLock
from types import MappingProxyType
from typing import Any, Mapping

from ._validation import (
    canonical_json,
    require_semver,
    require_sha256,
    require_string,
    require_workload_name,
)
from .identity import ComponentRef, WorkloadId
from .integrity import installed_distribution_digest
from .manifest import WorkloadManifest
from .plans import JobRequest, ValidatedJob, WorkflowPlan
from .protocols import (
    Planner,
    PlanningContext,
    PlanningResources,
    Reducer,
    Runner,
    Verifier,
)
from .runtime import (
    CompatibilityError,
    NegotiatedWorkload,
    RuntimeCapabilities,
    negotiate_manifest,
)
from .schema import validate_parameter_instance
from .workflow import StageKind


_DISCOVERY_IMPORT_LOCK = RLock()


def _normalized_distribution_name(value: str) -> str:
    return re.sub(r"[-_.]+", "-", value).lower()


def _validate_entry_point_ownership(entry_point: metadata.EntryPoint) -> None:
    """Require the entry-point module to be payload of its own distribution."""
    distribution = entry_point.dist
    if distribution is None:
        raise ValueError("workload entry point has no owning distribution")
    module_name = getattr(entry_point, "module", None)
    if not isinstance(module_name, str) or not module_name:
        value = getattr(entry_point, "value", "")
        module_name = value.partition(":")[0].strip() if isinstance(value, str) else ""
    parts = module_name.split(".")
    if not parts or any(not part.isidentifier() for part in parts):
        raise ValueError("workload entry point has an invalid module path")

    raw_top_level = distribution.read_text("top_level.txt")
    declared = (
        {line.strip() for line in raw_top_level.splitlines() if line.strip()}
        if raw_top_level is not None
        else set()
    )
    root_name = parts[0]
    if root_name not in declared:
        raise ValueError("workload entry point module is outside its distribution")

    owners = metadata.packages_distributions().get(root_name, ())
    normalized_owners = {_normalized_distribution_name(owner) for owner in owners}
    expected_owner = _normalized_distribution_name(distribution.name)
    if normalized_owners and normalized_owners != {expected_owner}:
        raise ValueError("workload entry point top-level package is not uniquely owned")

    package_root = Path(str(distribution.locate_file(root_name)))
    if not package_root.exists():
        root_spec = util.find_spec(root_name)
        locations = (
            tuple(root_spec.submodule_search_locations or ())
            if root_spec is not None
            else ()
        )
        if len(locations) == 1:
            package_root = Path(locations[0])
    if package_root.is_dir():
        module_base = package_root.joinpath(*parts[1:])
        ownership_root = package_root.resolve()
        candidates = [
            *(Path(str(module_base) + suffix) for suffix in machinery.SOURCE_SUFFIXES),
            *(
                Path(str(module_base) + suffix)
                for suffix in machinery.EXTENSION_SUFFIXES
            ),
            *(
                module_base / ("__init__" + suffix)
                for suffix in machinery.SOURCE_SUFFIXES
            ),
            *(
                module_base / ("__init__" + suffix)
                for suffix in machinery.EXTENSION_SUFFIXES
            ),
        ]
    else:
        if len(parts) != 1:
            raise ValueError("workload entry point module is outside its distribution")
        ownership_root = Path(str(distribution.locate_file("."))).resolve()
        module_base = Path(str(distribution.locate_file(root_name)))
        candidates = [
            *(Path(str(module_base) + suffix) for suffix in machinery.SOURCE_SUFFIXES),
            *(
                Path(str(module_base) + suffix)
                for suffix in machinery.EXTENSION_SUFFIXES
            ),
        ]
    existing = tuple(candidate for candidate in candidates if candidate.is_file())
    if len(existing) != 1 or not existing[0].resolve().is_relative_to(ownership_root):
        raise ValueError("workload entry point module is not an owned package payload")


@dataclass(frozen=True, slots=True)
class WorkloadDefinition:
    """The immutable binding of a manifest to its installed handlers.

    Validation at construction requires every stage entry point to have a
    matching runner/reducer, the manifest verifier to be installed with
    matching configuration, and verifier handler identities to match their
    keys.
    """

    manifest: WorkloadManifest
    planner: Planner
    runners: Mapping[str, Runner]
    reducers: Mapping[str, Reducer]
    verifiers: Mapping[str, Verifier]

    def __post_init__(self) -> None:
        if not isinstance(self.manifest, WorkloadManifest):
            raise ValueError("definition manifest must be a WorkloadManifest")
        if not callable(getattr(self.planner, "validate", None)) or not callable(
            getattr(self.planner, "plan", None)
        ):
            raise ValueError("definition planner must implement validate and plan")
        collections: list[tuple[str, Mapping[str, Any], str]] = [
            ("runners", self.runners, "run"),
            ("reducers", self.reducers, "reduce"),
            ("verifiers", self.verifiers, "verify"),
        ]
        for field, values, method in collections:
            if not isinstance(values, Mapping):
                raise ValueError(f"definition {field} must be an object")
            copied: dict[str, Any] = {}
            for name, handler in values.items():
                canonical = require_string(name, f"{field} entry point", max_length=256)
                if not callable(getattr(handler, method, None)):
                    raise ValueError(
                        f"definition {field} handler must implement {method}"
                    )
                copied[canonical] = handler
            object.__setattr__(self, field, MappingProxyType(copied))
        for stage in self.manifest.workflow.stages:
            if stage.kind is StageKind.PLAN:
                if getattr(self.planner, "entry_point", None) != stage.entry_point:
                    raise ValueError(
                        "PLAN stage entry point must match planner.entry_point"
                    )
                continue
            if stage.kind is StageKind.REDUCE:
                handlers = self.reducers
            else:
                # A VERIFY node is still an executable DAG stage. Its
                # ``entry_point`` is a Runner; ``stage.verifier`` selects the
                # independent acceptance component applied to its output.
                handlers = self.runners
            if stage.entry_point not in handlers:
                raise ValueError(
                    f"definition has no installed handler for stage entry point: {stage.entry_point}"
                )
        verifier_key = self.manifest.verifier.verifier.canonical
        if verifier_key not in self.verifiers:
            raise ValueError(
                f"definition has no installed manifest verifier: {verifier_key}"
            )
        for key, verifier in self.verifiers.items():
            try:
                declared_identity = ComponentRef.from_dict(key)
            except ValueError as error:
                raise ValueError(
                    "definition verifier keys must be component identities"
                ) from error
            if (
                declared_identity.canonical != key
                or getattr(verifier, "identity", None) != declared_identity
            ):
                raise ValueError(
                    "definition verifier handler identity does not match its key"
                )
        manifest_verifier = self.verifiers[verifier_key]
        handler_configuration = getattr(manifest_verifier, "configuration", None)
        if handler_configuration is None:
            if self.manifest.verifier.configuration:
                raise ValueError(
                    "manifest verifier configuration is not bound by its handler"
                )
        elif dict(handler_configuration) != dict(self.manifest.verifier.configuration):
            raise ValueError(
                "manifest verifier configuration does not match its handler"
            )
        for stage in self.manifest.workflow.stages:
            if (
                stage.verifier is not None
                and stage.verifier.canonical not in self.verifiers
            ):
                raise ValueError(
                    f"definition has no installed stage verifier: {stage.verifier.canonical}"
                )


@dataclass(frozen=True, slots=True)
class AllowedPackage:
    """An administrator's approval to load one installed workload version.

    Pins the distribution, the exact ``WorkloadId``, and the measured
    ``sha256:`` package digest that discovery must match.
    """

    distribution: str
    workload: WorkloadId
    digest: str

    def __post_init__(self) -> None:
        distribution = require_string(
            self.distribution, "distribution", max_length=128
        ).lower()
        if not re.fullmatch(r"[a-z0-9]+(?:[-_.][a-z0-9]+)*", distribution):
            raise ValueError(
                "distribution must be a canonical Python distribution name"
            )
        object.__setattr__(self, "distribution", distribution.replace("_", "-"))
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("allowed workload must be a WorkloadId")
        object.__setattr__(
            self, "digest", require_sha256(self.digest, "allowed digest", prefixed=True)
        )


def workload_allowlist_from_json(value: object) -> tuple[AllowedPackage, ...]:
    """Parse a JSON array of ``{distribution, name, version, digest}`` allowlist entries."""
    import json

    if value is None or value == "":
        return ()
    if not isinstance(value, str):
        raise ValueError("workload allowlist must be a JSON array")
    try:
        decoded = json.loads(value)
    except (TypeError, json.JSONDecodeError, RecursionError) as error:
        raise ValueError("workload allowlist must be valid JSON") from error
    if not isinstance(decoded, list):
        raise ValueError("workload allowlist must be a JSON array")
    entries: list[AllowedPackage] = []
    for item in decoded:
        if not isinstance(item, dict) or not {
            "distribution",
            "name",
            "version",
            "digest",
        }.issubset(item):
            raise ValueError(
                "workload allowlist entries need distribution, name, version, and digest"
            )
        entries.append(
            AllowedPackage(
                str(item["distribution"]),
                WorkloadId(str(item["name"]), str(item["version"])),
                str(item["digest"]),
            )
        )
    return tuple(entries)


@dataclass(frozen=True, slots=True)
class WorkloadDescription:
    """A read-only registry listing: identity, description, digest, and enablement."""

    workload: WorkloadId
    description: str
    package_digest: str
    enabled: bool


@dataclass(frozen=True, slots=True)
class _NegotiatedPlanningContext:
    base: PlanningResources
    negotiated: NegotiatedWorkload

    @property
    def catalog(self):
        return self.base.catalog

    @property
    def sink(self):
        return self.base.sink

    @property
    def workspace(self) -> Path:
        return self.base.workspace


class WorkloadRegistry:
    """Registry keyed by exact workload version and immutable package digest."""

    ENTRY_POINT_GROUP = "scimesh.workloads"

    def __init__(self) -> None:
        self._definitions: dict[tuple[str, str], WorkloadDefinition] = {}
        self._enabled: set[tuple[str, str, str]] = set()
        self._lock = RLock()

    def register(
        self, definition: WorkloadDefinition, *, enabled: bool = False
    ) -> None:
        if not isinstance(definition, WorkloadDefinition):
            raise ValueError("definition must be a WorkloadDefinition")
        workload = definition.manifest.workload
        key = (workload.name, workload.version)
        with self._lock:
            if key in self._definitions:
                raise ValueError(
                    f"workload version already registered: {workload.name}@{workload.version}"
                )
            self._definitions[key] = definition
            if enabled:
                self._enabled.add((*key, definition.manifest.package.digest))

    def enable(self, name: str, version: str, package_digest: str) -> None:
        digest = require_sha256(package_digest, "package_digest", prefixed=True)
        with self._lock:
            definition = self._registered(name, version)
            if digest != definition.manifest.package.digest:
                raise ValueError(
                    "package digest does not match the registered manifest"
                )
            self._enabled.add((definition.manifest.workload.name, version, digest))

    def disable(self, name: str, version: str, package_digest: str) -> None:
        canonical = require_workload_name(name)
        version = require_semver(version, "workload.version")
        digest = require_sha256(package_digest, "package_digest", prefixed=True)
        with self._lock:
            self._enabled.discard((canonical, version, digest))

    def _registered(self, name: str, version: str) -> WorkloadDefinition:
        canonical = require_workload_name(name)
        version = require_semver(version, "workload.version")
        with self._lock:
            try:
                return self._definitions[(canonical, version)]
            except KeyError as error:
                raise ValueError(
                    f"unknown workload version: {canonical}@{version}"
                ) from error

    def require(
        self,
        name: str,
        version: str,
        package_digest: str,
        *,
        runtime: RuntimeCapabilities | None = None,
    ) -> tuple[WorkloadDefinition, NegotiatedWorkload | None]:
        digest = require_sha256(package_digest, "package_digest", prefixed=True)
        with self._lock:
            definition = self._registered(name, version)
            identity = (
                definition.manifest.workload.name,
                definition.manifest.workload.version,
                digest,
            )
            if (
                digest != definition.manifest.package.digest
                or identity not in self._enabled
            ):
                raise ValueError("workload package digest is not enabled")
        negotiated = (
            negotiate_manifest(definition.manifest, runtime)
            if runtime is not None
            else None
        )
        return definition, negotiated

    def plan(
        self,
        request: JobRequest,
        package_digest: str,
        runtime: RuntimeCapabilities,
        context: PlanningResources,
    ) -> WorkflowPlan:
        """Negotiate first, then invoke only the pre-registered planner object."""
        if not isinstance(request, JobRequest):
            raise ValueError("request must be a JobRequest")
        definition, negotiated = self.require(
            request.workload.name,
            request.workload.version,
            package_digest,
            runtime=runtime,
        )
        assert negotiated is not None
        self._validate_request_compatibility(
            request, definition.manifest, runtime, negotiated
        )
        self._validate_request_shape(request, definition.manifest)
        validated = definition.planner.validate(request)
        if not isinstance(validated, ValidatedJob) or validated.request != request:
            raise ValueError(
                "planner.validate must return a ValidatedJob for the same request"
            )
        plan = definition.planner.plan(
            validated,
            _NegotiatedPlanningContext(context, negotiated),
        )
        if not isinstance(plan, WorkflowPlan) or plan.workload != request.workload:
            raise ValueError(
                "planner.plan must return a WorkflowPlan for the requested workload"
            )
        if (
            plan.package_digest != definition.manifest.package.digest
            or plan.manifest_digest != definition.manifest.digest
            or plan.trust_mode is not request.trust_mode
            or plan.sdk_api_version != runtime.sdk_api_version
            or plan.protocol_version != runtime.protocol_version
            or plan.manifest_schema_version
            != definition.manifest.manifest_schema_version
            or plan.workflow_schema_version
            != definition.manifest.workflow.schema_version
            or plan.environment_digest != definition.manifest.environment.digest
            or plan.verifier != definition.manifest.verifier.verifier
            or plan.selected_features != negotiated.selected_features
            or plan.optional_fallbacks != negotiated.optional_fallbacks
        ):
            raise ValueError(
                "planner plan does not carry the selected immutable workload pin"
            )
        plan.validate_workflow(definition.manifest.workflow)
        self._validate_plan_limits(request, plan, definition.manifest)
        return WorkflowPlan.from_json(plan.to_json())

    @staticmethod
    def _validate_request_compatibility(
        request: JobRequest,
        manifest: WorkloadManifest,
        runtime: RuntimeCapabilities,
        negotiated: NegotiatedWorkload,
    ) -> None:
        if request.trust_mode not in manifest.trust_modes:
            raise CompatibilityError(
                "trust-mode-undeclared",
                "requested trust mode is not declared by the workload",
            )
        if request.trust_mode not in runtime.trust_modes:
            raise CompatibilityError(
                "trust-mode-unavailable",
                "runtime cannot enforce the requested trust mode",
            )
        for stage in manifest.workflow.stages:
            if request.trust_mode.value not in stage.trust_modes:
                raise CompatibilityError(
                    "stage-trust-unavailable",
                    f"stage {stage.stage_id} does not support the requested trust mode",
                )
        declared = {
            feature.name: feature
            for feature in manifest.required_features + manifest.optional_features
        }
        for name in request.required_features:
            requirement = declared.get(name)
            if requirement is None:
                raise CompatibilityError(
                    "feature-undeclared",
                    f"job requests a feature not declared by the workload: {name}",
                )
            version = runtime.features.get(name)
            if version is None or not requirement.versions.contains(version):
                raise CompatibilityError(
                    "feature-unavailable",
                    f"job-required feature is unavailable or incompatible: {name}",
                )
            if name in negotiated.optional_fallbacks:
                raise CompatibilityError(
                    "feature-fallback-disallowed",
                    f"job-required feature cannot use its fallback: {name}",
                )

    @staticmethod
    def _validate_request_shape(
        request: JobRequest, manifest: WorkloadManifest
    ) -> None:
        if set(request.inputs) != set(manifest.inputs):
            raise ValueError("job input ports do not match the manifest")
        total_bytes = 0
        artifact_references: dict[str, object] = {}
        for name, port in manifest.inputs.items():
            port.validate_collection(request.inputs[name], f"job input {name}")
            total_bytes += request.inputs[name].size_bytes
            for item in request.inputs[name].items:
                existing = artifact_references.get(item.artifact.artifact_id)
                if existing is not None and existing != item.artifact:
                    raise ValueError(
                        "job reuses an artifact ID with conflicting metadata"
                    )
                artifact_references[item.artifact.artifact_id] = item.artifact
        if total_bytes > manifest.limits.max_input_bytes:
            raise ValueError("job inputs exceed the manifest byte limit")
        if len(artifact_references) > manifest.limits.max_artifacts:
            raise ValueError("job inputs exceed the manifest artifact limit")
        import json
        from ._validation import thaw_json

        if (
            len(
                json.dumps(thaw_json(request.parameters), allow_nan=False).encode(
                    "utf-8"
                )
            )
            > manifest.limits.max_parameter_bytes
        ):
            raise ValueError("job parameters exceed the manifest byte limit")
        validate_parameter_instance(request.parameters, manifest.parameters_schema)

    @staticmethod
    def _validate_plan_limits(
        request: JobRequest,
        plan: WorkflowPlan,
        manifest: WorkloadManifest,
    ) -> None:
        references = {
            item.artifact.artifact_id: item.artifact
            for collection in request.inputs.values()
            for item in collection.items
        }
        for task in plan.tasks:
            for collection in task.inputs.values():
                for item in collection.items:
                    existing = references.get(item.artifact.artifact_id)
                    if existing is not None and existing != item.artifact:
                        raise ValueError(
                            "workflow plan reuses an artifact ID with conflicting metadata"
                        )
                    references[item.artifact.artifact_id] = item.artifact
            if (
                len(canonical_json(task.parameters).encode("utf-8"))
                > manifest.limits.max_parameter_bytes
            ):
                raise ValueError(
                    "planned task parameters exceed the manifest byte limit"
                )
        if len(references) > manifest.limits.max_artifacts:
            raise ValueError("workflow plan exceeds the manifest artifact limit")
        if (
            len(canonical_json(plan.resolved_parameters).encode("utf-8"))
            > manifest.limits.max_parameter_bytes
        ):
            raise ValueError("resolved parameters exceed the manifest byte limit")

    def descriptions(self) -> tuple[WorkloadDescription, ...]:
        with self._lock:
            result = []
            for key, definition in sorted(self._definitions.items()):
                digest = definition.manifest.package.digest
                result.append(
                    WorkloadDescription(
                        definition.manifest.workload,
                        definition.manifest.description,
                        digest,
                        (*key, digest) in self._enabled,
                    )
                )
            return tuple(result)

    def discover_installed(self, allowlist: tuple[AllowedPackage, ...]) -> None:
        """Load only configured installed entry points; never accept job module paths."""
        allowed: dict[tuple[str, str, str], AllowedPackage] = {}
        for item in allowlist:
            if not isinstance(item, AllowedPackage):
                raise ValueError("allowlist must contain AllowedPackage values")
            key = (item.distribution, item.workload.name, item.workload.version)
            if key in allowed:
                raise ValueError("allowlist identities must be unique")
            allowed[key] = item
        entry_points = metadata.entry_points()
        selected = entry_points.select(group=self.ENTRY_POINT_GROUP)
        discovered: set[tuple[str, str, str]] = set()
        pending: list[WorkloadDefinition] = []
        for entry_point in selected:
            entry_dist = entry_point.dist
            if entry_dist is None:
                continue
            distribution = _normalized_distribution_name(entry_dist.name)
            for key, approval in allowed.items():
                if _normalized_distribution_name(key[0]) != distribution:
                    continue
                if (
                    entry_point.name
                    != f"{approval.workload.name}@{approval.workload.version}"
                ):
                    continue
                _validate_entry_point_ownership(entry_point)
                # Import policy is process-global, so installed discovery is
                # serialized and intended for application startup. An empty
                # cache prefix prevents pre-existing package pyc files from
                # being consumed, while dont_write_bytecode keeps the
                # measured source tree unchanged during both load and factory.
                with (
                    _DISCOVERY_IMPORT_LOCK,
                    TemporaryDirectory(
                        prefix="scimesh-discovery-cache-"
                    ) as cache_prefix,
                ):
                    measured_before = installed_distribution_digest(entry_dist)
                    if measured_before != approval.digest:
                        raise ValueError(
                            "installed package content does not match its allowlist digest"
                        )
                    previous_bytecode_policy = sys.dont_write_bytecode
                    previous_cache_prefix = sys.pycache_prefix
                    sys.dont_write_bytecode = True
                    sys.pycache_prefix = cache_prefix
                    try:
                        loaded = entry_point.load()
                        definition = (
                            loaded()
                            if callable(loaded)
                            and not isinstance(loaded, WorkloadDefinition)
                            else loaded
                        )
                    finally:
                        sys.pycache_prefix = previous_cache_prefix
                        sys.dont_write_bytecode = previous_bytecode_policy
                    if installed_distribution_digest(entry_dist) != measured_before:
                        raise ValueError(
                            "installed package content changed while loading its entry point"
                        )
                if not isinstance(definition, WorkloadDefinition):
                    raise ValueError(
                        "workload entry point must provide a WorkloadDefinition"
                    )
                if definition.manifest.workload != approval.workload:
                    raise ValueError(
                        "discovered workload identity does not match its allowlist entry"
                    )
                if definition.manifest.package.distribution != approval.distribution:
                    raise ValueError(
                        "discovered package identity does not match its allowlist entry"
                    )
                if definition.manifest.package.digest != approval.digest:
                    raise ValueError(
                        "discovered package digest does not match its allowlist entry"
                    )
                if key in discovered:
                    raise ValueError(
                        "multiple installed entry points match one allowlist entry"
                    )
                pending.append(definition)
                discovered.add(key)
                break
        missing = sorted(set(allowed) - discovered)
        if missing:
            identities = ", ".join(f"{name}@{version}" for _, name, version in missing)
            raise ValueError(
                "allowlisted workload entry points were not installed: " + identities
            )
        pending_keys = [
            (definition.manifest.workload.name, definition.manifest.workload.version)
            for definition in pending
        ]
        if len(pending_keys) != len(set(pending_keys)):
            raise ValueError(
                "multiple allowlisted distributions provide one workload version"
            )
        with self._lock:
            conflicts = [key for key in pending_keys if key in self._definitions]
            if conflicts:
                name, version = conflicts[0]
                raise ValueError(
                    f"workload version already registered: {name}@{version}"
                )
            definitions = dict(self._definitions)
            enabled = set(self._enabled)
            for key, definition in zip(pending_keys, pending):
                definitions[key] = definition
                enabled.add((*key, definition.manifest.package.digest))
            self._definitions = definitions
            self._enabled = enabled
