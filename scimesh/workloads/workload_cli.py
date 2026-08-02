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
    help = "List, run, and export SDK-built workloads."

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        subparsers = parser.add_subparsers(dest="workload_command", required=True)

        list_parser = subparsers.add_parser(
            "list", help="List installed and enabled SDK workloads."
        )
        list_parser.set_defaults(workload_handler=self.list_workloads)

        export_parser = subparsers.add_parser(
            "export", help="Write the workload library as JSON for the coordinator UI."
        )
        export_parser.add_argument(
            "-o",
            "--output",
            type=Path,
            default=Path("workloads.json"),
            help="Output JSON path (default: workloads.json)",
        )
        export_parser.set_defaults(workload_handler=self.export_workloads)

        allowlist_parser = subparsers.add_parser(
            "allowlist",
            help="Print the installed-package workload allowlist for worker configuration.",
        )
        allowlist_parser.set_defaults(workload_handler=self.export_allowlist)

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

    def export_workloads(self, args: argparse.Namespace) -> int:
        """Write the workload library as a JSON catalog for the coordinator UI."""
        import json

        from scimesh.sdk._validation import thaw_json

        registry = self._registry(args)
        workloads: list[dict[str, object]] = []
        for item in sorted(
            registry.descriptions(), key=lambda value: value.workload.name
        ):
            definition, _ = registry.require(
                item.workload.name,
                item.workload.version,
                item.package_digest,
            )
            manifest = definition.manifest
            workloads.append(
                {
                    "name": manifest.workload.name,
                    "version": manifest.workload.version,
                    "description": manifest.description,
                    "capabilities": list(manifest.capabilities),
                    "trust_modes": [mode.value for mode in manifest.trust_modes],
                    "determinism": manifest.determinism.value,
                    "verifier": manifest.verifier.verifier.canonical,
                    "enabled": item.enabled,
                    "parameters_schema": thaw_json(manifest.parameters_schema),
                    "ui_elements": [
                        element.to_dict() for element in manifest.ui_elements
                    ],
                    "reduction": manifest.reduction,
                    "upload_ready": manifest.upload_ready,
                    "inputs": {
                        name: port.schema.to_dict()
                        for name, port in manifest.inputs.items()
                    },
                    "outputs": {
                        name: port.schema.to_dict()
                        for name, port in manifest.outputs.items()
                    },
                }
            )
        payload: dict[str, object] = {
            "schema_version": 2,
            "generated_by": "scimesh workload export",
            "workloads": workloads,
        }
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with args.output.open("w", encoding="utf-8") as destination:
            json.dump(payload, destination, indent=2, sort_keys=True)
            destination.write("\n")
        print(f"Exported {len(workloads)} workloads to {args.output}")
        return 0

    def export_allowlist(self, args: argparse.Namespace) -> int:
        """Print the allowlist JSON that worker environments consume.

        The printed array feeds ``SCIMESH_WORKLOAD_ALLOWLIST`` on workers and
        mirrors the digest pins of the installed distribution.
        """
        import json

        registry = self._registry(args)
        payload = []
        for item in sorted(
            registry.descriptions(), key=lambda value: value.workload.name
        ):
            if not item.enabled:
                continue
            definition, _ = registry.require(
                item.workload.name,
                item.workload.version,
                item.package_digest,
            )
            manifest = definition.manifest
            payload.append(
                {
                    "distribution": manifest.package.distribution,
                    "name": manifest.workload.name,
                    "version": manifest.workload.version,
                    "digest": manifest.package.digest,
                }
            )
        print(json.dumps(payload, indent=2, sort_keys=True))
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
