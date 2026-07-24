# CTX-07: distributed workload protocol and planner contract

## Status and scope

This document is the implementation contract for CTX-07. Its generic protocol,
registry, strict JSON models, and deterministic reduction ordering are
implemented in `scimesh/distributed/`. CTX-08 implements the molecular
similarity-search planner, worker adapter, and pure reducer on top of it. This
document does not implement a coordinator API endpoint, database migration, or
durable final artifact. Until CTX-09 is complete, shard CSVs remain diagnostic
partial results.

The protocol gives local scientific workloads a coordinator-independent way to
validate a job, plan artifact-backed tasks, and later reduce completed outputs.
The Go coordinator owns durable artifacts, transactions, task rows, leases, and
HTTP. A Python workload must never access PostgreSQL or call the coordinator.

Read `PLAN.md`, `.agents/workloads.md`, and `docs/api-contract.md` before
implementing this CTX.

## Canonical vocabulary

- External workload names are lowercase hyphenated names: `similarity-search`
  and, later, `similarity-graph`.
- The existing underscore spellings are a temporary compatibility alias at the
  Python worker boundary only. Planners, persisted job/task payloads, and new
  API examples use the canonical hyphenated spelling.
- A **plan** contains only JSON-compatible values and coordinator artifact
  references. It contains no local filesystem path, worker URI, presigned URL,
  database connection, or callable.
- `chunk_index` is a non-negative integer, unique within a plan, and sorted
  ascending whenever results are enumerated.

## Python boundary

CTX-07 adds a small `DistributedWorkload` protocol under `scimesh/distributed/`
and a registry separate from the local CLI registry. Names below are proposed
public types; keep concrete implementation details minimal.

```python
class DistributedWorkload(Protocol):
    name: str

    def validate_job(self, parameters: Mapping[str, object]) -> None: ...

    def plan(
        self,
        input_path: Path,
        input_artifact_id: str,
        parameters: Mapping[str, object],
        shard_rows: int,
        workspace: Path,
    ) -> DistributedPlan: ...

    def reduce(
        self,
        partial_results: Sequence[CompletedPartial],
        parameters: Mapping[str, object],
        workspace: Path,
    ) -> FinalResult: ...
```

`input_path` and `workspace` are temporary files supplied by the coordinator
bridge. They are never serialized. `plan()` returns only a `DistributedPlan`;
the bridge validates it, persists artifact/task rows in one coordinator
transaction, and removes its temporary workspace. If validation or planning
fails, no job or task may be written.

## JSON models

All objects below are schema version `1`. Future incompatible changes require a
new version; never infer a schema from missing fields.

### Artifact reference

```json
{
  "artifact_id": "c4273293-f8b4-4ecb-99df-3b9f5a32b6a6",
  "sha256": "3b2d...64-lowercase-hex-characters",
  "content_type": "text/tab-separated-values"
}
```

The artifact ID is coordinator-owned. The checksum is included so planning and
tests can assert exactly which immutable input was used. A worker receives the
coordinator-generated download URI only through `POST /tasks/claim`.

### Distributed plan

```json
{
  "schema_version": 1,
  "workload": "similarity-search",
  "resolved_parameters": {
    "query_smiles": "COc1ccc(Nc2ncnc3cc(OCCCN4CCOCC4)c(OC)c23)cc1",
    "query_source": {"kind": "chembl_id", "value": "CHEMBL939"},
    "top_k": 20,
    "threshold": 0.7,
    "threshold_direction": "greater",
    "fingerprint": {"algorithm": "morgan", "radius": 2, "fp_size": 2048}
  },
  "tasks": [
    {
      "chunk_index": 0,
      "input_artifact": {
        "artifact_id": "69e41105-d9fb-4c7f-a2db-7dd9e3ba2c76",
        "sha256": "4c92...64-lowercase-hex-characters",
        "content_type": "text/tab-separated-values"
      },
      "parameters": {
        "query_smiles": "COc1ccc(Nc2ncnc3cc(OCCCN4CCOCC4)c(OC)c23)cc1",
        "top_k": 20,
        "threshold": 0.7,
        "threshold_direction": "greater",
        "fingerprint": {"algorithm": "morgan", "radius": 2, "fp_size": 2048}
      }
    }
  ]
}
```

