"""Security and version-pinning tests for the installed SDK registry."""

from __future__ import annotations

from dataclasses import replace
from importlib import metadata
from pathlib import Path
import py_compile
from types import SimpleNamespace

import pytest

from scimesh.sdk import (
    AllowedPackage,
    ArtifactCollection,
    ArtifactRef,
    CompatibilityError,
    FeatureRequirement,
    JobRequest,
    LocalArtifactStore,
    LocalPlanningContext,
    PackageSpec,
    TrustMode,
    VersionRange,
    WorkloadDefinition,
    WorkloadId,
    WorkloadRegistry,
    installed_distribution_digest,
)
from scimesh.sdk.schema import (
    ParameterValidationError,
    validate_parameter_instance,
    validate_schema_definition,
)
from scimesh.workloads.environment import current_scimesh_package_digest
from scimesh.workloads.library import default_sdk_runtime
from scimesh.workloads.search import similarity_search_sdk_definition


def _definition(
    *, version: str = "1.0.0", digest_character: str = "a"
) -> WorkloadDefinition:
    original = similarity_search_sdk_definition(shard_rows=2).definition()
    manifest = replace(
        original.manifest,
        workload=WorkloadId("similarity-search", version),
        package=PackageSpec("scimesh", "sha256:" + digest_character * 64),
    )
    return WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )


def test_registry_requires_an_explicit_enabled_version_and_digest() -> None:
    first = _definition(version="1.0.0", digest_character="a")
    second = _definition(version="2.0.0", digest_character="b")
    registry = WorkloadRegistry()
    registry.register(first, enabled=True)
    registry.register(second)

    resolved, _ = registry.require("similarity-search", "1.0.0", "sha256:" + "a" * 64)
    assert resolved is first
    with pytest.raises(ValueError, match="unknown workload version"):
        registry.require("similarity-search", "3.0.0", "sha256:" + "a" * 64)
    with pytest.raises(ValueError, match="not enabled"):
        registry.require("similarity-search", "2.0.0", "sha256:" + "b" * 64)
    with pytest.raises(ValueError, match="not enabled"):
        registry.require("similarity-search", "1.0.0", "sha256:" + "c" * 64)
    with pytest.raises(ValueError, match="already registered"):
        registry.register(first)

    registry.enable("similarity-search", "2.0.0", "sha256:" + "b" * 64)
    assert [item.workload.version for item in registry.descriptions()] == [
        "1.0.0",
        "2.0.0",
    ]


