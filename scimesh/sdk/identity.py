"""Versioned identities used across the SciMesh workload SDK."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping

from ._validation import (
    require_exact_keys,
    require_identifier,
    require_semver,
    require_string,
    require_workload_name,
    validate_version_range,
    version_in_range,
)


SDK_API_VERSION = "1.0.0"
MANIFEST_SCHEMA_VERSION = 1
WORKFLOW_SCHEMA_VERSION = 1
TASK_SCHEMA_VERSION = 1
OUTPUT_SCHEMA_VERSION = 1


@dataclass(frozen=True, slots=True)
class VersionRange:
    """A deliberately small, explicit compatibility range.

    The v1 SDK accepts comma-separated comparisons such as ``>=1.0,<2.0``.
    Wildcards and an omitted operator are rejected so a missing version can
    never be interpreted as "latest".
    """

    expression: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "expression", validate_version_range(self.expression, "version range"))

    def contains(self, version: str) -> bool:
        return version_in_range(version, self.expression)

    def to_dict(self) -> str:
        return self.expression

    @classmethod
    def from_dict(cls, value: object) -> "VersionRange":
        return cls(value)  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class WorkloadId:
    name: str
    version: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "name", require_workload_name(self.name))
        object.__setattr__(self, "version", require_semver(self.version, "workload.version"))

    def to_dict(self) -> dict[str, str]:
        return {"name": self.name, "version": self.version}

    @classmethod
    def from_dict(cls, value: object) -> "WorkloadId":
        if not isinstance(value, Mapping):
            raise ValueError("workload identity must be an object")
        require_exact_keys(value, {"name", "version"}, "workload identity")
        return cls(name=value["name"], version=value["version"])  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class SchemaRef:
    name: str
    version: int

    def __post_init__(self) -> None:
        object.__setattr__(self, "name", require_identifier(self.name, "schema.name"))
        if isinstance(self.version, bool) or not isinstance(self.version, int) or self.version < 1:
            raise ValueError("schema.version must be a positive integer")

    @property
    def canonical(self) -> str:
        return f"{self.name}@{self.version}"

    def to_dict(self) -> dict[str, object]:
        return {"name": self.name, "version": self.version}

    @classmethod
    def parse(cls, value: object, field: str = "schema") -> "SchemaRef":
        text = require_string(value, field, max_length=160)
        name, separator, raw_version = text.rpartition("@")
        if not separator or not raw_version.isdigit():
            raise ValueError(f"{field} must use the name@version form")
        return cls(name=name, version=int(raw_version))

    @classmethod
    def from_dict(cls, value: object) -> "SchemaRef":
        if isinstance(value, str):
            return cls.parse(value)
        if not isinstance(value, Mapping):
            raise ValueError("schema reference must be a name@version string or object")
        require_exact_keys(value, {"name", "version"}, "schema reference")
        return cls(name=value["name"], version=value["version"])  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class ComponentRef:
    """Versioned, package-owned planner/runner/reducer/verifier identity."""

    name: str
    version: int

    def __post_init__(self) -> None:
        object.__setattr__(self, "name", require_identifier(self.name, "component.name"))
        if isinstance(self.version, bool) or not isinstance(self.version, int) or self.version < 1:
            raise ValueError("component.version must be a positive integer")

    @property
    def canonical(self) -> str:
        return f"{self.name}@{self.version}"

    def to_dict(self) -> dict[str, object]:
        return {"name": self.name, "version": self.version}

    @classmethod
    def from_dict(cls, value: object) -> "ComponentRef":
        if isinstance(value, str):
            parsed = SchemaRef.parse(value, "component")
            return cls(parsed.name, parsed.version)
        if not isinstance(value, Mapping):
            raise ValueError("component reference must be an object")
        require_exact_keys(value, {"name", "version"}, "component reference")
        return cls(name=value["name"], version=value["version"])  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class FeatureRequirement:
    name: str
    versions: VersionRange
    fallback: str | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "name", require_identifier(self.name, "feature.name"))
        if not isinstance(self.versions, VersionRange):
            raise ValueError("feature.versions must be a VersionRange")
        if self.fallback is not None:
            object.__setattr__(self, "fallback", require_identifier(self.fallback, "feature.fallback"))

    def to_dict(self) -> dict[str, object]:
        result: dict[str, object] = {"name": self.name, "versions": self.versions.expression}
        if self.fallback is not None:
            result["fallback"] = self.fallback
        return result

    @classmethod
    def from_dict(cls, value: object) -> "FeatureRequirement":
        if not isinstance(value, Mapping):
            raise ValueError("feature requirement must be an object")
        require_exact_keys(
            value,
            {"name", "versions"},
            "feature requirement",
            optional={"fallback"},
        )
        return cls(
            name=value["name"],  # type: ignore[arg-type]
            versions=VersionRange.from_dict(value["versions"]),
            fallback=value.get("fallback"),  # type: ignore[arg-type]
        )
