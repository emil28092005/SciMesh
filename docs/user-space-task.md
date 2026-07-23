# Task: improve the operator user space

## Assignment

You are the senior developer responsible for the **user space**: the
human-facing local operator interface served by the Go coordinator. In this
task, “user space” means a clear UI and workflow for a trusted local operator;
it does **not** mean public accounts, registration, roles, multi-tenancy, or
remote deployment.

Create a small, coherent improvement to the existing UI so a person can
understand and operate a SciMesh pipeline without reading API payloads or
coordinator logs. Keep all interface copy in English.

## Read first

1. `AGENTS.md`
2. `.agents/coordinator.md` and `.agents/integration.md`
3. `docs/web-interface-plan.md`
4. `docs/api-contract.md`
5. `STATUS.md` and the current `coordinator/internal/transport/http/ui.go`

## Current baseline

`main` already provides local Basic Auth (`UI_AUTH_TOKEN`), a dashboard, job
submission for diagnostic similarity-search runs, task progress, partial CSV
downloads, a stop-job action, and an optional dataset row limit. A job is a
**pipeline check** until CTX-09 adds a reducer; individual shard CSVs are not a
final scientific result.

## Scope

Improve the end-to-end operator journey:

- make the dashboard explain service readiness, workers, jobs, and the next
  safe action in plain English;
- make job creation validation and success/failure feedback understandable;
- make job detail clearly distinguish queued, running, failed, stopped, and
  completed pipeline checks;
- keep polling and all user-visible states reliable after a page refresh;
- expose actionable, sanitized failure guidance without leaking paths, tokens,
  SQL errors, or tracebacks;
- document the workflow in `coordinator/README.md` or `README.md`.

Use server-rendered Go templates, embedded assets, and small vanilla
JavaScript only. Preserve the existing worker API and Basic Auth boundary.

## Explicitly out of scope

- user accounts, sign-up, roles, sessions, OAuth, or multi-tenancy;
- executing or controlling workers from the browser;
- direct browser access to PostgreSQL or worker endpoints;
- final-result reduction, distributed workload planning, or graph execution;
- artifact CSV preview/visualisation. That is assigned independently in
  `docs/artifact-preview-task.md`.

## Security and protocol rules

- `UI_AUTH_TOKEN` is never sent to HTML, JavaScript, URLs, logs, or storage.
- Use `html/template`; JavaScript must use `textContent`, never `innerHTML` for
  received data.
- UI artifact operations must be job-scoped and coordinator-owned.
- Do not disclose raw worker commands, local paths, bearer tokens, or database
  errors.
- Do not change worker/coordinator API contracts silently. Document any
  intentional API change in `docs/api-contract.md` and `docs/openapi.yaml`.

## Acceptance criteria

- A new operator can start the stack, authenticate, submit a small run, start
  workers, understand live progress, and safely stop a job from the UI.
- The UI never calls or displays worker bearer-token endpoints.
- All partial output is visibly labelled as diagnostic until a reducer exists.
- Disabled UI remains `404`; unauthenticated UI requests remain rejected.
- Go tests cover changed routes and states, including auth and a sanitized
  error case.
- `go test ./...`, `go vet ./...`, and the relevant real-PostgreSQL tests pass.

## Handoff

Use one branch and one PR. In the PR description state the user journey that
changed, screenshots if visual layout changed, API impact (`none` if none),
and exact test commands/results. Do not stage local datasets or `worker-data/`.
