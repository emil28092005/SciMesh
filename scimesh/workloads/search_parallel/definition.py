"""SDK-built ``similarity-search-parallel`` workload definition and handlers.

A subclass of ``SimilaritySearchSDKWorkload``: identical contract (plan-time
query resolution, deterministic sharding, top-k reduction, byte-identical
partials), but each shard's fingerprinting and scoring runs across a thread
pool (``threads`` parameter, default = CPU count).
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from scimesh.sdk.batch import MapReduceWorkload
from scimesh.sdk.identity import WorkloadId
from scimesh.sdk.plans import JobRequest
from scimesh.sdk.registry import WorkloadDefinition
from scimesh.sdk.ui import UIElement

from ..environment import current_environment_digest, current_scimesh_package_digest
from ..search.definition import SimilaritySearchSDKWorkload, _parameters_schema
from .core import run_search_shard_parallel

MAP_ENTRY_POINT = "scimesh.workloads.search_parallel.definition:map_search_parallel@v1"

# The parallel variant adds only the thread-count parameter on top of the
# search contract; everything else (schemas, entry points of the reduce stage,
# partitioning) is inherited.
_MAP_PARAMETERS = (
    "query_id",
    "query_smiles",
    "top_k",
    "threshold",
    "threshold_direction",
    "progress_every",
    "threads",
)


def _parallel_parameters_schema() -> dict[str, Any]:
    schema = dict(_parameters_schema())
    properties = dict(schema["properties"])
    properties["threads"] = {
        "type": "integer",
        "minimum": 1,
        "description": "Threads used to fingerprint and score one shard (default: CPU count).",
    }
    schema["properties"] = properties
    return schema


class SimilaritySearchParallelSDKWorkload(SimilaritySearchSDKWorkload):
    """Exact top-k Tanimoto search with a per-shard thread pool."""

    workload_id = WorkloadId("similarity-search-parallel", "1.0.0")
    description = (
        "Exact top-k Tanimoto molecular similarity search over deterministic "
        "TSV shards with a bounded merge; each shard is fingerprinted and "
        "scored across a thread pool. Output is byte-identical to "
        "similarity-search."
    )
    parameters_schema = _parallel_parameters_schema()
    map_parameter_names = _MAP_PARAMETERS
    map_entry_point = MAP_ENTRY_POINT
    ui_elements = SimilaritySearchSDKWorkload.ui_elements + (
        UIElement(
            "threads",
            "number",
            "Threads per shard",
            help="Threads used to fingerprint and score one shard (default: CPU count).",
            placeholder="auto",
            order=6,
        ),
    )

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        # The search base rejects unknown parameters; threads is our addition,
        # so it is validated here and stripped before delegating.
        rest = dict(parameters)
        threads = rest.pop("threads", None)
        if threads is not None and (
            isinstance(threads, bool) or not isinstance(threads, int) or threads < 1
        ):
            raise ValueError("threads must be a positive integer")
        super().domain_validate(rest)

    def resolved_parameters_for_plan(
        self,
        job,
        input_path,
        resolved,
    ):
        # threads is a map-stage-only knob; strip it from the plan-level
        # resolved parameters so the reduce stage projection stays clean.
        resolved = super().resolved_parameters_for_plan(job, input_path, resolved)
        stripped = dict(resolved)
        stripped.pop("threads", None)
        return stripped

    def resolved_parameters(self, request: JobRequest) -> dict[str, Any]:
        resolved = super().resolved_parameters(request)
        if "threads" in request.parameters:
            threads = request.parameters["threads"]
            if isinstance(threads, bool) or not isinstance(threads, int) or threads < 1:
                raise ValueError("threads must be a positive integer")
            resolved["threads"] = threads
        return resolved

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return run_search_shard_parallel(inputs["input"], parameters, output_path)


def similarity_search_parallel_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> SimilaritySearchParallelSDKWorkload:
    """Build the SDK-built parallel similarity-search definition for tests."""
    return SimilaritySearchParallelSDKWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the SDK-built parallel search."""
    return similarity_search_parallel_sdk_definition().definition()


def map_search_parallel(
    input_path: Path,
    parameters: Mapping[str, object],
    output_path: Path,
) -> dict[str, int]:
    """Digest-pinned map entry point for the parallel search shard."""
    return run_search_shard_parallel(input_path, parameters, output_path)
