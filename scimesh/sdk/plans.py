"""Strict job, task, workflow-plan, and expansion value objects."""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any, Mapping

from ._validation import (
    canonical_json,
    freeze_json_mapping,
    require_exact_keys,
    require_identifier,
    require_nonnegative_int,
    parse_release,
    require_positive_int,
    require_sha256,
    require_schema_version,
    require_string,
    require_task_key,
    require_uuid,
    thaw_json,
)
from .artifacts import ArtifactCollection, Cardinality, CollectionKind, PortSpec
from .execution import ExecutionProfile
from .identity import ComponentRef, TASK_SCHEMA_VERSION, WorkloadId
from .manifest import TrustMode
from .resources import ResourceRequirements
from .workflow import StageKind, StageSpec, WorkflowSpec


def _collections(
    value: Mapping[str, ArtifactCollection], field: str
) -> Mapping[str, ArtifactCollection]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    result: dict[str, ArtifactCollection] = {}
    for name, collection in value.items():
        canonical = require_identifier(name, f"{field} port")
        if not isinstance(collection, ArtifactCollection):
            raise ValueError(f"{field} values must be ArtifactCollection values")
        result[canonical] = collection
    return MappingProxyType(result)


def _ports(value: Mapping[str, PortSpec], field: str) -> Mapping[str, PortSpec]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    result: dict[str, PortSpec] = {}
    for name, port in value.items():
        canonical = require_identifier(name, f"{field} port")
        if not isinstance(port, PortSpec):
            raise ValueError(f"{field} values must be PortSpec values")
        result[canonical] = port
    return MappingProxyType(result)


def _feature_versions(value: object, field: str) -> Mapping[str, str]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    result: dict[str, str] = {}
    for name, version in value.items():
        canonical = require_identifier(name, f"{field} feature")
        text = require_string(version, f"{field} version", max_length=32)
        parse_release(text, f"{field} version")
        result[canonical] = text
    return MappingProxyType(result)


def _fallbacks(value: object, field: str) -> Mapping[str, str]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    return MappingProxyType(
        {
            require_identifier(name, f"{field} feature"): require_identifier(
                fallback,
                f"{field} fallback",
            )
            for name, fallback in value.items()
        }
    )


@dataclass(frozen=True, slots=True)
class JobRequest:
    """A user-requested job: workload identity, strict parameters, and inputs.

    Parameters are frozen, JSON-safe, and location-free; required features
    must be declared by the workload and available in the runtime.
    """

    workload: WorkloadId
    parameters: Mapping[str, Any]
    inputs: Mapping[str, ArtifactCollection]
    required_features: tuple[str, ...] = ()
    trust_mode: TrustMode = TrustMode.TRUSTED

    def __post_init__(self) -> None:
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("job workload must be a WorkloadId")
        object.__setattr__(
            self,
            "parameters",
            freeze_json_mapping(
                self.parameters, "job.parameters", forbid_locations=True
            ),
        )
        object.__setattr__(self, "inputs", _collections(self.inputs, "job.inputs"))
        features = tuple(
            require_identifier(value, "required_feature")
            for value in self.required_features
        )
        if len(features) != len(set(features)):
            raise ValueError("required_features must be unique")
        object.__setattr__(self, "required_features", features)
        try:
            trust_mode = TrustMode(self.trust_mode)
        except (TypeError, ValueError) as error:
            raise ValueError("job trust_mode is unsupported") from error
        object.__setattr__(self, "trust_mode", trust_mode)

    @property
    def parameters_digest(self) -> str:
        return hashlib.sha256(
            canonical_json(self.parameters).encode("utf-8")
        ).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "workload": self.workload.to_dict(),
            "parameters": thaw_json(self.parameters),
            "inputs": {name: value.to_dict() for name, value in self.inputs.items()},
            "required_features": list(self.required_features),
            "trust_mode": self.trust_mode.value,
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> "JobRequest":
        if not isinstance(value, Mapping):
            raise ValueError("job request must be an object")
        fields = {"workload", "parameters", "inputs", "required_features", "trust_mode"}
        require_exact_keys(value, fields, "job request")
        inputs = value["inputs"]
        features = value["required_features"]
        if not isinstance(inputs, Mapping) or not isinstance(features, list):
            raise ValueError(
                "job inputs must be an object and required_features an array"
            )
        return cls(
            workload=WorkloadId.from_dict(value["workload"]),
            parameters=value["parameters"],  # type: ignore[arg-type]
            inputs={
                name: ArtifactCollection.from_dict(item)
                for name, item in inputs.items()
            },
            required_features=tuple(features),
            trust_mode=value["trust_mode"],  # type: ignore[arg-type]
        )

    @classmethod
    def from_json(cls, value: str) -> "JobRequest":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("job request must be valid JSON") from error
        return cls.from_dict(decoded)


