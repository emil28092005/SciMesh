"""SDK-built ``molwt-filter`` workload.

A minimal authoring example built on ``MapReduceWorkload``: the scaffold's
default sharding and concatenation hooks are used unchanged, so the workload
only declares identity, parameters, ports, and the single scientific hook.
"""

from .core import MOLWT_COLUMNS, filter_molecules_by_molwt
from .definition import (
    MAP_ENTRY_POINT,
    REDUCE_ENTRY_POINT,
    MolwtFilterWorkload,
    molwt_filter_sdk_definition,
    workload_definition,
)

__all__ = [
    "MAP_ENTRY_POINT",
    "MOLWT_COLUMNS",
    "REDUCE_ENTRY_POINT",
    "MolwtFilterWorkload",
    "filter_molecules_by_molwt",
    "molwt_filter_sdk_definition",
    "workload_definition",
]
