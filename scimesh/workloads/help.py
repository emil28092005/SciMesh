"""Quick-start help workload."""

from __future__ import annotations

import argparse
from textwrap import dedent


HELP_TEXT = dedent(
    """\
    SciMesh quick start
    ===================

    SciMesh runs molecular similarity workloads locally. Input must be a ChEMBL TSV
    with chembl_id and canonical_smiles columns.

    1. Activate the project environment and create a directory for outputs:

       source .venv/bin/activate
       mkdir -p results

    2. Find molecules similar to gefitinib (CHEMBL939):

       scimesh similarity-search chembl_37_chemreps.txt \\
         --query-id CHEMBL939 \\
         --top-k 20 \\
         --max-rows 10000 \\
         --output results/gefitinib_top20.csv

    3. Search by a SMILES query instead of a ChEMBL ID:

       scimesh similarity-search chembl_37_chemreps.txt \\
         --query-smiles 'CCO' \\
         --top-k 20 \\
         --output results/smiles_search.csv

    4. Find the least similar molecules. With "less", results are ranked from
       lowest similarity upward; --threshold is an optional <= filter:

       scimesh similarity-search chembl_37_chemreps.txt \\
         --query-id CHEMBL939 \\
         --threshold-direction less \\
         --threshold 0.1 \\
         --top-k 20 \\
         --output results/least_similar.csv

    5. Build a small exact similarity graph:

       scimesh similarity-graph chembl_37_chemreps.txt \\
         --max-rows 1000 \\
         --threshold 0.7 \\
         --block-size 250 \\
         --output results/similarity_graph.csv

    Use --max-rows for quick local tests; omit it to process the full dataset.
    For all options, run:

       scimesh similarity-search --help
       scimesh similarity-graph --help
    """
)


class HelpWorkload:
    """Expose practical examples without adding special logic to the main CLI."""

    name = "help"
    help = "Show a quick start and runnable examples."

    def configure_parser(self, parser: argparse.ArgumentParser) -> None:
        parser.description = "Show SciMesh setup and usage examples."

    def run(self, args: argparse.Namespace) -> int:
        print(HELP_TEXT)
        return 0