@dataclass(frozen=True, slots=True)
class ValidatedJob:
    """A job after planner validation, carrying the resolved parameter set.

    The planner may replace ambiguous parameters (for example a query id)
    with their resolved values before tasks are planned.
    """

    request: JobRequest
    resolved_parameters: Mapping[str, Any]

    def __post_init__(self) -> None:
        if not isinstance(self.request, JobRequest):
            raise ValueError("validated job request must be a JobRequest")
        object.__setattr__(
            self,
            "resolved_parameters",
            freeze_json_mapping(
                self.resolved_parameters,
                "resolved_parameters",
                forbid_locations=True,
            ),
        )

    @property
    def parameters_digest(self) -> str:
        return hashlib.sha256(
            canonical_json(self.resolved_parameters).encode("utf-8")
        ).hexdigest()


@dataclass(frozen=True, slots=True)
class TaskSpec:
    workload: WorkloadId
    package_digest: str
    manifest_digest: str
    trust_mode: TrustMode
    sdk_api_version: str
    protocol_version: str
    manifest_schema_version: int
    workflow_schema_version: int
    environment_digest: str
    verifier: ComponentRef
    selected_features: Mapping[str, str]
    optional_fallbacks: Mapping[str, str]
    task_key: str
    stage_id: str
    parameters: Mapping[str, Any]
    inputs: Mapping[str, ArtifactCollection]
    expected_outputs: Mapping[str, PortSpec]
    resources: ResourceRequirements
    execution: ExecutionProfile
    expected_input_keys: Mapping[str, tuple[str, ...]] = field(default_factory=dict)
    schema_version: int = TASK_SCHEMA_VERSION

    def __post_init__(self) -> None:
        require_schema_version(
            self.schema_version, TASK_SCHEMA_VERSION, "task schema_version"
        )
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("task workload must be a WorkloadId")
        object.__setattr__(
            self,
            "package_digest",
            require_sha256(self.package_digest, "task package_digest", prefixed=True),
        )
        object.__setattr__(
            self,
            "manifest_digest",
            require_sha256(self.manifest_digest, "task manifest_digest"),
        )
        try:
            trust_mode = TrustMode(self.trust_mode)
        except (TypeError, ValueError) as error:
            raise ValueError("task trust_mode is unsupported") from error
        object.__setattr__(self, "trust_mode", trust_mode)
        object.__setattr__(
            self,
            "sdk_api_version",
            require_string(self.sdk_api_version, "task sdk_api_version", max_length=32),
        )
        object.__setattr__(
            self,
            "protocol_version",
            require_string(
                self.protocol_version, "task protocol_version", max_length=32
            ),
        )
        parse_release(self.sdk_api_version, "task sdk_api_version")
        parse_release(self.protocol_version, "task protocol_version")
        object.__setattr__(
            self,
            "manifest_schema_version",
            require_positive_int(
                self.manifest_schema_version, "task manifest_schema_version"
            ),
        )
        object.__setattr__(
            self,
            "workflow_schema_version",
            require_positive_int(
                self.workflow_schema_version, "task workflow_schema_version"
            ),
        )
        object.__setattr__(
            self,
            "environment_digest",
            require_sha256(
                self.environment_digest, "task environment_digest", prefixed=True
            ),
        )
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("task verifier must be a ComponentRef")
        object.__setattr__(
            self,
            "selected_features",
            _feature_versions(self.selected_features, "task selected_features"),
        )
        object.__setattr__(
            self,
            "optional_fallbacks",
            _fallbacks(self.optional_fallbacks, "task optional_fallbacks"),
        )
        if set(self.selected_features).intersection(self.optional_fallbacks):
            raise ValueError("one task feature cannot be selected and fallbacked")
        object.__setattr__(self, "task_key", require_task_key(self.task_key))
        object.__setattr__(
            self, "stage_id", require_identifier(self.stage_id, "stage_id")
        )
        object.__setattr__(
            self,
            "parameters",
            freeze_json_mapping(
                self.parameters, "task.parameters", forbid_locations=True
            ),
        )
        object.__setattr__(self, "inputs", _collections(self.inputs, "task.inputs"))
        object.__setattr__(
            self,
            "expected_outputs",
            _ports(self.expected_outputs, "task.expected_outputs"),
        )
        if not self.expected_outputs:
            raise ValueError("task expected_outputs must not be empty")
        if not isinstance(self.resources, ResourceRequirements):
            raise ValueError("task resources must be ResourceRequirements")
        if not isinstance(self.execution, ExecutionProfile):
            raise ValueError("task execution must be ExecutionProfile")
        self.execution.validate_resources(self.resources)
        if not isinstance(self.expected_input_keys, Mapping):
            raise ValueError("expected_input_keys must be an object")
        expected_keys: dict[str, tuple[str, ...]] = {}
        for port_name, keys in self.expected_input_keys.items():
            canonical_port = require_identifier(port_name, "expected input key port")
            if not isinstance(keys, (list, tuple)):
                raise ValueError("expected input keys must be arrays")
            canonical_keys = tuple(
                sorted(require_identifier(key, "expected input key") for key in keys)
            )
            if not canonical_keys or len(canonical_keys) != len(set(canonical_keys)):
                raise ValueError("expected input keys must be non-empty and unique")
            expected_keys[canonical_port] = canonical_keys
        object.__setattr__(self, "expected_input_keys", MappingProxyType(expected_keys))

    def validate_stage(self, stage: StageSpec) -> "TaskSpec":
        if not isinstance(stage, StageSpec) or stage.stage_id != self.stage_id:
            raise ValueError("task stage does not match its StageSpec")
        if set(self.inputs) != set(stage.inputs):
            raise ValueError("task input ports do not match the stage")
        for name, declaration in stage.inputs.items():
            declaration.validate_collection(self.inputs[name], f"task input {name}")
        for name, expected_keys in self.expected_input_keys.items():
            declaration = stage.inputs.get(name)
            if (
                declaration is None
                or declaration.cardinality is not Cardinality.MANY
                or declaration.collection is not CollectionKind.KEYED
            ):
                raise ValueError("expected input keys require a keyed-many stage input")
            actual_keys = tuple(
                item.key for item in self.inputs[name].items if item.key is not None
            )
            if set(actual_keys) != set(expected_keys):
                raise ValueError(
                    "task keyed input does not match its coordinator expected keys"
                )
        keyed_many_ports = {
            name
            for name, declaration in stage.inputs.items()
            if declaration.cardinality is Cardinality.MANY
            and declaration.collection is CollectionKind.KEYED
        }
        if set(self.expected_input_keys) != keyed_many_ports:
            raise ValueError("task must pin expected keys for every keyed-many input")
        if dict(self.expected_outputs) != dict(stage.outputs):
            raise ValueError("task expected outputs do not match the stage")
        if not set(self.parameters).issubset(stage.parameter_names):
            raise ValueError("task parameters are outside the stage projection")
        if self.resources != stage.resources or self.execution != stage.execution:
            raise ValueError("task execution requirements do not match the stage")
        if self.verifier != stage.verifier:
            raise ValueError(
                "task verifier does not match the stage acceptance verifier"
            )
        if self.trust_mode.value not in stage.trust_modes:
            raise ValueError("task trust mode is not allowed by the stage")
        return self

    def to_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "workload": self.workload.to_dict(),
            "package_digest": self.package_digest,
            "manifest_digest": self.manifest_digest,
            "trust_mode": self.trust_mode.value,
            "sdk_api_version": self.sdk_api_version,
            "protocol_version": self.protocol_version,
            "manifest_schema_version": self.manifest_schema_version,
            "workflow_schema_version": self.workflow_schema_version,
            "environment_digest": self.environment_digest,
            "verifier": self.verifier.canonical,
            "selected_features": dict(self.selected_features),
            "optional_fallbacks": dict(self.optional_fallbacks),
            "task_key": self.task_key,
            "stage_id": self.stage_id,
            "parameters": thaw_json(self.parameters),
            "inputs": {name: value.to_dict() for name, value in self.inputs.items()},
            "expected_outputs": {
                name: value.to_dict() for name, value in self.expected_outputs.items()
            },
            "resources": self.resources.to_dict(),
            "execution": self.execution.to_dict(),
            "expected_input_keys": {
                name: list(keys) for name, keys in self.expected_input_keys.items()
            },
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @property
    def digest(self) -> str:
        """Canonical digest used to pin a coordinator execution contract."""
        return hashlib.sha256(self.to_json().encode("utf-8")).hexdigest()

    @classmethod
    def from_dict(cls, value: object) -> "TaskSpec":
        if not isinstance(value, Mapping):
            raise ValueError("task specification must be an object")
        fields = {
            "schema_version",
            "workload",
            "package_digest",
            "manifest_digest",
            "trust_mode",
            "sdk_api_version",
            "protocol_version",
            "manifest_schema_version",
            "workflow_schema_version",
            "environment_digest",
            "verifier",
            "selected_features",
            "optional_fallbacks",
            "task_key",
            "stage_id",
            "parameters",
            "inputs",
            "expected_outputs",
            "resources",
            "execution",
            "expected_input_keys",
        }
        require_exact_keys(value, fields, "task specification")
        inputs, outputs = value["inputs"], value["expected_outputs"]
        if not isinstance(inputs, Mapping) or not isinstance(outputs, Mapping):
            raise ValueError("task inputs and expected_outputs must be objects")
        return cls(
            schema_version=value["schema_version"],  # type: ignore[arg-type]
            workload=WorkloadId.from_dict(value["workload"]),
            package_digest=value["package_digest"],  # type: ignore[arg-type]
            manifest_digest=value["manifest_digest"],  # type: ignore[arg-type]
            trust_mode=value["trust_mode"],  # type: ignore[arg-type]
            sdk_api_version=value["sdk_api_version"],  # type: ignore[arg-type]
            protocol_version=value["protocol_version"],  # type: ignore[arg-type]
            manifest_schema_version=value["manifest_schema_version"],  # type: ignore[arg-type]
            workflow_schema_version=value["workflow_schema_version"],  # type: ignore[arg-type]
            environment_digest=value["environment_digest"],  # type: ignore[arg-type]
            verifier=ComponentRef.from_dict(value["verifier"]),
            selected_features=value["selected_features"],  # type: ignore[arg-type]
            optional_fallbacks=value["optional_fallbacks"],  # type: ignore[arg-type]
            task_key=value["task_key"],  # type: ignore[arg-type]
            stage_id=value["stage_id"],  # type: ignore[arg-type]
            parameters=value["parameters"],  # type: ignore[arg-type]
            inputs={
                name: ArtifactCollection.from_dict(item)
                for name, item in inputs.items()
            },
            expected_outputs={
                name: PortSpec.from_dict(item) for name, item in outputs.items()
            },
            resources=ResourceRequirements.from_dict(value["resources"]),
            execution=ExecutionProfile.from_dict(value["execution"]),
            expected_input_keys=value["expected_input_keys"],  # type: ignore[arg-type]
        )

    @classmethod
    def from_json(cls, value: str) -> "TaskSpec":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("task specification must be valid JSON") from error
        return cls.from_dict(decoded)


