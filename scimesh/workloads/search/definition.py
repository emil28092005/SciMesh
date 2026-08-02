"""SDK-built ``similarity-search`` workload definition and handlers.

A thin subclass of ``MapReduceWorkload``: the SDK assembles the manifest,
stages, workflow, and digest-pinned handlers. This module declares the search
scientific contract (plan-time query resolution, deterministic sharding, local
top-k per shard, bounded heap merge) and the hooks that partition, compute,
and merge.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

from rdkit import Chem

from scimesh.chemistry.dataset import find_molecule_by_id, parse_smiles
from scimesh.chemistry.fingerprints import FP_RADIUS, FP_SIZE
from scimesh.sdk.artifacts import ArtifactSchema, ComponentRef, PortSpec
from scimesh.sdk.batch import MapReduceWorkload
from scimesh.sdk.identity import SchemaRef, WorkloadId
from scimesh.sdk.plans import JobRequest, ValidatedJob
from scimesh.sdk.registry import WorkloadDefinition
from scimesh.sdk.ui import UIElement

from ..environment import current_environment_digest, current_scimesh_package_digest
from .core import merge_search_partials, run_search_shard, write_search_shards

MAP_ENTRY_POINT = "scimesh.workloads.search.definition:map_search@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.search.definition:reduce_search@v1"

_MAP_PARAMETERS = (
    "query_id",
    "query_smiles",
    "top_k",
    "threshold",
    "threshold_direction",
    "progress_every",
)


def _parameters_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "query_id": {"type": "string", "minLength": 1, "maxLength": 200},
            "query_smiles": {"type": "string", "minLength": 1, "maxLength": 200},
            "top_k": {"type": "integer", "minimum": 1},
            "threshold": {"type": "number", "minimum": 0, "maximum": 1},
            "threshold_direction": {"enum": ["greater", "less"]},
            "max_rows": {"type": "integer", "minimum": 1},
            "progress_every": {"type": "integer", "minimum": 0},
        },
        "oneOf": [
            {"required": ["query_id"], "not": {"required": ["query_smiles"]}},
            {"required": ["query_smiles"], "not": {"required": ["query_id"]}},
        ],
    }


def _dataset_schema() -> ArtifactSchema:
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


def _search_table_schema(ref: SchemaRef, canonicalizer: str) -> ArtifactSchema:
    return ArtifactSchema(
        ref,
        "text/csv",
        "utf-8",
        max_bytes=1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["rank", "chembl_id", "canonical_smiles", "similarity"],
        },
        max_records=100_000,
        canonicalizer=canonicalizer,
    )


class SimilaritySearchSDKWorkload(MapReduceWorkload):
    """Exact top-k Tanimoto search over deterministic shards with a bounded merge."""

    workload_id = WorkloadId("similarity-search", "1.0.0")
    description = (
        "Exact top-k Tanimoto molecular similarity search over "
        "deterministic TSV shards with a bounded merge."
    )
    parameters_schema = _parameters_schema()
    input_port = PortSpec(_dataset_schema())
    partial_port = PortSpec(
        _search_table_schema(
            SchemaRef("similarity-search-partial", 1), "scimesh-search-partial-v1"
        )
    )
    output_port = PortSpec(
        _search_table_schema(
            SchemaRef("similarity-search-result", 1), "scimesh-search-result-v1"
        )
    )
    map_parameter_names = _MAP_PARAMETERS
    reduce_parameter_names = _MAP_PARAMETERS + ("query_source", "fingerprint")
    map_entry_point = MAP_ENTRY_POINT
    reduce_entry_point = REDUCE_ENTRY_POINT
    reduction = "top-k"
    ui_elements = (
        UIElement(
            "query_id",
            "text",
            "Query molecule id",
            help="ChEMBL id of the query molecule. Provide exactly one of id or SMILES.",
            order=1,
        ),
        UIElement(
            "query_smiles",
            "text",
            "Query molecule SMILES",
            help="SMILES of the query molecule. Provide exactly one of id or SMILES.",
            order=2,
        ),
        UIElement(
            "top_k",
            "number",
            "Top k",
            help="Number of most similar molecules to keep per shard (global merge keeps the best of these).",
            default=20,
            order=3,
        ),
        UIElement(
            "threshold_direction",
            "select",
            "Direction",
            help="Keep molecules with similarity greater or less than the threshold.",
            options=("greater", "less"),
            default="greater",
            order=4,
        ),
        UIElement(
            "threshold",
            "number",
            "Similarity threshold",
            help="Optional similarity bound: results are filtered to this direction.",
            placeholder="e.g. 0.8",
            order=5,
        ),
    )

    def __init__(
        self,
        *,
        shard_rows: int,
        package_digest: str,
        environment_digest: str,
    ) -> None:
        if (
            isinstance(shard_rows, bool)
            or not isinstance(shard_rows, int)
            or shard_rows < 1
        ):
            raise ValueError("shard_rows must be a positive integer")
        self.shard_rows = shard_rows
        super().__init__(
            package_digest=package_digest,
            environment_digest=environment_digest,
        )

    # ------------------------------------------------------------------
    # Scientific hooks
    # ------------------------------------------------------------------

    @staticmethod
    def _string(value: object, name: str) -> str:
        if not isinstance(value, str) or not value.strip() or len(value) > 200:
            raise ValueError(f"{name} must be a non-empty string")
        return value

    @staticmethod
    def _positive_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 1:
            raise ValueError(f"{name} must be a positive integer")
        return value

    @staticmethod
    def _nonnegative_int(value: object, name: str) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise ValueError(f"{name} must be a non-negative integer")
        return value

    @staticmethod
    def _unit_interval(value: object, name: str) -> float:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{name} must be a number between 0 and 1")
        return float(value)

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        unknown = set(parameters) - {
            "query_id",
            "query_smiles",
            "top_k",
            "threshold",
            "threshold_direction",
            "max_rows",
            "progress_every",
        }
        if unknown:
            raise ValueError(
                "unsupported similarity-search parameters: "
                + ", ".join(sorted(unknown))
            )
        query_id = parameters.get("query_id")
        query_smiles = parameters.get("query_smiles")
        if (query_id is None) == (query_smiles is None):
            raise ValueError("exactly one of query_id or query_smiles is required")
        if query_id is not None:
            self._string(query_id, "query_id")
        if query_smiles is not None:
            self._string(query_smiles, "query_smiles")
        self._positive_int(parameters.get("top_k", 20), "top_k")
        if "max_rows" in parameters:
            self._positive_int(parameters["max_rows"], "max_rows")
        if "progress_every" in parameters:
            self._nonnegative_int(parameters["progress_every"], "progress_every")
        if "threshold" in parameters:
            self._unit_interval(parameters["threshold"], "threshold")
        if "threshold_direction" in parameters and parameters[
            "threshold_direction"
        ] not in {"greater", "less"}:
            raise ValueError("threshold_direction must be 'greater' or 'less'")

    def resolved_parameters(self, request: JobRequest) -> dict[str, Any]:
        parameters = request.parameters
        query_id = parameters.get("query_id")
        if isinstance(query_id, str):
            query_source: dict[str, str] = {"kind": "chembl_id", "value": query_id}
        else:
            query_source = {
                "kind": "smiles",
                "value": self._string(parameters.get("query_smiles"), "query_smiles"),
            }
        resolved: dict[str, Any] = {
            "query_source": query_source,
            "top_k": self._positive_int(parameters.get("top_k", 20), "top_k"),
            "threshold_direction": parameters.get("threshold_direction", "greater"),
            "fingerprint": {
                "algorithm": "morgan",
                "radius": FP_RADIUS,
                "fp_size": FP_SIZE,
            },
        }
        if "threshold" in parameters:
            resolved["threshold"] = self._unit_interval(
                parameters["threshold"], "threshold"
            )
        if "max_rows" in parameters:
            resolved["max_rows"] = self._positive_int(
                parameters["max_rows"], "max_rows"
            )
        if "progress_every" in parameters:
            resolved["progress_every"] = self._nonnegative_int(
                parameters["progress_every"], "progress_every"
            )
        return resolved

    def resolved_parameters_for_plan(
        self,
        job: ValidatedJob,
        input_path: Path,
        resolved: dict[str, Any],
    ) -> dict[str, Any]:
        query_id = job.request.parameters.get("query_id")
        if isinstance(query_id, str):
            record = find_molecule_by_id(input_path, query_id)
            resolved["query_smiles"] = Chem.MolToSmiles(record.molecule, canonical=True)
        else:
            supplied = job.request.parameters["query_smiles"]
            assert isinstance(supplied, str)
            molecule = parse_smiles(supplied)
            if molecule is None:
                raise ValueError("query_smiles is invalid")
            resolved["query_smiles"] = Chem.MolToSmiles(molecule, canonical=True)
        return resolved

    def partition_input(
        self,
        input_path: Path,
        parameters: Mapping[str, Any],
        workspace: Path,
    ) -> list[Path]:
        max_rows = parameters.get("max_rows")
        return write_search_shards(
            input_path,
            workspace,
            self.shard_rows,
            int(max_rows) if isinstance(max_rows, int) else None,
        )

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return run_search_shard(inputs["input"], parameters, output_path)

    def reduce_partials(
        self,
        partial_paths: Sequence[Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return merge_search_partials(partial_paths, parameters, output_path)


def similarity_search_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> SimilaritySearchSDKWorkload:
    """Build the SDK-built similarity-search definition for tests."""
    return SimilaritySearchSDKWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the SDK-built similarity-search."""
    return similarity_search_sdk_definition().definition()
