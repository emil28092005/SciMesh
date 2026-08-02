"""Generic resource declarations, runtime inventory, and atomic local allocation."""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from threading import Lock
from typing import Mapping
from uuid import uuid4

from ._validation import (
    enum_value,
    freeze_json_mapping,
    require_exact_keys,
    require_identifier,
    require_nonnegative_int,
    require_opaque_resource_id,
    require_positive_int,
    require_sha256,
    require_string,
    thaw_json,
)


class AcceleratorMode(str, Enum):
    """How an accelerator is allocated: whole device or a managed partition."""

    NONE = "none"
    EXCLUSIVE_DEVICE = "exclusive_device"
    FRACTIONAL = "fractional"
    PARTITION = "partition"


def _resource_id(value: object, field: str) -> str:
    return require_opaque_resource_id(value, field)


@dataclass(frozen=True, slots=True)
class AcceleratorDevice:
    """One physical accelerator advertised in a host inventory.

    Declared but not schedulable until a runtime advertises the matching
    accelerator features.
    """

    kind: str
    vendor: str
    device_id: str
    model: str
    memory_mb: int
    modes: tuple[AcceleratorMode, ...]
    capabilities: Mapping[str, str]
    topology_group: str | None = None
    partition_id: str | None = None
    healthy: bool = True

    def __post_init__(self) -> None:
        object.__setattr__(
            self, "kind", require_identifier(self.kind, "accelerator.kind")
        )
        object.__setattr__(
            self, "vendor", require_identifier(self.vendor, "accelerator.vendor")
        )
        object.__setattr__(
            self, "device_id", _resource_id(self.device_id, "accelerator.device_id")
        )
        object.__setattr__(
            self,
            "model",
            require_string(self.model, "accelerator.model", max_length=160),
        )
        object.__setattr__(
            self,
            "memory_mb",
            require_positive_int(self.memory_mb, "accelerator.memory_mb"),
        )
        modes = tuple(
            enum_value(AcceleratorMode, mode, "accelerator.mode") for mode in self.modes
        )
        if not modes or AcceleratorMode.NONE in modes or len(modes) != len(set(modes)):
            raise ValueError(
                "accelerator modes must contain unique allocation modes other than none"
            )
        object.__setattr__(self, "modes", modes)
        capabilities = freeze_json_mapping(
            self.capabilities, "accelerator.capabilities"
        )
        if any(not isinstance(value, str) for value in capabilities.values()):
            raise ValueError("accelerator capabilities must use string values")
        object.__setattr__(self, "capabilities", capabilities)
        if self.topology_group is not None:
            object.__setattr__(
                self,
                "topology_group",
                _resource_id(self.topology_group, "topology_group"),
            )
        if self.partition_id is not None:
            object.__setattr__(
                self, "partition_id", _resource_id(self.partition_id, "partition_id")
            )
            if AcceleratorMode.PARTITION not in modes:
                raise ValueError("a partition_id requires partition allocation support")
            if AcceleratorMode.EXCLUSIVE_DEVICE in modes:
                raise ValueError(
                    "an accelerator partition cannot be allocated as a whole device"
                )
        if not isinstance(self.healthy, bool):
            raise ValueError("accelerator.healthy must be a boolean")

    @property
    def allocation_id(self) -> str:
        return self.partition_id or self.device_id

    def to_dict(self) -> dict[str, object]:
        return {
            "kind": self.kind,
            "vendor": self.vendor,
            "device_id": self.device_id,
            "model": self.model,
            "memory_mb": self.memory_mb,
            "modes": [mode.value for mode in self.modes],
            "capabilities": thaw_json(self.capabilities),
            "topology_group": self.topology_group,
            "partition_id": self.partition_id,
            "healthy": self.healthy,
        }

    @classmethod
    def from_dict(cls, value: object) -> "AcceleratorDevice":
        if not isinstance(value, Mapping):
            raise ValueError("accelerator device must be an object")
        fields = {
            "kind",
            "vendor",
            "device_id",
            "model",
            "memory_mb",
            "modes",
            "capabilities",
            "topology_group",
            "partition_id",
            "healthy",
        }
        require_exact_keys(value, fields, "accelerator device")
        modes = value["modes"]
        if not isinstance(modes, list):
            raise ValueError("accelerator modes must be an array")
        return cls(
            kind=value["kind"],  # type: ignore[arg-type]
            vendor=value["vendor"],  # type: ignore[arg-type]
            device_id=value["device_id"],  # type: ignore[arg-type]
            model=value["model"],  # type: ignore[arg-type]
            memory_mb=value["memory_mb"],  # type: ignore[arg-type]
            modes=tuple(modes),
            capabilities=value["capabilities"],  # type: ignore[arg-type]
            topology_group=value["topology_group"],  # type: ignore[arg-type]
            partition_id=value["partition_id"],  # type: ignore[arg-type]
            healthy=value["healthy"],  # type: ignore[arg-type]
        )


