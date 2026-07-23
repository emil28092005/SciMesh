# Task: safely preview partial CSV artifacts in the UI

## Assignment

You are the junior developer implementing one contained UI feature: an
authenticated operator can preview a small portion of a CSV artifact belonging
to the job they are viewing. This is a diagnostic aid, not a final-results
page.

## Read first

1. `AGENTS.md`
2. `.agents/coordinator.md`
3. `docs/web-interface-plan.md`
4. `docs/api-contract.md`
5. `coordinator/internal/transport/http/ui.go` and its tests

## Current baseline

The coordinator serves an authenticated local web UI. Job detail pages already
list partial result artifacts and provide job-scoped downloads. The browser has
no worker token and must not learn storage paths. A partial shard CSV is never
a global or final molecular-search result.

## Scope

Add a **Preview** action next to eligible CSV artifacts on a job detail page.

- Preview only artifacts that belong to the requested job.
- Show at most the first **30 rows** and read at most **64 KiB** from storage.
- State clearly when content was truncated.
- Preserve the existing download action.
- For a non-CSV artifact, return a friendly, sanitized explanation rather than
  attempting to render bytes as text.
- Use an existing UI route pattern or add a small UI-authenticated endpoint;
  keep it separate from worker API routes.

## Security rules

- Require UI Basic Auth for every preview request.
- Verify job ownership in the coordinator before opening the artifact; an
  artifact ID from another job must not be previewable.
- Never expose `storage_key`, filesystem paths, database errors, bearer tokens,
  or worker-local information.
- Do not use `innerHTML` for CSV fields. Use `html/template` escaping or
  `textContent` so strings such as `<script>alert(1)</script>` are displayed as
  data, not executed.
- Do not load the complete artifact into memory.

## Out of scope

- Charts, molecule imagery, RDKit rendering, client-side CSV libraries, React,
  and a new frontend service.
- Changing job/task state, retrying tasks, or implementing reducer output.
- Redesigning the broader dashboard or job-creation workflow; that belongs to
  `docs/user-space-task.md`.

## Suggested implementation shape

Keep UI transport, use case, and storage responsibilities separate. Return a
small view model containing artifact metadata, column headers, rows, and a
`truncated` flag. Reuse existing coordinator-owned artifact access rather than
reading a path supplied by the browser. Keep the handler streaming/limited.

## Acceptance criteria

- A valid partial CSV can be previewed from its own job detail page.
- The result shows no more than 30 data rows and marks 64 KiB/row truncation.
- Empty and malformed CSV content fail safely with a clear message.
- A non-CSV artifact is rejected safely.
- Unauthenticated access is rejected; a cross-job artifact request is not
  disclosed or served.
- HTML-like values are escaped in the rendered preview.
- Existing artifact downloads still work.
- Add Go tests for all cases above and run `go test ./...` and `go vet ./...`.

## Handoff

Work in one focused branch and one PR. Report files changed, any API impact,
test commands/results, and known limitations. Do not commit datasets, generated
CSV files, Docker volumes, `.venv`, or `worker-data/`.
