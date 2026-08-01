"""SDK definitions for existing SciMesh workloads and local core runtime."""

from __future__ import annotations

import hashlib
import os
import platform
import sys

from rdkit import rdBase

from scimesh.distributed.similarity_search import (
    SimilaritySearchDistributedWorkload,
    run_similarity_search_shard,
)

from .artifacts import ArtifactSchema, PortSpec
from .compat import LegacyDistributedWorkloadAdapter
from .identity import ComponentRef, SDK_API_VERSION, SchemaRef
from .integrity import installed_distribution_digest
from .registry import WorkloadRegistry
from .resources import ResourceInventory
from .runtime import RuntimeCapabilities


def current_scimesh_package_digest() -> str:
    """Hash installed SciMesh Python sources for the built-in trusted adapter.

    This is a local immutable-code pin, not a package signature or container
    attestation. Consequently the built-in compatibility manifest is trusted
    only; an administrator must supply signed image metadata before enabling an
    untrusted quorum policy.
    """
    # Source/editable installs are allowed only for this explicit local
    # development helper. Registry discovery keeps the secure default.
    return installed_distribution_digest("scimesh", allow_editable=True)


def current_environment_digest() -> str:
    payload = "\n".join(
        (
            current_scimesh_package_digest(),
            f"python={sys.implementation.name}-{platform.python_version()}",
            f"rdkit={rdBase.rdkitVersion}",
            f"platform={sys.platform}-{platform.machine().lower()}",
        )
    )
    return "sha256:" + hashlib.sha256(payload.encode("utf-8")).hexdigest()


def similarity_search_sdk_adapter(*, shard_rows: int = 10_000) -> LegacyDistributedWorkloadAdapter:
    dataset_schema = ArtifactSchema(
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
    partial_schema = ArtifactSchema(
        SchemaRef("similarity-search-partial", 1),
        "text/csv",
        "utf-8",
        max_bytes=1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["rank", "chembl_id", "canonical_smiles", "similarity"],
        },
        max_records=100_000,
        canonicalizer="scimesh-search-partial-v1",
    )
    result_schema = ArtifactSchema(
        SchemaRef("similarity-search-result", 1),
        "text/csv",
        "utf-8",
        max_bytes=1024 * 1024 * 1024,
        validator=ComponentRef("delimited-table", 1),
        validator_configuration={
            "columns": ["rank", "chembl_id", "canonical_smiles", "similarity"],
        },
        max_records=100_000,
        canonicalizer="scimesh-search-result-v1",
    )
    parameters_schema = {
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
    return LegacyDistributedWorkloadAdapter(
        SimilaritySearchDistributedWorkload(),
        run_similarity_search_shard,
        version="1.0.0",
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
        parameters_schema=parameters_schema,
        input_port=PortSpec(dataset_schema),
        partial_port=PortSpec(partial_schema),
        output_port=PortSpec(result_schema),
        resolved_parameter_names=("query_source", "fingerprint"),
        shard_rows=shard_rows,
    )


def default_sdk_registry(*, shard_rows: int = 10_000) -> WorkloadRegistry:
    registry = WorkloadRegistry()
    registry.register(similarity_search_sdk_adapter(shard_rows=shard_rows).definition(), enabled=True)
    return registry


def similarity_search_workload_definition():
    """Installed entry-point factory for the default shard-size definition."""
    return similarity_search_sdk_adapter().definition()


def default_sdk_runtime() -> RuntimeCapabilities:
    architecture = platform.machine().lower() or "unknown"
    return RuntimeCapabilities(
        sdk_api_version=SDK_API_VERSION,
        protocol_version="1.0.0",
        profiles=("core-batch-v1",),
        features={"artifact-collections": "1.0.0", "exact-verifier": "1.0.0"},
        workload_capabilities=("similarity-search",),
        inventory=ResourceInventory(
            cpu_cores=max(os.cpu_count() or 1, 1),
            memory_mb=4096,
            scratch_mb=4096,
            architecture=architecture,
            environment_digests=(current_environment_digest(),),
        ),
    )