@dataclass(frozen=True, slots=True)
class ResourceInventory:
    """What a host offers: CPU, memory, scratch, architecture, environments, accelerators."""

    cpu_cores: int
    memory_mb: int
    scratch_mb: int
    architecture: str
    accelerators: tuple[AcceleratorDevice, ...] = ()
    environment_digests: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "cpu_cores",
            require_positive_int(self.cpu_cores, "inventory.cpu_cores"),
        )
        object.__setattr__(
            self,
            "memory_mb",
            require_positive_int(self.memory_mb, "inventory.memory_mb"),
        )
        object.__setattr__(
            self,
            "scratch_mb",
            require_nonnegative_int(self.scratch_mb, "inventory.scratch_mb"),
        )
        object.__setattr__(
            self,
            "architecture",
            require_identifier(self.architecture, "inventory.architecture"),
        )
        devices = tuple(self.accelerators)
        if any(not isinstance(device, AcceleratorDevice) for device in devices):
            raise ValueError(
                "inventory accelerators must contain AcceleratorDevice values"
            )
        ids = [device.allocation_id for device in devices]
        if len(ids) != len(set(ids)):
            raise ValueError("inventory accelerator allocation IDs must be unique")
        object.__setattr__(self, "accelerators", devices)
        digests = tuple(
            require_sha256(value, "environment_digest", prefixed=True)
            for value in self.environment_digests
        )
        if len(digests) != len(set(digests)):
            raise ValueError("environment_digests must be unique")
        object.__setattr__(self, "environment_digests", digests)

    def to_dict(self) -> dict[str, object]:
        return {
            "cpu_cores": self.cpu_cores,
            "memory_mb": self.memory_mb,
            "scratch_mb": self.scratch_mb,
            "architecture": self.architecture,
            "accelerators": [device.to_dict() for device in self.accelerators],
            "environment_digests": list(self.environment_digests),
        }

    @classmethod
    def from_dict(cls, value: object) -> "ResourceInventory":
        if not isinstance(value, Mapping):
            raise ValueError("resource inventory must be an object")
        fields = {
            "cpu_cores",
            "memory_mb",
            "scratch_mb",
            "architecture",
            "accelerators",
            "environment_digests",
        }
        require_exact_keys(value, fields, "resource inventory")
        accelerators = value["accelerators"]
        digests = value["environment_digests"]
        if not isinstance(accelerators, list) or not isinstance(digests, list):
            raise ValueError(
                "inventory accelerators and environment_digests must be arrays"
            )
        return cls(
            cpu_cores=value["cpu_cores"],  # type: ignore[arg-type]
            memory_mb=value["memory_mb"],  # type: ignore[arg-type]
            scratch_mb=value["scratch_mb"],  # type: ignore[arg-type]
            architecture=value["architecture"],  # type: ignore[arg-type]
            accelerators=tuple(
                AcceleratorDevice.from_dict(device) for device in accelerators
            ),
            environment_digests=tuple(digests),
        )


