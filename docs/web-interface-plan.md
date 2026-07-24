# SciMesh: plan for the initial web interface

## 1. Purpose and outcome

Build a small, local-first web interface for manually checking the complete
SciMesh pipeline. A person should be able to open one address, upload a small
ChEMBL-style TSV, configure a supported run, observe workers and task progress,
inspect failures, and download artifacts without composing raw HTTP requests.

This is not a public multi-tenant product. It is an operator and demo interface
for a trusted local team. The coordinator remains the only process with direct
database and artifact-storage access; the browser never calls PostgreSQL and
never receives a worker bearer token.

## Current delivered scope

The initial operator UI and CTX-09 final reduction are now implemented. The
control room polls a bounded, coordinator-owned read model every two seconds
while a tab is visible. It shows the worker fleet, recent jobs, safe shard
diagnostics, the actual `reducing` phase, and final-result availability. A job
detail page renders the concrete pipeline stages—input accepted, shards,
worker CSVs, reduction, final CSV—from coordinator state and replaces task and
artifact views as work changes. All browser mutations remain limited to
validated dataset upload and operator cancellation.

The interface must distinguish an in-progress distributed search from a run
whose reducer has produced a durable final result:

| Mode | What it proves | What it must not claim |
| --- | --- | --- |
| **In-progress run** | Upload, task creation, claim, heartbeat, artifact upload, task completion, retries, and shard diagnostics work end-to-end. | That the partial CSVs are a global scientific answer. |
| **Final run** | A reducer has produced a durable final CSV for the full job. | Available for `similarity-search` after CTX-09; graph remains unavailable until CTX-10. |

Never label a partial artifact as a final molecular result. The UI must show a
clear waiting or `reducing` stage until a final artifact exists and the job is
`completed`.

## 2. Constraints and decisions

- Serve the UI from the Go coordinator at the same origin. No React, Vue, Node
  build, CDN, or separate frontend service.
- Use Go `html/template`, `embed`, ordinary CSS, and small vanilla JavaScript
  modules. The page works locally with `docker compose up`.
- Keep worker APIs and UI APIs separate. The UI handlers call Go use cases;
  they do not make HTTP calls to worker endpoints.
- Add a distinct `UI_AUTH_TOKEN` for browser/operator access. It must never
  reuse `WORKER_AUTH_TOKEN`, appear in page HTML, localStorage, logs, URLs, or
  error messages. For the initial local UI use HTTP Basic Auth over a trusted
  local/reverse-proxied connection. If `UI_AUTH_TOKEN` is unset, `/ui` and
  `/ui/api/*` return `404` and the coordinator stays API-only.
- Keep all user-controllable text escaped by `html/template`; JavaScript renders
  API fields through `textContent`, never `innerHTML`.
- All downloads go through coordinator-owned UI endpoints with authorization.
  Do not expose filesystem paths, `storage_key`, database errors, worker tokens,
  or raw worker tracebacks.
- The API vocabulary should be canonicalised before UI forms are implemented.
  Choose one external workload spelling (`similarity-search` and
  `similarity-graph` recommended, matching the CLI) and keep legacy underscore
  aliases only at the worker boundary. Update `docs/api-contract.md` and
  `docs/openapi.yaml` in the same change.

## 3. Target user journey

1. Start coordinator/PostgreSQL and one or more `scimesh-worker` processes.
2. Open `http://localhost:8080/ui`, authenticate with the UI token, and see
   readiness plus the registered-worker table.
3. Choose **New run**, select a workload, fill validated parameters, choose a
   TSV, and submit it.
4. The UI redirects to `/ui/jobs/{job_id}` and polls every two seconds.
5. The operator sees counters, per-task attempt/lease/error state, and worker
   activity. They may copy the worker launch command but cannot start arbitrary
   processes from the browser.
6. During a pipeline check, download an input shard or partial CSV to validate
   manually. For a final run, download the final CSV only when the job status is
   `completed` and a final artifact exists.
7. A failed run exposes only its sanitized failure reason and retry state. The
   browser offers a safe retry action only after an explicit future API supports
   it; v1 never fabricates a retry by changing task rows directly.

