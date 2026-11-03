# Stargate development stack

This stack owns the mock deployment charts, environments, load profiles, and
deployment scripts. Keep changes within the existing Helmfile and Python
patterns. Image builds and local/AWS setup are documented in [README.md](README.md).

## Build and test

Run from the repository root:

```sh
python3 -m unittest discover -s deploy/stacks/stargate-dev/tests
make -C deploy/helm/llm-request-router test
git diff --check
```

For the benchmark controller, also follow [benchmark/AGENTS.md](benchmark/AGENTS.md).

## Pull Request gates

Meaningful stack changes must satisfy the
[Stargate mock integration and performance gates](../../../src/libraries/rust/stargate/AGENTS.md#mandatory-pull-request-validation).
Rendering and fake-runner checks validate wiring; retain a runtime mock receipt
for the changed deployment behavior. Scheduling, resources, engine settings,
and load profiles also require baseline/candidate performance evidence.

Follow the root [local QA safety rules](../../../AGENTS.md#local-qa-and-testing-environment-safety)
before provisioning or modifying test environments. Campaigns that share
backends must run exclusively because they can reset caches and restart Pods.

Use plain ASCII in committed prose. Keep credentials and private environment
details out of public files and Pull Request evidence.
