"""Local core-batch execution and reusable SDK conformance checks."""

from __future__ import annotations

import hashlib
import os
import shutil
import stat
import tempfile
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from pathlib import Path
from threading import Event, Lock
from typing import Callable, Mapping
from uuid import NAMESPACE_URL, uuid4, uuid5

from ._validation import canonical_json, require_positive_int
from .artifacts import (
    ArtifactCollection,
    ArtifactItem,
    ArtifactRef,
    ArtifactSchema,
    CollectionKind,
    OutputManifest,
    Provenance,
)
from .identity import ComponentRef, SDK_API_VERSION, SchemaRef
from .execution import NetworkPolicy, ProcessModel
from .manifest import TrustMode, WorkloadManifest
from .plans import JobRequest, TaskSpec
from .protocols import ArtifactCatalog, ArtifactSink
from .registry import WorkloadDefinition, WorkloadRegistry
from .resources import ResourceAllocation, ResourcePool
from .runtime import RuntimeCapabilities
from .verification import (
    CandidateOutputs,
    VerificationBinding,
    VerificationDecision,
    VerificationStatus,
    VerifyContext,
)
from .workflow import StageKind, WorkflowFailurePolicy


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


class LocalArtifactStore:
    """Credential-free content store for local SDK/conformance execution."""

    def __init__(
        self,
        root: Path,
        *,
        inspectors: Mapping[
            str,
            tuple[
                ComponentRef,
                Callable[
                    [Path, Mapping[str, object]],
                    tuple[int | None, tuple[int, ...]],
                ],
            ],
        ] | None = None,
    ) -> None:
        self.root = root.resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self._paths: dict[str, Path] = {}
        self._references: dict[str, ArtifactRef] = {}
        self._refcounts: dict[str, int] = {}
        self._inspectors = dict(inspectors or {})
        if any(
            not isinstance(binding, tuple)
            or len(binding) != 2
            or not isinstance(binding[0], ComponentRef)
            or not callable(binding[1])
            for binding in self._inspectors.values()
        ):
            raise ValueError("artifact inspectors must bind an identity and callable")
        self._lock = Lock()

    def seal(
        self,
        path: Path,
        *,
        declaration: ArtifactSchema,
        records: int | None = None,
        dimensions: tuple[int, ...] = (),
    ) -> ArtifactRef:
        source_flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
        try:
            source_fd = os.open(path, source_flags)
        except OSError as error:
            raise ValueError("artifact sink could not open a regular non-symlink file") from error
        try:
            return self.seal_descriptor(
                source_fd,
                declaration=declaration,
                records=records,
                dimensions=dimensions,
            )
        finally:
            os.close(source_fd)

    def seal_descriptor(
        self,
        descriptor: int,
        *,
        declaration: ArtifactSchema,
        records: int | None = None,
        dimensions: tuple[int, ...] = (),
    ) -> ArtifactRef:
        """Copy and validate one already safely opened regular-file descriptor."""
        if isinstance(descriptor, bool) or not isinstance(descriptor, int) or descriptor < 0:
            raise ValueError("artifact descriptor must be an open file descriptor")
        if not isinstance(declaration, ArtifactSchema):
            raise ValueError("artifact declaration must be an ArtifactSchema")
        source_fd = os.dup(descriptor)
        temporary_fd, temporary_name = tempfile.mkstemp(prefix=".seal-", dir=self.root)
        temporary = Path(temporary_name)
        digest_builder = hashlib.sha256()
        try:
            if not stat.S_ISREG(os.fstat(source_fd).st_mode):
                raise ValueError("artifact sink accepts only regular files")
            with os.fdopen(source_fd, "rb", closefd=True) as source_file, os.fdopen(
                temporary_fd, "wb", closefd=True
            ) as destination_file:
                source_fd = -1
                temporary_fd = -1
                for block in iter(lambda: source_file.read(1024 * 1024), b""):
                    digest_builder.update(block)
                    destination_file.write(block)
                destination_file.flush()
                os.fsync(destination_file.fileno())
            digest = digest_builder.hexdigest()
            artifact_id = str(
                uuid5(NAMESPACE_URL, f"scimesh:{declaration.ref.canonical}:{digest}")
            )
            destination = self.root / artifact_id
            size_bytes = temporary.stat().st_size
            if size_bytes > declaration.max_bytes:
                raise ValueError("sealed artifact exceeds its declared byte limit")
            measured_records, measured_dimensions = self._inspect_content(
                temporary,
                declaration,
            )
            if records is not None and records != measured_records:
                raise ValueError("artifact record summary does not match inspected content")
            if dimensions and dimensions != measured_dimensions:
                raise ValueError("artifact dimension summary does not match inspected content")
            if declaration.max_records is not None:
                if measured_records is None:
                    raise ValueError("artifact validator did not produce a required record count")
                if measured_records > declaration.max_records:
                    raise ValueError("sealed artifact exceeds its declared record limit")
            if declaration.max_dimensions:
                if len(measured_dimensions) != len(declaration.max_dimensions) or any(
                    actual > maximum
                    for actual, maximum in zip(
                        measured_dimensions,
                        declaration.max_dimensions,
                    )
                ):
                    raise ValueError("sealed artifact exceeds its declared dimension limits")
            reference = ArtifactRef(
                artifact_id,
                digest,
                declaration.ref,
                declaration.media_type,
                size_bytes,
                records=measured_records,
                dimensions=measured_dimensions,
            )
            with self._lock:
                if destination.is_symlink():
                    raise ValueError("local artifact destination must not be a symbolic link")
                if destination.exists():
                    if not destination.is_file() or _sha256_file(destination) != digest:
                        raise ValueError("local artifact identity collision")
                    temporary.unlink()
                else:
                    os.replace(temporary, destination)
                destination.chmod(0o444)
                existing = self._references.get(artifact_id)
                if existing is not None and existing != reference:
                    raise ValueError("local artifact identity was reused with different metadata")
                self._paths[artifact_id] = destination
                self._references[artifact_id] = reference
                self._refcounts[artifact_id] = self._refcounts.get(artifact_id, 0) + 1
            return reference
        finally:
            if source_fd >= 0:
                os.close(source_fd)
            if temporary_fd >= 0:
                os.close(temporary_fd)
            if temporary.exists():
                temporary.unlink()

    def _inspect_content(
        self,
        path: Path,
        declaration: ArtifactSchema,
    ) -> tuple[int | None, tuple[int, ...]]:
        validator = declaration.validator
        configuration = declaration.validator_configuration
        if validator == ComponentRef("delimited-table", 1):
            return self._inspect_delimited(path, declaration)
        if validator == ComponentRef("json-document", 1):
            return self._inspect_json(path, declaration)
        if validator == ComponentRef("opaque-bytes", 1):
            if configuration:
                raise ValueError("opaque-bytes validator does not accept configuration")
            return None, ()
        binding = self._inspectors.get(declaration.ref.canonical)
        if binding is None or binding[0] != validator:
            raise ValueError("artifact schema has no matching registered validator")
        inspected = binding[1](path, configuration)
        if (
            not isinstance(inspected, tuple)
            or len(inspected) != 2
            or (
                inspected[0] is not None
                and (
                    isinstance(inspected[0], bool)
                    or not isinstance(inspected[0], int)
                    or inspected[0] < 0
                )
            )
            or not isinstance(inspected[1], tuple)
            or any(
                isinstance(value, bool) or not isinstance(value, int) or value < 0
                for value in inspected[1]
            )
        ):
            raise ValueError("artifact inspector returned an invalid summary")
        return inspected

    @staticmethod
    def _inspect_delimited(
        path: Path,
        declaration: ArtifactSchema,
    ) -> tuple[int, tuple[int, ...]]:
        import csv

        if declaration.media_type not in {"text/csv", "text/tab-separated-values"}:
            raise ValueError("delimited-table validator requires CSV or TSV media type")
        if declaration.encoding != "utf-8":
            raise ValueError("delimited-table@1 requires utf-8 encoding")
        configuration = dict(declaration.validator_configuration)
        unknown = set(configuration) - {"columns", "required_columns"}
        if unknown:
            raise ValueError("delimited-table validator configuration has unknown fields")
        columns = configuration.get("columns")
        required = configuration.get("required_columns", ())
        if columns is not None and not isinstance(columns, (list, tuple)):
            raise ValueError("delimited-table columns must be an array")
        if not isinstance(required, (list, tuple)):
            raise ValueError("delimited-table required_columns must be an array")
        expected_columns = tuple(columns) if columns is not None else None
        required_columns = tuple(required)
        for values, field_name in (
            (expected_columns or (), "columns"),
            (required_columns, "required_columns"),
        ):
            if (
                any(not isinstance(value, str) or not value for value in values)
                or len(values) != len(set(values))
            ):
                raise ValueError(f"delimited-table {field_name} must be unique strings")
        delimiter = "\t" if declaration.media_type == "text/tab-separated-values" else ","
        try:
            with path.open("r", encoding="utf-8", newline="") as source_file:
                reader = csv.reader(source_file, delimiter=delimiter)
                try:
                    header = tuple(next(reader))
                except StopIteration as error:
                    raise ValueError("delimited-table artifact must contain a header") from error
                if not header or any(not value for value in header) or len(header) != len(set(header)):
                    raise ValueError("delimited-table artifact has an invalid header")
                if expected_columns is not None and header != expected_columns:
                    raise ValueError("delimited-table artifact header does not match its schema")
                if not set(required_columns).issubset(header):
                    raise ValueError("delimited-table artifact is missing required columns")
                count = 0
                for row in reader:
                    if len(row) != len(header):
                        raise ValueError("delimited-table artifact has an inconsistent row width")
                    count += 1
                    if declaration.max_records is not None and count > declaration.max_records:
                        raise ValueError("sealed artifact exceeds its declared record limit")
        except (UnicodeError, csv.Error) as error:
            raise ValueError("sealed tabular artifact is not valid bounded text") from error
        return count, ()

    @staticmethod
    def _inspect_json(
        path: Path,
        declaration: ArtifactSchema,
    ) -> tuple[int | None, tuple[int, ...]]:
        import json

        if not (
            declaration.media_type == "application/json"
            or declaration.media_type.endswith("+json")
        ):
            raise ValueError("json-document validator requires a JSON media type")
        if declaration.encoding != "utf-8":
            raise ValueError("json-document@1 requires utf-8 encoding")
        configuration = dict(declaration.validator_configuration)
        if set(configuration) - {"top_level"}:
            raise ValueError("json-document validator configuration has unknown fields")
        top_level = configuration.get("top_level", "any")
        if top_level not in {"any", "array", "object"}:
            raise ValueError("json-document top_level is unsupported")
        try:
            with path.open("r", encoding="utf-8") as source_file:
                value = json.load(
                    source_file,
                    parse_constant=lambda _value: (_ for _ in ()).throw(
                        ValueError("non-finite JSON number")
                    ),
                )
        except (UnicodeError, json.JSONDecodeError, ValueError, RecursionError) as error:
            raise ValueError("sealed JSON artifact is not a valid bounded document") from error
        if top_level == "array" and not isinstance(value, list):
            raise ValueError("JSON artifact must contain a top-level array")
        if top_level == "object" and not isinstance(value, dict):
            raise ValueError("JSON artifact must contain a top-level object")

        def dimensions(current: object, depth: int = 0) -> tuple[int, ...]:
            if depth > 8 or not isinstance(current, list):
                return ()
            if not current:
                return (0,)
            children = tuple(dimensions(child, depth + 1) for child in current)
            if len(set(children)) != 1:
                raise ValueError("JSON array dimensions must be rectangular")
            return (len(current),) + children[0]

        measured_dimensions = dimensions(value) if declaration.max_dimensions else ()
        measured_records = len(value) if isinstance(value, list) else 1
        return measured_records, measured_dimensions

    def release(self, artifact: ArtifactRef) -> bool:
        """Release one seal reference and remove an unreferenced local blob."""
        if not isinstance(artifact, ArtifactRef):
            raise ValueError("artifact must be an ArtifactRef")
        with self._lock:
            if self._references.get(artifact.artifact_id) != artifact:
                return False
            remaining = self._refcounts[artifact.artifact_id] - 1
            if remaining > 0:
                self._refcounts[artifact.artifact_id] = remaining
                return True
            path = self._paths.pop(artifact.artifact_id)
            self._references.pop(artifact.artifact_id, None)
            self._refcounts.pop(artifact.artifact_id, None)
            path.chmod(0o600)
            path.unlink()
            return True

    def import_file(
        self,
        path: Path,
        *,
        declaration: ArtifactSchema,
        records: int | None = None,
        dimensions: tuple[int, ...] = (),
    ) -> ArtifactRef:
        return self.seal(
            path,
            declaration=declaration,
            records=records,
            dimensions=dimensions,
        )

    def materialize(self, artifact: ArtifactRef) -> Path:
        self.require(artifact)
        with self._lock:
            return self._paths[artifact.artifact_id]

    def require(self, artifact: ArtifactRef) -> None:
        if not isinstance(artifact, ArtifactRef):
            raise ValueError("artifact must be an ArtifactRef")
        with self._lock:
            try:
                path = self._paths[artifact.artifact_id]
                stored = self._references[artifact.artifact_id]
            except KeyError as error:
                raise ValueError("artifact is not present in the local store") from error
        if stored != artifact:
            raise ValueError("artifact metadata does not match the sealed local reference")
        if (
            path.is_symlink()
            or not path.is_file()
            or path.stat().st_size != artifact.size_bytes
            or _sha256_file(path) != artifact.sha256
        ):
            raise ValueError("local artifact checksum mismatch")


