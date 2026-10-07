# `nvcf-cli` flag reference

## Global flags (every subcommand)

| Flag | Purpose | Default |
|---|---|---|
| `--config FILE` | Config file path | `$HOME/.nvcf-cli.yaml` |
| `--debug` | Verbose HTTP logging on stderr | `false` |
| `--json` | JSONL events on stderr | auto (off in TTY, on under `--non-interactive`) |

## `self-hosted` parent (persistent across subcommands)

| Flag | Purpose | Default |
|---|---|---|
| `--control-plane-stack=...` | Control-plane bundle source: local path, git URL, or `oci://` URL | embedded OCI URL pinned by CLI version |
| `--compute-plane-stack=...` | Compute-plane bundle source: local path, git URL, or `oci://` URL | embedded OCI URL pinned by CLI version |
| `--env=local\|prd\|...` | Helmfile environment name. `check` reads the stack's `environments/<env>.yaml` only for an environment named with `--env` or `HELMFILE_ENV`, or for a plane `--pre` checks before its install, so pass the `--env` the install used; see [Settings read from the stack](#settings-read-from-the-stack) | `local` |
| `--non-interactive` | Disable all stdin prompts | `false` |
| `--token=$JWT` | Admin JWT, overrides stored session | - |
| `--no-apply` | `install` only - emit YAML, do not kubectl apply | `false` |
| `--output=text\|json` | Legacy alias for `--json` (deprecated, removed in next major) | `text` |
| `--plain` | Force plain streaming output | auto-detect |
| `--wait DURATION` | `check` only; run the checks again, 5s apart, until they pass or DURATION runs out (exit `5`, `final` with `success: false`). Each run includes the cluster-validator Job, which can take up to 5m, so allow for it or pass `--skip-cluster-validation`. A warning that is expected to clear, such as a rollout in progress, keeps it polling, and its `check_completed` event carries `transient: true`. A check the time budget stopped keeps it polling too, and its event carries `cutShort: true`. A check that can only warn and is stopped on two polls in a row ends the wait as a single run would: exit `0` with the warning. Cannot be combined with `--no-cleanup`. A malformed or non-positive DURATION is a usage error (exit `1`, no events) | - |
| `--control-plane-context CTX` | kubectl context for control plane (REQ-20) | current context |
| `--compute-plane-context CTX` | kubectl context for compute plane (REQ-20) | current context |
| `--icms-url URL` | Public ICMS URL; required when contexts differ. `check` probes SIS only at a URL set here, in `NVCF_ICMS_URL` or `NVCF_SIS_URL`, or in the config as `icms_url` or `base_http_url`, and not at one derived from a `base_http_url` on NVIDIA's hosted `nvcf.nvidia.com`, as the config template's is; with none, its `sis-reachability` row is a warning | derived from `base_http_url` |
| `--local-only` | `check` only; run the local-host checks and skip all cluster and registry contact, whatever scope flag is passed. The registry credential check and each selected role's cluster checks are reported as rows that pass at info and say `skipped (--local-only)`. Env: `NVCF_CLI_SELFHOSTED_LOCAL_ONLY`, on with `true`, `1`, `yes`, `y` or `on` and off with `false`, `0`, `no`, `n` or `off`, in any case. Any other value is an error | `false` |

## `check`-specific

At least one of `--pre`, `--control-plane`, `--compute-plane`, or `--all` is
required. `--pre` selects both roles; each other flag selects its own. Only
the selected roles contact a cluster. Pass both context flags or neither:
without them every selected role checks the current kubeconfig context.

