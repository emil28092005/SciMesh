"""Resource inventory and atomic local allocation tests for the SDK Agent layer."""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from threading import Barrier

import pytest

from scimesh.sdk import (
    AcceleratorDevice,
    AcceleratorMode,
    ResourceInventory,
    ResourcePool,
    ResourceRequirements,
    ResourceUnavailableError,
)


ENVIRONMENT_DIGEST = "sha256:" + "d" * 64


def gpu(device_id: str, *, topology_group: str = "socket-0") -> AcceleratorDevice:
    return AcceleratorDevice(
        kind="gpu",
        vendor="nvidia",
        device_id=device_id,
        model="Test GPU",
        memory_mb=16_384,
        modes=(AcceleratorMode.EXCLUSIVE_DEVICE,),
        capabilities={"compute": "9.0", "driver": "test"},
        topology_group=topology_group,
    )


def cpu_requirements(*, cpu_cores: int = 1, memory_mb: int = 256) -> ResourceRequirements:
    return ResourceRequirements(
        profile="cpu-v1",
        cpu_cores=cpu_cores,
        memory_mb=memory_mb,
        scratch_mb=128,
        architecture="x86-64",
        environment_digest=ENVIRONMENT_DIGEST,
        max_duration_seconds=120,
    )


def gpu_requirements(*, accelerator_count: int) -> ResourceRequirements:
    return ResourceRequirements(
        profile="gpu-v1",
        cpu_cores=1,
        memory_mb=512,
        scratch_mb=128,
        accelerator_count=accelerator_count,
        accelerator_kind="gpu",
        accelerator_memory_mb=8_192,
        accelerator_mode=AcceleratorMode.EXCLUSIVE_DEVICE,
        architecture="x86-64",
        environment_digest=ENVIRONMENT_DIGEST,
        max_duration_seconds=120,
    )


@pytest.mark.parametrize("device_id", ("GPU-0,GPU-1", "file:/dev/gpu0", "/dev/gpu0"))
def test_accelerator_ids_are_opaque_visibility_tokens(device_id: str) -> None:
    with pytest.raises(ValueError, match="opaque"):
        gpu(device_id)


def test_resource_inventory_and_requirements_round_trip_without_mutable_aliases() -> None:
    capabilities = {"compute": "9.0"}
    device = AcceleratorDevice(
        kind="gpu",
        vendor="nvidia",
        device_id="gpu-0",
        model="Test GPU",
        memory_mb=16_384,
        modes=(AcceleratorMode.EXCLUSIVE_DEVICE,),
        capabilities=capabilities,
        topology_group="socket-0",
    )
    inventory = ResourceInventory(
        cpu_cores=8,
        memory_mb=32_768,
        scratch_mb=8_192,
        architecture="x86-64",
        accelerators=(device,),
        environment_digests=(ENVIRONMENT_DIGEST,),
    )
    requirements = gpu_requirements(accelerator_count=1)

    capabilities["compute"] = "mutated"
    assert device.capabilities["compute"] == "9.0"
    with pytest.raises(TypeError):
        device.capabilities["compute"] = "mutated"
    assert ResourceInventory.from_dict(inventory.to_dict()) == inventory
    assert ResourceRequirements.from_dict(requirements.to_dict()) == requirements
    assert requirements.eligibility_errors(inventory) == ()

    incompatible = ResourceRequirements.from_dict(
        {**requirements.to_dict(), "architecture": "arm64"}
    )
    assert incompatible.eligibility_errors(inventory) == ("architecture-mismatch",)


def test_failed_multi_accelerator_reservation_is_atomic_and_releases_nothing_partial() -> None:
    inventory = ResourceInventory(
        cpu_cores=4,
        memory_mb=4_096,
        scratch_mb=2_048,
        architecture="x86-64",
        accelerators=(gpu("gpu-0"), gpu("gpu-1")),
        environment_digests=(ENVIRONMENT_DIGEST,),
    )
    pool = ResourcePool(inventory, max_concurrency=3)
    first = pool.reserve("task/first", gpu_requirements(accelerator_count=1))

    with pytest.raises(ResourceUnavailableError, match="accelerator-unavailable"):
        pool.reserve("task/gang", gpu_requirements(accelerator_count=2))

    assert pool.active_allocations() == (first,)
    assert pool.release(first.allocation_id)
    gang = pool.reserve("task/gang", gpu_requirements(accelerator_count=2))
    assert gang.accelerator_ids == ("gpu-0", "gpu-1")
    assert pool.active_allocations() == (gang,)


