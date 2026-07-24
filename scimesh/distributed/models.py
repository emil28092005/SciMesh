"""Versioned, JSON-safe value objects for distributed workload contracts."""

from __future__ import annotations

import json
import math
import re
from dataclasses import dataclass
from typing import Any, Mapping, Sequence
from uuid import UUID


SCHEMA_VERSION = 1
_WORKLOAD_NAME = re.compile(r"^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$")


def _canonical_uuid(value: object, field: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"{field} must be a UUID string")
    try:
        return str(UUID(value))
    except ValueError as error:
        raise ValueError(f"{field} must be a UUID string") from error


def _sha256(value: object, field: str) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError(f"{field} must be a lowercase SHA-256 hex digest")
    return value


def _content_type(value: object, field: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 128:
        raise ValueError(f"{field} must be a non-empty content type")
    if any(character.isspace() or ord(character) < 32 for character in value):
        raise ValueError(f"{field} must be a non-empty content type")
    return value


def _workload_name(value: object, field: str = "workload") -> str:
    if not isinstance(value, str) or not _WORKLOAD_NAME.fullmatch(value):
        raise ValueError(f"{field} must be a canonical hyphenated workload name")
    return value


def _json_value(value: object, field: str) -> Any:
    """Deep-copy a JSON value and reject non-finite or non-string-key data."""
    if value is None or isinstance(value, (bool, int)):
        return value
    if isinstance(value, str):
        # Coordinator-owned artifacts are represented exclusively by
        # ArtifactReference. A URI or a local path in a generic JSON payload
        # would let a planner accidentally leak a bridge/worker implementation
        # detail into durable task metadata.
        forbidden_prefixes = ("file://", "worker://", "http://", "https://", "s3://", "/")
        is_windows_path = len(value) >= 3 and value[0].isalpha() and value[1:3] in (":/", ":\\")
        if value.startswith(forbidden_prefixes) or is_windows_path:
            raise ValueError(f"{field} must not contain a URI or local path")
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"{field} must not contain NaN or infinity")
        return value
    if isinstance(value, Mapping):
        copied: dict[str, Any] = {}
        for key, child in value.items():
            if not isinstance(key, str):
                raise ValueError(f"{field} must use string object keys")
            copied[key] = _json_value(child, f"{field}.{key}")
        return copied
    if isinstance(value, (list, tuple)):
        return [_json_value(child, f"{field}[]") for child in value]
    raise ValueError(f"{field} must contain only JSON-compatible values")


def _json_mapping(value: object, field: str) -> dict[str, Any]:
    if not isinstance(value, Mapping):
        raise ValueError(f"{field} must be an object")
    return _json_value(value, field)


@dataclass(frozen=True)
class ArtifactReference:
    """Immutable coordinator-owned artifact identity used in a plan."""

    artifact_id: str
    sha256: str
    content_type: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "artifact_id", _canonical_uuid(self.artifact_id, "artifact_id"))
        object.__setattr__(self, "sha256", _sha256(self.sha256, "sha256"))
        object.__setattr__(self, "content_type", _content_type(self.content_type, "content_type"))

    def to_dict(self) -> dict[str, str]:
        return {
            "artifact_id": self.artifact_id,
            "sha256": self.sha256,
            "content_type": self.content_type,
        }

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactReference":
        if not isinstance(value, Mapping):
            raise ValueError("artifact reference must be an object")
        _require_exact_keys(value, {"artifact_id", "sha256", "content_type"}, "artifact reference")
        return cls(
            artifact_id=value["artifact_id"],
            sha256=value["sha256"],
            content_type=value["content_type"],
        )


@dataclass(frozen=True)
class PlannedTask:
    """One deterministically indexed, artifact-backed worker task."""

    chunk_index: int
    input_artifact: ArtifactReference
    parameters: Mapping[str, object]

    def __post_init__(self) -> None:
        if isinstance(self.chunk_index, bool) or not isinstance(self.chunk_index, int) or self.chunk_index < 0:
            raise ValueError("chunk_index must be a non-negative integer")
        if not isinstance(self.input_artifact, ArtifactReference):
            raise ValueError("input_artifact must be an ArtifactReference")
        object.__setattr__(self, "parameters", _json_mapping(self.parameters, "task parameters"))

    def to_dict(self) -> dict[str, Any]:
        return {
            "chunk_index": self.chunk_index,
            "input_artifact": self.input_artifact.to_dict(),
            "parameters": _json_value(self.parameters, "task parameters"),
        }

    @classmethod
    def from_dict(cls, value: object) -> "PlannedTask":
        if not isinstance(value, Mapping):
            raise ValueError("planned task must be an object")
        _require_exact_keys(value, {"chunk_index", "input_artifact", "parameters"}, "planned task")
        return cls(
            chunk_index=value["chunk_index"],
            input_artifact=ArtifactReference.from_dict(value["input_artifact"]),
            parameters=value["parameters"],
        )


