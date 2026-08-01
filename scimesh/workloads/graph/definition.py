"""SDK-built ``similarity-graph`` workload definition and handlers.

A ``MapReduceWorkload`` subclass with two non-default hooks: ``plan_tasks``
builds one task per block pair ``(i, j)`` with ``i <= j`` (each task receives
two block inputs), and the partial-key hooks parse ``map.<i>x<j>`` keys and
enforce the CTX-10 pair-coverage invariant. The final edge list is
byte-identical to the local brute-force reference.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Mapping, Sequence

from scimesh.sdk.artifacts import (
    ArtifactCollection,
    ArtifactSchema,
    ComponentRef,
    PortSpec,
)
from scimesh.sdk.batch import MapReduceWorkload
from scimesh.sdk.identity import SchemaRef, WorkloadId
from scimesh.sdk.plans import TaskSpec, ValidatedJob
from scimesh.sdk.protocols import PlanningContext
from scimesh.sdk.registry import WorkloadDefinition
from scimesh.sdk.workflow import StageSpec

from ..environment import current_environment_digest, current_scimesh_package_digest
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

MAP_ENTRY_POINT = "scimesh.workloads.graph.definition:map_graph@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.graph.definition:reduce_graph@v1"

_MAP_PARAMETERS = ("left_block", "right_block", "threshold", "threshold_direction")


def _parameters_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "threshold": {"type": "number", "minimum": 0, "maximum": 1},
            "threshold_direction": {"enum": ["greater", "less"]},
            "block_size": {"type": "integer", "minimum": 1},
            "max_rows": {"type": "integer", "minimum": 1},
        },
        "required": ["threshold"],
    }


def _molecule_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("molecule-table", 1),
        "text/tab-separated-values",
        "utf-8",
        max_bytes=10 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "required_columns": ["canonical_smiles", "chembl_id"],
        },
        max_records=100_000_000,
        canonicalizer="scimesh-tsv-v1",
    )


def _edge_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("similarity-edge-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["source_id", "target_id", "similarity"],
        },
        max_records=1_000_000_000,
        canonicalizer="similarity-edge-table-v1",
    )


class SimilarityGraphSDKWorkload(MapReduceWorkload):
    """Exact sparse Tanimoto graph over deterministic block pairs."""

    workload_id = WorkloadId("similarity-graph", "1.0.0")
    description = (
        "Exact sparse Tanimoto similarity graph over deterministic "
        "block pairs with a duplicate-safe, coverage-checked merge."
    )
    parameters_schema = _parameters_schema()
    input_port = PortSpec(_molecule_schema())
    block_port = PortSpec(_molecule_schema())
    partial_port = PortSpec(_edge_schema())
    output_port = PortSpec(_edge_schema())
    map_stage_inputs = {"left": block_port, "right": block_port}
    map_parameter_names = _MAP_PARAMETERS
    reduce_parameter_names = (
        "threshold",
        "threshold_direction",
        "block_size",
        "max_rows",
    )
    workflow_id = "graph-block-pairs-v1"
    map_entry_point = MAP_ENTRY_POINT
    reduce_entry_point = REDUCE_ENTRY_POINT

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        unknown = set(parameters) - {
            "threshold",
            "threshold_direction",
            "block_size",
            "max_rows",
        }
        if unknown:
            raise ValueError(
                "unsupported similarity-graph parameters: " + ", ".join(sorted(unknown))
            )
        threshold = parameters.get("threshold")
        if threshold is None:
            raise ValueError("threshold is required")
        self._unit_interval(threshold, "threshold")
        if "threshold_direction" in parameters and parameters[
            "threshold_direction"
        ] not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        if "block_size" in parameters:
            self._positive_int(parameters["block_size"], "block_size")
        if "max_rows" in parameters:
            self._positive_int(parameters["max_rows"], "max_rows")

    @staticmethod
    def _unit_interval(value: object, name: str) -> float:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)

    @staticmethod
    def _positive_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError(f"{name} must be a positive integer")
        return value

    def partition_input(
        self,
        input_path: Path,
        parameters: Mapping[str, Any],
        workspace: Path,
    ) -> list[Path]:
        block_size = int(parameters.get("block_size", 1_000))
        max_rows = parameters.get("max_rows")
        blocks, _stats = parse_molecule_blocks(
            input_path,
            block_size,
            int(max_rows) if isinstance(max_rows, int) else None,
        )
        paths: list[Path] = []
        for index, block in enumerate(blocks):
            path = workspace / f"block-{index:04d}.tsv"
            write_block_tsv(block, path)
            paths.append(path)
        return paths

    def plan_tasks(
        self,
        shard_paths: Sequence[Path],
        resolved: Mapping[str, Any],
        job: ValidatedJob,
        negotiated: Any,
        map_stage: StageSpec,
        context: PlanningContext,
    ) -> list[TaskSpec]:
        task_parameters = {
            "threshold": self._unit_interval(resolved.get("threshold"), "threshold"),
            "threshold_direction": resolved.get("threshold_direction", "greater"),
        }
        block_refs = [
            context.sink.seal(
                path,
                declaration=self.input_port.schema,
            )
            for path in shard_paths
        ]
        tasks: list[TaskSpec] = []
        for left in range(len(block_refs)):
            for right in range(left, len(block_refs)):
                tasks.append(
                    self.task_spec(
                        map_stage,
                        job,
                        negotiated,
                        f"map/{left:04d}x{right:04d}",
                        {
                            **task_parameters,
                            "left_block": left,
                            "right_block": right,
                        },
                        {
                            "left": ArtifactCollection.single(block_refs[left]),
                            "right": ArtifactCollection.single(block_refs[right]),
                        },
                    )
                )
        return tasks

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        left_block = parameters.get("left_block")
        right_block = parameters.get("right_block")
        if (
            isinstance(left_block, bool)
            or not isinstance(left_block, int)
            or isinstance(right_block, bool)
            or not isinstance(right_block, int)
        ):
            raise ValueError("graph map task requires block indices")
        diagonal = left_block == right_block
        threshold = self._unit_interval(parameters.get("threshold"), "threshold")
        direction = parameters.get("threshold_direction", "greater")
        if direction not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")
        left = read_block_rows(inputs["left"])
        right = left if diagonal else read_block_rows(inputs["right"])
        checked_pairs = (
            len(left) * (len(left) - 1) // 2 if diagonal else len(left) * len(right)
        )
        edges = compute_block_edges(left, right, threshold, direction)
        write_edge_csv(output_path, edges)
        return {"checked_pairs": checked_pairs, "edges_emitted": len(edges)}

    def parse_partial_key(self, key: str) -> Any:
        return block_pair_from_key(key)

    def validate_partial_keys(self, parsed: Sequence[Any]) -> None:
        check_pair_coverage(tuple(parsed))

    def reduce_partials(
        self,
        partial_paths: Sequence[Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return merge_edge_partials(partial_paths, output_path)


def similarity_graph_sdk_definition(
    *,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> SimilarityGraphSDKWorkload:
    """Build the SDK-based similarity-graph definition for tests."""
    return SimilarityGraphSDKWorkload(
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the SDK-based similarity-graph."""
    return similarity_graph_sdk_definition().definition()
