"""SDK-built ``similarity-search`` reference workload.

See ``core.py`` for the sharding/merge scientific core and ``definition.py``
for the manifest-backed planner/runner/reducer handlers.
"""

from .core import (
    merge_search_partials,
    run_search_shard,
    write_search_partial,
    write_search_shards,
)
from .definition import (
    MAP_ENTRY_POINT,
    REDUCE_ENTRY_POINT,
    SimilaritySearchSDKWorkload,
    similarity_search_sdk_definition,
    workload_definition,
)

__all__ = [
    "MAP_ENTRY_POINT",
    "REDUCE_ENTRY_POINT",
    "SimilaritySearchSDKWorkload",
    "merge_search_partials",
    "run_search_shard",
    "similarity_search_sdk_definition",
    "workload_definition",
    "write_search_partial",
    "write_search_shards",
]