| Flag | Purpose | Default |
|---|---|---|
| `--pre` | Pre-flight: local-host tools plus cluster readiness. Skips SIS reachability, since SIS is not installed yet, unless `--all` or `--compute-plane` is also passed. With `--all`, or with a role's own flag, that role's validator checks the cluster as installed. A missing or unsupported `kubectl`, `helmfile` or `helm` fails only with `--pre`; other scopes check an installed stack, which runs none of them, so it warns | `false` |
| `--control-plane` | Check the installed control plane: leftover namespaces and the control-plane cluster-validator. Never probes SIS | `false` |
| `--compute-plane` | Check the installed compute plane: leftover namespaces, node inotify limits, SIS reachability at a configured ICMS URL, and the compute-plane cluster-validator | `false` |
| `--all` | Both `--control-plane` and `--compute-plane`, SIS reachability included, with or without `--pre` | `false` |
| `--cluster-name NAME` | Cluster name shown in the output header. No check uses it, and `check` does not require it | - |
| `--skip-inotify-check` | Skip the per-node inotify-limits probe. Needed when the kubeconfig user cannot create pods in `default`, or when the probe image is on a registry that needs credentials, since the probe pods get no pull secret. Its `node-inotify-limits` row passes at info and says it was skipped. Env: `NVCF_CLI_SELFHOSTED_SKIP_INOTIFY`, on with `true`, `1`, `yes`, `y` or `on` and off with `false`, `0`, `no`, `n` or `off`, in any case. Any other value is an error | `false` |

### Cluster-validator flags

The validator runs as a Job in the cluster being checked. The CLI creates a
ServiceAccount, ClusterRole, and ClusterRoleBinding for it, plus a Role and
RoleBinding in `default` that let it read only its own ConfigMap, and removes
them after the run, so the kubeconfig context needs permission to manage those.
It reads the validator's transcript with `get` on `pods/log` in `default`.
Without it, a validator Job that succeeded still passes, and its row says the
transcript could not be read, so neither the checks it ran nor warnings it
reported are shown.
The ClusterRole grants only the calls each role's checks make: reads, the
namespace, DaemonSet and pod writes of the control-plane node-to-node probe,
and deleting test namespaces an earlier validator run left behind.

