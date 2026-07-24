"""Coordinator-independent contracts for distributed SciMesh workloads.

This package defines the typed plan and reduction boundary shared by future
planners, worker adapters, and coordinator bridges.  It intentionally has no
network, database, or coordinator imports.
"""

from .models import (
    ArtifactReference,
    CompletedPartial,
    DistributedPlan,
    FinalResult,
    PlannedTask,
)
from .registry import DistributedWorkloadRegistry, PlanningService, WorkloadDescription
from .workload import DistributedWorkload

__all__ = [
    "ArtifactReference",
    "CompletedPartial",
    "DistributedPlan",
    "DistributedWorkload",
    "DistributedWorkloadRegistry",
    "FinalResult",
    "PlannedTask",
    "PlanningService",
    "WorkloadDescription",
]