@dataclass(frozen=True, slots=True)
class ResourceRequirements:
    """What one task needs; eligibility is checked against the inventory.

    ``cpu_cores`` is a reservation, never a concurrency claim; accelerator
    declarations remain fail-closed until runtime support exists.
    """

    profile: str
    cpu_cores: int
    memory_mb: int
    scratch_mb: int
    accelerator_count: int = 0
    accelerator_kind: str | None = None
    accelerator_memory_mb: int = 0
    accelerator_mode: AcceleratorMode = AcceleratorMode.NONE
    architecture: str | None = None
    topology_group: str | None = None
    environment_digest: str | None = None
    estimated_input_bytes: int = 0
    estimated_output_bytes: int = 0
    max_duration_seconds: int = 3600

    def __post_init__(self) -> None:
        object.__setattr__(
            self, "profile", require_identifier(self.profile, "resources.profile")
        )
        object.__setattr__(
            self,
            "cpu_cores",
            require_positive_int(self.cpu_cores, "resources.cpu_cores"),
        )
        object.__setattr__(
            self,
            "memory_mb",
            require_positive_int(self.memory_mb, "resources.memory_mb"),
        )
        object.__setattr__(
            self,
            "scratch_mb",
            require_nonnegative_int(self.scratch_mb, "resources.scratch_mb"),
        )
        object.__setattr__(
            self,
            "accelerator_count",
            require_nonnegative_int(
                self.accelerator_count, "resources.accelerator_count"
            ),
        )
        object.__setattr__(
            self,
            "accelerator_memory_mb",
            require_nonnegative_int(
                self.accelerator_memory_mb, "resources.accelerator_memory_mb"
            ),
        )
        object.__setattr__(
            self,
            "accelerator_mode",
            enum_value(
                AcceleratorMode, self.accelerator_mode, "resources.accelerator_mode"
            ),
        )
        if self.accelerator_count == 0:
            if (
                self.accelerator_kind is not None
                or self.accelerator_memory_mb
                or self.accelerator_mode is not AcceleratorMode.NONE
            ):
                raise ValueError(
                    "CPU-only resources must not declare accelerator constraints"
                )
            if self.topology_group is not None:
                raise ValueError(
                    "CPU-only resources must not declare accelerator topology"
                )
        else:
            if self.accelerator_kind is None:
                raise ValueError(
                    "accelerator_kind is required when accelerator_count is non-zero"
                )
            object.__setattr__(
                self,
                "accelerator_kind",
                require_identifier(self.accelerator_kind, "accelerator_kind"),
            )
            if self.accelerator_mode is AcceleratorMode.NONE:
                raise ValueError(
                    "accelerator_mode is required when accelerator_count is non-zero"
                )
        if self.architecture is not None:
            object.__setattr__(
                self,
                "architecture",
                require_identifier(self.architecture, "resources.architecture"),
            )
        if self.topology_group is not None:
            object.__setattr__(
                self,
                "topology_group",
                _resource_id(self.topology_group, "resources.topology_group"),
            )
        if self.environment_digest is not None:
            object.__setattr__(
                self,
                "environment_digest",
                require_sha256(
                    self.environment_digest,
                    "resources.environment_digest",
                    prefixed=True,
                ),
            )
        object.__setattr__(
            self,
            "estimated_input_bytes",
            require_nonnegative_int(
                self.estimated_input_bytes, "estimated_input_bytes"
            ),
        )
        object.__setattr__(
            self,
            "estimated_output_bytes",
            require_nonnegative_int(
                self.estimated_output_bytes, "estimated_output_bytes"
            ),
        )
        object.__setattr__(
            self,
            "max_duration_seconds",
            require_positive_int(self.max_duration_seconds, "max_duration_seconds"),
        )

    def eligibility_errors(self, inventory: ResourceInventory) -> tuple[str, ...]:
        errors: list[str] = []
        if self.cpu_cores > inventory.cpu_cores:
            errors.append("insufficient-cpu")
        if self.memory_mb > inventory.memory_mb:
            errors.append("insufficient-memory")
        if self.scratch_mb > inventory.scratch_mb:
            errors.append("insufficient-scratch")
        if (
            self.architecture is not None
            and self.architecture != inventory.architecture
        ):
            errors.append("architecture-mismatch")
        if (
            self.environment_digest is not None
            and self.environment_digest not in inventory.environment_digests
        ):
            errors.append("environment-unavailable")
        matches = self._matching_devices(inventory.accelerators)
        if len(matches) < self.accelerator_count:
            errors.append("accelerator-unavailable")
        return tuple(errors)

    def _matching_devices(
        self,
        devices: tuple[AcceleratorDevice, ...],
        unavailable: set[str] | None = None,
    ) -> tuple[AcceleratorDevice, ...]:
        unavailable = unavailable or set()
        if self.accelerator_count == 0:
            return ()
        matches = [
            device
            for device in devices
            if device.healthy
            and device.allocation_id not in unavailable
            and device.kind == self.accelerator_kind
            and device.memory_mb >= self.accelerator_memory_mb
            and self.accelerator_mode in device.modes
            and (
                (
                    self.accelerator_mode is AcceleratorMode.PARTITION
                    and device.partition_id is not None
                )
                or (
                    self.accelerator_mode is AcceleratorMode.EXCLUSIVE_DEVICE
                    and device.partition_id is None
                )
                or self.accelerator_mode is AcceleratorMode.FRACTIONAL
            )
            and (
                self.topology_group is None
                or device.topology_group == self.topology_group
            )
        ]
        if self.accelerator_count > 1 and self.topology_group is None:
            groups: dict[str | None, list[AcceleratorDevice]] = {}
            for device in matches:
                groups.setdefault(device.topology_group, []).append(device)
            sufficiently_large = [
                group
                for group in groups.values()
                if len(group) >= self.accelerator_count
            ]
            if sufficiently_large:
                matches = min(
                    sufficiently_large,
                    key=lambda group: tuple(item.allocation_id for item in group),
                )
        return tuple(sorted(matches, key=lambda device: device.allocation_id))

    def to_dict(self) -> dict[str, object]:
        return {
            "profile": self.profile,
            "cpu_cores": self.cpu_cores,
            "memory_mb": self.memory_mb,
            "scratch_mb": self.scratch_mb,
            "accelerator_count": self.accelerator_count,
            "accelerator_kind": self.accelerator_kind,
            "accelerator_memory_mb": self.accelerator_memory_mb,
            "accelerator_mode": self.accelerator_mode.value,
            "architecture": self.architecture,
            "topology_group": self.topology_group,
            "environment_digest": self.environment_digest,
            "estimated_input_bytes": self.estimated_input_bytes,
            "estimated_output_bytes": self.estimated_output_bytes,
            "max_duration_seconds": self.max_duration_seconds,
        }

    @classmethod
    def from_dict(cls, value: object) -> "ResourceRequirements":
        if not isinstance(value, Mapping):
            raise ValueError("resource requirements must be an object")
        fields = {
            "profile",
            "cpu_cores",
            "memory_mb",
            "scratch_mb",
            "accelerator_count",
            "accelerator_kind",
            "accelerator_memory_mb",
            "accelerator_mode",
            "architecture",
            "topology_group",
            "environment_digest",
            "estimated_input_bytes",
            "estimated_output_bytes",
            "max_duration_seconds",
        }
        require_exact_keys(value, fields, "resource requirements")
        return cls(**value)  # type: ignore[arg-type]