@dataclass(frozen=True)
class DistributedPlan:
    """The complete schema-versioned output of a distributed planner."""

    workload: str
    resolved_parameters: Mapping[str, object]
    tasks: Sequence[PlannedTask]
    schema_version: int = SCHEMA_VERSION

    def __post_init__(self) -> None:
        if self.schema_version != SCHEMA_VERSION:
            raise ValueError(f"schema_version must be {SCHEMA_VERSION}")
        object.__setattr__(self, "workload", _workload_name(self.workload))
        object.__setattr__(self, "resolved_parameters", _json_mapping(self.resolved_parameters, "resolved_parameters"))
        task_list = tuple(self.tasks)
        if not task_list:
            raise ValueError("plan must contain at least one task")
        if any(not isinstance(task, PlannedTask) for task in task_list):
            raise ValueError("tasks must contain PlannedTask values")
        indexes = [task.chunk_index for task in task_list]
        if indexes != sorted(indexes) or len(set(indexes)) != len(indexes):
            raise ValueError("tasks must have unique, ascending chunk_index values")
        object.__setattr__(self, "tasks", task_list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "schema_version": self.schema_version,
            "workload": self.workload,
            "resolved_parameters": _json_value(self.resolved_parameters, "resolved_parameters"),
            "tasks": [task.to_dict() for task in self.tasks],
        }

    def to_json(self) -> str:
        """Return stable JSON suitable for hashing, tests, and durable payloads."""
        return json.dumps(self.to_dict(), sort_keys=True, separators=(",", ":"), allow_nan=False)

    @classmethod
    def from_dict(cls, value: object) -> "DistributedPlan":
        if not isinstance(value, Mapping):
            raise ValueError("distributed plan must be an object")
        _require_exact_keys(
            value,
            {"schema_version", "workload", "resolved_parameters", "tasks"},
            "distributed plan",
        )
        raw_tasks = value["tasks"]
        if not isinstance(raw_tasks, list):
            raise ValueError("tasks must be an array")
        return cls(
            schema_version=value["schema_version"],
            workload=value["workload"],
            resolved_parameters=value["resolved_parameters"],
            tasks=tuple(PlannedTask.from_dict(task) for task in raw_tasks),
        )

    @classmethod
    def from_json(cls, value: str) -> "DistributedPlan":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError) as error:
            raise ValueError("distributed plan must be valid JSON") from error
        return cls.from_dict(decoded)


@dataclass(frozen=True)
class CompletedPartial:
    """Coordinator-owned partial output supplied to a reducer."""

    chunk_index: int
    artifact: ArtifactReference
    metrics: Mapping[str, int | float]

    def __post_init__(self) -> None:
        if isinstance(self.chunk_index, bool) or not isinstance(self.chunk_index, int) or self.chunk_index < 0:
            raise ValueError("chunk_index must be a non-negative integer")
        if not isinstance(self.artifact, ArtifactReference):
            raise ValueError("artifact must be an ArtifactReference")
        if not isinstance(self.metrics, Mapping):
            raise ValueError("metrics must be an object")
        metrics: dict[str, int | float] = {}
        for name, value in self.metrics.items():
            if not isinstance(name, str) or not name:
                raise ValueError("metric names must be non-empty strings")
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
                raise ValueError("metric values must be finite JSON numbers")
            metrics[name] = value
        object.__setattr__(self, "metrics", metrics)


@dataclass(frozen=True)
class FinalResult:
    """A reducer's durable output, ready for coordinator persistence."""

    artifact: ArtifactReference
    metrics: Mapping[str, int | float]

    def __post_init__(self) -> None:
        if not isinstance(self.artifact, ArtifactReference):
            raise ValueError("artifact must be an ArtifactReference")
        # Reuse the CompletedPartial metric validation without inventing a fake
        # artifact lifecycle or widening the result contract.
        object.__setattr__(self, "metrics", CompletedPartial(0, self.artifact, self.metrics).metrics)


def _require_exact_keys(value: Mapping[str, object], expected: set[str], label: str) -> None:
    actual = set(value)
    if actual != expected:
        missing = sorted(expected - actual)
        unknown = sorted(actual - expected)
        details: list[str] = []
        if missing:
            details.append(f"missing {', '.join(missing)}")
        if unknown:
            details.append(f"unknown {', '.join(unknown)}")
        raise ValueError(f"{label} has {'; '.join(details)} fields")