@dataclass(frozen=True, slots=True)
class WorkflowPlan:
    """The immutable result of planning: tasks plus the exact workload pin.

    Every task must carry the same package, manifest, environment, trust
    mode, schema versions, and negotiated features as the plan itself.
    """

    workload: WorkloadId
    package_digest: str
    manifest_digest: str
    trust_mode: TrustMode
    sdk_api_version: str
    protocol_version: str
    manifest_schema_version: int
    workflow_schema_version: int
    environment_digest: str
    verifier: ComponentRef
    selected_features: Mapping[str, str]
    optional_fallbacks: Mapping[str, str]
    workflow_id: str
    resolved_parameters: Mapping[str, Any]
    tasks: tuple[TaskSpec, ...]
    schema_version: int = 1

    def __post_init__(self) -> None:
        require_schema_version(self.schema_version, 1, "workflow plan schema_version")
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("workflow plan workload must be a WorkloadId")
        object.__setattr__(
            self,
            "package_digest",
            require_sha256(self.package_digest, "plan package_digest", prefixed=True),
        )
        object.__setattr__(
            self,
            "manifest_digest",
            require_sha256(self.manifest_digest, "plan manifest_digest"),
        )
        try:
            trust_mode = TrustMode(self.trust_mode)
        except (TypeError, ValueError) as error:
            raise ValueError("plan trust_mode is unsupported") from error
        object.__setattr__(self, "trust_mode", trust_mode)
        object.__setattr__(
            self,
            "sdk_api_version",
            require_string(self.sdk_api_version, "plan sdk_api_version", max_length=32),
        )
        object.__setattr__(
            self,
            "protocol_version",
            require_string(
                self.protocol_version, "plan protocol_version", max_length=32
            ),
        )
        parse_release(self.sdk_api_version, "plan sdk_api_version")
        parse_release(self.protocol_version, "plan protocol_version")
        object.__setattr__(
            self,
            "manifest_schema_version",
            require_positive_int(
                self.manifest_schema_version, "plan manifest_schema_version"
            ),
        )
        object.__setattr__(
            self,
            "workflow_schema_version",
            require_positive_int(
                self.workflow_schema_version, "plan workflow_schema_version"
            ),
        )
        object.__setattr__(
            self,
            "environment_digest",
            require_sha256(
                self.environment_digest, "plan environment_digest", prefixed=True
            ),
        )
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("plan verifier must be a ComponentRef")
        object.__setattr__(
            self,
            "selected_features",
            _feature_versions(self.selected_features, "plan selected_features"),
        )
        object.__setattr__(
            self,
            "optional_fallbacks",
            _fallbacks(self.optional_fallbacks, "plan optional_fallbacks"),
        )
        if set(self.selected_features).intersection(self.optional_fallbacks):
            raise ValueError("one plan feature cannot be selected and fallbacked")
        object.__setattr__(
            self, "workflow_id", require_identifier(self.workflow_id, "workflow_id")
        )
        object.__setattr__(
            self,
            "resolved_parameters",
            freeze_json_mapping(
                self.resolved_parameters,
                "resolved_parameters",
                forbid_locations=True,
            ),
        )
        tasks = tuple(self.tasks)
        if not tasks or any(not isinstance(task, TaskSpec) for task in tasks):
            raise ValueError("workflow plan tasks must contain at least one TaskSpec")
        keys = [task.task_key for task in tasks]
        if keys != sorted(keys) or len(keys) != len(set(keys)):
            raise ValueError("workflow plan task keys must be unique and ascending")
        for task in tasks:
            if (
                task.workload != self.workload
                or task.package_digest != self.package_digest
                or task.manifest_digest != self.manifest_digest
                or task.trust_mode is not self.trust_mode
                or task.sdk_api_version != self.sdk_api_version
                or task.protocol_version != self.protocol_version
                or task.manifest_schema_version != self.manifest_schema_version
                or task.workflow_schema_version != self.workflow_schema_version
                or task.environment_digest != self.environment_digest
                or task.selected_features != self.selected_features
                or task.optional_fallbacks != self.optional_fallbacks
            ):
                raise ValueError(
                    "workflow plan tasks must carry the plan's exact workload pin"
                )
        object.__setattr__(self, "tasks", tasks)

    def validate_workflow(self, workflow: WorkflowSpec) -> "WorkflowPlan":
        if workflow.workflow_id != self.workflow_id:
            raise ValueError("workflow plan references another workflow")
        if len(self.tasks) > workflow.max_tasks:
            raise ValueError("workflow plan exceeds max_tasks")
        stages = {stage.stage_id: stage for stage in workflow.stages}
        task_counts: dict[str, int] = {}
        for task in self.tasks:
            try:
                task.validate_stage(stages[task.stage_id])
            except KeyError as error:
                raise ValueError(
                    f"workflow plan references unknown stage: {task.stage_id}"
                ) from error
            task_counts[task.stage_id] = task_counts.get(task.stage_id, 0) + 1
            if task_counts[task.stage_id] > stages[task.stage_id].max_fan_out:
                raise ValueError(
                    f"workflow plan exceeds max_fan_out for stage {task.stage_id}"
                )
        return self

    @property
    def digest(self) -> str:
        return hashlib.sha256(self.to_json().encode("utf-8")).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "workload": self.workload.to_dict(),
            "package_digest": self.package_digest,
            "manifest_digest": self.manifest_digest,
            "trust_mode": self.trust_mode.value,
            "sdk_api_version": self.sdk_api_version,
            "protocol_version": self.protocol_version,
            "manifest_schema_version": self.manifest_schema_version,
            "workflow_schema_version": self.workflow_schema_version,
            "environment_digest": self.environment_digest,
            "verifier": self.verifier.canonical,
            "selected_features": dict(self.selected_features),
            "optional_fallbacks": dict(self.optional_fallbacks),
            "workflow_id": self.workflow_id,
            "resolved_parameters": thaw_json(self.resolved_parameters),
            "tasks": [task.to_dict() for task in self.tasks],
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> "WorkflowPlan":
        if not isinstance(value, Mapping):
            raise ValueError("workflow plan must be an object")
        fields = {
            "schema_version",
            "workload",
            "package_digest",
            "manifest_digest",
            "trust_mode",
            "sdk_api_version",
            "protocol_version",
            "manifest_schema_version",
            "workflow_schema_version",
            "environment_digest",
            "verifier",
            "selected_features",
            "optional_fallbacks",
            "workflow_id",
            "resolved_parameters",
            "tasks",
        }
        require_exact_keys(value, fields, "workflow plan")
        tasks = value["tasks"]
        if not isinstance(tasks, list):
            raise ValueError("workflow plan tasks must be an array")
        return cls(
            schema_version=value["schema_version"],  # type: ignore[arg-type]
            workload=WorkloadId.from_dict(value["workload"]),
            package_digest=value["package_digest"],  # type: ignore[arg-type]
            manifest_digest=value["manifest_digest"],  # type: ignore[arg-type]
            trust_mode=value["trust_mode"],  # type: ignore[arg-type]
            sdk_api_version=value["sdk_api_version"],  # type: ignore[arg-type]
            protocol_version=value["protocol_version"],  # type: ignore[arg-type]
            manifest_schema_version=value["manifest_schema_version"],  # type: ignore[arg-type]
            workflow_schema_version=value["workflow_schema_version"],  # type: ignore[arg-type]
            environment_digest=value["environment_digest"],  # type: ignore[arg-type]
            verifier=ComponentRef.from_dict(value["verifier"]),
            selected_features=value["selected_features"],  # type: ignore[arg-type]
            optional_fallbacks=value["optional_fallbacks"],  # type: ignore[arg-type]
            workflow_id=value["workflow_id"],  # type: ignore[arg-type]
            resolved_parameters=value["resolved_parameters"],  # type: ignore[arg-type]
            tasks=tuple(TaskSpec.from_dict(task) for task in tasks),
        )

    @classmethod
    def from_json(cls, value: str) -> "WorkflowPlan":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("workflow plan must be valid JSON") from error
        return cls.from_dict(decoded)


