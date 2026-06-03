<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Known Test Issues

Tests listed here are skipped in GitLab CI but should pass locally when the
required services are available. Each section describes the failure, the skip
mechanism, and what is needed to re-enable the test.

All skips are gated on the `SKIP_INTEGRATION_TESTS` environment variable
(set in `tools/ci/subproject-validations.yaml` / child pipeline) or the `GITLAB_CI` variable (set automatically by
GitLab runners).

---

## 1. `dependency` package — vault dev server (#1)

**Tests affected:** All tests in `dependency/` (the `TestMain` panics before
any individual test runs).

**Symptom:**
```
panic: Error making API request | URL: POST http://127.0.0.1:<port>/v1/sys/mounts/pki
       | Code: 403 | Detail: permission denied
```

**Root cause:** The integration tests start a HashiCorp Vault dev server
(`vault server -dev`) and configure PKI with a known root token. In the
GitLab CI Kubernetes executor the dev server either fails to initialise
properly (missing `IPC_LOCK` / `mlock` capability) or another vault process
interferes, causing all authenticated API calls to return 403.

**Skip mechanism:** `SKIP_INTEGRATION_TESTS` env var in subproject CI config.
When set, `dependency.TestMain` exits 0 immediately.

**To re-enable:**
1. Build and push the CI image from `nv_releases/docker/Dockerfile.ci`.
2. Wire that image in `tools/ci/subproject-validations.yaml` (ess-agent checks).
3. Ensure the CI runner grants `IPC_LOCK` or set `VAULT_DISABLE_MLOCK=true`.
4. Remove `SKIP_INTEGRATION_TESTS` from subproject CI config.

---

## 2. `watch` package — vault dev server (#1)

**Tests affected:** All tests in `watch/`.

**Symptom:** Same 403 / startup failure as the `dependency` package.

**Skip mechanism:** Same `SKIP_INTEGRATION_TESTS` variable, checked in
`watch.main()`.

**To re-enable:** Same steps as §1.

---

## 3. Root package and `manager` — consul integration tests (#2)

**Tests affected:**
- `TestCLI_Run/once`, `TestCLI_Run/reload`, `TestCLI_Run/once_from_env`
  (root package, `cli_test.go`)
- `TestRunner_Start/single_dependency` and other consul-dependent subtests
  (`manager/runner_test.go`)

**Symptom (root):**
```
cli_test.go: timeout: "ESS Agent instance id: …\nESS Agent init(mode): false\n"
```

**Symptom (manager):**
```
panic: test timed out after 30m0s
    running tests: TestRunner_Start/single_dependency (3m0s)
```

**Root cause:** These tests start a test Consul server and depend on
live KV queries. In the CI Kubernetes pod, the Consul test server is
unreliable — template rendering stalls or KV propagation is too slow.
The `manager` package's `Runner.init()` sleeps waiting for a response
that never arrives, consuming the entire 30-minute `go test` timeout.

**Skip mechanism:**
- Root package: `SKIP_INTEGRATION_TESTS` env var in `main_test.go`
  exits `TestMain` early; individual `TestCLI_Run` subtests also call
  `t.Skip`.
- Manager: `SKIP_INTEGRATION_TESTS` in `manager/manager_test.go`
  exits `TestMain` early.

**To re-enable:** Fix the Consul test-server reliability in CI (may
require a privileged runner or the custom CI image). Remove the
`SKIP_INTEGRATION_TESTS` guards from `main_test.go` and
`manager/manager_test.go`.

---

## 4. `TestSyslogFilter` — no syslog in container (#4)

**Tests affected:** `TestSyslogFilter` in `logging/syslog_test.go`.

**Symptom:**
```
syslog_test.go: err: Unix syslog delivery error
```

**Root cause:** The CI container (`golang:1.25.6`) does not run a syslog
daemon. The test tries to connect to `/dev/log` which does not exist.

**Skip mechanism:** The test checks for `GITLAB_CI` (and `TRAVIS`,
`CIRCLECI`) environment variables and calls `t.Skip`.

**To re-enable:** Use a CI image that includes a syslog daemon, or accept
that this test only runs locally / on full VMs.

---

## 5. `child.TestReload_noSignal` — race-detector flake

**Tests affected:** `TestReload_noSignal` in `child/child_test.go`
(upstream HashiCorp consul-template).

**Symptom:**
```
==================
WARNING: DATA RACE
...
Goroutine N (running) created at:
  github.com/hashicorp/consul-template/child.(*Child).kill()
      /builds/nvcf/ess-agent/child/child.go:456 +0x8d2
  github.com/hashicorp/consul-template/child.(*Child).internalStop()
      /builds/nvcf/ess-agent/child/child.go:297 +0x384
  github.com/hashicorp/consul-template/child.(*Child).Stop()
      /builds/nvcf/ess-agent/child/child.go:275 +0x92
  github.com/hashicorp/consul-template/child.TestReload_noSignal.deferwrap1()
      /builds/nvcf/ess-agent/child/child_test.go:319 +0x1f
...
    testing.go:1617: race detected during execution of test
--- FAIL: TestReload_noSignal (0.15s)
FAIL	github.com/hashicorp/consul-template/child	1.168s
```

**Root cause:** The test uses `defer c.Stop()` and a 10ms `killTimeout`,
which exposes a race in upstream `Child.kill` between the goroutine that
waits for the killed shell process and the test's own teardown calling
`Stop` again. The race is in vendored upstream code
(`child/child.go:275/297/456`) and triggers intermittently under
`go test -race`. Confirmed flake history:
- `main` pipeline 48613751 (sha `cb95a2c`)
- MR !3 pipeline 50230314 (sha `b64c1c3`)

The race does not affect non-race builds and does not surface in
production code paths. Other `child/` tests (including `TestReload_signal`,
`TestKill_signal`) do not flake.

**Skip mechanism:** subproject CI `go-test:` script passes
`-skip '^TestReload_noSignal$'` to `go test`. This excludes only the
flaky test; every other test in `child/` continues to run with `-race`.
Local developer runs (without `-skip`) still execute the test, so the
upstream behaviour stays under coverage outside CI.

**To re-enable:** Either fix the race upstream (file an issue against
[hashicorp/consul-template](https://github.com/hashicorp/consul-template))
and pull the patch back into `mpl-files-modified.md`, or wait for
upstream to address it. Once fixed, drop the `-skip` flag from subproject CI config.

---

## 6. Docker build — RESOLVED

The root `Dockerfile` was converted from a single-stage image (expecting
pre-built binaries in `./dist/`) to a multi-stage build that compiles the
Go binary in a `golang:1.25.6-alpine` builder stage. The `docker-build` CI
job now works without a separate build step. See issue #3.
