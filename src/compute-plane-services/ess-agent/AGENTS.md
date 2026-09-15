# AGENTS.md - ess-agent

Native Go image-source subtree for the ESS Agent sidecar and init-container
used to fetch and render secrets from ESS.

## Build and Test

```bash
bazel build //...
bazel test //... --test_env=SKIP_INTEGRATION_TESTS=1 --flaky_test_attempts=3
bazel build //:image_index
bazel run //:image_load
```

Internal image push targets:

```bash
bazel run //:image_push
bazel run //:image_push_devops
bazel run //:image_push_ncp_dev
```

Regenerate Bazel metadata after Go or module changes:

```bash
bazel run //:gazelle
bazel mod tidy
```

CI subproject id: `ess-agent`. Native Bazel validation and release wiring live
in `tools/ci/subproject-validations.yaml`, an internal GitLab CI config not
present in this public snapshot.

## Local Gotchas

- This subtree is derived from HashiCorp consul-template.
- `api/` and `sdk/` are local workspace modules for modified Vault API/SDK
  code. Keep `go.work` and Gazelle prefixes aligned when changing them.
- Unused Vault auth backends are listed in `.bazelignore` to keep heavy cloud
  SDK dependencies out of `//...`.
- `child_test.TestReload_noSignal` is skipped through `go_test.args`; upstream
  vendored tests are tagged `manual` and excluded from the default test suite.
- `ESS_AGENT_INIT=true` makes the agent render templates and exit immediately.
