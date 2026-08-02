"""SDK-built ``molwt-filter`` workload definition.

The minimal authoring example: a subclass of ``MapReduceWorkload`` that only
declares identity, parameters, ports, and one scientific hook. The default
hooks of the scaffold provide deterministic row-bounded sharding and
header-preserving concatenation, so nothing else is needed.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from scimesh.sdk.artifacts import ArtifactSchema, ComponentRef, PortSpec
from scimesh.sdk.batch import MapReduceWorkload
from scimesh.sdk.identity import SchemaRef, WorkloadId
from scimesh.sdk.registry import WorkloadDefinition
from scimesh.sdk.ui import UIElement

from ..environment import current_environment_digest, current_scimesh_package_digest
from .core import MOLWT_COLUMNS, filter_molecules_by_molwt

MAP_ENTRY_POINT = "scimesh.workloads.molwt_filter.definition:map_molwt_filter@v1"
REDUCE_ENTRY_POINT = "scimesh.workloads.molwt_filter.definition:reduce_molwt_filter@v1"


def _parameters_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "min_molwt": {
                "type": "number",
                "minimum": 0,
                "description": "Keep molecules with MolWt >= this value",
            },
            "max_molwt": {
                "type": "number",
                "minimum": 0,
                "description": "Keep molecules with MolWt <= this value",
            },
            "skip_invalid": {
                "type": "boolean",
                "default": True,
                "description": "Skip rows with invalid SMILES instead of failing",
            },
        },
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


def _filtered_schema() -> ArtifactSchema:
    return ArtifactSchema(
        SchemaRef("molwt-filtered-table", 1),
        "text/csv",
        "utf-8",
        max_bytes=100 * 1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": list(MOLWT_COLUMNS),
        },
        max_records=100_000_000,
        canonicalizer="molwt-filtered-table-v1",
    )


class MolwtFilterWorkload(MapReduceWorkload):
    """Keep molecules whose exact RDKit molecular weight is within bounds."""

    workload_id = WorkloadId("molwt-filter", "1.0.0")
    description = (
        "Filter molecules by exact RDKit molecular weight, one canonical "
        "CSV row per kept input molecule, in deterministic input order."
    )
    parameters_schema = _parameters_schema()
    input_port = PortSpec(_molecule_schema())
    partial_port = PortSpec(_filtered_schema())
    output_port = PortSpec(_filtered_schema())
    map_parameter_names = ("min_molwt", "max_molwt", "skip_invalid")
    reduce_parameter_names = ("min_molwt", "max_molwt", "skip_invalid")
    map_entry_point = MAP_ENTRY_POINT
    reduce_entry_point = REDUCE_ENTRY_POINT
    ui_elements = (
        UIElement(
            "min_molwt",
            "number",
            "Minimum molecular weight",
            help="Keep molecules with MolWt at least this value. Optional.",
            placeholder="e.g. 100",
            order=1,
        ),
        UIElement(
            "max_molwt",
            "number",
            "Maximum molecular weight",
            help="Keep molecules with MolWt at most this value. Optional.",
            placeholder="e.g. 600",
            order=2,
        ),
        UIElement(
            "skip_invalid",
            "checkbox",
            "Skip invalid molecules",
            help="Skip rows with invalid SMILES instead of failing the shard.",
            default=True,
            order=3,
        ),
    )

    def __init__(
        self,
        *,
        shard_rows: int = 10_000,
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

    def domain_validate(self, parameters: Mapping[str, Any]) -> None:
        unknown = set(parameters) - {"min_molwt", "max_molwt", "skip_invalid"}
        if unknown:
            raise ValueError(
                "unsupported molwt-filter parameters: " + ", ".join(sorted(unknown))
            )
        minimum = parameters.get("min_molwt")
        maximum = parameters.get("max_molwt")
        if minimum is None and maximum is None:
            raise ValueError("at least one of min_molwt or max_molwt is required")
        for value, name in ((minimum, "min_molwt"), (maximum, "max_molwt")):
            if value is not None and (
                isinstance(value, bool) or not isinstance(value, (int, float))
            ):
                raise ValueError(f"{name} must be a number")
        if (
            minimum is not None
            and maximum is not None
            and float(minimum) > float(maximum)
        ):
            raise ValueError("min_molwt must not exceed max_molwt")
        skip_invalid = parameters.get("skip_invalid", True)
        if not isinstance(skip_invalid, bool):
            raise ValueError("skip_invalid must be a boolean")

    def compute_shard(
        self,
        inputs: Mapping[str, Path],
        parameters: Mapping[str, Any],
        output_path: Path,
    ) -> Mapping[str, int | float]:
        return filter_molecules_by_molwt(
            inputs["input"],
            output_path,
            min_molwt=parameters.get("min_molwt"),
            max_molwt=parameters.get("max_molwt"),
            skip_invalid=parameters.get("skip_invalid", True),
        )


def molwt_filter_sdk_definition(
    *,
    shard_rows: int = 10_000,
    package_digest: str | None = None,
    environment_digest: str | None = None,
) -> MolwtFilterWorkload:
    """Build the molwt-filter definition for tests."""
    return MolwtFilterWorkload(
        shard_rows=shard_rows,
        package_digest=package_digest or current_scimesh_package_digest(),
        environment_digest=environment_digest or current_environment_digest(),
    )


def workload_definition() -> WorkloadDefinition:
    """Installed entry-point factory for molwt-filter."""
    return molwt_filter_sdk_definition().definition()
