# Worker implementation: feedback for the next iteration

## Purpose

This is a technical debrief of the first `Workers` implementation. It is not a
blame document. Its purpose is to give an AI-assisted developer a compact set
of rules that prevents the same integration defects from returning when the Go
coordinator and PostgreSQL queue are implemented.

The worker must be treated as one participant in a distributed protocol, not
as an isolated Python script. A change is only complete when the worker,
coordinator API, durable storage, documentation and tests agree on the same
contract.

## What was good

- The worker was separated into configuration, daemon, coordinator client,
  artifact transport, runner and models. That is a sound seam for later Go
  integration.
- Input checksum validation, task-attempt directories, lease heartbeats,
  bounded failure messages, jittered polling, and mock-based tests were good
  instincts.
- The daemon does not talk to PostgreSQL directly. Keeping queue ownership in
  the coordinator is the right boundary.

Keep these properties. The corrections below are about completing the protocol
rather than changing that general direction.

## Findings from the review

### 1. A worker-local result URI was presented as a completed result

**Initial behaviour.** The worker submitted a `worker://...` URI after local
execution.

**Why this is wrong.** The coordinator, reducer and UI cannot read a file that
exists only on a worker machine. A task cannot be considered complete merely
because a worker created a local CSV. The result disappears when that worker
is removed or its workspace is cleaned.

**Correct rule.** Upload every output artifact to coordinator-managed durable
storage first. Only then submit a manifest containing the URI returned by the
upload endpoint, SHA-256 and content type. A result endpoint must reject a
`file://` or `worker://` URI.

**Required order.**

```text
run task locally
  -> compute SHA-256
  -> PUT artifact to coordinator storage
  -> coordinator returns durable URI
  -> POST result manifest
  -> coordinator marks task completed
```

**Regression test.** Test that a completion payload contains the URI returned
by `ArtifactClient.upload`, and that the worker performs no final `submit` if
an upload fails.

Status: fixed in `Workers`; enforce the same rule server-side.

### 2. Failures were sent to the success endpoint

**Initial behaviour.** Exception handling reported a payload with a failed
status to `/tasks/{id}/result`.

**Why this is wrong.** Completion and failure have different queue semantics.
A completion means the result is durable and can trigger reduction. A failure
may require a retry, backoff, attempt increment, or a terminal error. Combining
them makes it too easy for the coordinator to accept an invalid state
transition.

**Correct rule.** Use `POST /tasks/{id}/result` only for a successful,
fully-uploaded result. Use `POST /tasks/{id}/failure` for a safe failure
report. Include `worker_id`, `attempt`, a stable `error_code`, and a short
sanitised `error_message`.

**Regression test.** Make a runner raise; assert that `/failure` is called and
`/result` is never called.

Status: fixed in `Workers`; the Go service must validate the transition.

### 3. A heartbeat renewed the lease but the new expiry was discarded

**Initial behaviour.** The client treated a successful heartbeat as a boolean
and retained the old `lease_expires_at` value.

**Why this is wrong.** The worker used that stale timestamp to schedule future
heartbeats. Long-running work could then incorrectly decide that a valid lease
was expired, or choose an unsafe heartbeat interval.

**Correct rule.** A successful heartbeat must return the new canonical
`lease_expires_at` timestamp. Replace the stored expiry immediately and derive
the next heartbeat delay from it. Treat a missing or malformed expiry as a
protocol error.

**Regression test.** Return two different expiries from a fake coordinator;
verify that the second scheduling calculation uses the renewed value.

Status: fixed in `Workers`; documented as a required coordinator response.

### 4. The API contract was implemented in code before it was made explicit

**Symptom.** The client and task documents initially omitted or disagreed on
artifact upload, failure reporting and the heartbeat response.

**Why this is dangerous.** Two developers can write apparently reasonable
code that never interoperates. This is especially likely across Python and Go,
where type systems do not share the contract automatically.

**Correct rule.** Before coding an endpoint, update the contract table and
provide request and response examples. State:

- method and path;
- authentication and worker identity;
- mandatory JSON/body fields and types;
- success statuses and response body;
- idempotency behaviour;
- invalid ownership, stale attempt and expired-lease responses;
- whether the endpoint changes task state.

The source of truth is [PLAN.md](../PLAN.md) plus the task-specific database
and worker documents. If a code change alters the protocol, update all of
them in the same commit.

### 5. Client-side checks were treated as sufficient protection

**Problem.** Python checks for input hash, task attempt and lease timing are
helpful but are not authoritative. A buggy, stale or malicious worker can
still send a request.

**Correct rule.** The Go coordinator must make all authoritative decisions in
one database transaction: task is leased, `lease_owner` equals the caller,
attempt matches, lease is unexpired, and the requested state transition is
allowed. The worker is a client; it is never the queue authority.

**Server tests.** Verify that another worker, a stale attempt and an expired
lease cannot upload artifacts, heartbeat, complete or fail the task.

### 6. Happy-path testing hid cross-boundary defects