@dataclass(frozen=True, slots=True)
class ExpansionManifest:
    job_id: str
    parent_task_id: str
    parent_task_key: str
    parent_execution_contract_digest: str
    tasks: tuple[TaskSpec, ...]
    max_children: int
    schema_version: int = 1

    def __post_init__(self) -> None:
        require_schema_version(
            self.schema_version, 1, "expansion manifest schema_version"
        )
        object.__setattr__(
            self, "job_id", require_uuid(self.job_id, "expansion job_id")
        )
        object.__setattr__(
            self,
            "parent_task_id",
            require_uuid(self.parent_task_id, "expansion parent_task_id"),
        )
        object.__setattr__(
            self, "parent_task_key", require_task_key(self.parent_task_key)
        )
        object.__setattr__(
            self,
            "parent_execution_contract_digest",
            require_sha256(
                self.parent_execution_contract_digest,
                "expansion parent_execution_contract_digest",
            ),
        )
        object.__setattr__(
            self,
            "max_children",
            require_positive_int(self.max_children, "max_children"),
        )
        tasks = tuple(self.tasks)
        if not tasks or len(tasks) > self.max_children:
            raise ValueError(
                "expansion tasks must be non-empty and within max_children"
            )
        keys = [task.task_key for task in tasks]
        if keys != sorted(keys) or len(keys) != len(set(keys)):
            raise ValueError("expansion child task keys must be unique and ascending")
        if any(not key.startswith(self.parent_task_key + "/") for key in keys):
            raise ValueError(
                "expansion child task keys must be namespaced by the parent"
            )
        first = tasks[0]
        if any(
            task.workload != first.workload
            or task.package_digest != first.package_digest
            or task.manifest_digest != first.manifest_digest
            or task.trust_mode is not first.trust_mode
            or task.sdk_api_version != first.sdk_api_version
            or task.protocol_version != first.protocol_version
            or task.manifest_schema_version != first.manifest_schema_version
            or task.workflow_schema_version != first.workflow_schema_version
            or task.environment_digest != first.environment_digest
            or task.selected_features != first.selected_features
            or task.optional_fallbacks != first.optional_fallbacks
            for task in tasks[1:]
        ):
            raise ValueError("expansion child tasks must carry one exact workload pin")
        object.__setattr__(self, "tasks", tasks)

    def validate_against(
        self,
        parent: TaskSpec,
        workflow: WorkflowSpec,
        *,
        job_id: str,
        parent_task_id: str,
        declared_max_children: int,
        remaining_tasks: int,
        authorized_inputs: Mapping[str, Mapping[str, ArtifactCollection]],
        existing_stage_task_counts: Mapping[str, int],
    ) -> "ExpansionManifest":
        """Validate an expansion against coordinator-owned durable state.

        The IDs and remaining budget are deliberately supplied by the
        coordinator rather than trusted from the package-produced manifest.
        """
        if not isinstance(parent, TaskSpec):
            raise ValueError("expansion parent must be a TaskSpec")
        if not isinstance(workflow, WorkflowSpec):
            raise ValueError("expansion workflow must be a WorkflowSpec")
        if self.job_id != require_uuid(job_id, "coordinator job_id"):
            raise ValueError("expansion belongs to another job")
        if self.parent_task_id != require_uuid(
            parent_task_id, "coordinator parent_task_id"
        ):
            raise ValueError("expansion belongs to another durable parent task")
        if self.parent_task_key != parent.task_key:
            raise ValueError("expansion parent task key does not match")
        if self.parent_execution_contract_digest != parent.digest:
            raise ValueError("expansion parent execution contract does not match")

        remaining = require_nonnegative_int(remaining_tasks, "remaining_tasks")
        stages = {stage.stage_id: stage for stage in workflow.stages}
        try:
            parent_stage = stages[parent.stage_id]
        except KeyError as error:
            raise ValueError(
                "expansion parent references an unknown workflow stage"
            ) from error
        parent.validate_stage(parent_stage)
        if parent_stage.kind is not StageKind.PLAN:
            raise ValueError("v1 expansion parent must be a plan stage")
        declared_limit = require_positive_int(
            declared_max_children,
            "declared_max_children",
        )
        allowed_children = min(declared_limit, remaining)
        if self.max_children > declared_limit or len(self.tasks) > allowed_children:
            raise ValueError("expansion exceeds the coordinator child task budget")

        if not isinstance(authorized_inputs, Mapping):
            raise ValueError("authorized_inputs must be an object")
        allowed_by_target: dict[str, dict[str, ArtifactCollection]] = {}
        for stage_id, ports in authorized_inputs.items():
            canonical_stage = require_identifier(stage_id, "authorized input stage")
            if canonical_stage not in stages or not isinstance(ports, Mapping):
                raise ValueError("authorized_inputs references an unknown stage")
            allowed_ports: dict[str, ArtifactCollection] = {}
            for port_name, collection in ports.items():
                canonical_port = require_identifier(port_name, "authorized input port")
                declaration = stages[canonical_stage].inputs.get(canonical_port)
                if declaration is None or not isinstance(
                    collection, ArtifactCollection
                ):
                    raise ValueError(
                        "authorized_inputs references an unknown input port"
                    )
                declaration.validate_collection(
                    collection,
                    f"authorized input {canonical_stage}.{canonical_port}",
                )
                allowed_ports[canonical_port] = collection
            allowed_by_target[canonical_stage] = allowed_ports

        raw_counts = existing_stage_task_counts
        if not isinstance(raw_counts, Mapping):
            raise ValueError("existing_stage_task_counts must be an object")
        stage_counts: dict[str, int] = {}
        for stage_id, count in raw_counts.items():
            canonical = require_identifier(stage_id, "existing stage task count")
            if canonical not in stages:
                raise ValueError("existing task count references an unknown stage")
            stage_counts[canonical] = require_nonnegative_int(
                count,
                "existing stage task count",
            )

        for task in self.tasks:
            if (
                task.workload != parent.workload
                or task.package_digest != parent.package_digest
                or task.manifest_digest != parent.manifest_digest
                or task.trust_mode is not parent.trust_mode
                or task.sdk_api_version != parent.sdk_api_version
                or task.protocol_version != parent.protocol_version
                or task.manifest_schema_version != parent.manifest_schema_version
                or task.workflow_schema_version != parent.workflow_schema_version
                or task.environment_digest != parent.environment_digest
                or task.selected_features != parent.selected_features
                or task.optional_fallbacks != parent.optional_fallbacks
            ):
                raise ValueError(
                    "expansion child task does not share the parent workload pin"
                )
            try:
                stage = stages[task.stage_id]
            except KeyError as error:
                raise ValueError(
                    "expansion child references an unknown workflow stage"
                ) from error
            if parent.stage_id not in stage.needs:
                raise ValueError(
                    "v1 expansion child must be a direct successor of its parent stage"
                )
            task.validate_stage(stage)
            target_ports = allowed_by_target.get(task.stage_id, {})
            for port_name, collection in task.inputs.items():
                allowed = target_ports.get(port_name)
                if allowed is None or collection.kind is not allowed.kind:
                    raise ValueError(
                        "expansion child input target is not coordinator-authorized"
                    )
                if collection.kind is CollectionKind.ORDERED:
                    cursor = 0
                    for item in collection.items:
                        while (
                            cursor < len(allowed.items)
                            and allowed.items[cursor] != item
                        ):
                            cursor += 1
                        if cursor == len(allowed.items):
                            raise ValueError(
                                "expansion child input is not an authorized ordered subsequence"
                            )
                        cursor += 1
                elif any(item not in allowed.items for item in collection.items):
                    raise ValueError(
                        "expansion child input artifact is not coordinator-authorized"
                    )
            stage_counts[task.stage_id] = stage_counts.get(task.stage_id, 0) + 1
            if stage_counts[task.stage_id] > stage.max_fan_out:
                raise ValueError(
                    f"expansion exceeds max_fan_out for stage {task.stage_id}"
                )
        return self

    @property
    def digest(self) -> str:
        return hashlib.sha256(
            canonical_json(self.to_dict()).encode("utf-8")
        ).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "job_id": self.job_id,
            "parent_task_id": self.parent_task_id,
            "parent_task_key": self.parent_task_key,
            "parent_execution_contract_digest": self.parent_execution_contract_digest,
            "max_children": self.max_children,
            "tasks": [task.to_dict() for task in self.tasks],
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> "ExpansionManifest":
        if not isinstance(value, Mapping):
            raise ValueError("expansion manifest must be an object")
        fields = {
            "schema_version",
            "job_id",
            "parent_task_id",
            "parent_task_key",
            "parent_execution_contract_digest",
            "max_children",
            "tasks",
        }
        require_exact_keys(value, fields, "expansion manifest")
        tasks = value["tasks"]
        if not isinstance(tasks, list):
            raise ValueError("expansion tasks must be an array")
        return cls(
            schema_version=value["schema_version"],  # type: ignore[arg-type]
            job_id=value["job_id"],  # type: ignore[arg-type]
            parent_task_id=value["parent_task_id"],  # type: ignore[arg-type]
            parent_task_key=value["parent_task_key"],  # type: ignore[arg-type]
            parent_execution_contract_digest=value["parent_execution_contract_digest"],  # type: ignore[arg-type]
            max_children=value["max_children"],  # type: ignore[arg-type]
            tasks=tuple(TaskSpec.from_dict(task) for task in tasks),
        )

    @classmethod
    def from_json(cls, value: str) -> "ExpansionManifest":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("expansion manifest must be valid JSON") from error
        return cls.from_dict(decoded)
