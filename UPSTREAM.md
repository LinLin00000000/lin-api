---
schema_version: 1
component_id: new-api
provenance: external-upstream
repository_topology: github-fork
divergence_strategy: maintained-source-divergence
criticality: infra
visibility: public
owner: '@LinLin00000000'
upstream:
  locator: https://github.com/QuantumNous/new-api
  tracking_ref: refs/heads/main
  accepted_base: '36dbbf0f77e710455e745048f4a32e8120ad3fd2'
policy:
  cadence: on-demand
  merge_mode: guarded-after-baseline
  automation_baseline: pending
  offer_schedule_after_baseline: true
  product_posture: upstream-first-with-quality-floor
  max_automation: A2
  human_required_if: [risk>=R2, behavioral-conflict, behavioral-unknown, delta-scope-change, delta-retirement, invariant-change, product-tradeoff, production-deploy-outside-project-policy]
invariants:
  - id: INV-001
    statement: 'Preserve the upstream AGPLv3 license, required author attribution notice, and visible link to the original New API project.'
    validation_refs: ['LICENSE', 'README.md']
  - id: INV-002
    statement: 'Keep local changes narrow, auditable, and separable from upstream content so upstream updates remain reviewable.'
    validation_refs: ['README.md', 'UPSTREAM.md', 'git diff upstream/main...main']
  - id: INV-003
    statement: 'Production deployment requires the explicit project release policy; repository changes never authorize arbitrary restarts, data migration, or public exposure.'
    validation_refs: ['UPSTREAM.md', 'docs/development-and-release.md']
local_deltas:
  - id: D001
    intent: 'Present the downstream product as Lin API while retaining clear upstream provenance and an update-friendly README layout.'
    behavior_scope: 'Repository identity, maintenance documentation and Agent workflow policy; no runtime code change.'
    invariant_refs: [INV-001, INV-002]
    realization_refs: ['README.md', 'UPSTREAM.md', 'docs/development-and-release.md', 'AGENTS.md']
    retire_when: 'Lin API is retired or returns to an unmodified upstream distribution.'
  - id: D002
    intent: 'Build checked downstream images in GHCR from exact main or dev commits using the existing Dockerfile.'
    behavior_scope: 'Downstream CI, document-only change detection, native amd64 image publishing and isolated non-billable artifact smoke; private deployment remains outside this repository.'
    invariant_refs: [INV-001, INV-002, INV-003]
    realization_refs: ['.github/workflows/ci.yml', '.github/workflows/docker-image-branch.yml', '.github/scripts/runtime_changed.py']
    retire_when: 'The downstream no longer operates its own image publishing flow.'
validation:
  required_refs: ['README suffix byte comparison against accepted base', 'git diff --check', 'focused tests for each future runtime delta']
deployment_impact:
  policy_ref: 'docs/development-and-release.md; accepted main merges authorize the next maintenance window, subject to verification and migration gates.'
  release_policy_ref: 'AIOS upstream-reconciliation release contract: build-once/deploy-by-digest; merge, build, staging, production promotion, migration, and rollback are separate gates.'
  runbook_ref: 'Private OPS/service runbook owns runtime paths, deployment commands, and current state.'
release:
  artifact_identity: immutable-container-image-digest
  build_input: exact-source-commit
  production_source_mount: forbidden
  promotion: project-policy-and-environment-gate
  rollback: previous-known-good-artifact-and-compatible-config
  ui_api_coupling: 'web/dist is embedded into the Go binary by the production Docker build; frontend and backend promote and rollback atomically.'
---

# Lin API upstream adoption

