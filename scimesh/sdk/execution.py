"""Execution, retry, checkpoint, cancellation, and failure declarations."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from types import MappingProxyType
from typing import Any, Mapping

from ._validation import (
    enum_value,
    freeze_json_mapping,
    require_exact_keys,
    require_identifier,
    require_nonnegative_int,
    require_safe_message,
    require_positive_int,
    require_string,
    thaw_json,
)
from .identity import SchemaRef
from .resources import ResourceAllocation, ResourceRequirements


class ProcessModel(str, Enum):
    SINGLE = "single"
    PROCESS_POOL = "process_pool"
    THREAD_POOL = "thread_pool"
    EXTERNAL_RUNTIME = "external_runtime"


class NetworkPolicy(str, Enum):
    NONE = "none"
    COORDINATOR_ARTIFACTS_ONLY = "coordinator_artifacts_only"
    ALLOWLISTED_EGRESS = "allowlisted_egress"
    TRUSTED = "trusted"


class FailureCategory(str, Enum):
    INPUT = "input"
    SCIENTIFIC = "scientific"
    RESOURCE = "resource"
    INFRASTRUCTURE = "infrastructure"
    LEASE = "lease"
    VERIFICATION = "verification"
    POLICY = "policy"


@dataclass(frozen=True, slots=True)
class RetryPolicy:
    max_attempts: int = 1
    retryable_categories: tuple[FailureCategory, ...] = ()
    initial_backoff_seconds: int = 1
    max_backoff_seconds: int = 60

    def __post_init__(self) -> None:
        object.__setattr__(self, "max_attempts", require_positive_int(self.max_attempts, "retry.max_attempts"))
        categories = tuple(
            enum_value(FailureCategory, value, "retryable_category")
            for value in self.retryable_categories
        )
        if len(categories) != len(set(categories)):
            raise ValueError("retryable_categories must be unique")
        object.__setattr__(self, "retryable_categories", categories)
        object.__setattr__(
            self,
            "initial_backoff_seconds",
            require_nonnegative_int(self.initial_backoff_seconds, "retry.initial_backoff_seconds"),
        )
        object.__setattr__(
            self,
            "max_backoff_seconds",
            require_nonnegative_int(self.max_backoff_seconds, "retry.max_backoff_seconds"),
        )
        if self.max_backoff_seconds < self.initial_backoff_seconds:
            raise ValueError("retry max_backoff_seconds must not be less than initial_backoff_seconds")
        if self.max_attempts == 1 and categories:
            raise ValueError("a non-retrying policy must not list retryable categories")

    def to_dict(self) -> dict[str, object]:
        return {
            "max_attempts": self.max_attempts,
            "retryable_categories": [category.value for category in self.retryable_categories],
            "initial_backoff_seconds": self.initial_backoff_seconds,
            "max_backoff_seconds": self.max_backoff_seconds,
        }

    @classmethod
    def from_dict(cls, value: object) -> "RetryPolicy":
        if not isinstance(value, Mapping):
            raise ValueError("retry policy must be an object")
        fields = {"max_attempts", "retryable_categories", "initial_backoff_seconds", "max_backoff_seconds"}
        require_exact_keys(value, fields, "retry policy")
        categories = value["retryable_categories"]
        if not isinstance(categories, list):
            raise ValueError("retryable_categories must be an array")
        return cls(
            max_attempts=value["max_attempts"],  # type: ignore[arg-type]
            retryable_categories=tuple(categories),
            initial_backoff_seconds=value["initial_backoff_seconds"],  # type: ignore[arg-type]
            max_backoff_seconds=value["max_backoff_seconds"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class CheckpointPolicy:
    enabled: bool = False
    schema: SchemaRef | None = None
    compatibility_version: int | None = None
    interval_seconds: int | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.enabled, bool):
            raise ValueError("checkpoint.enabled must be a boolean")
        if not self.enabled:
            if any(value is not None for value in (self.schema, self.compatibility_version, self.interval_seconds)):
                raise ValueError("disabled checkpoint policy must not declare checkpoint fields")
            return
        if not isinstance(self.schema, SchemaRef):
            raise ValueError("enabled checkpoint policy requires a schema")
        if self.compatibility_version is None:
            raise ValueError("enabled checkpoint policy requires a compatibility_version")
        object.__setattr__(
            self,
            "compatibility_version",
            require_positive_int(self.compatibility_version, "checkpoint.compatibility_version"),
        )
        if self.interval_seconds is not None:
            object.__setattr__(
                self,
                "interval_seconds",
                require_positive_int(self.interval_seconds, "checkpoint.interval_seconds"),
            )

    def to_dict(self) -> dict[str, object]:
        return {
            "enabled": self.enabled,
            "schema": self.schema.canonical if self.schema is not None else None,
            "compatibility_version": self.compatibility_version,
            "interval_seconds": self.interval_seconds,
        }

    @classmethod
    def from_dict(cls, value: object) -> "CheckpointPolicy":
        if not isinstance(value, Mapping):
            raise ValueError("checkpoint policy must be an object")
        fields = {"enabled", "schema", "compatibility_version", "interval_seconds"}
        require_exact_keys(value, fields, "checkpoint policy")
        raw_schema = value["schema"]
        return cls(
            enabled=value["enabled"],  # type: ignore[arg-type]
            schema=None if raw_schema is None else SchemaRef.from_dict(raw_schema),
            compatibility_version=value["compatibility_version"],  # type: ignore[arg-type]
            interval_seconds=value["interval_seconds"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class ExecutionProfile:
    profile: str
    process_model: ProcessModel = ProcessModel.SINGLE
    max_processes: int = 1
    threads_per_process: int = 1
    native_threads: int = 1
    nested_parallelism: bool = False
    network: NetworkPolicy = NetworkPolicy.NONE
    timeout_seconds: int = 3600
    cancellation_grace_seconds: int = 10
    checkpoint: CheckpointPolicy = CheckpointPolicy()
    allowed_egress: tuple[str, ...] = ()
    secret_handles: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        object.__setattr__(self, "profile", require_identifier(self.profile, "execution.profile"))
        object.__setattr__(self, "process_model", enum_value(ProcessModel, self.process_model, "process_model"))
        object.__setattr__(self, "max_processes", require_positive_int(self.max_processes, "max_processes"))
        object.__setattr__(
            self,
            "threads_per_process",
            require_positive_int(self.threads_per_process, "threads_per_process"),
        )
        object.__setattr__(self, "native_threads", require_positive_int(self.native_threads, "native_threads"))
        if not isinstance(self.nested_parallelism, bool):
            raise ValueError("nested_parallelism must be a boolean")
        object.__setattr__(self, "network", enum_value(NetworkPolicy, self.network, "network"))
        object.__setattr__(self, "timeout_seconds", require_positive_int(self.timeout_seconds, "timeout_seconds"))
        object.__setattr__(
            self,
            "cancellation_grace_seconds",
            require_nonnegative_int(self.cancellation_grace_seconds, "cancellation_grace_seconds"),
        )
        if not isinstance(self.checkpoint, CheckpointPolicy):
            raise ValueError("checkpoint must be a CheckpointPolicy")
        egress = tuple(require_string(value, "allowed_egress", max_length=253) for value in self.allowed_egress)
        if len(egress) != len(set(egress)):
            raise ValueError("allowed_egress must be unique")
        if self.network is NetworkPolicy.ALLOWLISTED_EGRESS and not egress:
            raise ValueError("allowlisted egress policy requires at least one target")
        if self.network is not NetworkPolicy.ALLOWLISTED_EGRESS and egress:
            raise ValueError("allowed_egress is valid only for allowlisted egress")
        object.__setattr__(self, "allowed_egress", egress)
        handles = tuple(require_identifier(value, "secret_handle") for value in self.secret_handles)
        if len(handles) != len(set(handles)):
            raise ValueError("secret_handles must be unique")
        if handles and self.network is NetworkPolicy.NONE:
            raise ValueError("secret handles require an explicit network policy")
        object.__setattr__(self, "secret_handles", handles)
        if self.process_model is ProcessModel.SINGLE and (
            self.max_processes != 1 or self.threads_per_process != 1
        ):
            raise ValueError("single process model requires one process and one Python thread")
        if not self.nested_parallelism and self.threads_per_process > 1 and self.native_threads > 1:
            raise ValueError("nested thread pools require nested_parallelism=true")

    @property
    def maximum_cpu_threads(self) -> int:
        return self.max_processes * self.threads_per_process * self.native_threads

    def validate_resources(self, resources: ResourceRequirements) -> None:
        if self.maximum_cpu_threads > resources.cpu_cores:
            raise ValueError("execution profile can oversubscribe its CPU reservation")
        if self.timeout_seconds > resources.max_duration_seconds:
            raise ValueError("execution timeout exceeds the resource maximum duration")

    def allocation_environment(self, allocation: ResourceAllocation) -> Mapping[str, str]:
        """Return only allocation-derived thread/device isolation variables."""
        if not isinstance(allocation, ResourceAllocation):
            raise ValueError("allocation must be a ResourceAllocation")
        native = str(min(self.native_threads, allocation.cpu_cores))
        values = {
            "OMP_NUM_THREADS": native,
            "OPENBLAS_NUM_THREADS": native,
            "MKL_NUM_THREADS": native,
            "NUMEXPR_NUM_THREADS": native,
            "VECLIB_MAXIMUM_THREADS": native,
            # Empty visibility explicitly prevents a CPU task from inheriting
            # access to all host devices.
            "CUDA_VISIBLE_DEVICES": ",".join(allocation.accelerator_ids),
            "ROCR_VISIBLE_DEVICES": ",".join(allocation.accelerator_ids),
        }
        return MappingProxyType(values)

    def to_dict(self) -> dict[str, object]:
        return {
            "profile": self.profile,
            "process_model": self.process_model.value,
            "max_processes": self.max_processes,
            "threads_per_process": self.threads_per_process,
            "native_threads": self.native_threads,
            "nested_parallelism": self.nested_parallelism,
            "network": self.network.value,
            "timeout_seconds": self.timeout_seconds,
            "cancellation_grace_seconds": self.cancellation_grace_seconds,
            "checkpoint": self.checkpoint.to_dict(),
            "allowed_egress": list(self.allowed_egress),
            "secret_handles": list(self.secret_handles),
        }

    @classmethod
    def from_dict(cls, value: object) -> "ExecutionProfile":
        if not isinstance(value, Mapping):
            raise ValueError("execution profile must be an object")
        fields = {
            "profile", "process_model", "max_processes", "threads_per_process",
            "native_threads", "nested_parallelism", "network", "timeout_seconds",
            "cancellation_grace_seconds", "checkpoint", "allowed_egress", "secret_handles",
        }
        require_exact_keys(value, fields, "execution profile")
        allowed_egress = value["allowed_egress"]
        secret_handles = value["secret_handles"]
        if not isinstance(allowed_egress, list) or not isinstance(secret_handles, list):
            raise ValueError("execution allowed_egress and secret_handles must be arrays")
        return cls(
            profile=value["profile"],  # type: ignore[arg-type]
            process_model=value["process_model"],  # type: ignore[arg-type]
            max_processes=value["max_processes"],  # type: ignore[arg-type]
            threads_per_process=value["threads_per_process"],  # type: ignore[arg-type]
            native_threads=value["native_threads"],  # type: ignore[arg-type]
            nested_parallelism=value["nested_parallelism"],  # type: ignore[arg-type]
            network=value["network"],  # type: ignore[arg-type]
            timeout_seconds=value["timeout_seconds"],  # type: ignore[arg-type]
            cancellation_grace_seconds=value["cancellation_grace_seconds"],  # type: ignore[arg-type]
            checkpoint=CheckpointPolicy.from_dict(value["checkpoint"]),
            allowed_egress=tuple(allowed_egress),
            secret_handles=tuple(secret_handles),
        )


@dataclass(frozen=True, slots=True)
class FailureReport:
    code: str
    category: FailureCategory
    retryable: bool
    message: str
    evidence: Mapping[str, Any]

    def __post_init__(self) -> None:
        object.__setattr__(self, "code", require_identifier(self.code, "failure.code"))
        object.__setattr__(self, "category", enum_value(FailureCategory, self.category, "failure.category"))
        if not isinstance(self.retryable, bool):
            raise ValueError("failure.retryable must be a boolean")
        object.__setattr__(self, "message", require_safe_message(self.message, "failure.message", max_length=512))
        evidence = freeze_json_mapping(self.evidence, "failure.evidence", forbid_locations=True)
        import json
        if len(json.dumps(thaw_json(evidence), allow_nan=False).encode("utf-8")) > 16_384:
            raise ValueError("failure evidence exceeds 16 KiB")
        object.__setattr__(self, "evidence", evidence)

    def to_dict(self) -> dict[str, object]:
        return {
            "code": self.code,
            "category": self.category.value,
            "retryable": self.retryable,
            "message": self.message,
            "evidence": thaw_json(self.evidence),
        }

    def to_json(self) -> str:
        from ._validation import canonical_json

        return canonical_json(self.to_dict())

    @classmethod
    def from_dict(cls, value: object) -> "FailureReport":
        if not isinstance(value, Mapping):
            raise ValueError("failure report must be an object")
        fields = {"code", "category", "retryable", "message", "evidence"}
        require_exact_keys(value, fields, "failure report")
        return cls(
            code=value["code"],  # type: ignore[arg-type]
            category=value["category"],  # type: ignore[arg-type]
            retryable=value["retryable"],  # type: ignore[arg-type]
            message=value["message"],  # type: ignore[arg-type]
            evidence=value["evidence"],  # type: ignore[arg-type]
        )

    @classmethod
    def from_json(cls, value: str) -> "FailureReport":
        import json

        try:
            decoded = json.loads(value)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("failure report must be valid JSON") from error
        return cls.from_dict(decoded)