class LocalArtifactTransaction:
    """Track store references until one local Job is fully accepted."""

    def __init__(self, store: LocalArtifactStore) -> None:
        self._store = store
        self._references: list[ArtifactRef] = []
        self._closed = False
        self._lock = Lock()

    def track(self, artifact: ArtifactRef) -> None:
        with self._lock:
            if self._closed:
                raise ValueError("artifact transaction is already closed")
            self._references.append(artifact)

    def commit(self) -> None:
        with self._lock:
            if self._closed:
                raise ValueError("artifact transaction is already closed")
            self._closed = True
            self._references.clear()

    def rollback(self) -> None:
        with self._lock:
            if self._closed:
                return
            references = tuple(reversed(self._references))
            self._references.clear()
            self._closed = True
        for artifact in references:
            self._store.release(artifact)


class ScopedArtifactSink:
    """Restrict a local planning/attempt sink to one workspace tree."""

    def __init__(
        self,
        store: LocalArtifactStore,
        workspace: Path,
        *,
        max_artifacts: int = 100_000,
        max_bytes: int = 1 << 50,
        transaction: LocalArtifactTransaction | None = None,
    ) -> None:
        self._store = store
        self._workspace = workspace.resolve()
        self._workspace.mkdir(parents=True, exist_ok=True)
        self._max_artifacts = require_positive_int(max_artifacts, "sink.max_artifacts")
        self._max_bytes = require_positive_int(max_bytes, "sink.max_bytes")
        self._sealed: dict[str, ArtifactRef] = {}
        self._sealed_bytes = 0
        self._transaction = transaction
        self._lock = Lock()

    @property
    def sealed_references(self) -> tuple[ArtifactRef, ...]:
        with self._lock:
            return tuple(self._sealed[key] for key in sorted(self._sealed))

    def seal(
        self,
        path: Path,
        *,
        declaration: ArtifactSchema,
        records: int | None = None,
        dimensions: tuple[int, ...] = (),
    ) -> ArtifactRef:
        candidate = path if path.is_absolute() else self._workspace / path
        if not isinstance(declaration, ArtifactSchema):
            raise ValueError("artifact declaration must be an ArtifactSchema")
        lexical = Path(os.path.abspath(candidate))
        try:
            lexical_relative = lexical.relative_to(self._workspace)
        except ValueError as error:
            raise ValueError("attempt artifact must remain inside its workspace") from error
        if not lexical_relative.parts:
            raise ValueError("attempt artifact must name a file inside its workspace")
        directory_flags = (
            os.O_RDONLY
            | getattr(os, "O_DIRECTORY", 0)
            | getattr(os, "O_NOFOLLOW", 0)
        )
        opened_directories: list[int] = []
        file_descriptor = -1
        try:
            current_fd = os.open(self._workspace, directory_flags)
            opened_directories.append(current_fd)
            for component in lexical_relative.parts[:-1]:
                current_fd = os.open(
                    component,
                    directory_flags,
                    dir_fd=current_fd,
                )
                opened_directories.append(current_fd)
            file_descriptor = os.open(
                lexical_relative.parts[-1],
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0),
                dir_fd=current_fd,
            )
            candidate_size = os.fstat(file_descriptor).st_size
        except OSError as error:
            if file_descriptor >= 0:
                os.close(file_descriptor)
            for directory_fd in reversed(opened_directories):
                os.close(directory_fd)
            raise ValueError(
                "attempt artifact path must contain only real workspace directories"
            ) from error
        try:
            with self._lock:
                if len(self._sealed) >= self._max_artifacts:
                    raise ValueError("attempt artifact count exceeds its sink limit")
                if self._sealed_bytes + candidate_size > self._max_bytes:
                    raise ValueError("attempt artifact bytes exceed their sink limit")
                if candidate_size > declaration.max_bytes:
                    raise ValueError("attempt artifact exceeds its schema byte limit")
                reference = self._store.seal_descriptor(
                    file_descriptor,
                    declaration=declaration,
                    records=records,
                    dimensions=dimensions,
                )
                existing = self._sealed.get(reference.artifact_id)
                if existing is not None and existing != reference:
                    self._store.release(reference)
                    raise ValueError("attempt sealed conflicting metadata for one artifact")
                if existing is None:
                    if self._sealed_bytes + reference.size_bytes > self._max_bytes:
                        self._store.release(reference)
                        raise ValueError("attempt artifact bytes exceed their sink limit")
                    self._sealed[reference.artifact_id] = reference
                    self._sealed_bytes += reference.size_bytes
                    if self._transaction is not None:
                        self._transaction.track(reference)
                else:
                    # ``seal_descriptor`` acquired another store reference for
                    # identical content; one attempt owns only one reference.
                    self._store.release(reference)
                return reference
        finally:
            if file_descriptor >= 0:
                os.close(file_descriptor)
            for directory_fd in reversed(opened_directories):
                os.close(directory_fd)