Lin API is a personally maintained downstream fork of [QuantumNous/new-api](https://github.com/QuantumNous/new-api). It is not the official New API project and does not imply endorsement by its upstream maintainers.

## Baseline

The initial accepted base is commit [`36dbbf0f77e710455e745048f4a32e8120ad3fd2`](https://github.com/QuantumNous/new-api/commit/36dbbf0f77e710455e745048f4a32e8120ad3fd2), which is also tagged `v1.0.0-rc.31` in the fetched upstream repository. The fork tracks `upstream/main`, but updates are adopted only after an explicit diff review and risk-matched verification.

## Current local difference

The documentation and product-identity delta remains narrow:

- the repository is named **Lin API**;
- `README.md` contains a small Lin API header;
- `AGENTS.md` has a small Lin API workflow pointer above the preserved upstream conventions;
- the complete upstream README from the accepted base remains byte-for-byte unchanged below that header;
- this file records the canonical upstream relationship and local-delta policy.

The downstream also owns the CI/image-publishing delta D002. The `Lin API image` workflow runs on `main` or `dev` pushes, or an explicit main/dev dispatch. It checks the exact source, publishes only `linux/amd64` to the downstream GHCR namespace, and verifies the artifact without paid API calls. Known documentation-only pushes skip the application build. GitHub publishes the image; hosts pull it by digest. Unused upstream Docker Hub, desktop, and release workflows are disabled in the downstream repository's Actions settings; their upstream source files remain available for future reconciliation.

Future implementation changes must update `local_deltas` only when they become real. Planned work is not recorded as an active delta.

## Update workflow

1. Fetch the exact candidate revision from `upstream` without executing repository-provided hooks or lifecycle scripts.
2. Compare the candidate against `accepted_base` and every active invariant/local delta.
3. Prepare a focused branch or pull request; do not force-sync `main`.
4. Run the candidate's relevant build/tests plus focused Lin API regression checks.
5. Require human review for user-visible behavior, authentication, billing, pricing, routing, database migrations, branding/attribution, or deployment effects.
6. Merge source changes separately from any deployment, restart, data migration, or production cutover authorization.
7. After acceptance, update `accepted_base` to the exact immutable upstream commit and read back the public fork state.

## Release and production promotion

The project-specific source of truth is [Development and release](docs/development-and-release.md). Its standing release authorization replaces per-version approval within the declared scope; runtime automation is not enabled merely by documenting it.

The source repository and production runtime are deliberately separate:

- DEV preview and production use the same complete Docker build by default; no HMR is required. Production must not mount a changing source tree.
- Build from an exact source commit and identify the resulting production artifact by an immutable container-image digest. A branch, `latest`, or a successful build message is not the production identity.
- An authorized main merge grants next-window production promotion under [the project policy](docs/development-and-release.md), but is not evidence of deployment. Other pushes/builds do not grant production access; migrations, DNS changes and exposure remain independently bounded.
- Promote only the exact artifact that passed required isolated smoke/Invariant checks and the project-policy/environment gate. DEV serves as acceptance preview; no third persistent staging environment is required.
- This accepted layout embeds `web/dist` into the Go binary, so the frontend and backend are one atomic promotion and rollback unit. Splitting them later requires an explicit compatibility, health, and rollback contract.
- Runtime configuration, domains, database connections, and secrets remain outside the public artifact and are supplied by the private environment/Secret Runtime.
- A private release receipt binds `source_commit`, `artifact_digest`, runtime configuration revision, migration revision or `none`, promotion approval, and `previous_known_good`. It must not contain secret values or private infrastructure details.
- Rollback targets the exact previous-known-good artifact and compatible configuration. Irreversible migrations or uncertain data state return to Human review.

## Licensing and attribution

Lin API remains licensed under the repository's [GNU AGPLv3 license](LICENSE). Modified user interfaces must preserve the upstream Section 7 author-attribution notice and a visible link to the original project as stated in the upstream README. Lin API branding is additive and must not obscure upstream authorship or imply that this fork is official.

## Private operational boundary

Secrets, API keys, private infrastructure, live pricing state, user data, and deployment receipts do not belong in this public repository. They remain in the appropriate private runtime and operations sources. This repository contains source and public maintenance metadata only.
