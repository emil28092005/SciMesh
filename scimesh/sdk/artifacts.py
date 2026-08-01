"""Typed artifact ports, immutable collections, and output provenance."""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import Enum
from types import MappingProxyType
from typing import Any, Mapping, Sequence

from ._validation import (
    canonical_json,
    enum_value,
    freeze_json_mapping,
    require_exact_keys,
    require_finite_number,
    require_identifier,
    require_nonnegative_int,
    require_opaque_resource_id,
    require_positive_int,
    require_sha256,
    require_schema_version,
    require_string,
    require_task_key,
    require_uuid,
    thaw_json,
    parse_release,
)
from .identity import ComponentRef, OUTPUT_SCHEMA_VERSION, SchemaRef, WorkloadId


class CollectionKind(str, Enum):
    SINGLE = "single"
    ORDERED = "ordered"
    KEYED = "keyed"
    SET = "set"


class Cardinality(str, Enum):
    ONE = "one"
    OPTIONAL = "optional"
    MANY = "many"


@dataclass(frozen=True, slots=True)
class ArtifactSchema:
    """Logical artifact shape and hard parsing bounds."""

    ref: SchemaRef
    media_type: str
    encoding: str | None
    max_bytes: int
    validator: ComponentRef
    validator_configuration: Mapping[str, Any] = field(default_factory=dict)
    max_records: int | None = None
    max_dimensions: tuple[int, ...] = ()
    streaming: bool = False
    canonicalizer: str | None = None
    privacy_class: str = "project"
    retention_class: str = "durable"
    allow_nested_collections: bool = False

    def __post_init__(self) -> None:
        if not isinstance(self.ref, SchemaRef):
            raise ValueError("artifact schema ref must be a SchemaRef")
        object.__setattr__(self, "media_type", require_string(self.media_type, "media_type", max_length=128))
        if "/" not in self.media_type or any(character.isspace() for character in self.media_type):
            raise ValueError("media_type must be a valid type/subtype token")
        if self.encoding is not None:
            object.__setattr__(self, "encoding", require_identifier(self.encoding, "encoding"))
        object.__setattr__(self, "max_bytes", require_positive_int(self.max_bytes, "max_bytes"))
        if not isinstance(self.validator, ComponentRef):
            raise ValueError("artifact schema validator must be a ComponentRef")
        object.__setattr__(
            self,
            "validator_configuration",
            freeze_json_mapping(
                self.validator_configuration,
                "artifact validator_configuration",
                forbid_locations=True,
            ),
        )
        if self.max_records is not None:
            object.__setattr__(self, "max_records", require_positive_int(self.max_records, "max_records"))
        dimensions = tuple(self.max_dimensions)
        if any(
            isinstance(value, bool) or not isinstance(value, int) or value < 1
            for value in dimensions
        ):
            raise ValueError("max_dimensions must contain positive integers")
        if len(dimensions) > 8:
            raise ValueError("max_dimensions must contain at most 8 axes")
        object.__setattr__(self, "max_dimensions", dimensions)
        if self.canonicalizer is not None:
            object.__setattr__(
                self,
                "canonicalizer",
                require_identifier(self.canonicalizer, "canonicalizer"),
            )
        object.__setattr__(self, "privacy_class", require_identifier(self.privacy_class, "privacy_class"))
        object.__setattr__(self, "retention_class", require_identifier(self.retention_class, "retention_class"))
        if not isinstance(self.streaming, bool) or not isinstance(self.allow_nested_collections, bool):
            raise ValueError("streaming and allow_nested_collections must be booleans")

    def to_dict(self) -> dict[str, object]:
        return {
            "ref": self.ref.canonical,
            "media_type": self.media_type,
            "encoding": self.encoding,
            "max_bytes": self.max_bytes,
            "validator": self.validator.canonical,
            "validator_configuration": thaw_json(self.validator_configuration),
            "max_records": self.max_records,
            "max_dimensions": list(self.max_dimensions),
            "streaming": self.streaming,
            "canonicalizer": self.canonicalizer,
            "privacy_class": self.privacy_class,
            "retention_class": self.retention_class,
            "allow_nested_collections": self.allow_nested_collections,
        }

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactSchema":
        if not isinstance(value, Mapping):
            raise ValueError("artifact schema must be an object")
        fields = {
            "ref", "media_type", "encoding", "max_bytes", "validator",
            "validator_configuration", "max_records",
            "max_dimensions", "streaming", "canonicalizer", "privacy_class",
            "retention_class", "allow_nested_collections",
        }
        require_exact_keys(value, fields, "artifact schema")
        dimensions = value["max_dimensions"]
        if not isinstance(dimensions, list):
            raise ValueError("max_dimensions must be an array")
        return cls(
            ref=SchemaRef.from_dict(value["ref"]),
            media_type=value["media_type"],  # type: ignore[arg-type]
            encoding=value["encoding"],  # type: ignore[arg-type]
            max_bytes=value["max_bytes"],  # type: ignore[arg-type]
            validator=ComponentRef.from_dict(value["validator"]),
            validator_configuration=value["validator_configuration"],  # type: ignore[arg-type]
            max_records=value["max_records"],  # type: ignore[arg-type]
            max_dimensions=tuple(dimensions),
            streaming=value["streaming"],  # type: ignore[arg-type]
            canonicalizer=value["canonicalizer"],  # type: ignore[arg-type]
            privacy_class=value["privacy_class"],  # type: ignore[arg-type]
            retention_class=value["retention_class"],  # type: ignore[arg-type]
            allow_nested_collections=value["allow_nested_collections"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class PortSpec:
    schema: ArtifactSchema
    cardinality: Cardinality = Cardinality.ONE
    collection: CollectionKind = CollectionKind.SINGLE

    def __post_init__(self) -> None:
        if not isinstance(self.schema, ArtifactSchema):
            raise ValueError("port schema must be an ArtifactSchema")
        object.__setattr__(self, "cardinality", enum_value(Cardinality, self.cardinality, "cardinality"))
        object.__setattr__(self, "collection", enum_value(CollectionKind, self.collection, "collection"))
        if self.cardinality is Cardinality.MANY and self.collection is CollectionKind.SINGLE:
            raise ValueError("many cardinality requires an ordered, keyed, or set collection")
        if self.cardinality is not Cardinality.MANY and self.collection is not CollectionKind.SINGLE:
            raise ValueError("one and optional cardinality require a single collection")

    def validate_collection(self, value: "ArtifactCollection", field: str = "artifact collection") -> None:
        if value.kind is not self.collection:
            raise ValueError(f"{field} kind does not match its port declaration")
        count = len(value.items)
        if self.cardinality is Cardinality.ONE and count != 1:
            raise ValueError(f"{field} must contain exactly one artifact")
        if self.cardinality is Cardinality.OPTIONAL and count > 1:
            raise ValueError(f"{field} must contain at most one artifact")
        if self.cardinality is Cardinality.MANY and count < 1:
            raise ValueError(f"{field} must contain at least one artifact")
        for item in value.items:
            artifact = item.artifact
            if artifact.schema != self.schema.ref:
                raise ValueError(f"{field} contains an artifact with the wrong schema")
            if artifact.media_type != self.schema.media_type:
                raise ValueError(f"{field} contains an artifact with the wrong media type")
            if artifact.size_bytes > self.schema.max_bytes:
                raise ValueError(f"{field} exceeds its per-artifact byte limit")
            if self.schema.max_records is not None:
                if artifact.records is None:
                    raise ValueError(f"{field} is missing its required record summary")
                if artifact.records > self.schema.max_records:
                    raise ValueError(f"{field} exceeds its record limit")
            if self.schema.max_dimensions:
                if not artifact.dimensions:
                    raise ValueError(f"{field} is missing its required dimension summary")
                if len(artifact.dimensions) != len(self.schema.max_dimensions) or any(
                    actual > maximum
                    for actual, maximum in zip(artifact.dimensions, self.schema.max_dimensions)
                ):
                    raise ValueError(f"{field} exceeds its dimension limits")

    def to_dict(self) -> dict[str, object]:
        return {
            "schema": self.schema.to_dict(),
            "cardinality": self.cardinality.value,
            "collection": self.collection.value,
        }

    @classmethod
    def from_dict(cls, value: object) -> "PortSpec":
        if not isinstance(value, Mapping):
            raise ValueError("port specification must be an object")
        require_exact_keys(value, {"schema", "cardinality", "collection"}, "port specification")
        return cls(
            schema=ArtifactSchema.from_dict(value["schema"]),
            cardinality=value["cardinality"],  # type: ignore[arg-type]
            collection=value["collection"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class ArtifactRef:
    """Coordinator-owned artifact identity; transport URIs are intentionally absent."""

    artifact_id: str
    sha256: str
    schema: SchemaRef
    media_type: str
    size_bytes: int
    records: int | None = None
    dimensions: tuple[int, ...] = ()

    def __post_init__(self) -> None:
        object.__setattr__(self, "artifact_id", require_uuid(self.artifact_id, "artifact_id"))
        object.__setattr__(self, "sha256", require_sha256(self.sha256, "sha256"))
        if not isinstance(self.schema, SchemaRef):
            raise ValueError("artifact schema must be a SchemaRef")
        object.__setattr__(self, "media_type", require_string(self.media_type, "media_type", max_length=128))
        if "/" not in self.media_type or any(character.isspace() for character in self.media_type):
            raise ValueError("media_type must be a valid type/subtype token")
        object.__setattr__(self, "size_bytes", require_nonnegative_int(self.size_bytes, "size_bytes"))
        if self.records is not None:
            object.__setattr__(self, "records", require_nonnegative_int(self.records, "records"))
        dimensions = tuple(self.dimensions)
        if any(
            isinstance(value, bool) or not isinstance(value, int) or value < 0
            for value in dimensions
        ):
            raise ValueError("dimensions must contain non-negative integers")
        if len(dimensions) > 8:
            raise ValueError("dimensions must contain at most 8 axes")
        object.__setattr__(self, "dimensions", dimensions)

    def to_dict(self) -> dict[str, object]:
        return {
            "artifact_id": self.artifact_id,
            "sha256": self.sha256,
            "schema": self.schema.canonical,
            "media_type": self.media_type,
            "size_bytes": self.size_bytes,
            "records": self.records,
            "dimensions": list(self.dimensions),
        }

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactRef":
        if not isinstance(value, Mapping):
            raise ValueError("artifact reference must be an object")
        require_exact_keys(
            value,
            {"artifact_id", "sha256", "schema", "media_type", "size_bytes", "records", "dimensions"},
            "artifact reference",
        )
        dimensions = value["dimensions"]
        if not isinstance(dimensions, list):
            raise ValueError("artifact dimensions must be an array")
        return cls(
            artifact_id=value["artifact_id"],  # type: ignore[arg-type]
            sha256=value["sha256"],  # type: ignore[arg-type]
            schema=SchemaRef.from_dict(value["schema"]),
            media_type=value["media_type"],  # type: ignore[arg-type]
            size_bytes=value["size_bytes"],  # type: ignore[arg-type]
            records=value["records"],  # type: ignore[arg-type]
            dimensions=tuple(dimensions),
        )


@dataclass(frozen=True, slots=True)
class ArtifactItem:
    artifact: ArtifactRef
    key: str | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.artifact, ArtifactRef):
            raise ValueError("artifact item must contain an ArtifactRef")
        if self.key is not None:
            object.__setattr__(self, "key", require_identifier(self.key, "artifact key"))

    def to_dict(self) -> dict[str, object]:
        return {"key": self.key, "artifact": self.artifact.to_dict()}

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactItem":
        if not isinstance(value, Mapping):
            raise ValueError("artifact item must be an object")
        require_exact_keys(value, {"key", "artifact"}, "artifact item")
        return cls(artifact=ArtifactRef.from_dict(value["artifact"]), key=value["key"])  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class ArtifactCollection:
    kind: CollectionKind
    items: tuple[ArtifactItem, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "kind", enum_value(CollectionKind, self.kind, "collection.kind"))
        items = tuple(self.items)
        if any(not isinstance(item, ArtifactItem) for item in items):
            raise ValueError("collection items must be ArtifactItem values")
        if self.kind is CollectionKind.SINGLE:
            if len(items) > 1 or any(item.key is not None for item in items):
                raise ValueError("single collection contains at most one unkeyed artifact")
        elif self.kind is CollectionKind.KEYED:
            if any(item.key is None for item in items):
                raise ValueError("keyed collection requires a key for every artifact")
            keys = [item.key for item in items]
            if len(keys) != len(set(keys)):
                raise ValueError("keyed collection keys must be unique")
            items = tuple(sorted(items, key=lambda item: item.key or ""))
        else:
            if any(item.key is not None for item in items):
                raise ValueError("ordered and set collections must not use keys")
            if self.kind is CollectionKind.SET:
                identities = [
                    (item.artifact.schema, item.artifact.sha256, item.artifact.size_bytes)
                    for item in items
                ]
                if len(identities) != len(set(identities)):
                    raise ValueError("set collection must not contain duplicate artifacts")
                items = tuple(
                    sorted(
                        items,
                        key=lambda item: (
                            item.artifact.schema.canonical,
                            item.artifact.sha256,
                            item.artifact.size_bytes,
                        ),
                    )
                )
        object.__setattr__(self, "items", items)

    @classmethod
    def single(cls, artifact: ArtifactRef | None) -> "ArtifactCollection":
        return cls(CollectionKind.SINGLE, () if artifact is None else (ArtifactItem(artifact),))

    @property
    def size_bytes(self) -> int:
        return sum(item.artifact.size_bytes for item in self.items)

    @property
    def digest(self) -> str:
        payload = {
            "kind": self.kind.value,
            "items": [
                {
                    "key": item.key,
                    "sha256": item.artifact.sha256,
                    "schema": item.artifact.schema.canonical,
                    "media_type": item.artifact.media_type,
                    "size_bytes": item.artifact.size_bytes,
                }
                for item in self.items
            ],
        }
        return hashlib.sha256(canonical_json(payload).encode("utf-8")).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {"kind": self.kind.value, "items": [item.to_dict() for item in self.items]}

    @classmethod
    def from_dict(cls, value: object) -> "ArtifactCollection":
        if not isinstance(value, Mapping):
            raise ValueError("artifact collection must be an object")
        require_exact_keys(value, {"kind", "items"}, "artifact collection")
        items = value["items"]
        if not isinstance(items, list):
            raise ValueError("artifact collection items must be an array")
        return cls(
            kind=value["kind"],  # type: ignore[arg-type]
            items=tuple(ArtifactItem.from_dict(item) for item in items),
        )


def _timestamp(value: object, field: str) -> str:
    text = require_string(value, field, max_length=64)
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError(f"{field} must be an RFC 3339 timestamp") from error
    if parsed.tzinfo is None:
        raise ValueError(f"{field} must include a timezone")
    return parsed.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


@dataclass(frozen=True, slots=True)
class Provenance:
    workload: WorkloadId
    sdk_api_version: str
    protocol_version: str
    manifest_schema_version: int
    workflow_schema_version: int
    verifier: ComponentRef
    artifact_schemas: tuple[SchemaRef, ...]
    package_digest: str
    manifest_digest: str
    environment_digest: str
    worker_runtime: Mapping[str, Any]
    allocated_resource_ids: tuple[str, ...]
    parameters_digest: str
    input_collection_digest: str
    execution_contract_digest: str
    selected_features: Mapping[str, str]
    optional_fallbacks: Mapping[str, str]
    job_id: str
    task_id: str
    started_at: str
    finished_at: str
    trust_mode: str = "trusted"
    random_seed: int | None = None
    checkpoint_lineage: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if not isinstance(self.workload, WorkloadId):
            raise ValueError("provenance workload must be a WorkloadId")
        object.__setattr__(self, "sdk_api_version", require_string(self.sdk_api_version, "sdk_api_version"))
        object.__setattr__(self, "protocol_version", require_string(self.protocol_version, "protocol_version"))
        parse_release(self.sdk_api_version, "sdk_api_version")
        parse_release(self.protocol_version, "protocol_version")
        object.__setattr__(
            self,
            "manifest_schema_version",
            require_positive_int(self.manifest_schema_version, "manifest_schema_version"),
        )
        object.__setattr__(
            self,
            "workflow_schema_version",
            require_positive_int(self.workflow_schema_version, "workflow_schema_version"),
        )
        if not isinstance(self.verifier, ComponentRef):
            raise ValueError("provenance verifier must be a ComponentRef")
        schemas = tuple(self.artifact_schemas)
        if not schemas or any(not isinstance(schema, SchemaRef) for schema in schemas):
            raise ValueError("provenance artifact_schemas must contain SchemaRef values")
        if len(schemas) != len(set(schemas)):
            raise ValueError("provenance artifact_schemas must be unique")
        if schemas != tuple(sorted(schemas, key=lambda schema: schema.canonical)):
            raise ValueError("provenance artifact_schemas must be in canonical order")
        object.__setattr__(self, "artifact_schemas", schemas)
        object.__setattr__(self, "package_digest", require_sha256(self.package_digest, "package_digest", prefixed=True))
        object.__setattr__(self, "manifest_digest", require_sha256(self.manifest_digest, "manifest_digest"))
        object.__setattr__(self, "environment_digest", require_sha256(self.environment_digest, "environment_digest", prefixed=True))
        runtime = freeze_json_mapping(self.worker_runtime, "worker_runtime", forbid_locations=True)
        if len(canonical_json(runtime).encode("utf-8")) > 65_536:
            raise ValueError("worker_runtime exceeds 64 KiB")
        object.__setattr__(self, "worker_runtime", runtime)
        resource_ids = tuple(
            require_opaque_resource_id(value, "allocated_resource_id")
            for value in self.allocated_resource_ids
        )
        if not resource_ids or len(resource_ids) != len(set(resource_ids)):
            raise ValueError("allocated_resource_ids must be non-empty and unique")
        object.__setattr__(self, "allocated_resource_ids", resource_ids)
        object.__setattr__(self, "parameters_digest", require_sha256(self.parameters_digest, "parameters_digest"))
        object.__setattr__(self, "input_collection_digest", require_sha256(self.input_collection_digest, "input_collection_digest"))
        object.__setattr__(
            self,
            "execution_contract_digest",
            require_sha256(self.execution_contract_digest, "execution_contract_digest"),
        )
        selected_features = freeze_json_mapping(
            self.selected_features,
            "provenance.selected_features",
        )
        optional_fallbacks = freeze_json_mapping(
            self.optional_fallbacks,
            "provenance.optional_fallbacks",
        )
        for name, version in selected_features.items():
            require_identifier(name, "provenance selected feature")
            require_string(version, "provenance selected feature version", max_length=32)
            parse_release(version, "provenance selected feature version")
        for name, fallback in optional_fallbacks.items():
            require_identifier(name, "provenance fallback feature")
            require_identifier(fallback, "provenance fallback")
        if set(selected_features).intersection(optional_fallbacks):
            raise ValueError("provenance feature cannot be selected and fallbacked")
        object.__setattr__(self, "selected_features", selected_features)
        object.__setattr__(self, "optional_fallbacks", optional_fallbacks)
        object.__setattr__(self, "job_id", require_uuid(self.job_id, "provenance.job_id"))
        object.__setattr__(self, "task_id", require_uuid(self.task_id, "provenance.task_id"))
        object.__setattr__(self, "started_at", _timestamp(self.started_at, "started_at"))
        object.__setattr__(self, "finished_at", _timestamp(self.finished_at, "finished_at"))
        if datetime.fromisoformat(self.finished_at.replace("Z", "+00:00")) < datetime.fromisoformat(
            self.started_at.replace("Z", "+00:00")
        ):
            raise ValueError("finished_at must not precede started_at")
        trust_mode = require_identifier(self.trust_mode, "provenance.trust_mode")
        if trust_mode not in {"trusted", "verified", "untrusted_quorum"}:
            raise ValueError("provenance.trust_mode is unsupported")
        object.__setattr__(self, "trust_mode", trust_mode)
        if self.random_seed is not None and (isinstance(self.random_seed, bool) or not isinstance(self.random_seed, int)):
            raise ValueError("random_seed must be an integer")
        lineage = tuple(require_uuid(value, "checkpoint_lineage") for value in self.checkpoint_lineage)
        if len(lineage) != len(set(lineage)):
            raise ValueError("checkpoint_lineage must not contain duplicate artifacts")
        object.__setattr__(self, "checkpoint_lineage", lineage)

    def to_dict(self) -> dict[str, object]:
        return {
            "workload": self.workload.to_dict(),
            "sdk_api_version": self.sdk_api_version,
            "protocol_version": self.protocol_version,
            "manifest_schema_version": self.manifest_schema_version,
            "workflow_schema_version": self.workflow_schema_version,
            "verifier": self.verifier.canonical,
            "artifact_schemas": [schema.canonical for schema in self.artifact_schemas],
            "package_digest": self.package_digest,
            "manifest_digest": self.manifest_digest,
            "environment_digest": self.environment_digest,
            "worker_runtime": thaw_json(self.worker_runtime),
            "allocated_resource_ids": list(self.allocated_resource_ids),
            "parameters_digest": self.parameters_digest,
            "input_collection_digest": self.input_collection_digest,
            "execution_contract_digest": self.execution_contract_digest,
            "selected_features": thaw_json(self.selected_features),
            "optional_fallbacks": thaw_json(self.optional_fallbacks),
            "job_id": self.job_id,
            "task_id": self.task_id,
            "started_at": self.started_at,
            "finished_at": self.finished_at,
            "trust_mode": self.trust_mode,
            "random_seed": self.random_seed,
            "checkpoint_lineage": list(self.checkpoint_lineage),
        }

    @classmethod
    def from_dict(cls, value: object) -> "Provenance":
        if not isinstance(value, Mapping):
            raise ValueError("provenance must be an object")
        fields = {
            "workload", "sdk_api_version", "protocol_version", "manifest_schema_version",
            "workflow_schema_version", "verifier", "artifact_schemas", "package_digest",
            "manifest_digest", "environment_digest", "worker_runtime", "allocated_resource_ids",
            "parameters_digest", "input_collection_digest", "execution_contract_digest",
            "selected_features", "optional_fallbacks",
            "job_id", "task_id",
            "started_at", "finished_at",
            "trust_mode", "random_seed", "checkpoint_lineage",
        }
        require_exact_keys(value, fields, "provenance")
        resource_ids = value["allocated_resource_ids"]
        artifact_schemas = value["artifact_schemas"]
        lineage = value["checkpoint_lineage"]
        if not isinstance(resource_ids, list) or not isinstance(artifact_schemas, list) or not isinstance(lineage, list):
            raise ValueError("provenance resource IDs and checkpoint lineage must be arrays")
        return cls(
            workload=WorkloadId.from_dict(value["workload"]),
            sdk_api_version=value["sdk_api_version"],  # type: ignore[arg-type]
            protocol_version=value["protocol_version"],  # type: ignore[arg-type]
            manifest_schema_version=value["manifest_schema_version"],  # type: ignore[arg-type]
            workflow_schema_version=value["workflow_schema_version"],  # type: ignore[arg-type]
            verifier=ComponentRef.from_dict(value["verifier"]),
            artifact_schemas=tuple(SchemaRef.from_dict(item) for item in artifact_schemas),
            package_digest=value["package_digest"],  # type: ignore[arg-type]
            manifest_digest=value["manifest_digest"],  # type: ignore[arg-type]
            environment_digest=value["environment_digest"],  # type: ignore[arg-type]
            worker_runtime=value["worker_runtime"],  # type: ignore[arg-type]
            allocated_resource_ids=tuple(resource_ids),
            parameters_digest=value["parameters_digest"],  # type: ignore[arg-type]
            input_collection_digest=value["input_collection_digest"],  # type: ignore[arg-type]
            execution_contract_digest=value["execution_contract_digest"],  # type: ignore[arg-type]
            selected_features=value["selected_features"],  # type: ignore[arg-type]
            optional_fallbacks=value["optional_fallbacks"],  # type: ignore[arg-type]
            job_id=value["job_id"],  # type: ignore[arg-type]
            task_id=value["task_id"],  # type: ignore[arg-type]
            started_at=value["started_at"],  # type: ignore[arg-type]
            finished_at=value["finished_at"],  # type: ignore[arg-type]
            trust_mode=value["trust_mode"],  # type: ignore[arg-type]
            random_seed=value["random_seed"],  # type: ignore[arg-type]
            checkpoint_lineage=tuple(lineage),
        )


@dataclass(frozen=True, slots=True)
class OutputManifest:
    task_key: str
    outputs: Mapping[str, ArtifactCollection]
    metrics: Mapping[str, int | float]
    provenance: Provenance
    schema_version: int = OUTPUT_SCHEMA_VERSION

    def __post_init__(self) -> None:
        require_schema_version(self.schema_version, OUTPUT_SCHEMA_VERSION, "output schema_version")
        object.__setattr__(self, "task_key", require_task_key(self.task_key))
        if not isinstance(self.outputs, Mapping) or not self.outputs:
            raise ValueError("outputs must be a non-empty object")
        outputs: dict[str, ArtifactCollection] = {}
        for name, collection in self.outputs.items():
            canonical = require_identifier(name, "output port")
            if not isinstance(collection, ArtifactCollection):
                raise ValueError("output values must be ArtifactCollection values")
            outputs[canonical] = collection
        object.__setattr__(self, "outputs", MappingProxyType(outputs))
        if not isinstance(self.metrics, Mapping):
            raise ValueError("metrics must be an object")
        metrics: dict[str, int | float] = {}
        for name, value in self.metrics.items():
            canonical = require_identifier(name, "metric name")
            metrics[canonical] = require_finite_number(value, "metric value")
        if len(canonical_json(metrics).encode("utf-8")) > 16_384:
            raise ValueError("output metrics exceed 16 KiB")
        object.__setattr__(self, "metrics", MappingProxyType(metrics))
        if not isinstance(self.provenance, Provenance):
            raise ValueError("provenance must be a Provenance value")

    def validate_against(
        self,
        expected: Mapping[str, PortSpec],
        *,
        max_output_bytes: int,
    ) -> "OutputManifest":
        if set(self.outputs) != set(expected):
            missing = sorted(set(expected) - set(self.outputs))
            unexpected = sorted(set(self.outputs) - set(expected))
            details = []
            if missing:
                details.append("missing " + ", ".join(missing))
            if unexpected:
                details.append("unexpected " + ", ".join(unexpected))
            raise ValueError("output ports do not match the declaration: " + "; ".join(details))
        total = 0
        for name, port in expected.items():
            if not isinstance(port, PortSpec):
                raise ValueError("expected outputs must contain PortSpec values")
            port.validate_collection(self.outputs[name], f"output {name}")
            total += self.outputs[name].size_bytes
        if total > require_positive_int(max_output_bytes, "max_output_bytes"):
            raise ValueError("output manifest exceeds the total byte limit")
        return self

    @property
    def digest(self) -> str:
        payload = {
            "outputs": {
                name: {"kind": collection.kind.value, "digest": collection.digest}
                for name, collection in sorted(self.outputs.items())
            }
        }
        return hashlib.sha256(canonical_json(payload).encode("utf-8")).hexdigest()

    @property
    def manifest_digest(self) -> str:
        """Digest the complete audit manifest, including provenance and metrics."""
        return hashlib.sha256(self.to_json().encode("utf-8")).hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "task_key": self.task_key,
            "outputs": {name: value.to_dict() for name, value in self.outputs.items()},
            "metrics": dict(self.metrics),
            "provenance": self.provenance.to_dict(),
        }

    def to_json(self) -> str:
        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> "OutputManifest":
        if not isinstance(value, Mapping):
            raise ValueError("output manifest must be an object")
        require_exact_keys(
            value,
            {"schema_version", "task_key", "outputs", "metrics", "provenance"},
            "output manifest",
        )
        outputs = value["outputs"]
        if not isinstance(outputs, Mapping):
            raise ValueError("outputs must be an object")
        return cls(
            schema_version=value["schema_version"],  # type: ignore[arg-type]
            task_key=value["task_key"],  # type: ignore[arg-type]
            outputs={name: ArtifactCollection.from_dict(item) for name, item in outputs.items()},
            metrics=value["metrics"],  # type: ignore[arg-type]
            provenance=Provenance.from_dict(value["provenance"]),
        )

    @classmethod
    def from_json(cls, value: str) -> "OutputManifest":
        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("output manifest must be valid JSON") from error
        return cls.from_dict(decoded)
