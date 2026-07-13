"""Registry for locally available workloads."""

from __future__ import annotations

import argparse

from scimesh.core.workload import Workload


class WorkloadRegistry:
    """Collect workloads and expose each one as a CLI subcommand."""

    def __init__(self) -> None:
        self._workloads: dict[str, Workload] = {}

    def register(self, workload: Workload) -> None:
        """Register a workload by its unique command name."""
        if workload.name in self._workloads:
            raise ValueError(f"Workload already registered: {workload.name}")
        self._workloads[workload.name] = workload

    def add_subparsers(self, subparsers: argparse._SubParsersAction) -> None:
        """Add a parser for every registered workload."""
        for workload in self._workloads.values():
            parser = subparsers.add_parser(workload.name, help=workload.help)
            workload.configure_parser(parser)
            parser.set_defaults(handler=workload.run)
