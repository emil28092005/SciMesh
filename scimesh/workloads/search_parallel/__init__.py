"""SDK-built ``similarity-search-parallel`` workload.

Same contract as ``similarity-search`` with a per-shard thread pool. See
``core.py`` for the parallel scoring core and ``definition.py`` for the
manifest-backed handlers.
"""

from .core import (
    run_search_shard_parallel,
    search_similar_parallel,
    write_search_shards,
)
from .definition import (
    MAP_ENTRY_POINT,
    SimilaritySearchParallelSDKWorkload,
    similarity_search_parallel_sdk_definition,
    workload_definition,
)

__all__ = [
    "MAP_ENTRY_POINT",
    "SimilaritySearchParallelSDKWorkload",
    "similarity_search_parallel_sdk_definition",
    "workload_definition",
    "run_search_shard_parallel",
    "search_similar_parallel",
    "write_search_shards",
]
