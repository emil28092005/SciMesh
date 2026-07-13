"""Built-in SciMesh workloads."""

from __future__ import annotations

from scimesh.core.registry import WorkloadRegistry
from scimesh.workloads.help import HelpWorkload
from scimesh.workloads.similarity_graph import SimilarityGraphWorkload
from scimesh.workloads.similarity_search import SimilaritySearchWorkload


def register_workloads(registry: WorkloadRegistry) -> None:
    """Register built-in workloads in one place, outside the main CLI."""
    registry.register(HelpWorkload())
    registry.register(SimilaritySearchWorkload())
    registry.register(SimilarityGraphWorkload())
