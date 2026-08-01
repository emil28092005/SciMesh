"""SDK-built ``similarity-graph`` workload.

A user workload script built on the SciMesh Workload SDK. See ``core.py`` for
the block/pair scientific core and ``definition.py`` for the manifest-backed
planner/runner/reducer handlers.
"""

from .core import (
    block_pair_from_key,
    check_pair_coverage,
    compute_block_edges,
    merge_edge_partials,
    parse_molecule_blocks,
    read_block_rows,
    write_block_tsv,
    write_edge_csv,
)
from .definition import (
    MAP_ENTRY_POINT,
    REDUCE_ENTRY_POINT,
    SimilarityGraphSDKWorkload,
    similarity_graph_sdk_definition,
    workload_definition,
)

__all__ = [
    "MAP_ENTRY_POINT",
    "REDUCE_ENTRY_POINT",
    "SimilarityGraphSDKWorkload",
    "block_pair_from_key",
    "check_pair_coverage",
    "compute_block_edges",
    "merge_edge_partials",
    "parse_molecule_blocks",
    "read_block_rows",
    "similarity_graph_sdk_definition",
    "workload_definition",
    "write_block_tsv",
    "write_edge_csv",
]