def test_compatibility_failure_occurs_before_planner_invocation(
    tmp_path: Path,
) -> None:
    original = similarity_search_sdk_definition(shard_rows=2).definition()

    class CountingPlanner:
        calls = 0
        entry_point = "tests.sdk_fixture:plan@v1"

        def validate(self, request):
            self.calls += 1
            return original.planner.validate(request)

        def plan(self, job, context):
            self.calls += 1
            return original.planner.plan(job, context)

    planner = CountingPlanner()
    definition = WorkloadDefinition(
        original.manifest,
        planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    input_port = definition.manifest.inputs["input"]
    artifact = ArtifactRef(
        "11111111-1111-4111-8111-111111111111",
        "a" * 64,
        input_port.schema.ref,
        input_port.schema.media_type,
        1,
    )
    request = JobRequest(
        definition.manifest.workload,
        {"query_smiles": "CCO"},
        {"input": ArtifactCollection.single(artifact)},
    )
    incompatible = replace(default_sdk_runtime(), protocol_version="2.0.0")
    store = LocalArtifactStore(tmp_path / "artifacts")

    with pytest.raises(CompatibilityError) as raised:
        registry.plan(
            request,
            definition.manifest.package.digest,
            incompatible,
            LocalPlanningContext(store, store, tmp_path / "plan"),
        )
    assert raised.value.code == "protocol-mismatch"
    assert planner.calls == 0


@pytest.mark.parametrize(
    ("request_changes", "error_code"),
    (
        ({"required_features": ("undeclared-feature",)}, "feature-undeclared"),
        ({"trust_mode": TrustMode.VERIFIED}, "trust-mode-undeclared"),
    ),
)
def test_job_selected_features_and_trust_mode_fail_closed_before_planning(
    tmp_path: Path,
    request_changes: dict[str, object],
    error_code: str,
) -> None:
    definition = similarity_search_sdk_definition(shard_rows=2).definition()
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    input_port = definition.manifest.inputs["input"]
    artifact = ArtifactRef(
        "11111111-1111-4111-8111-111111111111",
        "a" * 64,
        input_port.schema.ref,
        input_port.schema.media_type,
        1,
        records=1,
    )
    values: dict[str, object] = {
        "workload": definition.manifest.workload,
        "parameters": {"query_smiles": "CCO"},
        "inputs": {"input": ArtifactCollection.single(artifact)},
    }
    values.update(request_changes)
    request = JobRequest(**values)  # type: ignore[arg-type]
    store = LocalArtifactStore(tmp_path / "artifacts")

    with pytest.raises(CompatibilityError) as raised:
        registry.plan(
            request,
            definition.manifest.package.digest,
            default_sdk_runtime(),
            LocalPlanningContext(store, store, tmp_path / "plan"),
        )
    assert raised.value.code == error_code


class _EntryPoints(tuple):
    def select(self, *, group: str):
        assert group == WorkloadRegistry.ENTRY_POINT_GROUP
        return self


def test_discovery_imports_only_an_exact_allowlisted_installed_entry_point(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    definition = similarity_search_sdk_definition().definition()
    loaded: list[str] = []

    class EntryPoint:
        def __init__(self, name: str, distribution: str) -> None:
            self.name = name
            self.dist = (
                metadata.distribution("scimesh")
                if distribution == "scimesh"
                else SimpleNamespace(name=distribution)
            )
            self.value = "scimesh.workloads.search:similarity_search_sdk_definition"

        @property
        def module(self) -> str:
            return self.value.partition(":")[0]

        def load(self):
            loaded.append(self.name)
            return lambda: definition

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: _EntryPoints(
            (
                EntryPoint("evil-workload@1.0.0", "unapproved"),
                EntryPoint("similarity-search@1.0.0", "scimesh"),
            )
        ),
    )
    monkeypatch.setattr(
        "scimesh.sdk.registry.installed_distribution_digest",
        lambda _distribution: definition.manifest.package.digest,
    )
    registry = WorkloadRegistry()
    registry.discover_installed(
        (
            AllowedPackage(
                "scimesh",
                definition.manifest.workload,
                definition.manifest.package.digest,
            ),
        )
    )

    assert loaded == ["similarity-search@1.0.0"]
    assert registry.descriptions()[0].enabled


def test_discovery_measures_package_before_importing_entry_point(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    definition = similarity_search_sdk_definition().definition()
    loaded = False

    class EntryPoint:
        name = "similarity-search@1.0.0"
        dist = metadata.distribution("scimesh")
        value = "scimesh.workloads.search:similarity_search_sdk_definition"
        module = "scimesh.workloads.search"

        def load(self):
            nonlocal loaded
            loaded = True
            return lambda: definition

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: _EntryPoints((EntryPoint(),)),
    )
    monkeypatch.setattr(
        "scimesh.sdk.registry.installed_distribution_digest",
        lambda _distribution: "sha256:" + "f" * 64,
    )

    with pytest.raises(ValueError, match="content does not match"):
        WorkloadRegistry().discover_installed(
            (
                AllowedPackage(
                    "scimesh",
                    definition.manifest.workload,
                    definition.manifest.package.digest,
                ),
            )
        )
    assert loaded is False


def test_installed_digest_is_stable_when_python_generates_a_pycache(
    tmp_path: Path,
) -> None:
    package = tmp_path / "fixture_pkg"
    package.mkdir()
    source = package / "__init__.py"
    source.write_text("VALUE = 1\n", encoding="utf-8")

    class FixtureDistribution:
        name = "fixture-dist"
        files = (Path("fixture_pkg/__init__.py"),)
        entry_points = ()

        @staticmethod
        def read_text(name: str) -> str | None:
            return "fixture_pkg\n" if name == "top_level.txt" else None

        @staticmethod
        def locate_file(value: object) -> Path:
            return tmp_path / str(value)

    distribution = FixtureDistribution()
    before = installed_distribution_digest(distribution)  # type: ignore[arg-type]
    py_compile.compile(str(source), doraise=True)

    assert installed_distribution_digest(distribution) == before  # type: ignore[arg-type]


def test_discovery_rejects_entry_point_module_owned_by_another_distribution(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    owned = tmp_path / "owned_pkg"
    owned.mkdir()
    (owned / "__init__.py").write_text("", encoding="utf-8")
    loaded = False

    class Distribution:
        name = "allowed-dist"
        files = (Path("owned_pkg/__init__.py"),)
        entry_points = ()

        @staticmethod
        def read_text(name: str) -> str | None:
            return "owned_pkg\n" if name == "top_level.txt" else None

        @staticmethod
        def locate_file(value: object) -> Path:
            return tmp_path / str(value)

    class EntryPoint:
        name = "similarity-search@1.0.0"
        dist = Distribution()
        value = "foreign_pkg.workload:factory"
        module = "foreign_pkg.workload"

        def load(self):
            nonlocal loaded
            loaded = True
            raise AssertionError("foreign entry point must not load")

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: _EntryPoints((EntryPoint(),)),
    )
    definition = similarity_search_sdk_definition().definition()
    with pytest.raises(ValueError, match="outside its distribution"):
        WorkloadRegistry().discover_installed(
            (
                AllowedPackage(
                    "allowed-dist",
                    definition.manifest.workload,
                    "sha256:" + "a" * 64,
                ),
            )
        )
    assert loaded is False


def test_missing_allowlisted_entry_point_fails_without_loading_or_registering(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    loaded: list[str] = []

    class EntryPoint:
        name = "job-selected-module@1.0.0"
        dist = SimpleNamespace(name="unapproved")

        def load(self):
            loaded.append(self.name)
            raise AssertionError("unapproved entry point must not load")

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: _EntryPoints((EntryPoint(),)),
    )
    registry = WorkloadRegistry()
    with pytest.raises(ValueError, match="were not installed"):
        registry.discover_installed(
            (
                AllowedPackage(
                    "scimesh",
                    WorkloadId("similarity-search", "1.0.0"),
                    current_scimesh_package_digest(),
                ),
            )
        )
    assert loaded == []
    assert registry.descriptions() == ()


def test_parameter_schema_accepts_finite_big_integer_bounds() -> None:
    bound = 10**400
    schema = {"type": "integer", "minimum": -bound, "maximum": bound}

    validate_schema_definition(schema)
    validate_parameter_instance(bound, schema)

    with pytest.raises(ParameterValidationError, match="violates maximum"):
        validate_parameter_instance(bound + 1, schema)


def test_job_parameters_reject_unbounded_json_integers_early() -> None:
    with pytest.raises(ValueError, match="4096-bit JSON bound"):
        JobRequest(
            WorkloadId("similarity-search", "1.0.0"),
            {"value": 10**2_000},
            {},
        )


@pytest.mark.parametrize(
    ("value", "multiple", "accepted"),
    [
        (3 * 10**400, 3, True),
        (10**400, 3, False),
        (10**400, 0.1, True),
        (0.3, 0.1, True),
        (0.31, 0.1, False),
    ],
)
def test_parameter_schema_multiple_of_is_exact_without_float_overflow(
    value: int | float,
    multiple: int | float,
    accepted: bool,
) -> None:
    schema = {"type": "number", "multipleOf": multiple}
    validate_schema_definition(schema)

    if accepted:
        validate_parameter_instance(value, schema)
    else:
        with pytest.raises(ParameterValidationError, match="violates multipleOf"):
            validate_parameter_instance(value, schema)


def test_parameter_schema_equality_uses_json_types() -> None:
    validate_schema_definition({"enum": [True, 1]})
    with pytest.raises(ValueError, match="enum values must be unique"):
        validate_schema_definition({"enum": [1, 1.0]})

    validate_parameter_instance(True, {"enum": [True]})
    with pytest.raises(ParameterValidationError, match="outside enum"):
        validate_parameter_instance(1, {"enum": [True]})
    validate_parameter_instance(1.0, {"enum": [1]})

    validate_parameter_instance({"enabled": True}, {"const": {"enabled": True}})
    with pytest.raises(ParameterValidationError, match="does not match const"):
        validate_parameter_instance({"enabled": 1}, {"const": {"enabled": True}})

    unique = {"type": "array", "uniqueItems": True}
    validate_parameter_instance([True, 1, {"enabled": True}, {"enabled": 1}], unique)
    with pytest.raises(ParameterValidationError, match="items must be unique"):
        validate_parameter_instance([1, 1.0], unique)


def test_disabled_workload_is_not_resolvable_until_re_enabled() -> None:
    definition = _definition()
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    resolved, _ = registry.require("similarity-search", "1.0.0", "sha256:" + "a" * 64)
    assert resolved is definition

    registry.disable("similarity-search", "1.0.0", "sha256:" + "a" * 64)
    with pytest.raises(ValueError, match="not enabled"):
        registry.require("similarity-search", "1.0.0", "sha256:" + "a" * 64)

    registry.enable("similarity-search", "1.0.0", "sha256:" + "a" * 64)
    resolved, _ = registry.require("similarity-search", "1.0.0", "sha256:" + "a" * 64)
    assert resolved is definition


def test_discovery_rechecks_the_package_digest_after_loading(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    definition = similarity_search_sdk_definition().definition()
    digests = iter((definition.manifest.package.digest, "sha256:" + "e" * 64))

    class EntryPoint:
        name = "similarity-search@1.0.0"
        dist = metadata.distribution("scimesh")
        value = "scimesh.workloads.search:similarity_search_sdk_definition"
        module = "scimesh.workloads.search"

        def load(self):
            return lambda: definition

    monkeypatch.setattr(
        "scimesh.sdk.registry.metadata.entry_points",
        lambda: _EntryPoints((EntryPoint(),)),
    )
    monkeypatch.setattr(
        "scimesh.sdk.registry.installed_distribution_digest",
        lambda _distribution: next(digests),
    )

    registry = WorkloadRegistry()
    with pytest.raises(ValueError, match="changed while loading"):
        registry.discover_installed(
            (
                AllowedPackage(
                    "scimesh",
                    definition.manifest.workload,
                    definition.manifest.package.digest,
                ),
            )
        )
    assert registry.descriptions() == ()


def test_request_trust_mode_must_be_enforceable_by_runtime_and_stages(
    tmp_path: Path,
) -> None:
    original = similarity_search_sdk_definition(shard_rows=2).definition()
    stages = tuple(
        replace(stage, trust_modes=("trusted",))
        for stage in original.manifest.workflow.stages
    )
    manifest = replace(
        original.manifest,
        workflow=replace(original.manifest.workflow, stages=stages),
        trust_modes=(TrustMode.TRUSTED, TrustMode.VERIFIED),
    )
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    input_port = definition.manifest.inputs["input"]
    artifact = ArtifactRef(
        "11111111-1111-4111-8111-111111111111",
        "a" * 64,
        input_port.schema.ref,
        input_port.schema.media_type,
        1,
        records=1,
    )
    request = JobRequest(
        definition.manifest.workload,
        {"query_smiles": "CCO"},
        {"input": ArtifactCollection.single(artifact)},
        trust_mode=TrustMode.VERIFIED,
    )
    store = LocalArtifactStore(tmp_path / "artifacts")

    with pytest.raises(CompatibilityError) as raised:
        registry.plan(
            request,
            manifest.package.digest,
            default_sdk_runtime(),
            LocalPlanningContext(store, store, tmp_path / "runtime-plan"),
        )
    assert raised.value.code == "trust-mode-unavailable"

    runtime = replace(
        default_sdk_runtime(),
        trust_modes=(TrustMode.TRUSTED, TrustMode.VERIFIED),
    )
    with pytest.raises(CompatibilityError) as raised:
        registry.plan(
            request,
            manifest.package.digest,
            runtime,
            LocalPlanningContext(store, store, tmp_path / "stage-plan"),
        )
    assert raised.value.code == "stage-trust-unavailable"


def test_job_cannot_require_a_feature_outside_the_runtime(tmp_path: Path) -> None:
    original = similarity_search_sdk_definition(shard_rows=2).definition()
    manifest = replace(
        original.manifest,
        optional_features=(
            FeatureRequirement("gpu-fastpath", VersionRange(">=1,<2"), "cpu-fallback"),
        ),
    )
    definition = WorkloadDefinition(
        manifest,
        original.planner,
        original.runners,
        original.reducers,
        original.verifiers,
    )
    registry = WorkloadRegistry()
    registry.register(definition, enabled=True)
    input_port = definition.manifest.inputs["input"]
    artifact = ArtifactRef(
        "11111111-1111-4111-8111-111111111111",
        "a" * 64,
        input_port.schema.ref,
        input_port.schema.media_type,
        1,
        records=1,
    )
    request = JobRequest(
        definition.manifest.workload,
        {"query_smiles": "CCO"},
        {"input": ArtifactCollection.single(artifact)},
        required_features=("gpu-fastpath",),
    )

    negotiated = registry.require(
        "similarity-search",
        "1.0.0",
        manifest.package.digest,
        runtime=default_sdk_runtime(),
    )[1]
    assert negotiated is not None
    assert negotiated.optional_fallbacks == {"gpu-fastpath": "cpu-fallback"}

    with pytest.raises(CompatibilityError) as raised:
        registry.plan(
            request,
            manifest.package.digest,
            default_sdk_runtime(),
            LocalPlanningContext(
                LocalArtifactStore(tmp_path / "artifacts"),
                LocalArtifactStore(tmp_path / "artifacts"),
                tmp_path / "plan",
            ),
        )
    assert raised.value.code == "feature-unavailable"