## 4. What exists today and required gaps

| Capability | Current state | UI plan |
| --- | --- | --- |
| Upload/chunk dataset | `POST /jobs/upload` exists | Reuse through a UI handler with server-side multipart validation. |
| Aggregate status | `GET /jobs/{id}` exists | Add list/detail read models for the UI. |
| Worker registration/lease flow | Implemented | Add a read-only worker list; no browser worker controls. |
| Task diagnostics | No public list/detail response | Add sanitized job task list with attempt, status, lease owner, expiry and error. |
| Artifact download | Worker endpoint exists | Add UI-authorized, job-scoped download proxy. |
| Final result | CTX-09 final artifact and download route exist | Show the `reducing` stage, then make the final CSV prominent only for `completed`. |
| Distributed graph correctness | Planner/reducer unavailable | Do not advertise a multi-shard graph as final until CTX-10. |

## 5. Proposed structure

```text
coordinator/
  web/
    templates/
      layout.html
      dashboard.html
      job_new.html
      job_detail.html
      error.html
    static/
      app.css
      dashboard.js
      job-detail.js
  internal/
    transport/http/
      ui_handlers.go
      ui_dto.go
      ui_auth.go
      ui_handlers_test.go
    usecase/
      ui.go                 # read-only DTO orchestration, no HTML
    domain/
      ui.go                 # only if a shared value object is genuinely needed
    storage/postgres/
      ui_read_repo.go       # parameterized listing/detail queries
```

Embed `web/templates` and `web/static` into the coordinator binary with
`go:embed`. No assets are generated at runtime; `go test ./...` must work
without Node/npm.

## 6. UI surface and routes

### HTML routes

| Route | Purpose | Availability |
| --- | --- | --- |
| `GET /ui` | Dashboard: readiness, recent jobs, workers, quick actions. | WUI-03 |
| `GET /ui/jobs/new` | New-run form and parameter help. | WUI-04 |
| `GET /ui/jobs/{id}` | Job detail and polling shell. | WUI-03 |
| `GET /ui/jobs/{id}/artifacts/{artifact_id}` | Authorized download proxy with attachment headers. | WUI-05 |

### JSON routes used only by the pages

| Route | Response / action | Notes |
| --- | --- | --- |
| `GET /ui/api/overview` | readiness, recent jobs, workers | No secrets, no storage paths. |
| `GET /ui/api/jobs` | cursor/page of compact job cards | Default 20, deterministic `created_at DESC, id DESC`. |
| `POST /ui/api/jobs/upload` | validates form, streams dataset, returns `201 {job_id}` | Same input limits as `/jobs/upload`; form fields first, file last. |
| `GET /ui/api/jobs/{id}` | job detail, counters, tasks, allowed artifacts | Polling endpoint, no raw DB model. |
| `GET /ui/api/jobs/{id}/events` | **deferred** | Start with polling; no SSE/WebSocket in v1. |

All `/ui` routes use UI authentication. Existing worker API routes keep worker
authentication and are not relaxed for the browser.

## 7. Read models and data minimisation

Create UI-specific DTOs; do not return domain/database entities directly.

```text
JobCard:
  id, workload, created_at, status,
  total, pending, leased, running, completed, failed

JobDetail:
  JobCard fields,
  parameters (allowlisted/redacted),
  tasks: [{id, chunk_index, status, attempt, max_attempts,
           lease_owner_display, lease_expires_at, error_code, error_message}],
  artifacts: [{id, kind, filename, size_bytes, sha256, downloadable}]

WorkerCard:
  id, name, capabilities, status, last_heartbeat_at, created_at
```

Rules:

- Display a shortened UUID by default but provide a copy button with the full
  value; never interpolate it into HTML.
- Do not expose `storage_key`, absolute artifact paths, raw metrics containing
  unexpected values, auth configuration, or worker-local directories.
- An artifact is downloadable only when it belongs to the requested job. A
  `final_result` is downloadable only after the job is `completed`; partial
  artifacts are marked diagnostic.