class ScopedArtifactCatalog:
    """Materialize verified, read-only copies inside one attempt workspace."""

    def __init__(
        self,
        store: LocalArtifactStore,
        workspace: Path,
        allowed_artifacts: tuple[ArtifactRef, ...],
    ) -> None:
        self.__store = store
        allowed: dict[str, ArtifactRef] = {}
        for artifact in allowed_artifacts:
            if not isinstance(artifact, ArtifactRef):
                raise ValueError("catalog allowlist must contain ArtifactRef values")
            existing = allowed.get(artifact.artifact_id)
            if existing is not None and existing != artifact:
                raise ValueError("catalog allowlist contains conflicting artifact metadata")
            allowed[artifact.artifact_id] = artifact
        self.__allowed = allowed
        resolved_workspace = workspace.resolve()
        input_root = resolved_workspace / "inputs"
        if input_root.is_symlink():
            raise ValueError("attempt input directory must not be a symbolic link")
        self.__input_root = input_root
        self.__input_root.mkdir(parents=True, exist_ok=True)

    def materialize(self, artifact: ArtifactRef) -> Path:
        if self.__allowed.get(artifact.artifact_id) != artifact:
            raise ValueError("artifact is outside this context's input allowlist")
        source = self.__store.materialize(artifact)
        destination = self.__input_root / artifact.artifact_id
        if destination.is_symlink():
            raise ValueError("attempt input destination must not be a symbolic link")
        if destination.exists():
            if (
                not destination.is_file()
                or destination.stat().st_size != artifact.size_bytes
                or _sha256_file(destination) != artifact.sha256
            ):
                raise ValueError("existing attempt input does not match its artifact")
        else:
            temporary_fd, temporary_name = tempfile.mkstemp(
                prefix=".input-",
                dir=self.__input_root,
            )
            os.close(temporary_fd)
            temporary = Path(temporary_name)
            try:
                shutil.copyfile(source, temporary)
                if (
                    temporary.stat().st_size != artifact.size_bytes
                    or _sha256_file(temporary) != artifact.sha256
                ):
                    raise ValueError("copied attempt input does not match its artifact")
                temporary.chmod(0o444)
                os.replace(temporary, destination)
            finally:
                if temporary.exists():
                    temporary.unlink()
            if (
                destination.stat().st_size != artifact.size_bytes
                or _sha256_file(destination) != artifact.sha256
            ):
                raise ValueError("copied attempt input does not match its artifact")
        destination.chmod(0o444)
        return destination