| Flag | Purpose | Default |
|---|---|---|
| `--cluster-validator-image REF` | Validator image. Resolution order: flag, `NVCF_CLI_CLUSTER_VALIDATOR_IMAGE`, config key `cluster_validator_image`. A ref with no tag discovers the latest stable tag from the registry. If no tag can be discovered, the validator does not run and its row fails the check, saying to pin a tag; `--wait` tries again on every poll. Unset everywhere, the validator does not run and each role's `cluster-validator` row is a warning, so the verdict is `warnings` | - |
| `--cluster-validator-registries host[:port][/path],...` | Extra registries to check: their credentials from this machine, and their reachability from a pod of the control-plane validator. A path scopes the credential check, as in `harbor.example.com/nvcf`. They are added to the registries the install pulls from: the validator image's registry, the stack's `global.image.registry` and `global.image.repository`, and `quay.io` for the cert-manager ACME solver unless the stack leaves cert-manager out or sets `certManager.acmesolver.image`. `nvcr.io` is probed, as a warning only, when neither the image nor the stack names a registry. The in-pod probes are warnings only, since the pod has no proxy. A malformed entry fails the command. Repeatable or comma-separated. Env: `NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES`; config key `cluster_validator_registries`, a list of strings in this form | - |
| `--cluster-validator-probe-image REF` | Image for the node inotify probe and the control-plane validator's node-to-node overlay probe. Needs `sh` and a busybox-style `nc`. Set a mirror for air-gapped clusters. Env: `NVCF_CLI_CLUSTER_VALIDATOR_PROBE_IMAGE`; config key `cluster_validator_probe_image` | `busybox:1.36` from Docker Hub |
| `--cluster-validator-external-components NAME,...` | Stack dependencies that run outside the stack, or not at all, so the validator does not look for them in the cluster: a subset of `nats`, `openbao` and `cassandra`. An unknown name fails the command. Repeatable or comma-separated. Falls back to `NVCF_EXTERNAL_COMPONENTS`, then to the components whose `<name>.enabled` the stack's environment file sets to anything but `true`. Env: `NVCF_CLI_CLUSTER_VALIDATOR_EXTERNAL_COMPONENTS`; config key `cluster_validator_external_components`, a list of names | from the stack |
| `--cluster-validator-tolerations key[=value][:effect],...` | Tolerations added to the validator Job, beside the control-plane ones it always carries, for clusters whose nodes use other taints. Effect is `NoSchedule`, `PreferNoSchedule` or `NoExecute`. A malformed entry fails the command. Repeatable or comma-separated. Env: `NVCF_CLI_CLUSTER_VALIDATOR_TOLERATIONS`; config key `cluster_validator_tolerations`, a list of strings in this form, not Kubernetes toleration objects | - |
| `--cluster-validator-pull-secret NAME` | docker-registry Secret in `default` used to pull the validator image. When empty, the CLI uses a Secret an operator created in `default` for the image's registry. Failing that, it copies the registry's entry, and only that entry, from a Secret in the NVCF namespaces into `default` for the run. Failing that, it creates one for the run from this machine's credential for the registry, the one the `registry-credentials` row checks. The validator row's detail says what was copied or created, and from where | auto-detect |
| `--skip-cluster-validation` | Skip the in-cluster validator probe entirely. A validator that is configured but cannot run fails the check, so use this to opt out explicitly, for example when the cluster cannot pull the image. Each role's `cluster-validator` row then passes at info and says it was skipped. Env: `NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION`, on with `true`, `1`, `yes`, `y` or `on` and off with `false`, `0`, `no`, `n` or `off`, in any case. Any other value is an error | `false` |
| `--no-cleanup` | Keep the validator Job, its pod, RBAC, pull secret and ConfigMap for debugging. A kept Job whose pod cannot pull its image is suspended, so its pod is deleted rather than retrying forever; inspect it with `kubectl describe job`. A later check that runs the validator removes them once they are 24 hours old. The row, the `final` event's `cleanup` list and the last stderr lines print the command that removes them now. Cannot be combined with `--wait` | `false` |
| `--show-logs` | Print each validator transcript to stderr after the check events, framed by `--- cluster-validator logs (<category>) ---` and a matching end line, where the category is `control-plane-cluster` or `compute-plane-cluster`. The transcript is not JSON, and `--json` also writes to stderr, so leave this off when a parser is reading the stream | `false` |

### Registry credentials

`check` probes each registry above from this machine with its local
credential, the one `docker` would use: `$DOCKER_CONFIG/config.json`, or
`~/.docker/config.json`, through any `credsStore` or `credHelpers` helper.
For `nvcr.io` the NGC API key is also read, from the first of
`NGC_IMAGE_PULL_API_KEY`, `NVCF_NGCR_API_KEY`, `NVCF_NGC_API_KEY` and
`NGC_API_KEY` that is set. It goes ahead of the docker login only when the
registry check's plane, below, is the control plane, checked before its
install with the `local` environment, since `up` creates its pull secrets
from it, and it is never sent to another registry. Elsewhere the key
is sent when there is no docker login, or for a repository where `nvcr.io`
rejects the docker login (a 401) or gives it no access (a 403). This is
decided once per repository, before tag discovery, the row and the
validator's pull secret use it, so all three get the same credential, and a
login with no access to one org still serves the orgs it reaches. A 403 is
reported as no access to that repository, never as a rejected login. Each
`--wait` poll reads the credentials again, so a login renewed while the run
waits counts on the next poll.

The probe asks for pull access to the validator image's repository and to the
stack's image path: any path in `global.image.registry`, then
`global.image.repository`. A stack path of one segment, such as an org with no
team, is not sent as the scope, since `nvcr.io` refuses it. When the stack's
OCI chart source, `global.helm.sources.registry` and `.repository`, is that
same path, its row is also the one `helm` on this machine pulls the stack's
charts from, with the docker login.

The registry check belongs to one plane, whose stack names the registries
and whose install state grades them: a plane the run checks before its
install, which this machine installs next (the control plane for a bare
`--pre` and for `--pre --compute-plane`, the compute plane for
`--pre --control-plane`), else the control plane when the run visits it,
else the compute plane.

