"""Tests for the MapReduceWorkload authoring scaffold."""

from __future__ import annotations

import csv
from dataclasses import replace
from pathlib import Path

import pytest

from scimesh.sdk import (
    ArtifactCollection,
    ArtifactSchema,
    ComponentRef,
    DeterminismProfile,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
    LocalPlanningContext,
    MapReduceWorkload,
    PortSpec,
    SchemaRef,
    StageKind,
    TrustMode,
    WorkloadId,
    WorkloadRegistry,
    assert_manifest_round_trip,
)
from scimesh.workloads.library import default_sdk_runtime
from scimesh.workloads.environment import (
    current_environment_digest,
    current_scimesh_package_digest,
)


def _molecule_port() -> PortSpec:
    return PortSpec(
        ArtifactSchema(
            SchemaRef("molecule-table", 1),
            "text/tab-separated-values",
            "utf-8",
            10**9,
            ComponentRef("delimited-table", 1),
            validator_configuration={
                "required_columns": ["canonical_smiles", "chembl_id"]
            },
            max_records=10**8,
        )
    )


def _count_port() -> PortSpec:
    return PortSpec(
        ArtifactSchema(
            SchemaRef("count-table", 1),
            "text/csv",
            "utf-8",
            10**9,
            ComponentRef("delimited-table", 1),
            validator_configuration={"columns": ["id", "rows"]},
            max_records=10**8,
        )
    )


class CountRowsWorkload(MapReduceWorkload):
    """A minimal author-written workload: three scientific hooks only."""

    workload_id = WorkloadId("count-rows", "1.0.0")
    description = "Count TSV data rows per shard and concatenate the counts."
    parameters_schema = {
        "type": "object",
        "additionalProperties": False,
        "properties": {"prefix": {"type": "string", "minLength": 1, "maxLength": 50}},
    }
    input_port = _molecule_port()
    partial_port = _count_port()
    output_port = _count_port()
    map_parameter_names = ("prefix",)
    reduce_parameter_names = ("prefix",)

    def partition_input(self, input_path, parameters, workspace):
        paths = []
        with input_path.open(encoding="utf-8", newline="") as source:
            for index, row in enumerate(csv.DictReader(source, delimiter="\t")):
                path = workspace / f"shard-{index}.tsv"
                path.write_text(
                    "chembl_id\tcanonical_smiles\n"
                    + row["chembl_id"]
                    + "\t"
                    + row["canonical_smiles"]
                    + "\n",
                    encoding="utf-8",
                )
                paths.append(path)
        return paths

    def compute_shard(self, inputs, parameters, output_path):
        lines = inputs["input"].read_text(encoding="utf-8").splitlines()
        rows = max(len(lines) - 1, 0)
        output_path.write_text(
            "id,rows\n" + parameters.get("prefix", "shard") + "," + str(rows) + "\n",
            encoding="utf-8",
        )
        return {"rows": rows}  # type: ignore[return-value]

    def reduce_partials(self, partial_paths, parameters, output_path):
        total = 0
        with output_path.open("w", encoding="utf-8") as destination:
            destination.write("id,rows\n")
            for partial in partial_paths:
                for index, line in enumerate(
                    partial.read_text(encoding="utf-8").splitlines()
                ):
                    if index == 0:
                        continue
                    destination.write(line + "\n")
                    total += int(line.split(",")[1])
        return {"rows_total": total, "partial_count": len(partial_paths)}  # type: ignore[return-value]


def _registered_count_rows():
    workload = CountRowsWorkload(
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
    )
    registry = WorkloadRegistry()
    registry.register(workload.definition(), enabled=True)
    runtime = replace(
        default_sdk_runtime(),
        workload_capabilities=(
            *default_sdk_runtime().workload_capabilities,
            "count-rows",
        ),
    )
    return workload, registry, runtime


def _write_dataset(path: Path) -> None:
    path.write_text(
        "chembl_id\tcanonical_smiles\nA\tCCO\nB\tCCCC\nC\tCCN\n",
        encoding="utf-8",
    )


def _request(workload, store, dataset) -> JobRequest:
    artifact = store.import_file(
        dataset,
        declaration=workload.manifest.inputs["input"].schema,
    )
    return JobRequest(
        workload=workload.manifest.workload,
        parameters={"prefix": "x"},
        inputs={"input": ArtifactCollection.single(artifact)},
    )


