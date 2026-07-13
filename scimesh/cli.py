"""Command-line entry point for SciMesh."""

from __future__ import annotations

import argparse
import sys

from scimesh.core.registry import WorkloadRegistry
from scimesh.workloads import register_workloads


def build_parser() -> argparse.ArgumentParser:
    """Build the top-level parser from the registered workloads."""
    parser = argparse.ArgumentParser(
        prog="scimesh",
        description="Run local scientific workloads on molecular datasets.",
        epilog="Run 'scimesh help' for a quick start and copy-paste examples.",
    )
    subparsers = parser.add_subparsers(dest="workload", required=True)
    registry = WorkloadRegistry()
    register_workloads(registry)
    registry.add_subparsers(subparsers)
    return parser


def main(argv: list[str] | None = None) -> int:
    """Run a selected workload and return its exit status."""
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.handler(args)
    except (OSError, ValueError) as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