`resolved_parameters` are immutable job metadata. A task copies only the
values required by its worker runner. The coordinator may add its own durable
task ID and generated input URI; it must not alter scientific parameters.

## Similarity-search planning rules

1. Accept exactly one of `query_id` and `query_smiles` at the public boundary.
2. Validate a supplied SMILES once. For `query_id`, find and validate that
   molecule once against the original uploaded TSV **before** creating shards.
3. Persist the resolved canonical query SMILES and the original query source in
   `resolved_parameters`. Workers receive `query_smiles`, never `query_id`.
4. Fingerprint settings are fixed to Morgan radius `2` and `fp_size` `2048`.
   Reject a request that tries to override them rather than silently changing
   scientific semantics.
5. Split source rows in input order. Every shard includes the original TSV
   header and has a contiguous, zero-based `chunk_index`.
6. Each shard uses the global `top_k`, not a smaller local limit. A global
   reducer cannot recover a candidate discarded by every shard.
7. Preserve `threshold`, `threshold_direction`, and valid `max_rows` semantics
   in the resolved plan. A job-level row limit is applied before sharding, not
   independently by every worker.

Invalid row SMILES are not planner failures. They remain shard data and are
counted by the worker exactly as the local workload does. An invalid query is a
planning failure.

## Partial-result contract

A completed similarity-search task owns exactly one coordinator-uploaded CSV
artifact with content type `text/csv` and these columns, in this order:

```csv
rank,chembl_id,canonical_smiles,similarity
1,CHEMBL123,CCO,0.875000
```

- `rank` is one-based local rank.
- `similarity` uses a round-trip decimal representation of the computed float
  (for Python, `repr(similarity)`). This preserves exact cross-shard ranking;
  the reducer writes the user-facing final CSV with the local CLI's six-decimal
  display formatting.
- Rows are sorted by `(-similarity, chembl_id, canonical_smiles)` for
  `threshold_direction=greater`, or `(similarity, chembl_id,
  canonical_smiles)` for `less`.
- The query molecule and every candidate with the same canonical query SMILES
  are excluded using the existing local-workload definition.
- Empty valid result files still include the header.

The worker completion metrics must include JSON numbers for `scanned_rows`,
`valid_molecules`, `invalid_smiles`, `matches_emitted`, and
`elapsed_seconds`. Metrics are observability data; the reducer derives final
scientific output exclusively from coordinator-owned partial artifacts.

## Reduction boundary

CTX-09 invokes the registered reducer only after every task is completed. It
passes `CompletedPartial` values ordered by `chunk_index`, each containing its
coordinator artifact reference and validated metrics.

For similarity-search the reducer:

1. reads partial CSVs in `chunk_index` order;
2. validates header, row shape, rank, finite similarity in `[0, 1]`, and sort
   order;
3. retains a bounded heap of at most the global `top_k` candidates using the
   exact local ranking key;
4. writes the same header and deterministic rank numbering as the local CLI.

It must not deduplicate ordinary records: the local reference keeps input-row
multiplicity. Reduction is independent of worker completion order and uses
`O(top_k + shard_rows)` memory apart from CSV streaming buffers.

## Required tests for the CTX-07 implementation

- unknown workload is rejected before any coordinator job/task write;
- invalid public parameters and invalid `query_id` produce no partial plan;
- `query_id` resolution occurs once, before shard construction;
- the same input, parameters, and shard size generate byte-equivalent
  JSON plans and identical shard order;
- every task payload is JSON-serializable and contains only artifact references
  and validated scalar/object values;
- a two-shard dummy workload proves coordinator transaction rollback on planner
  validation failure;
- completed partial artifacts reach the reducer ordered by `chunk_index`, even
  when workers finish in a different order;
- the protocol registry never imports the Go coordinator or database code.

## Deferred work

CTX-09 materializes the planned shard files as coordinator artifacts, invokes
the registered reducer once, and persists its final artifact/job state. CTX-10
defines graph-specific triangular block plans; it must not reuse the search
shard scheme without its pair-coverage invariants.