- SQL uses explicit columns, pagination/cursors, deterministic ordering, and
  joins constrained by `job_id`.

## 8. Workload forms and validation

### 8.1 Common fields

- TSV file, required, streamed; show expected columns
  `chembl_id` and `canonical_smiles`.
- `chunk_rows`: integer 1--100000, default 1000.
- `max_rows`: optional positive integer. The coordinator creates shards only
  from the first N data rows, so a user can test a large upload without
  creating thousands of tasks. It does not truncate the stored source blob.
- optional human-readable run name is a later schema/API addition; v1 does not
  silently store it.
- display file name and client-side size only as convenience; server limits and
  validation remain authoritative.

### 8.2 Similarity search

Inputs: exactly one `query_smiles` or `query_id`, `top_k`, optional threshold,
threshold direction, `max_rows`, and `progress_every`.

The current upload form accepts `query_smiles`, because resolving a
cross-shard `query_id` has not yet been connected to coordinator uploads. The
detail page calls an artifact a **partial top-k CSV** until all shards are
complete and CTX-09 reduction stores the final global result.

### 8.3 Similarity graph

Inputs: threshold, threshold direction, block size, `max_rows`, and progress
interval. The form may show a disabled **Experimental — not globally reduced**
card, but it must not submit multi-shard graph jobs until CTX-10 implements
block-pair planning and deterministic reduction. A one-shard pipeline check is
allowed only behind an explicit acknowledgement and produces a diagnostic edge
list, not a final graph.

Validation exists in three places: HTML constraints for feedback, a small
JavaScript schema for form behaviour, and authoritative Go validation mapped to
typed workload parameters. Never pass arbitrary parameter maps straight from
the browser to workers.

## 9. Delivery packages

Each package is a separate PR/task context. Do not start a later package until
its listed dependency and tests are green.

### WUI-00 — Freeze UI contract and demo scope

**Depends on:** current `main`.

**Deliver:** this plan, canonical workload naming decision, and updates to
`docs/api-contract.md`/`docs/openapi.yaml` if names or statuses change.

**Acceptance:** API has a precise distinction between diagnostic artifacts and
final results; UI security model has a distinct token; CTX-07/09/10 limitations
are visible.

### WUI-01 — UI read models and PostgreSQL queries

**Depends on:** WUI-00.

**Deliver:** repository/use-case methods for deterministic job lists, sanitized
job details, task summaries, artifact metadata, and worker lists. Add indexes
only if `EXPLAIN ANALYZE` on a realistic list query shows need.

**Acceptance:** no N+1 query path; no storage key/secret leaks; unknown job is
404; artifact lookup is constrained to its job; Go unit plus real-PostgreSQL
integration tests cover ordering, empty lists and ownership boundaries.

### WUI-02 — UI auth and embedded asset foundation

**Depends on:** WUI-01.

**Deliver:** `UI_AUTH_TOKEN` config validation, Basic Auth middleware, embedded
template/static serving, security headers, and an API-only fallback when UI is
disabled.

**Acceptance:** worker token never authorizes `/ui`; UI token never authorizes
worker routes; `/ui` is 404 when disabled; HTML/content-security headers are
tested; no token appears in logs or errors.

### WUI-03 — Read-only dashboard and job detail

**Depends on:** WUI-01, WUI-02.

**Deliver:** dashboard, worker table, job list, detail page, two-second polling
with pause when the tab is hidden, error/retry display, and accessible empty/
loading/error states.

**Acceptance:** a manually created job changes from pending to running on the
page without reload; all text is escaped; polling stops on terminal states;
task attempts and sanitized errors are visible; handler/template tests cover
XSS-shaped names and errors.

### WUI-04 — New-run upload form

**Depends on:** WUI-02, WUI-03.

**Deliver:** workload-specific form, typed Go validation, streamed upload,
progress/submit state, redirect to job detail, and copyable worker launch
instructions.

**Acceptance:** invalid query combinations fail with 400 and clear UI feedback;
valid small similarity-search upload creates deterministic shard count; upload
limits are enforced; browser never sees worker auth; test covers malformed TSV,
large/invalid fields, duplicate form fields, and failed coordinator storage.