@dataclass(frozen=True, slots=True)
class ResourceAllocation:
    allocation_id: str
    owner_id: str
    cpu_cores: int
    memory_mb: int
    scratch_mb: int
    accelerator_ids: tuple[str, ...]

    def __post_init__(self) -> None:
        object.__setattr__(
            self, "allocation_id", _resource_id(self.allocation_id, "allocation_id")
        )
        object.__setattr__(
            self,
            "owner_id",
            require_string(self.owner_id, "reservation owner_id", max_length=256),
        )
        object.__setattr__(
            self,
            "cpu_cores",
            require_positive_int(self.cpu_cores, "allocation.cpu_cores"),
        )
        object.__setattr__(
            self,
            "memory_mb",
            require_positive_int(self.memory_mb, "allocation.memory_mb"),
        )
        object.__setattr__(
            self,
            "scratch_mb",
            require_nonnegative_int(self.scratch_mb, "allocation.scratch_mb"),
        )
        ids = tuple(
            _resource_id(value, "accelerator_id") for value in self.accelerator_ids
        )
        if len(ids) != len(set(ids)):
            raise ValueError("accelerator_ids must be unique")
        object.__setattr__(self, "accelerator_ids", ids)

    @property
    def task_key(self) -> str:
        """Compatibility alias; new callers must supply a globally unique attempt owner."""
        return self.owner_id


class ResourceUnavailableError(RuntimeError):
    """Raised before execution when a complete atomic reservation is unavailable."""


