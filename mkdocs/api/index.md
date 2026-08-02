# API reference

This section is **generated from docstrings** by
[`mkdocstrings`](https://mkdocstrings.github.io) — it is the complete public
API surface of `scimesh.sdk`. Markdown pages in `api/` are thin wrappers
(`::: scimesh.sdk.<module>`) and must not be hand-edited; change the code and
rebuild with `make docs`.

All value objects are frozen, recursively immutable, JSON-safe, canonically
serialized, and strict about unknown fields. Constructing them performs
full validation; invalid input raises `ValueError`.

## Module map

| Page | Module | Contents |
| --- | --- | --- |
| [Artifacts and ports](sdk-artifacts.md) | `scimesh.sdk.artifacts` | `ArtifactSchema`, `PortSpec`, `ArtifactRef`, `ArtifactCollection`, `OutputManifest`, `Provenance` |
| [Batch scaffold](sdk-batch.md) | `scimesh.sdk.batch` | `MapReduceWorkload`, `concatenate_partial_tables` |
| [Conformance runtime](sdk-conformance.md) | `scimesh.sdk.conformance` | `LocalArtifactStore`, `LocalCoreBatchExecutor`, scoped contexts, round-trip helper |
| [Execution profiles](sdk-execution.md) | `scimesh.sdk.execution` | `ExecutionProfile`, `RetryPolicy`, `CheckpointPolicy`, `FailureReport` |
| [Identities](sdk-identity.md) | `scimesh.sdk.identity` | `WorkloadId`, `VersionRange`, `SchemaRef`, `ComponentRef`, `FeatureRequirement` |
| [Package integrity](sdk-integrity.md) | `scimesh.sdk.integrity` | `installed_distribution_digest` |
| [Manifests](sdk-manifest.md) | `scimesh.sdk.manifest` | `WorkloadManifest`, `PackageSpec`, `EnvironmentSpec`, `VerifierSpec`, `WorkloadLimits`, trust/determinism enums |
| [Plans and tasks](sdk-plans.md) | `scimesh.sdk.plans` | `JobRequest`, `ValidatedJob`, `TaskSpec`, `WorkflowPlan`, `ExpansionManifest` |
| [Handler protocols](sdk-protocols.md) | `scimesh.sdk.protocols` | `Planner`, `Runner`, `Reducer`, `Verifier`, contexts, catalog/sink |
| [Registry](sdk-registry.md) | `scimesh.sdk.registry` | `WorkloadRegistry`, `WorkloadDefinition`, `AllowedPackage`, discovery |
| [Resources](sdk-resources.md) | `scimesh.sdk.resources` | `ResourceRequirements`, `ResourceInventory`, `ResourcePool`, accelerators |
| [Runtime negotiation](sdk-runtime.md) | `scimesh.sdk.runtime` | `RuntimeCapabilities`, `negotiate_manifest`, `CompatibilityError` |
| [Parameter schemas](sdk-schema.md) | `scimesh.sdk.schema` | Bounded JSON Schema subset |
| [Verification](sdk-verification.md) | `scimesh.sdk.verification` | Verifiers, decisions, bindings, candidate envelopes |
| [Workflow DAGs](sdk-workflow.md) | `scimesh.sdk.workflow` | `WorkflowSpec`, `StageSpec`, `ArtifactEdge`, advanced declarations |

## Reading the generated pages

- **Classes** show their full signature, validation rules, and public
  methods; properties are listed with their type.
- **Module-level functions** (for example `negotiate_manifest`) document
  their exact contract and failure modes.
- Cross-references to other SDK symbols link automatically.

To keep the reference correct:

- write docstrings in **Google style** (`Args:` / `Returns:` / `Raises:`);
- document validation failures and fail-closed behavior;
- rebuild with `make docs` after any docstring change.