class CancellationFlag:
    def __init__(self) -> None:
        self._event = Event()

    def cancel(self) -> None:
        self._event.set()

    def cancelled(self) -> bool:
        return self._event.is_set()

    def raise_if_cancelled(self) -> None:
        if self.cancelled():
            raise RuntimeError("task-cancelled")


@dataclass(frozen=True, slots=True)
class LocalPlanningContext:
    catalog: ArtifactCatalog
    sink: ArtifactSink
    workspace: Path
    allowed_artifacts: tuple[ArtifactRef, ...] = ()
    max_artifacts: int = 100_000
    max_bytes: int = 1 << 50
    transaction: LocalArtifactTransaction | None = None

    def __post_init__(self) -> None:
        workspace = self.workspace.resolve()
        object.__setattr__(self, "workspace", workspace)
        allowed_artifacts = tuple(self.allowed_artifacts)
        if any(not isinstance(value, ArtifactRef) for value in allowed_artifacts):
            raise ValueError("allowed_artifacts must contain ArtifactRef values")
        object.__setattr__(self, "allowed_artifacts", allowed_artifacts)
        object.__setattr__(self, "max_artifacts", require_positive_int(self.max_artifacts, "max_artifacts"))
        object.__setattr__(self, "max_bytes", require_positive_int(self.max_bytes, "max_bytes"))
        if isinstance(self.catalog, LocalArtifactStore):
            object.__setattr__(
                self,
                "catalog",
                ScopedArtifactCatalog(self.catalog, workspace, allowed_artifacts),
            )
        if isinstance(self.sink, LocalArtifactStore):
            object.__setattr__(
                self,
                "sink",
                ScopedArtifactSink(
                    self.sink,
                    workspace,
                    max_artifacts=self.max_artifacts,
                    max_bytes=self.max_bytes,
                    transaction=self.transaction,
                ),
            )