Only a credential the registry rejects fails the run, and only for an NVIDIA
registry the image or the stack names. That includes a docker login
`nvcr.io` rejects even when the NGC key works in its place, before and after
install, since `docker` and `helm` on this machine still send it. The key is
still used for tag discovery and the validator's pull secret, and the row
names both. After install the row also says whether one of the pull secrets
below holds that docker login. Before install, a docker login with no access
to the row the stack's charts come from fails it too, although the NGC key
reaches it, since `helm` pulls the charts with the docker login. That is
reported as no access, not as a rejected login. Elsewhere, and after install,
a docker login with no access passes where the NGC key reaches the
repository. A rejected NGC key is a warning after install only when `check`
reads the pull secrets of the clusters it visits, in the namespaces the
validator copies pull secrets from, and none of them holds that key; when
one does, or they cannot all be read, it is an error. A registry this machine
cannot reach, a token service that fails, and a missing local credential are
warnings. A
registry the probe cannot speak to, such as ECR or one using Basic auth, is
skipped with a command to check it by hand. A registry that lets this
machine in anonymously is reported as such, not as valid credentials.

### Settings read from the stack

The validator gets these settings from the CLI. Each one set with its
environment variable or config key wins over the stack.

| Setting | Read from the stack | Env and config key |
|---|---|---|
| NVCF Gateways | `ingress.gatewayApi.gateways`, gated the way the stack wires routes: none unless `ingress.gatewayApi.enabled` is `true`, `nats` only with `routes.nats.enabled`, `llmGrpc` and `llmQuic` only with `routes.llmWorker.enabled` | `NVCF_GATEWAY_NAMES`, as comma-separated `namespace/name` entries. A bare name is dropped by the validator. Set this way it skips the stack's gates |
| Envoy Gateway namespace | `ingress.gatewayApi.controllerNamespace` | `NVCF_ENVOY_GATEWAY_NAMESPACE` |
| External components | `nats`, `openbao` or `cassandra` whose `enabled` is set and is not `true` | `--cluster-validator-external-components`, or `NVCF_EXTERNAL_COMPONENTS` |
| StorageClass | `global.storageClass`. When set, the validator checks that this class exists instead of requiring a default class | `NVCF_STORAGE_CLASS` |
| OpenBao namespace | - | `NVCF_OPENBAO_NAMESPACE` |
| Overlay probe image | - | `--cluster-validator-probe-image`, or `NVCF_N2N_PROBE_IMAGE` |

The registries above also come from the stack. `check` reads stack values only
when they describe the install:

- The environment is named with `--env` or `HELMFILE_ENV`. The `--env`
  default is used only for a plane `--pre` checks before its install,
  since `up` installs with it. Otherwise pass the `--env` the install used.
- `environments/<env>.yaml` is read, layered over `base.yaml`. `base.yaml`
  alone is not used.
- The file comes from the stack the install used: `--control-plane-stack`
  (`--compute-plane-stack` for the registries when the registry check
  belongs to the compute plane), else the CLI's built-in stack. A local
  directory is read directly, and an `oci://` stack from the copy an earlier
  command extracted into the CLI cache. A git stack, `file://` included, is
  not read, since the install cloned its committed state. A CLI with no
  built-in stack and no stack flag reads the stack checkout above the
  working directory.

Otherwise nothing from the stack is forwarded: the validator finds the
Gateways from the routes, and `nvcr.io` is probed as a warning only.

## `up`-specific

| Flag | Purpose | Default |
|---|---|---|
| `--cluster-name NAME` | ICMS cluster row identifier (required) | - |
| `--nca-id ID` | NCA account ID | `nvcf-default` |
| `--region REGION` | Cluster region | `us-west-1` |
| `--plan-only` | Dry-run; emit phase plan + ETAs without changes | `false` |

## `status`-specific

