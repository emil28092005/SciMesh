"""Per-task execution for the Go worker agent.

This package contains the Python side of a claimed task: the wire value
objects (``models``), the SDK execution bridge (``runners``), and the
command-line task entry (``task``). The agent lifecycle itself lives in the
Go worker agent (``coordinator/internal/agent``); this package is only ever
invoked by it, one process per task.
"""
