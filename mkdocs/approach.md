# Documentation approach

These rules are the contract for this site. Every page must follow them;
reviewers enforce them.

## 1. Purpose and scope

This MkDocs site documents **the Workload SDK**: how to use it, how to
author workloads, and the complete `scimesh.sdk` API. It does **not** host
the project's internal documentation (contracts, task briefs, planning
documents) — those live in the repository's `docs/` directory and are not
part of the site. Where a guide needs a contract detail, link to the
repository file in prose; do not copy its content.

## 2. Page types and status markers

Every page starts with one of:

- **concept** — explains a model (for example the SDK overview);
- **guide** — how to accomplish a task (authoring workloads, CLI, worker);
- **reference** — generated API documentation, never hand-written.

Guides may be marked with an admonition when a section describes future or
fail-closed behavior:

```markdown
!!! warning "Not yet supported"
    The v1 coordinator contract does not persist resource requirements...
```

## 3. The reference is generated, not written

- `api/` pages contain only mkdocstrings directives
  (`::: scimesh.sdk.<module>`); editing them by hand is an error.
- Public API must be documented in **Google-style docstrings** in the code:
  `Args:`, `Returns:`, `Raises:`.
- Every validation failure and fail-closed path must be documented in the
  docstring.
- After any docstring change, rebuild: `make docs`.

## 4. Terminology

Use the single glossary from `PLAN.md`:

| Term | Meaning |
| --- | --- |
| Job / Run | A user-requested full computation |
| Task | One independently executable unit of a job |
| Attempt | A lease for one task execution |
| Artifact | A durable input, shard, partial, or final result |
| Workload | A user script (package) built on the SDK |

Never introduce synonyms (`pipeline`, `run` for task, etc.). The word
"workload" in this site means an SDK workload (a user script), not a
"workload" in the performance sense.

## 5. Code and output conventions

- Use language-tagged fenced blocks: ```python, ```bash, ```text, ```json.
- Never include local machine paths, tokens, or private data in examples.
- Show complete runnable examples; prefer the real built-in workloads
  (`molwt-filter`, `descriptor-batch`) over invented ones.
- Keep command output minimal and accurate; regenerate it, don't retype it.

## 6. Linking

- Relative links inside `mkdocs/` (for example `../api/sdk-batch.md`).
- Repository files outside the site (`docs/`, `PLAN.md`) are referenced in
  prose with their path, not linked as site pages.
- Every guide must link to the relevant API pages.

## 7. When to write or update

- **New workload** → update `sdk/authoring-workloads.md` examples and the
  UI workload catalog (`make workloads-export`).
- **SDK API change** → update docstrings; the reference rebuilds.
- **Worker/coordinator behavior change** → update
  `sdk/worker-integration.md` and the fail-closed warnings.
- **New CLI surface** → update `sdk/cli.md`.
- Behavior changes without documentation updates are incomplete changes.

## 8. Build and verification

```bash
make docs          # build into site/
make docs-serve    # http://localhost:8000
```

- `mkdocs build` must succeed with no errors.
- New or changed pages must render (check the generated HTML, not just the
  markdown).
- The site is served inside the coordinator UI at `/ui/docs/`; the demo
  mounts `site/` automatically.
