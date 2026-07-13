"""Minimal interface implemented by every SciMesh workload."""

from __future__ import annotations

import argparse
from typing import Protocol


class Workload(Protocol):
    """A workload that can add its CLI and execute from parsed arguments."""

    name: str
    help: str

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        """Add workload-specific command-line arguments."""

    def run(self, args: argparse.Namespace) -> int:
        """Execute the workload."""
