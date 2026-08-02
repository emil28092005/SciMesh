from __future__ import annotations

import pytest

from scimesh.cli import main


def test_help_command_prints_runnable_examples(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert main(["help"]) == 0
    output = capsys.readouterr().out
    assert "scimesh similarity-search" in output
    assert "scimesh similarity-graph" in output
    assert "mkdir -p results" in output
    assert "make demo-ui" in output