@dataclass(frozen=True, slots=True)
class LocalTaskContext:
    task: TaskSpec
    catalog: ArtifactCatalog
    sink: ArtifactSink
    workspace: Path
    cancellation: CancellationFlag
    provenance: Provenance
    accepted_inputs: Mapping[str, ArtifactCollection]
    max_artifacts: int = 100_000
    max_bytes: int = 1 << 50
    transaction: LocalArtifactTransaction | None = None

    def __post_init__(self) -> None:
        workspace = self.workspace.resolve()
        object.__setattr__(self, "workspace", workspace)
        object.__setattr__(self, "max_artifacts", require_positive_int(self.max_artifacts, "max_artifacts"))
        object.__setattr__(self, "max_bytes", require_positive_int(self.max_bytes, "max_bytes"))
        if isinstance(self.catalog, LocalArtifactStore):
            allowed_artifacts = tuple(
                item.artifact
                for collection in self.task.inputs.values()
                for item in collection.items
            )
            object.__setattr__(
                self,
                "catalog",
                ScopedArtifactCatalog(self.catalog, workspace, allowed_artifacts),
            )
        if isinstance(self.sink, LocalArtifactStore):
            object.__setattr__(
                self,
                "sink",
                ScopedArtifactSink(
                    self.sink,
                    workspace,
                    max_artifacts=self.max_artifacts,
                    max_bytes=self.max_bytes,
                    transaction=self.transaction,
                ),
            )


def assert_manifest_round_trip(manifest: WorkloadManifest) -> None:
    """Assert strict canonical serialization and reconstructive equality."""
    reconstructed = WorkloadManifest.from_json(manifest.to_json())
    if reconstructed != manifest or reconstructed.to_json() != manifest.to_json():
        raise AssertionError("manifest canonical round-trip changed its value")


def _input_digest(inputs: Mapping[str, ArtifactCollection]) -> str:
    value = {name: collection.digest for name, collection in sorted(inputs.items())}
    return hashlib.sha256(canonical_json(value).encode("utf-8")).hexdigest()


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _provenance(
    definition: WorkloadDefinition,
    runtime: RuntimeCapabilities,
    task: TaskSpec,
    allocation: ResourceAllocation,
    started_at: str,
    job_id: str,
    task_id: str,
) -> Provenance:
    parameters_digest = hashlib.sha256(canonical_json(task.parameters).encode("utf-8")).hexdigest()
    return Provenance(
        workload=definition.manifest.workload,
        sdk_api_version=task.sdk_api_version,
        protocol_version=task.protocol_version,
        manifest_schema_version=task.manifest_schema_version,
        workflow_schema_version=task.workflow_schema_version,
        verifier=task.verifier,
        artifact_schemas=tuple(
            sorted(
                {
                    item.artifact.schema
                    for collection in task.inputs.values()
                    for item in collection.items
                }.union(
                    port.schema.ref for port in task.expected_outputs.values()
                ),
                key=lambda value: value.canonical,
            )
        ),
        package_digest=task.package_digest,
        manifest_digest=task.manifest_digest,
        environment_digest=task.environment_digest,
        worker_runtime={"kind": "local-conformance", "sdk_api": SDK_API_VERSION},
        allocated_resource_ids=(allocation.allocation_id,) + allocation.accelerator_ids,
        parameters_digest=parameters_digest,
        input_collection_digest=_input_digest(task.inputs),
        execution_contract_digest=hashlib.sha256(task.to_json().encode("utf-8")).hexdigest(),
        selected_features=task.selected_features,
        optional_fallbacks=task.optional_fallbacks,
        job_id=job_id,
        task_id=task_id,
        started_at=started_at,
        finished_at=started_at,
        trust_mode=task.trust_mode.value,
    )


