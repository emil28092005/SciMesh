"""Tests for the generic ``scimesh workload`` CLI."""

from __future__ import annotations

from pathlib import Path

from scimesh.cli import main


def test_workload_cli_lists_sdk_workloads(capsys: object) -> None:
    assert main(["workload", "list"]) == 0
    output = capsys.readouterr().out
    assert "descriptor-batch" in output
    assert "similarity-graph" in output
    assert "similarity-search" in output
    assert "enabled" in output


def test_workload_cli_runs_descriptor_batch(tmp_path: Path, capsys: object) -> None:
    dataset = tmp_path / "molecules.tsv"
    dataset.write_text(
        "chembl_id\tcanonical_smiles\nA\tCCO\nB\tCCCC\nC\tCCN\n",
        encoding="utf-8",
    )
    output = tmp_path / "descriptors.csv"

    code = main(
        [
            "workload",
            "run",
            "descriptor-batch",
            "--input",
            str(dataset),
            "--params",
            '{"skip_invalid": true}',
            "--shard-rows",
            "2",
            "-o",
            str(output),
        ]
    )

    assert code == 0
    lines = output.read_text(encoding="utf-8").splitlines()
    assert lines[0].startswith("chembl_id,canonical_smiles,ExactMolWt")
    assert len(lines) == 4
    assert "rows_emitted" in capsys.readouterr().out


def test_workload_cli_runs_similarity_search(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    dataset.write_text(
        "chembl_id\tcanonical_smiles\nQUERY\tCCO\nMATCH\tCCCO\n",
        encoding="utf-8",
    )
    output = tmp_path / "search.csv"

    code = main(
        [
            "workload",
            "run",
            "similarity-search",
            "--input",
            str(dataset),
            "--params",
            '{"query_smiles": "CCO", "top_k": 5, "progress_every": 0}',
            "--shard-rows",
            "2",
            "-o",
            str(output),
        ]
    )

    assert code == 0
    lines = output.read_text(encoding="utf-8").splitlines()
    assert lines[0] == "rank,chembl_id,canonical_smiles,similarity"
    assert len(lines) == 2


def test_workload_cli_rejects_unknown_or_missing_workload(tmp_path: Path) -> None:
    import pytest

    dataset = tmp_path / "molecules.tsv"
    dataset.write_text("chembl_id\tcanonical_smiles\nA\tCCO\n", encoding="utf-8")
    assert main(["workload", "run", "no-such-workload", "--input", str(dataset)]) == 1
    with pytest.raises(SystemExit):
        main(["workload", "run", "descriptor-batch"])


def test_workload_cli_rejects_invalid_params_json(tmp_path: Path) -> None:
    dataset = tmp_path / "molecules.tsv"
    dataset.write_text("chembl_id\tcanonical_smiles\nA\tCCO\n", encoding="utf-8")
    assert (
        main(
            [
                "workload",
                "run",
                "descriptor-batch",
                "--input",
                str(dataset),
                "--params",
                "{broken",
            ]
        )
        == 1
    )


def test_workload_cli_runs_an_allowlisted_custom_workload(
    tmp_path: Path, monkeypatch: object, capsys: object
) -> None:
    import csv

    from scimesh.sdk import (
        ArtifactSchema,
        ComponentRef,
        MapReduceWorkload,
        PortSpec,
        SchemaRef,
        WorkloadId,
    )
    from scimesh.sdk.registry import WorkloadRegistry
    from scimesh.workloads.environment import (
        current_environment_digest,
        current_scimesh_package_digest,
    )

    class CountRowsWorkload(MapReduceWorkload):
        workload_id = WorkloadId("count-rows", "1.0.0")
        description = "Count TSV data rows per shard."
        parameters_schema = {
            "type": "object",
            "additionalProperties": False,
            "properties": {},
        }
        input_port = PortSpec(
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
        partial_port = output_port = PortSpec(
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
                "id,rows\nshard," + str(rows) + "\n", encoding="utf-8"
            )
            return {"rows": rows}

        def reduce_partials(self, partial_paths, parameters, output_path):
            with output_path.open("w", encoding="utf-8") as destination:
                destination.write("id,rows\n")
                total = 0
                for partial in partial_paths:
                    rows = partial.read_text(encoding="utf-8").splitlines()[1:]
                    destination.write("".join(row + "\n" for row in rows))
                    total += len(rows)
            return {"rows_total": total, "partial_count": len(partial_paths)}

    definition = CountRowsWorkload(
        package_digest=current_scimesh_package_digest(),
        environment_digest=current_environment_digest(),
    ).definition()

    def fake_discover(self: WorkloadRegistry, allowlist) -> None:
        self.register(definition, enabled=True)

    monkeypatch.setattr(WorkloadRegistry, "discover_installed", fake_discover)
    monkeypatch.setenv(
        "SCIMESH_WORKLOAD_ALLOWLIST",
        '[{"distribution": "scimesh", "name": "count-rows", "version": "1.0.0", '
        '"digest": "' + current_scimesh_package_digest() + '"}]',
    )

    dataset = tmp_path / "molecules.tsv"
    dataset.write_text(
        "chembl_id\tcanonical_smiles\nA\tCCO\nB\tCCCC\n", encoding="utf-8"
    )
    output = tmp_path / "counts.csv"
    assert (
        main(
            [
                "workload",
                "run",
                "count-rows",
                "--input",
                str(dataset),
                "-o",
                str(output),
            ]
        )
        == 0
    )
    assert output.read_text(encoding="utf-8") == "id,rows\nshard,1\nshard,1\n"


def test_workload_cli_exports_the_library_as_json(tmp_path: Path) -> None:
    import json

    output = tmp_path / "workloads.json"
    assert main(["workload", "export", "-o", str(output)]) == 0
    payload = json.loads(output.read_text(encoding="utf-8"))
    assert payload["schema_version"] == 1
    names = [item["name"] for item in payload["workloads"]]
    assert names == sorted(
        ["descriptor-batch", "molwt-filter", "similarity-graph", "similarity-search"]
    )
    for item in payload["workloads"]:
        assert item["version"] == "1.0.0"
        assert item["enabled"] is True
        assert item["determinism"] == "byte_exact"
        assert item["verifier"] == "exact-artifact@1"
        assert "input" in item["inputs"]
        assert "result" in item["outputs"]
    molwt = next(item for item in payload["workloads"] if item["name"] == "molwt-filter")
    assert molwt["parameters_schema"]["properties"] == {
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
    }
