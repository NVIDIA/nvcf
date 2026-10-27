# docs-gds

Design and intermediate documents for the Global Discovery Service (GDS) of the Dynamo Global
Router. Everything about GDS is written here; the sibling design docs in the parent directory
in ~/projects/nvidia are left untouched.

- `global-discovery-service-design.md`: the design (requirements, architecture, data flows,
  API, storage, controller, clients, integration, testing, risks, alternatives).
- `implementation-plan.md`: the approved step-by-step implementation plan (files, ordering,
  verification), kept in sync with the plan used by the coding session.
- `network-interaction-comparison.md`: how Stargate and Pylon interact today versus the HLD
  components, at the network level, and what that implies for GDS and MCS, including the
  cross-region request path.
- `qa-gds-value-for-llm-stack.md`: Q&A on why GDS and MCS help the unmodified LLM stack and
  whether GDS can be consumed without MCS (discussion note, no design change).
- `upstream-dynamo-comparison.md`: the open upstream Dynamo discovery work (DEP 103, the
  global-discovery PRs, the global-router-v2 branch) compared with the GDS design and
  implementation, with recommendations and open questions (discussion note, no design change).
- `dynamo-integration-design.md`: the LLM stack (gateway, Stargate, Pylon, Pylon Operator) as
  the Global Router tier over Dynamo pools: the frontend contract (override, passthrough,
  query-then-pin), relay-fed stats for multi-model DGDs, affinity and KV awareness, HLD
  requirement mapping; consumes GDS as-is.

This directory lives in the nvcf monorepo on branch
`feat/global-discovery-service`, to be ported to `github.com/ai-dynamo/stargate`.
