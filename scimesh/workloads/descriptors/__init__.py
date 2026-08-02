"""SDK-built ``descriptor-batch`` reference workload.

See ``core.py`` for the pinned scientific contract and ``definition.py`` for
the manifest-backed planner/runner/reducer handlers.
"""

from .core import (
    DESCRIPTOR_COLUMNS,
    DESCRIPTOR_NAMES,
    DescriptorRow,
    compute_descriptor_batch,
    descriptor_calculator,
    validate_descriptor_names,
    write_descriptor_rows,
)
from .definition import (
    MAP_ENTRY_POINT,
    REDUCE_ENTRY_POINT,
    DescriptorBatchWorkload,
    descriptor_batch_sdk_definition,
    workload_definition,
)

__all__ = [
    "DESCRIPTOR_COLUMNS",
    "DESCRIPTOR_NAMES",
    "MAP_ENTRY_POINT",
    "REDUCE_ENTRY_POINT",
    "DescriptorBatchWorkload",
    "DescriptorRow",
    "compute_descriptor_batch",
    "descriptor_batch_sdk_definition",
    "descriptor_calculator",
    "validate_descriptor_names",
    "workload_definition",
    "write_descriptor_rows",
]
