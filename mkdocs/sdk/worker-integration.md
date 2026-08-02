# Worker integration

The Worker Agent (`scimesh-worker`) is a coordinator client, never a
database client. It polls the coordinator over HTTP, executes SDK-built
workloads, and uploads partial results through the coordinator — results
never carry `file://` or `worker://` URIs, and failures go to `/failure`.

The same scientific handlers run in three places: the local CLI cores, the
`LocalCoreBatchExecutor` conformance harness, and the worker — because the
worker executes the workload's own SDK runner.

## Claim lifecycle

```text
register -> claim (one task) -> download input + verify sha256
         -> run via SDK bridge -> upload partial -> submit result
```

- **Register**: the worker advertises its capabilities (`similarity-search`
  by default; extend with `SCIMESH_CAPABILITIES`).
- **Claim**: atomic lease of one task; `204` means idle.
- **Download**: the input is streamed and its SHA-256 verified; the bearer
  token is stripped on cross-origin redirects.
- **Heartbeat**: a background thread renews the lease from the returned
  deadline at less than half the remaining TTL.
- **Upload**: the partial CSV is streamed to the coordinator with
  `X-Worker-ID` / `X-Task-Attempt` headers, then the completion is submitted
  referencing the coordinator-owned artifact id.
- **Failure**: sanitized `error_code` + message (≤300 chars, no local
  paths, no tracebacks); transient transport errors are retried.

## The SDK execution bridge

`scimesh/worker/runners.py` is workload-generic. For a claimed task it:

1. normalizes the workload name (underscores → hyphens) and looks up the
   loaded definition;
2. runs compatibility negotiation against a runtime derived from the loaded
   definitions (capabilities + pinned environment digests) and the worker
   inventory (CPU/memory from configuration);
3. verifies the workload's map stage fits the v1 contract — a single
   `input` port and a single `partial` output — otherwise it fails closed
   with a clear message;
4. imports the downloaded input into a content-addressed local store;
5. builds a digest-pinned `TaskSpec` (package/manifest/environment digests,
   trust mode, negotiated features, stage resources and execution profile);
6. reserves resources through `ResourcePool` and runs the workload's own
   `Runner` with a `LocalTaskContext` (scoped catalog/sink, provenance,
   cancellation flag);
7. validates the returned `OutputManifest` (task key, provenance, sealed
   vs. declared artifacts, byte budget) and returns the sealed partial for
   upload.

Scientific policy lives in the workload: `query_id` resolution, parameter
validation, and `max_rows` rejection are all handled by the workload's own
hooks — the bridge passes task parameters through unchanged.

## Loading workloads

The worker loads workloads from `SCIMESH_WORKLOAD_ALLOWLIST` (a JSON array
of `{distribution, name, version, digest}` entries matched against installed
`scimesh.workloads` entry points). Discovery measures the installed package
before and after importing and fails transactionally on any mismatch. When
no allowlist is configured, the worker falls back to the built-in
`similarity-search`.

```bash
SCIMESH_WORKLOAD_ALLOWLIST='[{"distribution": "scimesh",
  "name": "descriptor-batch", "version": "1.0.0",
  "digest": "sha256:..."}]' scimesh-worker --coordinator-url https://...
```

## v1 contract limits

The coordinator protocol v1 persists flat one-input/one-result tasks. Until
a versioned protocol rollout:

- map stages with more than one input port (for example
  `similarity-graph`'s block pairs) are **rejected by the bridge** — the
  coordinator does not create such tasks anyway;
- `max_rows` is a plan-time option and is rejected per task;
- workloads beyond the allowlisted set are rejected as unsupported.