def _verification_binding(manifest: OutputManifest) -> VerificationBinding:
    provenance = manifest.provenance
    return VerificationBinding(
        workload=provenance.workload,
        task_key=manifest.task_key,
        package_digest=provenance.package_digest,
        manifest_digest=provenance.manifest_digest,
        environment_digest=provenance.environment_digest,
        parameters_digest=provenance.parameters_digest,
        input_collection_digest=provenance.input_collection_digest,
        execution_contract_digest=provenance.execution_contract_digest,
        selected_features=provenance.selected_features,
        optional_fallbacks=provenance.optional_fallbacks,
        job_id=provenance.job_id,
        task_id=provenance.task_id,
        verifier=provenance.verifier,
        sdk_api_version=provenance.sdk_api_version,
        protocol_version=provenance.protocol_version,
        manifest_schema_version=provenance.manifest_schema_version,
        workflow_schema_version=provenance.workflow_schema_version,
        artifact_schemas=provenance.artifact_schemas,
        trust_mode=provenance.trust_mode,
    )


class LocalCoreBatchExecutor:
    """Trusted in-process correctness runtime for the static map/reduce profile.

    This executor deliberately accepts only profiles that declare trusted host
    execution.  It is useful for SDK conformance and scientific parity tests;
    it is not a process, network, credential, lease, or timeout isolation
    boundary.
    """

    def __init__(
        self,
        registry: WorkloadRegistry,
        runtime: RuntimeCapabilities,
        artifact_store: LocalArtifactStore,
        work_root: Path,
    ) -> None:
        self.registry = registry
        self.runtime = runtime
        self.artifact_store = artifact_store
        self.work_root = work_root.resolve()
        self.work_root.mkdir(parents=True, exist_ok=True)
        self.resources = ResourcePool(runtime.inventory, max_concurrency=1)

    @staticmethod
    def _assert_supported_profile(request: JobRequest, definition: WorkloadDefinition) -> None:
        if request.trust_mode is not TrustMode.TRUSTED:
            raise ValueError("local conformance execution supports only trusted workloads")
        workflow = definition.manifest.workflow
        if workflow.failure_policy is not WorkflowFailurePolicy.FAIL_FAST:
            raise ValueError("local conformance execution supports only fail-fast workflows")
        for stage in workflow.stages:
            if stage.kind not in {StageKind.MAP, StageKind.REDUCE}:
                raise ValueError("local conformance execution does not implement advanced stages")
            execution = stage.execution
            if (
                execution.process_model is not ProcessModel.SINGLE
                or execution.max_processes != 1
                or execution.threads_per_process != 1
                or execution.native_threads != 1
                or execution.nested_parallelism
            ):
                raise ValueError("local conformance execution supports one non-nested host thread")
            if execution.network is not NetworkPolicy.TRUSTED:
                raise ValueError(
                    "local conformance execution cannot enforce a restricted network policy"
                )
            if (
                execution.checkpoint.enabled
                or execution.allowed_egress
                or execution.secret_handles
                or stage.gang is not None
                or stage.resources.accelerator_count
            ):
                raise ValueError("local conformance execution cannot enforce this stage profile")
            if stage.retry.max_attempts != 1:
                raise ValueError("local conformance execution does not implement retries")
        reducers = tuple(stage for stage in workflow.stages if stage.kind is StageKind.REDUCE)
        if len(reducers) == 1:
            reducer = reducers[0]
            if (
                set(workflow.outputs) != set(reducer.outputs)
                or any(
                    external_name != reference.port
                    or reference.stage_id != reducer.stage_id
                    for external_name, reference in workflow.outputs.items()
                )
            ):
                raise ValueError(
                    "local conformance execution requires identity-mapped reducer outputs"
                )

    def _track_artifacts(
        self,
        collections: Mapping[str, ArtifactCollection],
        *,
        known: dict[str, ArtifactRef],
        max_artifacts: int,
        output_ids: set[str] | None = None,
    ) -> int:
        added_output_bytes = 0
        for collection in collections.values():
            for item in collection.items:
                artifact = item.artifact
                self.artifact_store.require(artifact)
                existing = known.get(artifact.artifact_id)
                if existing is not None and existing != artifact:
                    raise ValueError("one artifact ID carries conflicting metadata")
                known[artifact.artifact_id] = artifact
                if len(known) > max_artifacts:
                    raise ValueError("job exceeds the manifest artifact limit")
                if output_ids is not None and artifact.artifact_id not in output_ids:
                    output_ids.add(artifact.artifact_id)
                    added_output_bytes += artifact.size_bytes
        return added_output_bytes

    def _run_task(
        self,
        definition: WorkloadDefinition,
        task: TaskSpec,
        workspace: Path,
        operation: Callable[[LocalTaskContext], OutputManifest],
        *,
        job_id: str,
        transaction: LocalArtifactTransaction,
        max_artifacts: int,
        max_output_bytes: int,
    ) -> OutputManifest:
        task_id = str(uuid4())
        allocation = self.resources.reserve(task_id, task.resources)
        try:
            started_at = _utc_now()
            stage = next(
                stage
                for stage in definition.manifest.workflow.stages
                if stage.stage_id == task.stage_id
            )
            if stage.verifier is None:
                raise ValueError("local task stage has no declared acceptance verifier")
            if (
                task.workload != definition.manifest.workload
                or task.package_digest != definition.manifest.package.digest
                or task.manifest_digest != definition.manifest.digest
                or task.sdk_api_version != self.runtime.sdk_api_version
                or task.protocol_version != self.runtime.protocol_version
                or task.manifest_schema_version
                != definition.manifest.manifest_schema_version
                or task.workflow_schema_version != definition.manifest.workflow.schema_version
                or task.environment_digest != definition.manifest.environment.digest
                or task.verifier != stage.verifier
            ):
                raise ValueError("task resolved pins do not match the selected runtime and manifest")
            provenance = _provenance(
                definition,
                self.runtime,
                task,
                allocation,
                started_at,
                job_id,
                task_id,
            )
            context = LocalTaskContext(
                task,
                self.artifact_store,
                self.artifact_store,
                workspace,
                CancellationFlag(),
                provenance,
                task.inputs,
                max_artifacts,
                max_output_bytes,
                transaction,
            )
            manifest = operation(context)
            if not isinstance(manifest, OutputManifest):
                raise ValueError("workload handler must return an OutputManifest")
            if manifest.task_key != task.task_key:
                raise ValueError("handler output task_key does not match its trusted task")
            if manifest.provenance != provenance:
                raise ValueError("handler output provenance does not match its trusted context")
            manifest.validate_against(
                task.expected_outputs,
                max_output_bytes=max_output_bytes,
            )
            if not isinstance(context.sink, ScopedArtifactSink):
                raise ValueError("local execution requires a scoped artifact sink")
            declared = {
                item.artifact.artifact_id: item.artifact
                for collection in manifest.outputs.values()
                for item in collection.items
            }
            issued = {
                artifact.artifact_id: artifact
                for artifact in context.sink.sealed_references
            }
            if issued != declared:
                raise ValueError(
                    "handler outputs must declare exactly the artifacts sealed by its attempt"
                )
            for collection in manifest.outputs.values():
                for item in collection.items:
                    self.artifact_store.require(item.artifact)
            finished = replace(provenance, finished_at=_utc_now())
            completed = replace(manifest, provenance=finished)
            self._verify_output(
                definition,
                stage.verifier,
                completed,
                task.expected_outputs,
                max_output_bytes,
            )
            return completed
        finally:
            self.resources.release(allocation.allocation_id)

    @staticmethod
    def _verify_output(
        definition: WorkloadDefinition,
        verifier_ref: ComponentRef,
        output: OutputManifest,
        expected_outputs: Mapping[str, object],
        max_output_bytes: int,
    ) -> None:
        verifier = definition.verifiers[verifier_ref.canonical]
        decision = verifier.verify(
            VerifyContext(
                expected_outputs,  # type: ignore[arg-type]
                max_output_bytes,
                binding=_verification_binding(output),
                trust_mode=output.provenance.trust_mode,
            ),
            CandidateOutputs((output,)),
        )
        if not isinstance(decision, VerificationDecision):
            raise ValueError("declared verifier must return a VerificationDecision")
        if decision.verifier != verifier_ref:
            raise ValueError("verification decision identity does not match the declared verifier")
        if decision.status is not VerificationStatus.ACCEPTED:
            raise ValueError("task output did not pass its declared verifier")

    def execute(self, request: JobRequest, package_digest: str) -> OutputManifest:
        run_root = self.work_root / f"run-{uuid4()}"
        run_root.mkdir(parents=False, exist_ok=False)
        transaction = LocalArtifactTransaction(self.artifact_store)
        try:
            result = self._execute_run(
                request,
                package_digest,
                run_root,
                transaction,
            )
            shutil.rmtree(run_root, ignore_errors=False)
        except BaseException:
            transaction.rollback()
            if run_root.exists():
                shutil.rmtree(run_root, ignore_errors=True)
            raise
        transaction.commit()
        return result

    def _execute_run(
        self,
        request: JobRequest,
        package_digest: str,
        run_root: Path,
        transaction: LocalArtifactTransaction,
    ) -> OutputManifest:
        definition, _ = self.registry.require(
            request.workload.name,
            request.workload.version,
            package_digest,
            runtime=self.runtime,
        )
        self._assert_supported_profile(request, definition)
        workflow = definition.manifest.workflow
        map_stages = [stage for stage in workflow.stages if stage.kind is StageKind.MAP]
        reduce_stages = [stage for stage in workflow.stages if stage.kind is StageKind.REDUCE]
        unsupported = [
            stage for stage in workflow.stages
            if stage.kind not in {StageKind.MAP, StageKind.REDUCE}
        ]
        if len(map_stages) != 1 or len(reduce_stages) != 1 or unsupported:
            raise ValueError("local core-batch executor supports one static map stage and one reducer")
        limits = definition.manifest.limits
        output_limit = min(limits.max_output_bytes, workflow.max_output_bytes)
        job_id = str(uuid4())
        known_artifacts: dict[str, ArtifactRef] = {}
        output_artifact_ids: set[str] = set()
        output_bytes = 0
        self._track_artifacts(
            request.inputs,
            known=known_artifacts,
            max_artifacts=limits.max_artifacts,
        )
        planning = LocalPlanningContext(
            self.artifact_store,
            self.artifact_store,
            run_root / "planning",
            allowed_artifacts=tuple(
                item.artifact
                for collection in request.inputs.values()
                for item in collection.items
            ),
            max_artifacts=limits.max_artifacts,
            max_bytes=limits.max_input_bytes,
            transaction=transaction,
        )
        plan = self.registry.plan(request, package_digest, self.runtime, planning)
        if len(plan.tasks) + 1 > min(workflow.max_tasks, limits.max_tasks):
            raise ValueError("core map/reduce execution exceeds the total task limit")
        if not isinstance(planning.sink, ScopedArtifactSink):
            raise ValueError("local planning requires a scoped artifact sink")
        planned_references = {
            item.artifact.artifact_id: item.artifact
            for task in plan.tasks
            for collection in task.inputs.values()
            for item in collection.items
        }
        for issued in planning.sink.sealed_references:
            if planned_references.get(issued.artifact_id) != issued:
                raise ValueError("planner sealed an artifact that is not referenced by its plan")
        authorized_plan_inputs = {
            item.artifact.artifact_id: item.artifact
            for collection in request.inputs.values()
            for item in collection.items
        }
        authorized_plan_inputs.update(
            {
                artifact.artifact_id: artifact
                for artifact in planning.sink.sealed_references
            }
        )
        for artifact_id, artifact in planned_references.items():
            if authorized_plan_inputs.get(artifact_id) != artifact:
                raise ValueError(
                    "workflow plan references an artifact outside job inputs and planning outputs"
                )
        map_stage = map_stages[0]
        reducer_stage = reduce_stages[0]
        if (
            len(workflow.stages) != 2
            or map_stage.needs
            or reducer_stage.needs != (map_stage.stage_id,)
            or any(
                edge.source.stage_id is not None
                for edge in workflow.edges
                if edge.target.stage_id == map_stage.stage_id
            )
            or any(
                edge.source.stage_id != map_stage.stage_id
                for edge in workflow.edges
                if edge.target.stage_id == reducer_stage.stage_id
            )
            or any(
                reference.stage_id != reducer_stage.stage_id
                for reference in workflow.outputs.values()
            )
        ):
            raise ValueError("local core-batch executor requires a canonical map-to-reduce DAG")
        runner = definition.runners[map_stage.entry_point]
        map_results: list[OutputManifest] = []
        for task_index, task in enumerate(plan.tasks):
            task.validate_stage(map_stage)
            self._track_artifacts(
                task.inputs,
                known=known_artifacts,
                max_artifacts=limits.max_artifacts,
            )
            manifest = self._run_task(
                definition,
                task,
                run_root / "tasks" / f"map-{task_index:08d}",
                runner.run,
                job_id=job_id,
                transaction=transaction,
                max_artifacts=limits.max_artifacts,
                max_output_bytes=output_limit - output_bytes,
            )
            output_bytes += self._track_artifacts(
                manifest.outputs,
                known=known_artifacts,
                max_artifacts=limits.max_artifacts,
                output_ids=output_artifact_ids,
            )
            if output_bytes > output_limit:
                raise ValueError("job exceeds the cumulative output byte limit")
            map_results.append(manifest)
        if len(map_results) != len(plan.tasks):
            raise ValueError("map execution did not produce exactly one accepted result per task")
        if len(map_stage.outputs) != 1 or len(reducer_stage.inputs) != 1:
            raise ValueError("core map/reduce adapter requires one map output and one reducer input")
        map_port = next(iter(map_stage.outputs))
        reducer_input_name = next(iter(reducer_stage.inputs))
        partial_items: list[ArtifactItem] = []
        for task, result in zip(plan.tasks, map_results):
            collection = result.outputs[map_port]
            if len(collection.items) != 1:
                raise ValueError("core map stage must produce exactly one partial per planned task")
            partial_items.append(
                ArtifactItem(
                    collection.items[0].artifact,
                    key=task.task_key.replace("/", "."),
                )
            )
        if len(partial_items) != len(plan.tasks):
            raise ValueError("core map stage must produce exactly one partial per planned task")
        accepted = ArtifactCollection(CollectionKind.KEYED, tuple(partial_items))
        reducer_task = TaskSpec(
            workload=plan.workload,
            package_digest=plan.package_digest,
            manifest_digest=plan.manifest_digest,
            trust_mode=plan.trust_mode,
            sdk_api_version=plan.sdk_api_version,
            protocol_version=plan.protocol_version,
            manifest_schema_version=plan.manifest_schema_version,
            workflow_schema_version=plan.workflow_schema_version,
            environment_digest=plan.environment_digest,
            verifier=reducer_stage.verifier,
            selected_features=plan.selected_features,
            optional_fallbacks=plan.optional_fallbacks,
            task_key="reduce/final",
            stage_id=reducer_stage.stage_id,
            parameters=plan.resolved_parameters,
            inputs={reducer_input_name: accepted},
            expected_outputs=reducer_stage.outputs,
            resources=reducer_stage.resources,
            execution=reducer_stage.execution,
            expected_input_keys={
                reducer_input_name: tuple(
                    item.key for item in accepted.items if item.key is not None
                )
            },
        ).validate_stage(reducer_stage)
        final = self._run_task(
            definition,
            reducer_task,
            run_root / "tasks" / "reduce-final",
            definition.reducers[reducer_stage.entry_point].reduce,
            job_id=job_id,
            transaction=transaction,
            max_artifacts=limits.max_artifacts,
            max_output_bytes=output_limit - output_bytes,
        )
        output_bytes += self._track_artifacts(
            final.outputs,
            known=known_artifacts,
            max_artifacts=limits.max_artifacts,
            output_ids=output_artifact_ids,
        )
        if output_bytes > output_limit:
            raise ValueError("job exceeds the cumulative output byte limit")
        final.validate_against(definition.manifest.outputs, max_output_bytes=output_limit)
        return final