**Problem.** Local mocks can make an endpoint mismatch invisible: the mock
accepts a payload that the actual Go coordinator has not implemented.

**Correct rule.** Keep fast unit tests, but add contract and integration tests
as soon as the coordinator exists:

- Python worker against a disposable Go coordinator and PostgreSQL database;
- claim → heartbeat → download → upload → result lifecycle;
- runner failure → failure endpoint → retry/terminal policy;
- duplicate delivery of result and failure payloads;
- coordinator restart with a leased task;
- unauthorized and cross-origin artifact cases.

Every bug fixed at a boundary needs a regression test at that boundary, not
only an internal unit test.

### 7. Transport security must be deliberate

**What must not happen.** A coordinator bearer token must not follow an HTTP
redirect to arbitrary object storage or another host.

**Correct rule.** Attach the coordinator bearer token only to same-origin
requests. Strip it on cross-origin redirects. Validate that coordinator URLs
are absolute `http` or `https` URLs. Never place a permanent token in task
payloads, logs or artifact URIs.

Status: same-origin redirect protection and upload URL validation are now in
the Python worker. The coordinator still needs its own authentication and
authorization rules.

### 8. Idempotency and retries need a written policy, not assumptions

**Problem.** Network timeouts make it unknowable whether the coordinator
processed a request. Retrying can create duplicate artifacts or incompatible
state changes unless both sides define what is safe.

**Correct rule.** The tuple `(task_id, attempt, worker_id)` identifies the
lease. Make completion, failure and artifact upload idempotent for that lease.
The coordinator should return a stable success response for an identical
duplicate and a conflict for a different worker or attempt. Do not quietly
overwrite an artifact belonging to a different attempt.

**Do not do this.** Do not implement a retry loop that blindly repeats every
POST because it "usually works". Classify timeout/5xx, conflict, validation,
and authorization responses first.

### 9. Readability matters at protocol boundaries

**Observed risk.** Dense single-line payloads and broad exception blocks make
it hard to review fields such as `attempt`, artifact checksum or result URI.
These are correctness fields, not cosmetic details.

**Correct rule.** Use named payload builders or multi-line dictionaries for
network messages. Keep methods short enough that the lifecycle is visible in
order: claim, lease, download, validate, run, upload, submit/fail, cleanup.
Use broad exception handling only at the daemon boundary, then report a
sanitised error. Do not silently swallow failures that should stop a task.

## Mandatory pre-PR checklist for a worker change

### Contract

- [ ] The task/response JSON matches the documented API exactly.
- [ ] Worker ID, task ID and attempt are present where ownership is required.
- [ ] Heartbeat returns and the worker uses the renewed expiry.
- [ ] Result, failure and artifact endpoints have different, documented roles.
- [ ] Success and error status codes are handled intentionally.

### Correctness

- [ ] The runner computes only within its task-attempt directory.
- [ ] Input SHA-256 is checked before workload execution.
- [ ] Every output is uploaded before completion is submitted.
- [ ] Completion contains a durable URI, SHA-256 and content type.
- [ ] No result is submitted after a lost heartbeat/lease.
- [ ] The task's own identifier and attempt are never guessed or replaced.

### Resilience and security

- [ ] Transient coordinator failures use bounded backoff and do not spin.
- [ ] Failure reports have a bounded, sanitised message; no traceback,
      workspace path, bearer token or secret is included.
- [ ] Authorization is sent only to the coordinator origin.
- [ ] Duplicate delivery and stale attempts have defined outcomes.
- [ ] Cleanup does not delete another task's or another attempt's files.

### Validation

- [ ] Unit tests cover successful run, checksum mismatch, runner failure,
      upload failure, heartbeat failure and empty runner output.
- [ ] A test asserts no `worker://` or `file://` URI reaches completion.
- [ ] A test asserts that failure uses `/failure`, never `/result`.
- [ ] A test asserts that the renewed heartbeat expiry is used.
- [ ] Formatting, type checking and the full test suite pass.
- [ ] If the protocol changed, Go coordinator integration tests pass too.

## How to approach the next task

1. Read the relevant `CTX-*` section in [PLAN.md](../PLAN.md), plus the
   worker and database task documents.
2. Write down the exact request/response and state transition before changing
   Python or Go code.
3. Implement the smallest vertical slice across both boundaries.
4. Add a regression test for the unhappy path first discovered by review.
5. Review the diff specifically for data ownership, lease/attempt checks,
   durable artifacts, retry behaviour and secrets in logs.
6. Only then expand functionality or refactor.

## Short version for an AI coding prompt

> SciMesh workers are untrusted clients of a coordinator-owned queue. Never
> complete a task until outputs are durably uploaded through the coordinator.
> Keep success and failure endpoints separate. Every lease-sensitive request
> carries worker ID and attempt, and the coordinator validates both
> transactionally. A heartbeat returns a new expiry that the worker must use.
> Treat retries as an idempotency design problem, not as a blind loop. Update
> code, API documentation and regression tests together.
