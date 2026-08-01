"""Versioned workflow DAG and bounded advanced-stage declarations."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from types import MappingProxyType
from typing import Mapping

from ._validation import (
    enum_value,
    require_entry_point,
    require_exact_keys,
    require_identifier,
    require_nonnegative_int,
    require_positive_int,
    require_schema_version,
    require_string,
)
from .artifacts import PortSpec
from .execution import ExecutionProfile, NetworkPolicy, RetryPolicy
from .identity import ComponentRef, SchemaRef, WORKFLOW_SCHEMA_VERSION
from .resources import ResourceRequirements


class StageKind(str, Enum):
    PLAN = "plan"
    MAP = "map"
    REDUCE = "reduce"
    VERIFY = "verify"
    LOOP_CONTROLLER = "loop-controller"
    STREAM = "stream"
    SERVICE = "service"
    SIDE_EFFECT = "side-effect"


class WorkflowFailurePolicy(str, Enum):
    FAIL_FAST = "fail_fast"
    CONTINUE_INDEPENDENT = "continue_independent"
    ALLOW_PARTIAL = "allow_partial"
    COMPENSATE = "compensate"


@dataclass(frozen=True, slots=True)
class LoopSpec:
    state_schema: SchemaRef
    max_iterations: int
    max_wall_seconds: int
    body_workflow: str
    continue_when: ComponentRef
    checkpoint_every: int
    on_limit: str = "fail"

    def __post_init__(self) -> None:
        if not isinstance(self.state_schema, SchemaRef):
            raise ValueError("loop state_schema must be a SchemaRef")
        object.__setattr__(self, "max_iterations", require_positive_int(self.max_iterations, "loop.max_iterations"))
        object.__setattr__(self, "max_wall_seconds", require_positive_int(self.max_wall_seconds, "loop.max_wall_seconds"))
        object.__setattr__(self, "body_workflow", require_identifier(self.body_workflow, "loop.body_workflow"))
        if not isinstance(self.continue_when, ComponentRef):
            raise ValueError("loop continue_when must be a ComponentRef")
        object.__setattr__(self, "checkpoint_every", require_positive_int(self.checkpoint_every, "loop.checkpoint_every"))
        if self.checkpoint_every > self.max_iterations:
            raise ValueError("loop checkpoint_every must not exceed max_iterations")
        if self.on_limit not in {"fail", "accept-best", "return-inconclusive"}:
            raise ValueError("loop on_limit must be fail, accept-best, or return-inconclusive")

    def to_dict(self) -> dict[str, object]:
        return {
            "state_schema": self.state_schema.canonical,
            "max_iterations": self.max_iterations,
            "max_wall_seconds": self.max_wall_seconds,
            "body_workflow": self.body_workflow,
            "continue_when": self.continue_when.canonical,
            "checkpoint_every": self.checkpoint_every,
            "on_limit": self.on_limit,
        }

    @classmethod
    def from_dict(cls, value: object) -> "LoopSpec":
        if not isinstance(value, Mapping):
            raise ValueError("loop specification must be an object")
        fields = {
            "state_schema", "max_iterations", "max_wall_seconds", "body_workflow",
            "continue_when", "checkpoint_every", "on_limit",
        }
        require_exact_keys(value, fields, "loop specification")
        return cls(
            state_schema=SchemaRef.from_dict(value["state_schema"]),
            max_iterations=value["max_iterations"],  # type: ignore[arg-type]
            max_wall_seconds=value["max_wall_seconds"],  # type: ignore[arg-type]
            body_workflow=value["body_workflow"],  # type: ignore[arg-type]
            continue_when=ComponentRef.from_dict(value["continue_when"]),
            checkpoint_every=value["checkpoint_every"],  # type: ignore[arg-type]
            on_limit=value["on_limit"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class StreamSpec:
    source: str
    partitioning: str
    checkpoint_schema: SchemaRef
    window_seconds: int
    watermark_seconds: int
    backpressure_limit: int
    delivery_guarantee: str
    max_windows: int

    def __post_init__(self) -> None:
        object.__setattr__(self, "source", require_identifier(self.source, "stream.source"))
        object.__setattr__(self, "partitioning", require_identifier(self.partitioning, "stream.partitioning"))
        if not isinstance(self.checkpoint_schema, SchemaRef):
            raise ValueError("stream checkpoint_schema must be a SchemaRef")
        object.__setattr__(self, "window_seconds", require_positive_int(self.window_seconds, "stream.window_seconds"))
        object.__setattr__(self, "watermark_seconds", require_nonnegative_int(self.watermark_seconds, "stream.watermark_seconds"))
        object.__setattr__(
            self,
            "backpressure_limit",
            require_positive_int(self.backpressure_limit, "stream.backpressure_limit"),
        )
        if self.delivery_guarantee not in {"at_least_once", "exactly_once"}:
            raise ValueError("stream delivery_guarantee must be at_least_once or exactly_once")
        object.__setattr__(self, "max_windows", require_positive_int(self.max_windows, "stream.max_windows"))

    def to_dict(self) -> dict[str, object]:
        return {
            "source": self.source,
            "partitioning": self.partitioning,
            "checkpoint_schema": self.checkpoint_schema.canonical,
            "window_seconds": self.window_seconds,
            "watermark_seconds": self.watermark_seconds,
            "backpressure_limit": self.backpressure_limit,
            "delivery_guarantee": self.delivery_guarantee,
            "max_windows": self.max_windows,
        }

    @classmethod
    def from_dict(cls, value: object) -> "StreamSpec":
        if not isinstance(value, Mapping):
            raise ValueError("stream specification must be an object")
        fields = {
            "source", "partitioning", "checkpoint_schema", "window_seconds",
            "watermark_seconds", "backpressure_limit", "delivery_guarantee", "max_windows",
        }
        require_exact_keys(value, fields, "stream specification")
        return cls(
            source=value["source"],  # type: ignore[arg-type]
            partitioning=value["partitioning"],  # type: ignore[arg-type]
            checkpoint_schema=SchemaRef.from_dict(value["checkpoint_schema"]),
            window_seconds=value["window_seconds"],  # type: ignore[arg-type]
            watermark_seconds=value["watermark_seconds"],  # type: ignore[arg-type]
            backpressure_limit=value["backpressure_limit"],  # type: ignore[arg-type]
            delivery_guarantee=value["delivery_guarantee"],  # type: ignore[arg-type]
            max_windows=value["max_windows"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class GangSpec:
    replicas: int
    per_replica_resources: ResourceRequirements
    same_topology_group: bool = False
    bandwidth_class: str | None = None
    failure_mode: str = "fail_all"

    def __post_init__(self) -> None:
        object.__setattr__(self, "replicas", require_positive_int(self.replicas, "gang.replicas"))
        if self.replicas < 2:
            raise ValueError("gang execution requires at least two replicas")
        if not isinstance(self.per_replica_resources, ResourceRequirements):
            raise ValueError("gang per_replica_resources must be ResourceRequirements")
        if not isinstance(self.same_topology_group, bool):
            raise ValueError("gang same_topology_group must be a boolean")
        if self.bandwidth_class is not None:
            object.__setattr__(self, "bandwidth_class", require_identifier(self.bandwidth_class, "gang.bandwidth_class"))
        if self.failure_mode != "fail_all":
            raise ValueError("SDK v1 gang failure_mode must be fail_all")

    def to_dict(self) -> dict[str, object]:
        return {
            "replicas": self.replicas,
            "per_replica_resources": self.per_replica_resources.to_dict(),
            "same_topology_group": self.same_topology_group,
            "bandwidth_class": self.bandwidth_class,
            "failure_mode": self.failure_mode,
        }

    @classmethod
    def from_dict(cls, value: object) -> "GangSpec":
        if not isinstance(value, Mapping):
            raise ValueError("gang specification must be an object")
        fields = {
            "replicas", "per_replica_resources", "same_topology_group",
            "bandwidth_class", "failure_mode",
        }
        require_exact_keys(value, fields, "gang specification")
        return cls(
            replicas=value["replicas"],  # type: ignore[arg-type]
            per_replica_resources=ResourceRequirements.from_dict(value["per_replica_resources"]),
            same_topology_group=value["same_topology_group"],  # type: ignore[arg-type]
            bandwidth_class=value["bandwidth_class"],  # type: ignore[arg-type]
            failure_mode=value["failure_mode"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class SideEffectSpec:
    target: str
    idempotency_key_parameter: str
    credential_scope: str
    compensation: str
    manual_approval: bool = True

    def __post_init__(self) -> None:
        object.__setattr__(self, "target", require_identifier(self.target, "side_effect.target"))
        object.__setattr__(
            self,
            "idempotency_key_parameter",
            require_identifier(self.idempotency_key_parameter, "side_effect.idempotency_key_parameter"),
        )
        object.__setattr__(self, "credential_scope", require_identifier(self.credential_scope, "side_effect.credential_scope"))
        object.__setattr__(self, "compensation", require_identifier(self.compensation, "side_effect.compensation"))
        if not isinstance(self.manual_approval, bool):
            raise ValueError("side_effect.manual_approval must be a boolean")

    def to_dict(self) -> dict[str, object]:
        return {
            "target": self.target,
            "idempotency_key_parameter": self.idempotency_key_parameter,
            "credential_scope": self.credential_scope,
            "compensation": self.compensation,
            "manual_approval": self.manual_approval,
        }

    @classmethod
    def from_dict(cls, value: object) -> "SideEffectSpec":
        if not isinstance(value, Mapping):
            raise ValueError("side-effect specification must be an object")
        fields = {
            "target", "idempotency_key_parameter", "credential_scope", "compensation", "manual_approval",
        }
        require_exact_keys(value, fields, "side-effect specification")
        return cls(**value)  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class PortRef:
    """A stage port, or an external workflow input when ``stage_id`` is None."""

    port: str
    stage_id: str | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "port", require_identifier(self.port, "port reference"))
        if self.stage_id is not None:
            object.__setattr__(self, "stage_id", require_identifier(self.stage_id, "stage reference"))

    def to_dict(self) -> dict[str, object]:
        return {"stage_id": self.stage_id, "port": self.port}

    @classmethod
    def from_dict(cls, value: object) -> "PortRef":
        if not isinstance(value, Mapping):
            raise ValueError("port reference must be an object")
        require_exact_keys(value, {"stage_id", "port"}, "port reference")
        return cls(stage_id=value["stage_id"], port=value["port"])  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class ArtifactEdge:
    source: PortRef
    target: PortRef

    def __post_init__(self) -> None:
        if not isinstance(self.source, PortRef) or not isinstance(self.target, PortRef):
            raise ValueError("artifact edge endpoints must be PortRef values")
        if self.target.stage_id is None:
            raise ValueError("artifact edge target must be a stage input")

    def to_dict(self) -> dict[str, object]:
        return {"source": self.source.to_dict(), "target": self.target.to_dict()}

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactEdge":
        if not isinstance(value, Mapping):
            raise ValueError("artifact edge must be an object")
        require_exact_keys(value, {"source", "target"}, "artifact edge")
        return cls(source=PortRef.from_dict(value["source"]), target=PortRef.from_dict(value["target"]))


def _port_mapping(value: Mapping[str, PortSpec], field: str) -> Mapping[str, PortSpec]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    ports: dict[str, PortSpec] = {}
    for name, port in value.items():
        canonical = require_identifier(name, f"{field} port")
        if not isinstance(port, PortSpec):
            raise ValueError(f"{field} values must be PortSpec values")
        ports[canonical] = port
    return MappingProxyType(ports)


@dataclass(frozen=True, slots=True)
class StageSpec:
    stage_id: str
    kind: StageKind
    entry_point: str
    needs: tuple[str, ...]
    inputs: Mapping[str, PortSpec]
    outputs: Mapping[str, PortSpec]
    parameter_names: tuple[str, ...]
    resources: ResourceRequirements
    execution: ExecutionProfile
    retry: RetryPolicy
    verifier: ComponentRef | None = None
    trust_modes: tuple[str, ...] = ("trusted",)
    max_fan_out: int = 1
    cacheable: bool = False
    loop: LoopSpec | None = None
    stream: StreamSpec | None = None
    gang: GangSpec | None = None
    side_effect: SideEffectSpec | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "stage_id", require_identifier(self.stage_id, "stage_id"))
        object.__setattr__(self, "kind", enum_value(StageKind, self.kind, "stage.kind"))
        object.__setattr__(self, "entry_point", require_entry_point(self.entry_point, "stage.entry_point"))
        needs = tuple(require_identifier(value, "stage.needs") for value in self.needs)
        if self.stage_id in needs or len(needs) != len(set(needs)):
            raise ValueError("stage.needs must contain unique other stage IDs")
        object.__setattr__(self, "needs", needs)
        object.__setattr__(self, "inputs", _port_mapping(self.inputs, "stage.inputs"))
        object.__setattr__(self, "outputs", _port_mapping(self.outputs, "stage.outputs"))
        if not self.outputs:
            raise ValueError("a stage must declare at least one output port")
        names = tuple(require_identifier(value, "parameter_name") for value in self.parameter_names)
        if len(names) != len(set(names)):
            raise ValueError("parameter_names must be unique")
        object.__setattr__(self, "parameter_names", names)
        if not isinstance(self.resources, ResourceRequirements):
            raise ValueError("stage.resources must be ResourceRequirements")
        if not isinstance(self.execution, ExecutionProfile):
            raise ValueError("stage.execution must be ExecutionProfile")
        self.execution.validate_resources(self.resources)
        if not isinstance(self.retry, RetryPolicy):
            raise ValueError("stage.retry must be RetryPolicy")
        if self.verifier is not None and not isinstance(self.verifier, ComponentRef):
            raise ValueError("stage.verifier must be a ComponentRef")
        modes = tuple(require_identifier(value, "trust_mode") for value in self.trust_modes)
        if not modes or len(modes) != len(set(modes)):
            raise ValueError("stage.trust_modes must be non-empty and unique")
        if not set(modes).issubset({"trusted", "verified", "untrusted_quorum"}):
            raise ValueError("stage.trust_modes contains an unsupported trust mode")
        object.__setattr__(self, "trust_modes", modes)
        object.__setattr__(self, "max_fan_out", require_positive_int(self.max_fan_out, "stage.max_fan_out"))
        if not isinstance(self.cacheable, bool):
            raise ValueError("stage.cacheable must be a boolean")
        advanced = {
            StageKind.LOOP_CONTROLLER: self.loop,
            StageKind.STREAM: self.stream,
            StageKind.SIDE_EFFECT: self.side_effect,
        }
        expected_types = {
            StageKind.LOOP_CONTROLLER: LoopSpec,
            StageKind.STREAM: StreamSpec,
            StageKind.SIDE_EFFECT: SideEffectSpec,
        }
        for kind, declaration in advanced.items():
            if self.kind is kind and declaration is None:
                raise ValueError(f"{kind.value} stage requires its bounded declaration")
            if self.kind is not kind and declaration is not None:
                raise ValueError(f"{kind.value} declaration is valid only for a {kind.value} stage")
            if declaration is not None and not isinstance(declaration, expected_types[kind]):
                raise ValueError(f"{kind.value} declaration has the wrong type")
        if self.gang is not None and not isinstance(self.gang, GangSpec):
            raise ValueError("stage.gang must be a GangSpec")
        if self.gang is not None:
            self.execution.validate_resources(self.gang.per_replica_resources)
            if self.kind is StageKind.SIDE_EFFECT:
                raise ValueError("side-effect stages cannot use gang execution")
        if self.kind is StageKind.SIDE_EFFECT:
            if self.cacheable:
                raise ValueError("side-effect stages cannot be cached")
            if self.execution.network not in {NetworkPolicy.ALLOWLISTED_EGRESS, NetworkPolicy.TRUSTED}:
                raise ValueError("side-effect stages require explicit egress")
            assert self.side_effect is not None
            if self.side_effect.idempotency_key_parameter not in self.parameter_names:
                raise ValueError("side-effect idempotency key must be projected into the stage")

    def to_dict(self) -> dict[str, object]:
        return {
            "stage_id": self.stage_id,
            "kind": self.kind.value,
            "entry_point": self.entry_point,
            "needs": list(self.needs),
            "inputs": {name: port.to_dict() for name, port in self.inputs.items()},
            "outputs": {name: port.to_dict() for name, port in self.outputs.items()},
            "parameter_names": list(self.parameter_names),
            "resources": self.resources.to_dict(),
            "execution": self.execution.to_dict(),
            "retry": self.retry.to_dict(),
            "verifier": self.verifier.canonical if self.verifier is not None else None,
            "trust_modes": list(self.trust_modes),
            "max_fan_out": self.max_fan_out,
            "cacheable": self.cacheable,
            "loop": self.loop.to_dict() if self.loop is not None else None,
            "stream": self.stream.to_dict() if self.stream is not None else None,
            "gang": self.gang.to_dict() if self.gang is not None else None,
            "side_effect": self.side_effect.to_dict() if self.side_effect is not None else None,
        }

    @classmethod
    def from_dict(cls, value: object) -> "StageSpec":
        if not isinstance(value, Mapping):
            raise ValueError("stage specification must be an object")
        fields = {
            "stage_id", "kind", "entry_point", "needs", "inputs", "outputs",
            "parameter_names", "resources", "execution", "retry", "verifier",
            "trust_modes", "max_fan_out", "cacheable", "loop", "stream", "gang", "side_effect",
        }
        require_exact_keys(value, fields, "stage specification")
        arrays = (value["needs"], value["parameter_names"], value["trust_modes"])
        if any(not isinstance(item, list) for item in arrays):
            raise ValueError("stage needs, parameter_names, and trust_modes must be arrays")
        inputs, outputs = value["inputs"], value["outputs"]
        if not isinstance(inputs, Mapping) or not isinstance(outputs, Mapping):
            raise ValueError("stage inputs and outputs must be objects")
        return cls(
            stage_id=value["stage_id"],  # type: ignore[arg-type]
            kind=value["kind"],  # type: ignore[arg-type]
            entry_point=value["entry_point"],  # type: ignore[arg-type]
            needs=tuple(value["needs"]),  # type: ignore[arg-type]
            inputs={name: PortSpec.from_dict(port) for name, port in inputs.items()},
            outputs={name: PortSpec.from_dict(port) for name, port in outputs.items()},
            parameter_names=tuple(value["parameter_names"]),  # type: ignore[arg-type]
            resources=ResourceRequirements.from_dict(value["resources"]),
            execution=ExecutionProfile.from_dict(value["execution"]),
            retry=RetryPolicy.from_dict(value["retry"]),
            verifier=None if value["verifier"] is None else ComponentRef.from_dict(value["verifier"]),
            trust_modes=tuple(value["trust_modes"]),  # type: ignore[arg-type]
            max_fan_out=value["max_fan_out"],  # type: ignore[arg-type]
            cacheable=value["cacheable"],  # type: ignore[arg-type]
            loop=None if value["loop"] is None else LoopSpec.from_dict(value["loop"]),
            stream=None if value["stream"] is None else StreamSpec.from_dict(value["stream"]),
            gang=None if value["gang"] is None else GangSpec.from_dict(value["gang"]),
            side_effect=None if value["side_effect"] is None else SideEffectSpec.from_dict(value["side_effect"]),
        )


@dataclass(frozen=True, slots=True)
class WorkflowSpec:
    workflow_id: str
    inputs: Mapping[str, PortSpec]
    stages: tuple[StageSpec, ...]
    edges: tuple[ArtifactEdge, ...]
    outputs: Mapping[str, PortRef]
    failure_policy: WorkflowFailurePolicy = WorkflowFailurePolicy.FAIL_FAST
    max_tasks: int = 10_000
    max_output_bytes: int = 10 * 1024 * 1024 * 1024
    schema_version: int = WORKFLOW_SCHEMA_VERSION

    def __post_init__(self) -> None:
        require_schema_version(self.schema_version, WORKFLOW_SCHEMA_VERSION, "workflow schema_version")
        object.__setattr__(self, "workflow_id", require_identifier(self.workflow_id, "workflow_id"))
        object.__setattr__(self, "inputs", _port_mapping(self.inputs, "workflow.inputs"))
        stages = tuple(self.stages)
        if not stages or any(not isinstance(stage, StageSpec) for stage in stages):
            raise ValueError("workflow stages must contain at least one StageSpec")
        stage_by_id = {stage.stage_id: stage for stage in stages}
        if len(stage_by_id) != len(stages):
            raise ValueError("workflow stage IDs must be unique")
        object.__setattr__(self, "stages", stages)
        edges = tuple(self.edges)
        if any(not isinstance(edge, ArtifactEdge) for edge in edges):
            raise ValueError("workflow edges must contain ArtifactEdge values")
        if len({(edge.source, edge.target) for edge in edges}) != len(edges):
            raise ValueError("workflow edges must be unique")
        object.__setattr__(self, "edges", edges)
        if not isinstance(self.outputs, Mapping) or not self.outputs:
            raise ValueError("workflow outputs must be a non-empty object")
        outputs: dict[str, PortRef] = {}
        for name, reference in self.outputs.items():
            canonical = require_identifier(name, "workflow output")
            if not isinstance(reference, PortRef) or reference.stage_id is None:
                raise ValueError("workflow outputs must reference stage output ports")
            outputs[canonical] = reference
        object.__setattr__(self, "outputs", MappingProxyType(outputs))
        object.__setattr__(
            self,
            "failure_policy",
            enum_value(WorkflowFailurePolicy, self.failure_policy, "workflow.failure_policy"),
        )
        object.__setattr__(self, "max_tasks", require_positive_int(self.max_tasks, "workflow.max_tasks"))
        object.__setattr__(
            self,
            "max_output_bytes",
            require_positive_int(self.max_output_bytes, "workflow.max_output_bytes"),
        )
        self._validate_graph(stage_by_id)

    def _source_port(self, reference: PortRef, stages: Mapping[str, StageSpec]) -> PortSpec:
        if reference.stage_id is None:
            try:
                return self.inputs[reference.port]
            except KeyError as error:
                raise ValueError(f"unknown workflow input port: {reference.port}") from error
        try:
            stage = stages[reference.stage_id]
            return stage.outputs[reference.port]
        except KeyError as error:
            raise ValueError(
                f"unknown source stage output: {reference.stage_id}.{reference.port}"
            ) from error

    def _validate_graph(self, stages: Mapping[str, StageSpec]) -> None:
        incoming: dict[tuple[str, str], ArtifactEdge] = {}
        dependencies: dict[str, set[str]] = {stage_id: set() for stage_id in stages}
        for edge in self.edges:
            source_port = self._source_port(edge.source, stages)
            assert edge.target.stage_id is not None
            try:
                target_stage = stages[edge.target.stage_id]
                target_port = target_stage.inputs[edge.target.port]
            except KeyError as error:
                raise ValueError(
                    f"unknown target stage input: {edge.target.stage_id}.{edge.target.port}"
                ) from error
            target_key = (edge.target.stage_id, edge.target.port)
            if target_key in incoming:
                raise ValueError("each stage input must have exactly one artifact edge")
            incoming[target_key] = edge
            same_schema = source_port.schema == target_port.schema
            direct_match = source_port == target_port
            map_fan_in = (
                source_port.cardinality.value == "one"
                and target_port.cardinality.value == "many"
                and target_port.collection.value in {"ordered", "keyed", "set"}
            )
            if not same_schema or not (direct_match or map_fan_in):
                raise ValueError("artifact edge source and target port declarations are incompatible")
            if edge.source.stage_id is not None:
                dependencies[edge.target.stage_id].add(edge.source.stage_id)
        for stage in stages.values():
            missing = [name for name in stage.inputs if (stage.stage_id, name) not in incoming]
            if missing:
                raise ValueError(
                    f"stage {stage.stage_id} has unbound inputs: {', '.join(sorted(missing))}"
                )
            if dependencies[stage.stage_id] != set(stage.needs):
                raise ValueError(f"stage {stage.stage_id} needs do not match its artifact edges")
        remaining = {name: set(values) for name, values in dependencies.items()}
        ready = sorted(name for name, values in remaining.items() if not values)
        visited: list[str] = []
        while ready:
            current = ready.pop(0)
            visited.append(current)
            for name, values in remaining.items():
                if current in values:
                    values.remove(current)
                    if not values and name not in visited and name not in ready:
                        ready.append(name)
                        ready.sort()
        if len(visited) != len(stages):
            raise ValueError("workflow graph must be acyclic")
        for reference in self.outputs.values():
            self._source_port(reference, stages)

    def output_ports(self) -> Mapping[str, PortSpec]:
        stages = {stage.stage_id: stage for stage in self.stages}
        return MappingProxyType({
            name: self._source_port(reference, stages)
            for name, reference in self.outputs.items()
        })

    def to_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "workflow_id": self.workflow_id,
            "inputs": {name: port.to_dict() for name, port in self.inputs.items()},
            "stages": [stage.to_dict() for stage in self.stages],
            "edges": [edge.to_dict() for edge in self.edges],
            "outputs": {name: reference.to_dict() for name, reference in self.outputs.items()},
            "failure_policy": self.failure_policy.value,
            "max_tasks": self.max_tasks,
            "max_output_bytes": self.max_output_bytes,
        }

    @classmethod
    def from_dict(cls, value: object) -> "WorkflowSpec":
        if not isinstance(value, Mapping):
            raise ValueError("workflow specification must be an object")
        fields = {
            "schema_version", "workflow_id", "inputs", "stages", "edges",
            "outputs", "failure_policy", "max_tasks", "max_output_bytes",
        }
        require_exact_keys(value, fields, "workflow specification")
        inputs, outputs = value["inputs"], value["outputs"]
        stages, edges = value["stages"], value["edges"]
        if not isinstance(inputs, Mapping) or not isinstance(outputs, Mapping):
            raise ValueError("workflow inputs and outputs must be objects")
        if not isinstance(stages, list) or not isinstance(edges, list):
            raise ValueError("workflow stages and edges must be arrays")
        return cls(
            schema_version=value["schema_version"],  # type: ignore[arg-type]
            workflow_id=value["workflow_id"],  # type: ignore[arg-type]
            inputs={name: PortSpec.from_dict(port) for name, port in inputs.items()},
            stages=tuple(StageSpec.from_dict(stage) for stage in stages),
            edges=tuple(ArtifactEdge.from_dict(edge) for edge in edges),
            outputs={name: PortRef.from_dict(reference) for name, reference in outputs.items()},
            failure_policy=value["failure_policy"],  # type: ignore[arg-type]
            max_tasks=value["max_tasks"],  # type: ignore[arg-type]
            max_output_bytes=value["max_output_bytes"],  # type: ignore[arg-type]
        )