def test_resource_pool_enforces_aggregate_limits_under_concurrent_reservations() -> None:
    inventory = ResourceInventory(
        cpu_cores=4,
        memory_mb=1_024,
        scratch_mb=512,
        architecture="x86-64",
        environment_digests=(ENVIRONMENT_DIGEST,),
    )
    pool = ResourcePool(inventory, max_concurrency=8)
    barrier = Barrier(8)

    def attempt(index: int):
        barrier.wait()
        try:
            return pool.reserve(f"task/{index}", cpu_requirements())
        except ResourceUnavailableError:
            return None

    with ThreadPoolExecutor(max_workers=8) as executor:
        results = tuple(executor.map(attempt, range(8)))

    successful = tuple(result for result in results if result is not None)
    assert len(successful) == 4
    assert sum(item.cpu_cores for item in successful) == inventory.cpu_cores
    assert sum(item.memory_mb for item in successful) <= inventory.memory_mb
    assert sum(item.scratch_mb for item in successful) <= inventory.scratch_mb
    assert pool.active_allocations() == tuple(sorted(successful, key=lambda item: item.task_key))


def test_resource_pool_slot_and_task_identity_limits_do_not_leak_capacity() -> None:
    inventory = ResourceInventory(
        cpu_cores=4,
        memory_mb=2_048,
        scratch_mb=1_024,
        architecture="x86-64",
        environment_digests=(ENVIRONMENT_DIGEST,),
    )
    pool = ResourcePool(inventory, max_concurrency=1)
    allocation = pool.reserve("task/one", cpu_requirements())

    with pytest.raises(ValueError, match="already has"):
        pool.reserve("task/one", cpu_requirements())
    with pytest.raises(ResourceUnavailableError, match="execution-slot-unavailable"):
        pool.reserve("task/two", cpu_requirements())
    assert pool.active_allocations() == (allocation,)

    assert pool.release(allocation.allocation_id)
    assert not pool.release(allocation.allocation_id)
    replacement = pool.reserve("task/two", cpu_requirements())
    assert replacement.task_key == "task/two"


def test_exclusive_gpu_and_its_partitions_share_one_conflict_domain() -> None:
    full = AcceleratorDevice(
        kind="gpu",
        vendor="nvidia",
        device_id="gpu-0",
        model="Test GPU",
        memory_mb=16_384,
        modes=(AcceleratorMode.EXCLUSIVE_DEVICE, AcceleratorMode.PARTITION),
        capabilities={},
    )
    partitions = tuple(
        AcceleratorDevice(
            kind="gpu",
            vendor="nvidia",
            device_id="gpu-0",
            partition_id=f"mig-{index}",
            model="Test MIG",
            memory_mb=8_192,
            modes=(AcceleratorMode.PARTITION,),
            capabilities={},
        )
        for index in range(2)
    )
    inventory = ResourceInventory(
        cpu_cores=4,
        memory_mb=4_096,
        scratch_mb=2_048,
        architecture="x86-64",
        accelerators=(full, *partitions),
        environment_digests=(ENVIRONMENT_DIGEST,),
    )
    pool = ResourcePool(inventory, max_concurrency=3)
    exclusive = pool.reserve("task/exclusive", gpu_requirements(accelerator_count=1))
    partition_request = ResourceRequirements(
        **{
            **gpu_requirements(accelerator_count=1).to_dict(),
            "accelerator_mode": AcceleratorMode.PARTITION,
        }
    )
    with pytest.raises(ResourceUnavailableError, match="accelerator-unavailable"):
        pool.reserve("task/partition", partition_request)
    pool.release(exclusive.allocation_id)

    first = pool.reserve("task/partition-0", partition_request)
    second = pool.reserve("task/partition-1", partition_request)
    assert set(first.accelerator_ids + second.accelerator_ids) == {"mig-0", "mig-1"}
    with pytest.raises(ResourceUnavailableError, match="accelerator-unavailable"):
        pool.reserve("task/full", gpu_requirements(accelerator_count=1))
