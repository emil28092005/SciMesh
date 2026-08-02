# Workload CLI

`scimesh workload` is a generic SDK tool: it contains no workload-specific
logic, so new workloads never require changes to the CLI or any other part
of the program.

```text
scimesh workload list|run|export
```

## list

Show every installed and enabled SDK workload:

```bash
scimesh workload list
```

Output: `name  version  description  [enabled <digest-prefix>]`. With
`SCIMESH_WORKLOAD_ALLOWLIST` set, allowlisted installed workloads are shown
instead of (or in addition to) the built-ins.

## run

Execute one workload locally against an input file:

```bash
scimesh workload run molwt-filter \
  --input molecules.tsv \
  --params '{"min_molwt": 40.0}' \
  --shard-rows 1000 \
  -o filtered.csv
```

| Option | Meaning |
| --- | --- |
| `name` | Workload name, for example `descriptor-batch` |
| `--version` | Exact workload version (default: the enabled one) |
| `--input FILE` | Input dataset file |
| `--params JSON` | Job parameters as a JSON object |
| `--shard-rows N` | Rows per planned shard (default 10000) |
| `-o, --output FILE` | Output path for the final artifact |
| `--work-dir DIR` | Working directory (default: a fresh temporary directory) |

```bash
scimesh workload run similarity-search \
  --input molecules.tsv \
  --params '{"query_smiles": "CCO", "top_k": 20, "progress_every": 0}'
```

The runner prints the saved path and the final metrics.

## export

Write the workload library as a JSON catalog — the same catalog the
coordinator UI embeds on its **Workloads** page:

```bash
scimesh workload export -o workloads.json
```

Regenerate the coordinator's embedded catalog with:

```bash
make workloads-export
```

## Environment

| Variable | Meaning |
| --- | --- |
| `SCIMESH_WORKLOAD_ALLOWLIST` | JSON array of `{distribution, name, version, digest}` entries; discovery loads the matching installed `scimesh.workloads` entry points |
| `SCIMESH_CAPABILITIES` | Comma-separated capabilities the worker advertises (default `similarity-search,similarity_search`) |

Both variables are read by the Go worker agent's task subprocess and the
workload CLI.