| Flag | Purpose | Default |
|---|---|---|
| `--cluster-name NAME` | Limit to one compute plane | - (control + all clusters) |
| `--watch` | Live re-render | `false` |
| `--watch-interval DUR` | `--watch` cadence | `5s` |
| `--component NAME` | Filter components panel | all |
| `--no-events` | Skip events panel | `false` |

## `cluster register`-specific

| Flag | Purpose | Default |
|---|---|---|
| `--name NAME` | Cluster name (required) | - |
| `--nca-id ID` | NCA ID (required) | - |
| `--region REGION` | Region | `us-west-1` |
| `--icms-url URL` | ICMS endpoint | from config |
| `--ignore-existing` | Match-or-create instead of fail-on-exists | `false` |

## `api-key generate`-specific

| Flag | Purpose | Default |
|---|---|---|
| `--for function\|task` | Generate only the specified key type | (omit to generate both) |
| `--description TEXT` | Human-readable label for the key | `Generated by nvcf-cli` |
| `--expires-in DURATION` | Expiry offset (e.g. `24h`, `7d`); parsed as extended duration | `24h` |
| `--scopes SCOPE[,SCOPE]` | Override default scopes; requires `--for` | (service defaults) |
| `--validate` | Validate each generated key immediately after minting | `false` |

Extended duration units: `s` (seconds), `m` (minutes), `h` (hours), `d` (days), `w` (weeks).

## `function create`-specific

`--input-file FILE` is recommended for repeatable function definitions. For ad-hoc creates:

| Flag | Purpose | Default |
|---|---|---|
| `--name NAME` | Function name | - |
| `--image IMAGE` | Container image | - |
| `--inference-url URI` | Container inference endpoint | - |
| `--inference-port PORT` | Container inference port | - |
| `--function-type DEFAULT\|STREAMING\|LLM` | Function type | `DEFAULT` |
| `--helm-chart CHART` | Helm chart URL or OCI reference. Can be used with `--function-type=LLM` for chart-packaged LLM workloads. | - |
| `--helm-chart-service NAME` | Kubernetes Service name exposed by the chart. Required when `--helm-chart` is set. | - |
| `--models NAME:VERSION:URI` | Standard model artifact; repeatable | - |
| `--llm-model SPEC` | LLM model config; format `name=<model>,uris=<uri>\|<uri>,routingMethod=<method>,tokenRateLimit=<limit>`; repeatable. Token limits use `<value>-<unit>` with `S`, `M`, `H`, `D`, or `W`, for example `1000-S`. Use input JSON for combined token limits because inline specs use commas as field separators. | - |
| `--llm-default-priority PRIORITY` | Function-level default request priority. Lower values have higher priority. | - |
| `--llm-per-account-priority NCA-ID:PRIORITY` | Per-account priority override; repeatable; supports up to 64 distinct NCA ID overrides. Requires a default priority. | - |

In JSON and inline specs, LLM functions set `functionType: "LLM"` and model routing metadata under `models[].llmConfig`. `llmConfig.uris` declares the OpenAI-compatible upstream paths exposed by the model. Current supported paths are `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings`. `llmConfig.routingMethod` accepts `round_robin`, `power_of_two`, `groq_multiregion`, `pulsar`, or `random`.
`llmConfig.tokenRateLimit` accepts one or more comma-separated positive integer token limits in `<value>-<unit>` format. Supported units are `S` (seconds), `M` (minutes), `H` (hours), `D` (days), and `W` (weeks). Use distinct units when combining limits, for example `1000-S,5000-M,100000-H,500000-D,1000000-W` in input JSON.

Helm chart packaging is independent of `functionType`. For a Helm-chart backed LLM function, set `functionType: "LLM"`, `helmChart`, `helmChartServiceName`, and `models[].llmConfig` in the same create request. `inferencePort` must be the Kubernetes Service port exposed by `helmChartServiceName`.

LLM invocation requests use `model: "<function-id>/<model-name>"`. The function ID selects the NVCF function, and the model name is forwarded upstream. Chat completions and Responses API requests can use `x-multi-turn-session-id` for session stickiness; embeddings requests do not.

