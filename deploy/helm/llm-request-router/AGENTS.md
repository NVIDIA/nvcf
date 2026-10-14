# LLM request router chart

Keep templates and values consistent with the existing chart helpers. Extend
the render checks under `scripts/` when changing generated resources.

## Build and test

Run from this directory:

```sh
helm lint ./llm-request-router
make test
git diff --check
```

## Pull Request gates

Meaningful Stargate chart changes must satisfy the
[Stargate mock integration and performance gates](../../../src/libraries/rust/stargate/AGENTS.md#mandatory-pull-request-validation).
Deploy the candidate chart in the chosen runtime mock test. Render checks alone
do not validate registration, transport, readiness, or request handling.
Resource, scheduling, and router-policy changes require baseline/candidate
performance evidence.

Follow the root local QA safety rules before changing a test environment.
Use plain ASCII in committed prose and keep secrets out of render output.
