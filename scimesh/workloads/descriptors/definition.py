"""SDK-built ``descriptor-batch`` workload definition and handlers.

A thin subclass of ``MapReduceWorkload``: the SDK assembles the manifest, the
map/reduce stages, the workflow, and the digest-pinned handlers; this module
only declares the scientific contract (pinned descriptors, canonical CSV,
row-bounded shards, header-preserving concatenation) and the three hooks that
partition, compute, and merge.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Mapping, Sequence

from scimesh.sdk.artifacts import ArtifactSchema, ComponentRef, PortSpec
from scimesh.sdk.batch import MapReduceWorkload
from scimesh.sdk.identity import SchemaRef, WorkloadId
from scimesh.sdk.registry import WorkloadDefinition

from ..environment import current_environment_digest, current_scimesh_package_digest
from .core import (
    DESCRIPTOR_COLUMNS,
    compute_descriptor_batch,
    concatenate_descriptor_shards,
    validate_descriptor_names,
    write_descriptor_shards,
)

MAP_ENTRY_POINT = "scimesh.workloads.descriptors.definition:map_descriptors@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.descriptors.definition:reduce_descriptors@v1"


def _parameters_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "skip_invalid": {
                "type": "boolean",
                "default": True,
                "description": "Skip rows with invalid SMILES instead of failing",
            },
        },
    }


def _input_schema() -> ArtifactSchema:
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


def _descriptor_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("descriptor-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": list(DESCRIPTOR_COLUMNS),
        },
        max_records=100_000_000,
        canonicalizer="descriptor-table-v1",
    )


class DescriptorBatchWorkload(MapReduceWorkload):
    """Pinned RDKit 2D descriptor computation, one canonical row per input."""

    workload_id = WorkloadId("descriptor-batch", "1.0.0")
    description = (
        "Compute a pinned set of RDKit 2D descriptors, one canonical "
        "CSV row per input molecule, in deterministic input order."
    )
    parameters_schema = _parameters_schema()
    input_port = PortSpec(_input_schema())
    partial_port = PortSpec(_descriptor_schema())
    output_port = PortSpec(_descriptor_schema())
    map_parameter_names = ("skip_invalid",)
    reduce_parameter_names = ("skip_invalid",)
    map_entry_point = MAP_ENTRY_POINT
    reduce_entry_point = REDUCE_ENTRY_POINT

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
        validate_descriptor_names()
        self.shard_rows = shard_rows
        super().__init__(
            package_digest=package_digest,
            environment_digest=environment_digest,
        )

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        value = parameters.get("skip_invalid", True)
        if not isinstance(value, bool):
            raise ValueError("skip_invalid must be a boolean")

    def partition_input(
        self,
        input_path: Path,
        parameters: Mapping[str, Any],
        workspace: Path,
    ) -> list[Path]:
        return write_descriptor_shards(input_path, workspace, self.shard_rows)

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return compute_descriptor_batch(
            inputs["input"],
            output_path,
            skip_invalid=self.domain_validate_check(parameters),
        )

    @staticmethod
    def domain_validate_check(parameters: Mapping[str, Any]) -> bool:
        value = parameters.get("skip_invalid", True)
        if not isinstance(value, bool):
            raise ValueError("skip_invalid must be a boolean")
        return value

    def reduce_partials(
        self,
        partial_paths: Sequence[Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return concatenate_descriptor_shards(partial_paths, output_path)


def descriptor_batch_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> DescriptorBatchWorkload:
    """Build the default descriptor-batch definition for tests."""
    return DescriptorBatchWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for the descriptor-batch workload."""
    return descriptor_batch_sdk_definition().definition()
