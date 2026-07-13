from __future__ import annotations

from scimesh.cli import main


def test_help_command_prints_runnable_examples(capsys: object) -> None:
    assert main(["help"]) == 0
    output = capsys.readouterr().out
    assert "scimesh similarity-search" in output
    assert "scimesh similarity-graph" in output
    assert "mkdir -p results" in output