class ResourcePool:
    """Lock-protected local allocator used by an Agent execution layer.

    This object is intentionally coordinator-independent. A protocol-v2 Agent
    will bind its returned allocation ID to a coordinator-owned reservation
    token; the current protocol must not enable concurrent claims based only on
    this local state.
    """

    def __init__(
        self, inventory: ResourceInventory, *, max_concurrency: int = 1
    ) -> None:
        if not isinstance(inventory, ResourceInventory):
            raise ValueError("inventory must be a ResourceInventory")
        self.inventory = inventory
        self.max_concurrency = require_positive_int(max_concurrency, "max_concurrency")
        self._lock = Lock()
        self._allocations: dict[str, ResourceAllocation] = {}
        self._allocated_devices: dict[str, tuple[AcceleratorDevice, ...]] = {}

    @staticmethod
    def _devices_conflict(left: AcceleratorDevice, right: AcceleratorDevice) -> bool:
        if left.device_id != right.device_id:
            return False
        if left.partition_id is None or right.partition_id is None:
            return True
        return left.partition_id == right.partition_id

    def reserve(
        self, owner_id: str, requirements: ResourceRequirements
    ) -> ResourceAllocation:
        if not isinstance(requirements, ResourceRequirements):
            raise ValueError("requirements must be ResourceRequirements")
        owner_id = require_string(owner_id, "reservation owner_id", max_length=256)
        if requirements.accelerator_mode is AcceleratorMode.FRACTIONAL:
            raise ResourceUnavailableError("fractional-accelerator-unsupported")
        with self._lock:
            if any(
                allocation.owner_id == owner_id
                for allocation in self._allocations.values()
            ):
                raise ValueError(
                    "reservation owner already has an active resource allocation"
                )
            if len(self._allocations) >= self.max_concurrency:
                raise ResourceUnavailableError("execution-slot-unavailable")
            used_cpu = sum(
                allocation.cpu_cores for allocation in self._allocations.values()
            )
            used_memory = sum(
                allocation.memory_mb for allocation in self._allocations.values()
            )
            used_scratch = sum(
                allocation.scratch_mb for allocation in self._allocations.values()
            )
            if used_cpu + requirements.cpu_cores > self.inventory.cpu_cores:
                raise ResourceUnavailableError("insufficient-cpu")
            if used_memory + requirements.memory_mb > self.inventory.memory_mb:
                raise ResourceUnavailableError("insufficient-memory")
            if used_scratch + requirements.scratch_mb > self.inventory.scratch_mb:
                raise ResourceUnavailableError("insufficient-scratch")
            static_errors = tuple(
                error
                for error in requirements.eligibility_errors(self.inventory)
                if error
                not in {
                    "insufficient-cpu",
                    "insufficient-memory",
                    "insufficient-scratch",
                    "accelerator-unavailable",
                }
            )
            if static_errors:
                raise ResourceUnavailableError(static_errors[0])
            reserved_devices = tuple(
                device
                for values in self._allocated_devices.values()
                for device in values
            )
            available_devices = tuple(
                device
                for device in self.inventory.accelerators
                if not any(
                    self._devices_conflict(device, reserved)
                    for reserved in reserved_devices
                )
            )
            devices = requirements._matching_devices(available_devices)
            if len(devices) < requirements.accelerator_count:
                raise ResourceUnavailableError("accelerator-unavailable")
            selected = tuple(
                device.allocation_id
                for device in devices[: requirements.accelerator_count]
            )
            allocation = ResourceAllocation(
                allocation_id=str(uuid4()),
                owner_id=owner_id,
                cpu_cores=requirements.cpu_cores,
                memory_mb=requirements.memory_mb,
                scratch_mb=requirements.scratch_mb,
                accelerator_ids=selected,
            )
            self._allocations[allocation.allocation_id] = allocation
            self._allocated_devices[allocation.allocation_id] = tuple(
                devices[: requirements.accelerator_count]
            )
            return allocation

    def release(self, allocation_id: str) -> bool:
        allocation_id = _resource_id(allocation_id, "allocation_id")
        with self._lock:
            removed = self._allocations.pop(allocation_id, None)
            self._allocated_devices.pop(allocation_id, None)
            return removed is not None

    def active_allocations(self) -> tuple[ResourceAllocation, ...]:
        with self._lock:
            return tuple(
                sorted(self._allocations.values(), key=lambda item: item.owner_id)
            )