### WUI-05 — Safe artifact downloads and diagnostic preview

**Depends on:** WUI-01--04.

**Deliver:** job-scoped download proxy, CSV preview limited by byte/row count,
checksum/size metadata display, and prominent partial/final labels.

**Acceptance:** artifact from another job is 404; path traversal cannot select a
file; `Content-Disposition` is safe; preview never loads an unbounded CSV; no
final-result button exists before CTX-09.

### WUI-06 — Final-result UX after CTX-09 — implemented

**Depends on:** CTX-09 and WUI-05.

**Deliver:** `reducing` state, final artifact card, final CSV preview/download,
and deterministic result metadata.

**Acceptance:** final link is shown only for `completed`; reducer failure is
sanitized; page refresh/restart preserves final artifact; end-to-end test
compares downloaded final search CSV with local reference output.

### WUI-07 — Full similarity-graph UX after CTX-10

**Depends on:** CTX-10 and WUI-06.

**Deliver:** enabled graph form, block-pair planning summary, graph-specific
progress, final edge-list preview/download, and warnings for low thresholds.

**Acceptance:** graph result equals local brute force on a small fixture; no
dense matrix is introduced; threshold direction is displayed and preserved;
result order is deterministic.

### WUI-08 — Manual-demo script and CI browser checks

**Depends on:** WUI-05; extend after WUI-06/07.

**Deliver:** `make demo-ui` (or documented compose profile), a tiny tracked TSV
fixture, start/stop instructions, and headless browser smoke tests.

**Acceptance:** clean checkout can start coordinator, a worker, open UI,
submit fixture, observe task completion, and download a diagnostic artifact.
CI covers auth, upload, polling JSON, job isolation, and download permission.

## 10. Testing strategy

| Layer | Required checks |
| --- | --- |
| Go domain/use case | status projection, artifact/job ownership, pagination ordering, redaction. |
| Go HTTP | UI auth separation, malformed multipart, CSRF-safe same-origin policy, 404/401, headers, escaping. |
| PostgreSQL | fresh migrations, list/detail query ordering, cross-job artifact denial, completed/final state. |
| Browser | form validation, upload success/failure, polling transition, terminal state, safe text rendering. |
| End-to-end | real PostgreSQL + coordinator + Python worker + small fixture; verify actual bytes/checksum. |

Use Playwright only if it can run in CI without adding a production runtime
dependency. Otherwise start with Go handler tests and a small `curl`/HTML
smoke script, then add browser automation in WUI-08.

## 11. Manual verification script after WUI-05

```sh
# terminal 1
cd coordinator
UI_AUTH_TOKEN='local-ui-secret' make up

# terminal 2: use a separate worker token; do not paste it into the browser
SCIMESH_COORDINATOR_URL=http://localhost:8080 \
SCIMESH_BEARER_TOKEN='worker-secret' \
scimesh-worker --work-dir ./worker-data

# browser
# http://localhost:8080/ui
# authenticate with the UI token, upload a tiny TSV, then watch /ui/jobs/{id}
```

The actual configuration variable names, compose wiring, and launch command are
implemented in WUI-02/WUI-08; this block is the acceptance target, not a claim
that the interface exists today.

## 12. Explicit non-goals for the initial interface

- no database browser or SQL console;
- no browser-side RDKit or scientific calculation;
- no worker start/stop shell execution from UI;
- no multi-user accounts, RBAC, password reset, public internet exposure, or
  user-provided storage credentials;
- no WebSocket/SSE, React/Vue, Docker requirement for the Python package, or
  deployment platform;
- no claim that a distributed graph or search result is final before its
  planner/reducer acceptance criteria are met.

## 13. Definition of done for the first hand-testable release

The hand-testable release is complete when a clean local checkout can run a
trusted, authenticated local UI; display workers, jobs, pipeline stages, tasks
and safe errors; submit a valid small search; poll it through `reducing`; and
download the coordinator-owned final CSV only after completion. The page must
make the distinction between partial diagnostics and the final result
impossible to miss.