## `function update`-specific

| Flag | Purpose | Default |
|---|---|---|
| `--tags TAG[,TAG]` | Replace function tags | - |
| `--llm-model-update SPEC` | LLM model update; format `name=<model>,routingMethod=<method>,tokenRateLimit=<limit>`; repeatable. Routing methods match `--llm-model`. Token limit example: `1000-S`. Use input JSON for combined token limits. | - |
| `--llm-default-priority PRIORITY` | Replace the function-level priority configuration with this default and any supplied per-account overrides. | - |
| `--llm-per-account-priority NCA-ID:PRIORITY` | Per-account priority override; repeatable; supports up to 64 distinct NCA ID overrides. Requires a default priority. | - |

In JSON, `function update` accepts `modelUpdates[]` entries with `modelName` and `llmConfig.routingMethod` and/or `llmConfig.tokenRateLimit`. `uris` are create-time model metadata and are not part of model updates.

## `function deploy create`-specific

`--input-file FILE` is recommended (full JSON spec). For ad-hoc one-shot use:

| Flag | Purpose | Default |
|---|---|---|
| `--function-id ID` | Function ID (required) | - |
| `--gpu NAME` | GPU family | `H100` |
| `--instance-type TYPE` | SKU | `NCP.GPU.H100_1x` |
| `--min-instances N` | Min replicas | `1` |
| `--max-instances N` | Max replicas | `1` |
| `--max-request-concurrency N` | Concurrency | `0` (unset; server default applies) |
| `--clusters NAME[,NAME]` | Target specific compute clusters | (any matching SKU) |

## `task create`-specific

`--input-file FILE` is recommended for repeatable task definitions. For ad-hoc use:

| Flag | Purpose | Notes |
|---|---|---|
| `--name NAME` | Task name (required) | 1-128 chars, `^[a-z0-9A-Z][a-z0-9A-Z\-_]*$` |
| `--gpu GPU` | GPU name (required without `--input-file`) | Example: `H100` |
| `--instance-type TYPE` | Instance type (required without `--input-file`) | Example: `NCP.GPU.H100_1x` |
| `--image IMAGE` | Container image | Mutually exclusive with `--helm-chart` |
| `--helm-chart CHART` | Helm chart URL or OCI reference | Mutually exclusive with `--image` |
| `--input-file FILE` | JSON task spec file | CLI flags override JSON values when both are set |
| `--backend BACKEND` | Backend / CSP | - |
| `--clusters NAME[,NAME]` | Specific clusters within the instance | Comma-separated |
| `--container-args ARGS` | Args passed when launching the container | Single string, e.g. `"--epochs 10 --batch-size 32"` |
| `--container-env NAME=value` | Container environment variable; repeatable | - |
| `--secrets NAME=value` | Secret environment variable; repeatable; encrypted at rest | - |
| `--max-runtime DURATION` | Max wall-clock run time (ISO 8601, e.g. `PT4H`) | No default; omit for no time limit |
| `--max-queued DURATION` | Max time in queue before cancellation (ISO 8601) | Server default `PT72H` |
| `--termination-grace DURATION` | Grace period after stop signal (ISO 8601) | Server default `PT1H` |
| `--result-strategy UPLOAD\|NONE` | How to handle task output artifacts | - |
| `--results-location LOCATION` | Result upload target, format `org-name/[team-name/]model-name`; required when `--result-strategy=UPLOAD` | - |
| `--models NAME:VERSION:URI` | Model artifact; repeatable | - |
| `--resources NAME:VERSION:URI` | Resource artifact; repeatable | - |
| `--tags TAG[,TAG]` | Task tags | Comma-separated |
| `--description TEXT` | Task description | - |
| `--logs-telemetry-id UUID` | Logs telemetry endpoint ID | - |
| `--metrics-telemetry-id UUID` | Metrics telemetry endpoint ID | - |
| `--traces-telemetry-id UUID` | Traces telemetry endpoint ID | - |
