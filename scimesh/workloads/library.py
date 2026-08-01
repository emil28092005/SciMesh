"""Built-in workload library: default registry and runtime.

This module is workload code, not SDK framework code. It composes the
installed SciMesh workload packages (``search``, ``graph``, ``descriptors``)
with the SDK registry and runtime. External workload libraries can follow the
same pattern with their own packages and entry points.
"""

from __future__ import annotations

import os
import platform

from scimesh.sdk.identity import SDK_API_VERSION
from scimesh.sdk.registry import WorkloadRegistry
from scimesh.sdk.resources import ResourceInventory
from scimesh.sdk.runtime import RuntimeCapabilities

from .descriptors import descriptor_batch_sdk_definition
from .environment import current_environment_digest
from .graph import similarity_graph_sdk_definition
from .molwt_filter import molwt_filter_sdk_definition
from .search import similarity_search_sdk_definition

__all__ = [
    "default_sdk_registry",
    "default_sdk_runtime",
]


def default_sdk_registry(
    *,
    shard_rows: int = 10_000,
    allowlist: tuple[AllowedPackage, ...] | None = None,
) -> WorkloadRegistry:
    """Registry of every built-in SDK-built workload, all enabled.

    When ``allowlist`` is provided, installed workloads are discovered through
    the ``scimesh.workloads`` entry points instead of the built-ins.
    """
    registry = WorkloadRegistry()
    if allowlist:
        registry.discover_installed(allowlist)
        return registry
    registry.register(
        similarity_search_sdk_definition(shard_rows=shard_rows).definition(),
        enabled=True,
    )
    registry.register(
        similarity_graph_sdk_definition().definition(),
        enabled=True,
    )
    registry.register(
        descriptor_batch_sdk_definition(shard_rows=shard_rows).definition(),
        enabled=True,
    )
    registry.register(
        molwt_filter_sdk_definition(shard_rows=shard_rows).definition(),
        enabled=True,
    )
    return registry


def default_sdk_runtime(
    *,
    workload_capabilities: tuple[str, ...] | None = None,
    environment_digests: tuple[str, ...] | None = None,
) -> RuntimeCapabilities:
    """Runtime advertising the built-in workloads' capabilities and inventory.

    ``workload_capabilities`` and ``environment_digests`` override the built-in
    defaults, for example when a registry was populated from an allowlist of
    installed user workloads instead of the built-ins.
    """
    architecture = platform.machine().lower() or "unknown"
    return RuntimeCapabilities(
        sdk_api_version=SDK_API_VERSION,
        protocol_version="1.0.0",
        profiles=("core-batch-v1",),
        features={"artifact-collections": "1.0.0", "exact-verifier": "1.0.0"},
        workload_capabilities=(
            workload_capabilities
            or ("similarity-search", "similarity-graph", "descriptor-batch", "molwt-filter")
        ),
        inventory=ResourceInventory(
            cpu_cores=max(os.cpu_count() or 1, 1),
            memory_mb=4096,
            scratch_mb=4096,
            architecture=architecture,
            environment_digests=environment_digests or (current_environment_digest(),),
        ),
    )