def test_map_reduce_scaffold_assembles_the_manifest_and_runs(tmp_path: Path) -> None:
    workload, registry, runtime = _registered_count_rows()
    manifest = workload.manifest

    assert manifest.workload.name == "count-rows"
    assert manifest.workload.version == "1.0.0"
    assert manifest.determinism is DeterminismProfile.BYTE_EXACT
    assert manifest.verifier.verifier.canonical == "exact-artifact@1"
    assert set(mode.value for mode in manifest.trust_modes) == {
        "trusted",
        "untrusted_quorum",
    }
    assert [stage.kind for stage in manifest.workflow.stages] == [
        StageKind.MAP,
        StageKind.REDUCE,
    ]
    assert set(workload.definition().runners) == {
        manifest.workflow.stages[0].entry_point
    }
    assert set(workload.definition().reducers) == {
        manifest.workflow.stages[1].entry_point
    }
    assert_manifest_round_trip(manifest)

    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    store = LocalArtifactStore(tmp_path / "artifacts")
    result = LocalCoreBatchExecutor(
        registry,
        runtime,
        store,
        tmp_path / "work",
    ).execute(_request(workload, store, dataset), workload.manifest.package.digest)

    assert result.task_key == "reduce/final"
    assert dict(result.metrics) == {"rows_total": 3, "partial_count": 3}
    text = store.materialize(result.outputs["result"].items[0].artifact).read_text(
        encoding="utf-8"
    )
    assert text == "id,rows\nx,1\nx,1\nx,1\n"


def test_map_reduce_scaffold_derives_pinned_plans_and_parameters(
    tmp_path: Path,
) -> None:
    workload, registry, runtime = _registered_count_rows()
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    store = LocalArtifactStore(tmp_path / "artifacts")
    request = _request(workload, store, dataset)
    input_artifact = request.inputs["input"].items[0].artifact

    plan = registry.plan(
        request,
        workload.manifest.package.digest,
        runtime,
        LocalPlanningContext(
            store,
            store,
            tmp_path / "plan",
            allowed_artifacts=(input_artifact,),
        ),
    )
    assert [task.task_key for task in plan.tasks] == [
        "map/00000000",
        "map/00000001",
        "map/00000002",
    ]
    assert all(task.parameters == {"prefix": "x"} for task in plan.tasks)
    assert all(task.package_digest == plan.package_digest for task in plan.tasks)
    assert all(task.manifest_digest == plan.manifest_digest for task in plan.tasks)
    assert all(task.trust_mode is TrustMode.TRUSTED for task in plan.tasks)


def test_map_reduce_scaffold_requires_scientific_hooks() -> None:
    class MissingHooksWorkload(MapReduceWorkload):
        workload_id = WorkloadId("missing-hooks", "1.0.0")
        description = "A workload that forgets its scientific hooks."
        parameters_schema = {
            "type": "object",
            "additionalProperties": False,
            "properties": {},
        }
        input_port = _molecule_port()
        partial_port = _count_port()
        output_port = _count_port()

    workload = MissingHooksWorkload(
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
    )
    with pytest.raises(NotImplementedError, match="partition_input"):
        workload.partition_input(Path("input"), {}, Path("workspace"))
    with pytest.raises(NotImplementedError, match="compute_shard"):
        workload.compute_shard({}, {}, Path("output"))
    with pytest.raises(NotImplementedError, match="reduce_partials"):
        workload.reduce_partials([], {}, Path("output"))


def test_map_reduce_scaffold_default_partial_keys_are_contiguous() -> None:
    workload = CountRowsWorkload(
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
    )
    assert workload.parse_partial_key("map.00000000") == 0
    assert workload.parse_partial_key("map.00000002") == 2
    with pytest.raises(ValueError, match="eight-digit-index"):
        workload.parse_partial_key("map.0")
    workload.validate_partial_keys((0, 1, 2))
    with pytest.raises(ValueError, match="complete and contiguous"):
        workload.validate_partial_keys((0, 2))
    with pytest.raises(ValueError, match="complete and contiguous"):
        workload.validate_partial_keys((0, 0, 1))


def test_map_reduce_scaffold_rejects_domain_invalid_parameters(tmp_path: Path) -> None:
    workload, registry, runtime = _registered_count_rows()
    dataset = tmp_path / "molecules.tsv"
    _write_dataset(dataset)
    store = LocalArtifactStore(tmp_path / "artifacts")
    artifact = store.import_file(
        dataset,
        declaration=workload.manifest.inputs["input"].schema,
    )

    class StrictCountRows(CountRowsWorkload):
        def domain_validate(self, parameters):
            if "prefix" not in parameters:
                raise ValueError("prefix is required")

    strict = StrictCountRows(
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
    )
    registry2 = WorkloadRegistry()
    registry2.register(strict.definition(), enabled=True)
    request = JobRequest(
        workload=strict.manifest.workload,
        parameters={},
        inputs={"input": ArtifactCollection.single(artifact)},
    )
    with pytest.raises(ValueError, match="prefix is required"):
        registry2.plan(
            request,
            strict.manifest.package.digest,
            runtime,
            LocalPlanningContext(store, store, tmp_path / "plan"),
        )
