"""Author-facing planner, runner, reducer, and verifier protocols."""

from __future__ import annotations

from pathlib import Path
from typing import Mapping, Protocol, Sequence

from .artifacts import (
    ArtifactCollection,
    ArtifactRef,
    ArtifactSchema,
    OutputManifest,
    Provenance,
)
from .identity import ComponentRef
from .plans import JobRequest, TaskSpec, ValidatedJob, WorkflowPlan
from .runtime import NegotiatedWorkload
from .verification import CandidateOutputs, VerificationDecision, VerifyContext


class ArtifactCatalog(Protocol):
    """Bridge-owned, read-only access to durable input artifacts."""

    def materialize(self, artifact: ArtifactRef) -> Path:
        """Return an attempt-scoped verified local copy without exposing credentials."""
        ...


class ArtifactSink(Protocol):
    """Agent/bridge-owned sealing boundary for scientific output files."""

    def seal(
        self,
        path: Path,
        *,
        declaration: ArtifactSchema,
        records: int | None = None,
        dimensions: tuple[int, ...] = (),
    ) -> ArtifactRef:
        """Validate/upload bytes and return coordinator-owned immutable metadata."""
        ...


class CancellationToken(Protocol):
    """Cooperative cancellation observable by workload handlers."""

    def cancelled(self) -> bool: ...

    def raise_if_cancelled(self) -> None: ...


class PlanningResources(Protocol):
    """Caller-provided catalog, sink, and workspace for registry planning."""

    @property
    def catalog(self) -> ArtifactCatalog: ...

    @property
    def sink(self) -> ArtifactSink: ...

    @property
    def workspace(self) -> Path: ...


class PlanningContext(PlanningResources, Protocol):
    """Planner-facing resources augmented by completed negotiation."""

    @property
    def negotiated(self) -> NegotiatedWorkload:
        """Resolved optional fallbacks and the exact negotiated manifest."""
        ...


class TaskContext(Protocol):
    """Everything a runner needs: the pinned task, scoped catalog/sink,
    workspace, cancellation, and provenance to stamp on outputs."""

    @property
    def task(self) -> TaskSpec: ...

    @property
    def catalog(self) -> ArtifactCatalog: ...

    @property
    def sink(self) -> ArtifactSink: ...

    @property
    def workspace(self) -> Path: ...

    @property
    def cancellation(self) -> CancellationToken: ...

    @property
    def provenance(self) -> Provenance: ...


class ReduceContext(TaskContext, Protocol):
    """TaskContext plus the keyed partial artifacts accepted for reduction."""

    @property
    def accepted_inputs(self) -> Mapping[str, ArtifactCollection]: ...


class Planner(Protocol):
    """Validates a job and produces a digest-pinned ``WorkflowPlan``.

    ``entry_point`` must match the workflow's PLAN stage when one exists.
    """

    entry_point: str

    def validate(self, request: JobRequest) -> ValidatedJob: ...

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan: ...


class Runner(Protocol):
    """Executes one map (or verify) task and seals its partial output."""

    def run(self, context: TaskContext) -> OutputManifest: ...


class Reducer(Protocol):
    """Merges accepted partial artifacts into the final result."""

    def reduce(self, context: ReduceContext) -> OutputManifest: ...


class Verifier(Protocol):
    """Accepts or rejects candidate outputs with bounded sanitized evidence.

    ``identity`` must match the key under which the verifier is registered.
    """

    identity: ComponentRef

    def verify(
        self,
        context: VerifyContext,
        candidates: CandidateOutputs,
    ) -> VerificationDecision: ...
