"""Author-facing planner, runner, reducer, and verifier protocols."""

from __future__ import annotations

from pathlib import Path
from typing import Mapping, Protocol, Sequence

from .artifacts import ArtifactCollection, ArtifactRef, ArtifactSchema, OutputManifest, Provenance
from .plans import JobRequest, TaskSpec, ValidatedJob, WorkflowPlan
from .runtime import NegotiatedWorkload
from .verification import CandidateOutputs, VerificationDecision, VerifyContext


class ArtifactCatalog(Protocol):
    """Bridge-owned, read-only access to durable input artifacts."""

    def materialize(self, artifact: ArtifactRef) -> Path:
        """Return an attempt-scoped verified local copy without exposing credentials."""


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


class CancellationToken(Protocol):
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


class TaskContext(Protocol):
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
    @property
    def accepted_inputs(self) -> Mapping[str, ArtifactCollection]: ...


class Planner(Protocol):
    entry_point: str

    def validate(self, request: JobRequest) -> ValidatedJob: ...

    def plan(self, job: ValidatedJob, context: PlanningContext) -> WorkflowPlan: ...


class Runner(Protocol):
    def run(self, context: TaskContext) -> OutputManifest: ...


class Reducer(Protocol):
    def reduce(self, context: ReduceContext) -> OutputManifest: ...


class Verifier(Protocol):
    def verify(
        self,
        context: VerifyContext,
        candidates: CandidateOutputs,
    ) -> VerificationDecision: ...
