# Identity and service pricing: development candidate

This implementation separates account identity from service tier. Effective component price is the base model component price multiplied by the service factor and the identity factor. Model-specific factors replace their respective defaults; missing entries inherit, while explicit zero is free. Authorization is explicit and separate from pricing.

## Activation boundary

The runtime remains in `legacy` mode. `ValidateForStorage` rejects `identity_service` with `ErrActivationPending`; validation responses report `activation_ready: false`. There is no supported activation switch in this candidate. Draft editing and quote previews do not authorize model calls or migrate existing billing.

Per-model channel routing consistency changes are separate and are not disabled by this mode boundary. Review [routing semantics](channel/model-routing.md) before running the candidate.

## Implemented scope

- Versioned configuration with compare-and-swap writes and protection against generic option-write bypass.
- Explicit service/model and identity/model eligibility, preserving existing user and token restrictions.
- Frozen identity, service factors and price snapshots for synchronous and durable asynchronous billing.
- Configuration, current quote and historical frozen-quote interfaces. Historical display does not borrow the current price catalog; older schema-dependent displays can be less detailed.
- Independent ordinary-role accounts with identity changes and owner-bound self-service keys; no new administrator key-issuance flow.

## Remaining acceptance and rollout gates

Focused local tests and bounded reviews do not establish production readiness. Full-process restart validation remains distinct from reopening a database. SQLite lock flakiness and existing full-suite race failures remain unresolved; no claim of globally race-free behavior is made.

Before activation, validate backup/restore, complete authorization and component-price migration equivalence, in-flight task isolation, rollback compatibility, runtime cache behavior and applicable custom monetary settlement semantics. Independent violation fees can conflict with a free-price promise and must be resolved explicitly. Retain the storage rejection until these gates and explicit activation authorization are satisfied.

Source integration, image construction, environment deployment and mode activation are separate events. No production data or private operational evidence belongs in this document.
