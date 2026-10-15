<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
# ESS Agent

## Build with Bazel

`bazel build //...` produces the `ess-agent` binary; `bazel test //...`
runs the unit-test suite. The image lives at
`//:image_index` (multi-arch OCI), with three push targets for the
internal NGC registries.

```shell
# Build everything Bazel knows about.
bazel build //...

# Run all tests with auto-retry on timing-sensitive failures.
# child_test.TestReload_noSignal is skipped via go_test.args (race
# flake in upstream consul-template code, see KNOWN_TEST_ISSUES.md
# §5). Upstream-vendored test targets are tagged `manual` and excluded
# from `//...`; run them explicitly when needed.
bazel test //... --test_env=SKIP_INTEGRATION_TESTS=1 --flaky_test_attempts=3

# Build the multi-arch OCI image index (linux/amd64 + linux/arm64).
bazel build //:image_index

# Load the host-arch image into the local docker daemon.
bazel run //:image_load
```

The OCI image is published to three internal NGC registries from CI on
the default branch. The macro emits one target per registry; run each
to push:

```shell
bazel run //:image_push           # primary ESS registry
bazel run //:image_push_devops    # nvcr.io/nv-ngc-devops/ess-agent
bazel run //:image_push_ncp_dev   # nvcr.io/0651155215864979/ncp-dev/ess-agent
```

### Multi-module workspace

`go.work` lists the root, `./api`, and `./sdk` because the upstream
HashiCorp vault api / sdk are vendored as local modules via `replace`
directives in `go.mod`. Each workspace member carries a
`gazelle:prefix` directive so Gazelle emits the right importpaths and
references local Bazel targets (`//api`, `//sdk/...`) instead of
re-fetching the modules.

### Unused vault auth backends

Unused vault auth providers (aws, azure, gcp, ldap, userpass, approle)
are listed in `.bazelignore`. Only the kubernetes backend is wired in
via `dependency/client_set.go`, so the heavy cloud-sdk transitive deps
stay out of the build. Add a directory back to participate in `//...`
when a new auth backend is genuinely needed.

### Regenerating BUILD files

```shell
# After Go source changes.
bazel run //:gazelle

# After go.mod / api/go.mod / sdk/go.mod changes.
bazel mod tidy
```

## Special Envs

| Name | Purpose |
|----|----|
| `ESS_AGENT_INIT=true` | Indicates to the ess-agent that it is running under an init container. It will render the secrets to the template destination and exit the process/container immediately after. |


## Configuration

1. Sample ess-agent config with no telemetry support

```toml
ess {

  # This is the address of the ESS
  address = "https://ess.stg.nvidia.com"

  namespace = "nvcf"

  ess_agent_token_file = "./jwt.token"

  # The default lease duration of each ess secret
  default_lease_duration = "15m"

  # The fraction of the lease duration of a secret a 2% +/- jitter will be added on each instance
  lease_renewal_threshold = 0.80

}

# Your temmplate goes here (eg)
template {
  source = "./example.tmpl"
  destination = "./output.json"
}
```

2. Sample ess-agent config with telemetry w/o TLS enabled

The default port defined for ess-agent metrics is `9103` which is also exposed in the container by default.  If overriding both the agent config and the container startup must have the same port defined.

```toml
ess {

  # This is the address of the ESS
  address = "https://ess.stg.nvidia.com"

  namespace = "nvcf"

  ess_agent_token_file = "./jwt.token"

  # The default lease duration of each ess secret
  default_lease_duration = "15m"

  # The fraction of the lease duration of a secret
  lease_renewal_threshold = 0.80

}

template {
  source = "./example.tmpl"
  destination = "./output.json"
}

telemetry {
  prometheus {
    tls_disable = true
    ip = "0.0.0.0"  # defaults to 0.0.0.0 if not specified
    port = 9103     # defaults to 9103 if not specified
  }
}
```

