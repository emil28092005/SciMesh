"""Generic SDK workload runner CLI: ``scimesh workload list|run``.

This is a generic SDK tool, not a workload. It lists the enabled SDK-built
workloads and executes any of them locally through ``LocalCoreBatchExecutor``,
so a user-written workload package can be inspected and verified without
touching any other part of the program.
"""

from __future__ import annotations

import argparse
import json
import shutil
import tempfile
from pathlib import Path

from scimesh.sdk import (
    ArtifactCollection,
    JobRequest,
    LocalArtifactStore,
    LocalCoreBatchExecutor,
)
from scimesh.sdk.registry import workload_allowlist_from_json
from scimesh.workloads.library import default_sdk_registry, default_sdk_runtime


class WorkloadCLI:
    """Inspect and run SDK-built workloads from the command line."""

    name = "workload"
    help = "List and run SDK-built workloads locally."

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        subparsers = parser.add_subparsers(dest="workload_command", required=True)

        list_parser = subparsers.add_parser(
            "list", help="List installed and enabled SDK workloads."
        )
        list_parser.set_defaults(workload_handler=self.list_workloads)

        run_parser = subparsers.add_parser(
            "run", help="Run one SDK workload locally against an input file."
        )
        run_parser.add_argument(
            "name", help="Workload name, for example descriptor-batch"
        )
        run_parser.add_argument(
            "--version", help="Exact workload version (default: the enabled one)"
        )
        run_parser.add_argument(
            "--input", required=True, type=Path, help="Input dataset file"
        )
        run_parser.add_argument(
            "--params", default="{}", help="Job parameters as a JSON object"
        )
        run_parser.add_argument(
            "--shard-rows",
            type=int,
            default=10_000,
            help="Rows per planned shard for workloads that shard by rows",
        )
        run_parser.add_argument(
            "-o",
            "--output",
            type=Path,
            default=Path("workload_result.csv"),
            help="Output path for the final artifact",
        )
        run_parser.add_argument(
            "--work-dir",
            type=Path,
            help="Temporary working directory (default: a fresh temporary directory)",
        )
        run_parser.set_defaults(workload_handler=self.run_workload)

    def run(self, args: argparse.Namespace) -> int:
        handler = getattr(args, "workload_handler", None)
        if handler is None:
            raise ValueError("select a workload subcommand: list or run")
        return handler(args)

    @staticmethod
    def _registry(args: argparse.Namespace):
        import os

        allowlist = workload_allowlist_from_json(
            os.getenv("SCIMESH_WORKLOAD_ALLOWLIST")
        )
        return default_sdk_registry(
            shard_rows=getattr(args, "shard_rows", 10_000),
            allowlist=allowlist,
        )

    def list_workloads(self, args: argparse.Namespace) -> int:
        registry = self._registry(args)
        descriptions = registry.descriptions()
        if not descriptions:
            print("No SDK workloads are installed or enabled.")
            return 0
        width = max(len(item.workload.name) for item in descriptions)
        for item in sorted(descriptions, key=lambda value: value.workload.name):
            digest = item.package_digest.removeprefix("sha256:")[:12]
            state = "enabled" if item.enabled else "disabled"
            print(
                f"{item.workload.name:<{width}}  {item.workload.version}  "
                f"{item.description}  [{state} {digest}]"
            )
        return 0

    def run_workload(self, args: argparse.Namespace) -> int:
        registry = self._registry(args)
        descriptions = registry.descriptions()
        try:
            parameters = json.loads(args.params)
        except (TypeError, json.JSONDecodeError, RecursionError) as error:
            raise ValueError("--params must be a valid JSON object") from error
        if not isinstance(parameters, dict):
            raise ValueError("--params must be a JSON object")
        description = next(
            (
                item
                for item in descriptions
                if item.workload.name == args.name
                and (args.version is None or item.workload.version == args.version)
            ),
            None,
        )
        if description is None:
            raise ValueError(f"unknown or disabled SDK workload: {args.name}")
        definition, _ = registry.require(
            description.workload.name,
            description.workload.version,
            description.package_digest,
        )
        runtime = default_sdk_runtime(
            workload_capabilities=tuple(item.workload.name for item in descriptions),
            environment_digests=(definition.manifest.environment.digest,),
        )
        if not args.input.is_file():
            raise ValueError(f"input file does not exist: {args.input}")
        with tempfile.TemporaryDirectory(prefix="scimesh-workload-") as temporary:
            root = Path(temporary)
            store = LocalArtifactStore(root / "artifacts")
            artifact = store.import_file(
                args.input,
                declaration=definition.manifest.inputs["input"].schema,
            )
            request = JobRequest(
                workload=definition.manifest.workload,
                parameters=parameters,
                inputs={"input": ArtifactCollection.single(artifact)},
            )
            result = LocalCoreBatchExecutor(
                registry,
                runtime,
                store,
                args.work_dir or root / "attempts",
            ).execute(request, description.package_digest)
            result_artifact = result.outputs["result"].items[0].artifact
            source = store.materialize(result_artifact)
            args.output.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, args.output)
        print(
            f"Saved {description.workload.name} result to {args.output} "
            f"(metrics: {dict(result.metrics)})"
        )
        return 0