3. Sample config with telemetry with TLS enabled (optional)
```toml
ess {

  # This is the address of the ESS
  address = "https://ess.stg.nvidia.com"

  namespace = "nvcf"

  ess_agent_token_file = "./jwt.token"

  # The default lease duration of each ess secret
  default_lease_duration = "15m"

  # The fraction of the lease duration of a secret
  lease_renewal_threshold = 0.80

}

template {
  source = "./example.tmpl"
  destination = "./output.json"
}

telemetry {
  prometheus {
    tls_disable = false
    ip = "0.0.0.0"  # defaults to 0.0.0.0 if not specified
    port = 9103     # defaults to 9103 if not specified
    tls_key_path = "/etc/ssl/agent.key"   # absolute path to the private key
    tls_cert_path = "/etc/ssl/agent.pem"  # absolute path to the cert pem
  }
}
```

## Telemetry

The following metrics are reported in addition to the default go prometheus metrics.

| Metric| Description | Type |
| -------- | ------- | ------- |
| ess_process_uptime_sec |The uptime of the current process in seconds| Counter |
| ess_last_token_file_refresh_unix | The last time the ess_agent_token_file was refreshed in memory.  Value is unix epoch timestamp. | Gauge |
| ess_configured_templates | The number of templates configured | Counter |
| ess_templates_rendered | A counter of templates rendered with labels id=templateID and status=(success\|fail). <br/> This will only increment on a new secret being fetched/rendered or a failure of the template to render due to invalid template. | Counter |
| ess_templates_request_total | A counter for each secret path referenced in a template using id=templateID and status=(success\|fail). This metric will increment on every API failure or success on initial template refresh. Retries due to 50x server responses will not increment fail counter. | Counter |
| ess_templates_stopped_total | The current number of templates stopped due to client errors (40x responses). Templates are stopped when `stop_processing_on_client_error=true` and the destination file doesn't exist. | Gauge |


Note: Each `ess_templates_*` metric is labeled with an internal id of the template generated on startup which can change on each launch.

Default labels:

| Label | Description | Example |
| -------- | ------- | ------- |
| ess_agent_id | unique agent id generated on startup | `45852a0cd7b290d5b804a07e3d16d5e4`



Metrics can be scrapped on the below endpoint
```
$ curl http://<ip>:port/metrics
...
# HELP ess_configured_templates_total The number of templates configured.
# TYPE ess_configured_templates_total counter
ess_configured_templates_total 2

# HELP ess_last_token_file_refresh_unix The last ess token file refresh unix timestamp since epoch
# TYPE ess_last_token_file_refresh_unix gauge
ess_last_token_file_refresh_unix 1.724189538e+09
# HELP ess_process_uptime_sec_total The uptime of the current process in seconds.

# TYPE ess_process_uptime_sec_total counter
ess_process_uptime_sec_total 255

# HELP ess_templates_rendered_total A counter of templates rendered with labels id=templateID and status=(success|fail)
# TYPE ess_templates_rendered_total counter
ess_templates_rendered_total{ess_agent_id="45852a0cd7b290d5b804a07e3d16d5e4",id="ea1b91173404189224dae79dc55765e6",status="success"} 1
ess_templates_rendered_total{ess_agent_id="45852a0cd7b290d5b804a07e3d16d5e4",id="fd0d88c4c3ef155e529c953fe8cd317f",status="success"} 1

# HELP ess_templates_request_total A counter of requests per secret paths with labels id=templateID and status=(success|fail)
# TYPE ess_templates_request_total counter
ess_templates_request_total{ess_agent_id="ea1b91173404189224dae79dc55765e6",id="6322d47a1f820e41d7355002bbe2b23c",operation="read",secret="abc/data/test1",status="fail"} 1
ess_templates_request_total{ess_agent_id="ea1b91173404189224dae79dc55765e6",id="fd0d88c4c3ef155e529c953fe8cd317f",operation="read",secret="kv2/data/test",status="success"} 1

target_info{otel_scope_name="ess-agent",otel_scope_version="v0.1.0",ess_agent_id="45852a0cd7b290d5b804a07e3d16d5e4"} 1
...
```

**NOTE:** with TLS enabled, you can use the below command

```bash
$ curl https://<ip>:<port>/metrics \
    --cacert <absolutePathToTheCertPem>
```
----

<br/>

# ESS Agent Lifecycle

## Init Container

When environment variable `ESS_AGENT_INIT=true` is passed to the container the ess-agent will run in the init mode which aligns with Kubernetes Init containers which are expected to start, run a process, and exit allowing dependent containers to start.

Init containers will:
1. load the JWT ESS Auth Token from disk
2. render any secrets found in the configured template(s) to configured destination(s)
3. exit service with code 0

Note: The init container instance does not expose metrics as a successful init containers lifespan is a few seconds.

### Failures
There are four failure scenarios that can occur in the init container which result in different outcomes:

#### 1. Invalid Template
Will occur when a template contains incorract usage of `with secret` and cannot be processed. The agent will exit with code 1.

#### 2. 40x client error returned by ESS API
Will occur when a bad/invalid JWT token used (401), token does not have access to read secrets (403), secret path used in template
 does not exist (404) or secret does not contain an expected key found in template.

When encountered the agent will exit with code 1 as there is no recovery without outside changes applied.

#### 3. 429 Rate-limited
Too many requests to ESS API have occurred from the same IP address.
 The agent will exponentially retry for up to 10 minutes and exit is time limit is reached.

#### 4. 50x Server Error
ESS API is not correctly functioning and returning a server error.
 The agent will exponentially retry for up to 10 minutes and exit is time limit is reached.


## Sidecar

When all the init containers have completed and exited cleanly, the pod will bring the remaining containers which include a long-lived ESS Agent container that will continue to monitor the JWT auth token and refresh secrets on a set cadence.

The agent sidecar is configured to contact ESS API within every 15 minutes and pull/render any changes ESS API returns.  The agent will fetch secrets at 78-82% of the configured lease time (15m) or approximately every 12 minutes.

The agent will monitor the JWT auth token file for changes every 2 seconds and when a change is detected all future calls to ESS API will use the new token until the next token refresh is detected.  When a new JWT auth token is detected the metric `ess_last_token_file_refresh_unix` is updated with the epoch timestamp then token update was detected.

When fetching secrets from ESS API the agent will fetch and render the output of the provided templates to a temp file. If the temp file and the existing file are the same the temp file will be deleted, if they are different the temp file will replace the existing file in full. A successful rendering of a new secret is exposed in the metric `ess_templates_rendered_total`.

On startup, if the agent sidecar detects a template destination file exists, the agent will skip the first fetch of secrets from ESS API to avoid back-to-back calls within a few seconds in the same pod.  The 15 minute cadence will be used for the next secret fetch.

### Failures

The agent sidecar is designed to keep running regardless of failure events for as long as the pod is running, but it does behave differently depending on the error encountered.

All ESS API errors regardless of status code will increment the metric `ess_templates_request_total` with label `status="fail"`.

#### 1. 40x Client Errors

Any 40x client error response from ESS API will have the agent stop attempting to render the template and reset the refresh secret cadence (15m).


#### 2. 429 Rate-Limited

Similar to 40x errors, the agent will stop rendering the template and reset the refresh secret cadence.

#### 3. 50x Server Errors

50x errors will use an exponential retry policy starting at 500 milliseconds (e.g. 500ms, 1s, 2s, 4s...) with a max retry duration of 1 minute until a total of 10 minutes has passed.

If the 10 minute max duration is reached, retries will be stopped and the refresh secret cadence will be restarted. If this event occurs the total duration between secret fetches is 25 minutes (10 retry max + 15 minute refresh cadence).

Note: Exponential retries do not increment the `ess_templates_request_total` metric with `status="fail"` only the first API call failure will be tracked.


<br/>

# Development

## Build

Refer [Building & Release](nv_releases/docs/deploy.md) on how to generate a ess-agent build.

## Testing
Refer [link](nv_releases/docs/test.md) for local testing.
