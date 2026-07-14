---
name: nvcf-self-managed-cli
description: |
  Install, operate, and tear down self-hosted NVIDIA Cloud Functions (NVCF)
  deployments with nvcf-cli. Use for control-plane or compute-plane install,
  status checks, cluster registration, function deploy/invoke, task
  create/list/cancel/delete, API keys, admin tokens, JWKS rotation,
  failed-install diagnosis, and uninstall or down workflows. Trigger keywords:
  nvcf, nvcf-cli, self-hosted nvcf, self-managed nvcf, NVCFBackend, NVCA, NCP,
  ICMS, helmfile, control plane, compute plane, LLM function,
  OpenAI-compatible invocation, Responses API, embeddings, batch task, task
  monitor, cluster rotate, cluster delete, helm task, helm-based task,
  task secrets, update task secrets, retrieve task results, bulk fetch tasks,
  task results.
allowed-tools: Bash, Read, AskUserQuestion
argument-hint: "[install|status|check|deploy-function|register-cluster|teardown] [args]"
---
<!--
Token Budget:
- Level 1 (YAML): ~120 tokens
- Level 2 (this file): ~1900 tokens (target <2000)
- Level 3 (prompts/, reference/, examples/): loaded on demand
-->

# NVCF Self-Hosted CLI

`nvcf-cli` drives every step of bringing up self-hosted NVIDIA Cloud Functions: cluster registration, control-plane install, compute-plane install, function deploy/invoke, and lifecycle management. Use this skill any time the user wants to operate self-hosted NVCF.

## When to use

- "install self-hosted NVCF" / "bring up an NVCF cluster"
- "register a (compute|GPU) cluster with NVCF"
- "deploy a (container|GPU) function" / "invoke an NVCF function"
- "check NVCF cluster health" / "is my NVCF install OK?"
- "rotate NVCF cluster JWKS" / "the NVCA agent stopped authenticating"
- "tear down NVCF" / "remove the compute plane" / "uninstall NVCF" / "deregister this cluster"
- "preview what `down` would do" / "dry-run uninstall"
- "create a task" / "run a task" / "submit a GPU job" / "monitor a task"
- "cancel a task" / "delete a task" / "list tasks" / "list running tasks"
- "run a helm-based task" / "helm task" / "submit a helm task"
- "update secrets on my task" / "replace task secrets" / "task update-secrets"
- "retrieve task results" / "bulk fetch task details" / "get task results"
- Any reference to `NVCFBackend`, `NVCA`, ICMS, helm releases like `helm-nvcf-*`, or `icms.<domain>` / `api.<domain>` URLs.
- Any reference to `nvcf-cli task` or batch tasks.

## Quick start

For remote one-click installs, prepare Gateway API ingress and CLI endpoint
configuration before running `self-hosted up`. The command applies the control
plane and then immediately calls API, API Keys, invocation, and gRPC endpoints.
If the Gateway is not programmed or the CLI host headers do not match the
HTTPRoutes rendered by the stack environment, post-install health and cluster
registration will fail.

```sh
# Single-cluster (control + compute on the current kubeconfig context):
nvcf-cli self-hosted up --cluster-name=ncp-local

# Split-cluster (control plane on context A, compute plane on context B):
KUBECONFIG=cp.yaml:gpu1.yaml nvcf-cli self-hosted up \
  --cluster-name=ncp-local \
  --control-plane-context=admin@cp \
  --compute-plane-context=admin@gpu1 \
  --icms-url=https://icms.nvcf.example.com

# Add a new compute plane to an existing control plane (no kubectl access to CP needed;
# reaches the control plane via the public ICMS HTTPRoute):
nvcf-cli self-hosted add-compute-plane \
  --cluster-name=ncp-local-2 \
  --compute-plane-context=admin@gpu2 \
  --icms-url=https://icms.nvcf.example.com \
  --token=$ADMIN_JWT

# Tear down (always plan-only first):
nvcf-cli self-hosted down --plan-only --cluster-name=ncp-local --json | jq
nvcf-cli self-hosted down --cluster-name=ncp-local

# Per-plane uninstall (GitOps; mirrors `install`):
nvcf-cli self-hosted uninstall --no-apply --compute-plane --cluster-name=ncp-local | kubectl delete -f -
```

> **`up` vs `add-compute-plane`.** `up` always installs both planes — use it for the *first* install. `add-compute-plane` is the right subcommand any time the control plane is already running and you want to attach an Nth compute cluster.

> **`down` always with `--plan-only` first.** Show the user the `willUninstall.commands[]` array before running for real.

## Authentication

`nvcf-cli` uses two credentials. Each is resolved independently in this order:

| Precedence | Source |
|---|---|
| 1 (highest) | Environment variable (`NVCF_TOKEN`, `NVCF_API_KEY`) |
| 2 | Config file (`~/.nvcf-cli.yaml`, keys `token` and `api_key`) |
| 3 | State file (`~/.nvcf-cli.state`), skipped when the stored token is expired |

| Env var | Token type | Used by |
|---|---|---|
| `NVCF_TOKEN` | Admin JWT | `function create` / `deploy` / `update` / `delete`, cluster management (`cluster register`/`rotate`/`delete`, `self-hosted` ops). Required for admin commands; preferred for the rest. |
| `NVCF_API_KEY` | `nvapi-...` API key | `function invoke` / `list` / `get`, queue details. Falls back to `NVCF_TOKEN` when unset. |

Token generation flow:

1. Point the CLI at your self-hosted endpoints. Defaults target production NVCF; override with env vars or a yaml file (`--config <path>`, `./.nvcf-cli.yaml`, or `~/.nvcf-cli.yaml`; a full template ships at `src/clis/nvcf-cli/.nvcf-cli.yaml.template`).

   `NVCF_BASE_HTTP_URL` / `API_KEYS_SERVICE_URL` / `NVCF_BASE_GRPC_URL` tell the CLI *where to send requests*. `API_HOST` / `API_KEYS_HOST` / `INVOKE_HOST` set the `Host:` header on those requests — needed when every NVCF service sits behind a single gateway that routes by hostname (Envoy Gateway HTTPRoute on `api.<ELB>`, `api-keys.<ELB>`, `invocation.<ELB>`, `llm.<ELB>`). Skip the three Host vars when the base URL already resolves to the right service.

   Env-var form:
   ```sh
   export NVCF_BASE_HTTP_URL=https://gw.example.com
   export API_KEYS_SERVICE_URL=https://gw.example.com
   export NVCF_BASE_GRPC_URL=gw.example.com:443

   # Only when the gateway routes by Host header:
   export API_KEYS_HOST=api-keys.gw.example.com
   export API_HOST=api.gw.example.com
   export INVOKE_HOST=invocation.gw.example.com
   ```

   Yaml form (`~/.nvcf-cli.yaml`):
   ```yaml
   base_http_url: https://gw.example.com
   api_keys_service_url: https://gw.example.com
   base_grpc_url: gw.example.com:443

   # Only when the gateway routes by Host header:
   api_keys_host: api-keys.gw.example.com
   api_host: api.gw.example.com
   invoke_host: invocation.gw.example.com
   ```

2. `nvcf-cli init` calls the API Keys service via those endpoints and writes the admin token to `~/.nvcf-cli.state`. Use `nvcf-cli refresh` to rotate a stored token.
3. After `init`, `nvcf-cli api-key generate` mints function + task API keys (also stored in state).

After `init`, the credentials live in `~/.nvcf-cli.state`, so later commands work without exporting `NVCF_TOKEN` or `NVCF_API_KEY`. Export the env var only to override the stored value (for example a shorter-lived token from CI).

## Core subcommands

| Subcommand | What it does | When to use |
|---|---|---|
| `nvcf-cli self-hosted check --pre [--local-only \| --control-plane-context=X \| --compute-plane-context=Y]` | Pre-flight: local-host tools + cluster-side prerequisites | Always run first on a new environment |
| `nvcf-cli self-hosted install --control-plane \| kubectl apply -f -` | Render + apply the control plane | When you want manual control over apply (GitOps-friendly) |
| `nvcf-cli self-hosted install --compute-plane --cluster-name=X \| kubectl apply -f -` | Register cluster + render compute plane | Same — manual apply path |
| `nvcf-cli self-hosted up --cluster-name=X` | One-shot first install: pre-flight → control plane → register → compute plane | Standard install path (both planes from scratch) |
| `nvcf-cli self-hosted up --plan-only --cluster-name=X` | Dry-run: emit phase-by-phase plan + ETA without changing state | Agent / CI preview before commit |
| `nvcf-cli self-hosted add-compute-plane --cluster-name=X --compute-plane-context=Y --icms-url=… --token=$JWT` | Add a new compute plane to an *existing* control plane (no CP install) | Adding the 2nd, 3rd, … GPU cluster after the initial `up` |
| `nvcf-cli self-hosted uninstall --control-plane` | Per-plane primitive: `helmfile destroy` on the control plane (refuses if compute planes still registered) | Final teardown after all compute planes are gone, or scripted pipelines |
| `nvcf-cli self-hosted uninstall --compute-plane --cluster-name=X` | Per-plane primitive: `helmfile destroy` on a compute plane (no ICMS unregister, no drain) | Just remove the helm releases without the ICMS-side cleanup |
| `nvcf-cli self-hosted uninstall --no-apply <plane>` | Render delete YAML via `helm get manifest` | GitOps; `\| kubectl delete -f -` or commit + Argo applies |
| `nvcf-cli self-hosted down --cluster-name=X` | Orchestrator: drain → uninstall --compute-plane → cluster delete in ICMS | Standard "remove one GPU cluster" path |
| `nvcf-cli self-hosted down --all --confirm` | Orchestrator: tear down every registered compute plane + control plane | Full uninstall |
| `nvcf-cli self-hosted down --plan-only --cluster-name=X` | Dry-run preview (phases + helm releases + ICMS rows + ETAs; no helm/helmfile contact) | **ALWAYS run before any actual `down`** |
| `nvcf-cli self-hosted status [--cluster-name=X] [--watch] [--json]` | Snapshot dashboard of cluster identity + component health + recent events | Routine health checks; `--watch` for live |
| `nvcf-cli init` | Mint admin token from API Keys service via the public api gateway | Before any cluster-management operation; idempotent |
| `nvcf-cli cluster register --name=X --nca-id=Y --region=Z [--ignore-existing]` | Register a cluster JWKS+OIDC issuer with ICMS | Standalone register (without compute-plane install) |
| `nvcf-cli cluster rotate --cluster-id=ID` | Rotate cluster JWKS in ICMS | When NVCA's K8s signing key changed and PSAT verification started 401-ing |
| `nvcf-cli cluster delete --cluster-id=ID` | Remove cluster registration from ICMS | **Confirm with user.** Destroys ICMS state for the cluster. |
| `nvcf-cli api-key generate --description="…" --expires-in=1h` | Mint both a function API key and a task API key (default) | Before invoking functions or creating tasks; run after every `init` |
| `nvcf-cli api-key generate --for function --description="…"` | Mint a function API key only (`invoke_function` scope) | When only function invocation is needed |
| `nvcf-cli api-key generate --for task --description="…"` | Mint a task API key only | When only task operations are needed |
| `nvcf-cli function create --input-file=<json>` | Create function metadata in ICMS | First step of any function deploy; use `functionType: "LLM"` and `models[].llmConfig` for LLM functions |
| `nvcf-cli function deploy create --input-file=<json>` | Schedule a deployment of a created function | Waits for ACTIVE before returning (timeout 900s) |
| `nvcf-cli function invoke --input-file=<json>` | Invoke a deployed function | Requires API key (not admin token) |
| `nvcf-cli function delete --function-id=ID --version-id=VID` | Remove a function and its deployment | **Confirm with user.** |
| `nvcf-cli task create --name=X --gpu=H100 --instance-type=Y --image=Z` | Submit a container task; saves task ID to CLI state | Requires task API key; set `NVCF_BASE_NVCT_URL` to the NVCT gateway endpoint |
| `nvcf-cli task create --name=X --gpu=H100 --instance-type=Y --helm-chart=Z` | Submit a Helm task | Same as container task; `--helm-chart` replaces `--image` |
| `nvcf-cli task create --input-file=<json>` | Submit a task from a JSON config file | Recommended for repeatable configurations |
| `nvcf-cli task list [--status=QUEUED\|RUNNING\|COMPLETED]` | List tasks, optionally filtered by status | |
| `nvcf-cli task get [taskId]` | Get details for a task; uses saved task ID if omitted | |
| `nvcf-cli task events [taskId]` | Stream lifecycle events for a task | |
| `nvcf-cli task results [taskId]` | List result artifacts for a completed task | Result upload not yet supported; returns empty list for `NONE` strategy |
| `nvcf-cli task cancel [taskId]` | Cancel a queued or running task | |
| `nvcf-cli task delete [taskId]` | Delete a task record | **Confirm with user.** |
| `nvcf-cli task update-secrets [taskId] --secrets NAME=value` | Replace all secrets on a task (full replacement) | |
| `nvcf-cli task bulk --task-ids=ID1,ID2` | Fetch details for multiple tasks in one call | |

> Registry credential propagation: after `registry-credential add`/`update`/`delete`, task creation can keep using the previous credential for up to about 5 minutes (NVCT caches account credentials at `nvct.nvcf.cache-ttl`, default `PT5M`), even though `registry-credential list`/`get` show the new value immediately. Wait about 5 minutes, or `kubectl -n nvcf rollout restart deployment/nvct-api` to apply immediately. See [reference/troubleshooting.md](reference/troubleshooting.md).

## LLM function type

Use `functionType: "LLM"` for OpenAI-compatible models served through the self-managed LLM Gateway. LLM functions must define at least one `models[]` entry with `name` and `llmConfig.uris`; the supported upstream paths are `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings`.

Use `/health` on port `8000` as the default OpenAI-compatible container health probe unless the image exposes a different readiness path.

LLM function type is independent of workload packaging. For a Helm-chart backed LLM function, keep `functionType: "LLM"` and `models[].llmConfig`, then set `helmChart` and `helmChartServiceName` in the create request. `helmChartServiceName` must match the Kubernetes Service exposed by the chart, and `inferencePort` must be that Service port.

Invocation uses the LLM route, for example `https://llm.invocation.<domain>/v1/chat/completions`. The OpenAI `model` value must be `<function-id>/<model-name>`; the function ID is the routing key and the model name is forwarded upstream.

Update mutable per-model routing settings with `nvcf-cli function update --llm-model-update='name=<model>,routingMethod=<round_robin|power_of_two|random>,tokenRateLimit=<limit>'`, or put the same fields under `modelUpdates[].llmConfig` in an update JSON file. `tokenRateLimit` supports positive integer limits for `S`, `M`, `H`, `D`, and `W`; use JSON input for combined limits such as `1000-S,5000-M,100000-H,500000-D,1000000-W`. Do not include `uris` in model updates.

For `/v1/responses`, the gateway proxies the native Responses path upstream, relays SSE to streaming clients, and aggregates the terminal JSON response for non-streaming clients. For `/v1/embeddings`, input may be a string or string array, must be non-empty, and may contain at most 2048 entries.

Session stickiness uses `x-multi-turn-session-id` for chat completions and Responses API requests only. Embeddings requests do not use stickiness.

## Common workflows

For step-by-step playbooks, load the prompt that matches the user's intent:

- **Install from scratch.** [prompts/install-from-scratch.md](prompts/install-from-scratch.md) — k3d cluster → preflight → up → deploy a smoke function.
- **Add a new compute plane.** [prompts/add-compute-plane.md](prompts/add-compute-plane.md) — split-cluster `up` against an existing control plane.
- **Deploy and invoke a function.** [prompts/deploy-and-invoke.md](prompts/deploy-and-invoke.md) — create → deploy → API key → invoke, including the LLM create/invoke variant.
- **Diagnose a failed install.** [prompts/diagnose-failed-install.md](prompts/diagnose-failed-install.md) — `status --json` → identify failed component → kubectl describe → remediation.
- **Rotate JWKS.** [prompts/rotate-cluster-jwks.md](prompts/rotate-cluster-jwks.md) — when PSAT auth starts failing.
- **Tear down.** [prompts/teardown.md](prompts/teardown.md) — `down --plan-only` first, then real run. `down --cluster-name=X` for one compute plane (orchestrator: drain + uninstall + cluster delete); `uninstall --control-plane` for the control plane (per-plane primitive); `down --all --confirm` for everything; `uninstall --no-apply <plane> | kubectl delete -f -` for GitOps.
- **Create and run a task.** [prompts/create-and-run-task.md](prompts/create-and-run-task.md) — mint API keys → task create (container or Helm) → monitor with `task get` / `task events` → retrieve results → cleanup.

## Reference

- [reference/commands.md](reference/commands.md) — full subcommand cheat sheet
- [reference/flags.md](reference/flags.md) — global + per-command flags
- [reference/exit-codes.md](reference/exit-codes.md) — what each non-zero exit means
- [reference/troubleshooting.md](reference/troubleshooting.md) — known errors → remediation table

## Safety rules — CRITICAL

**NEVER do these without explicit user confirmation:**
- `nvcf-cli self-hosted down` or `uninstall` in any form — destructive. **ALWAYS run with `--plan-only` (`down`) or `--no-apply` (`uninstall`) first** and show the user what would happen. State which compute plane(s) and whether persistent state would be wiped.
- `nvcf-cli self-hosted down --remove-persistent` (or `uninstall --remove-persistent`) — deletes Cassandra rows, OpenBao seal keys, sr-default user data. **Loss is unrecoverable.** Confirm explicitly that this is what the user wants.
- `nvcf-cli self-hosted uninstall --control-plane --force-with-registered-clusters` — orphans every registered compute plane (PSAT auth breaks immediately). State the consequence before passing this flag.
- `nvcf-cli self-hosted down --all` — nukes everything. Always show the cluster list (`nvcf-cli cluster list`) and get confirmation.
- `nvcf-cli cluster delete` — removes the cluster's ICMS registration; the compute plane immediately stops being able to authenticate.
- `nvcf-cli function delete` — removes a function and any active deployment.
- Any raw `helm uninstall` or `kubectl delete pvc/pv` — affects persistent state. Prefer `nvcf-cli self-hosted down` (orchestrator) or `uninstall` (per-plane) which handle this safely.
- Any `--force` flag (`--force-with-registered-clusters`, `--confirm` in non-interactive contexts).

**ALWAYS do these:**
- Run `nvcf-cli self-hosted status` before assuming a cluster exists / is healthy.
- Show the planned action (cluster name, function name, GPU type, cost if known) before creating.
- Before creating or deploying a container or LLM function, confirm the exact function name and container image with the user. For LLM functions, also confirm the exact model name used in `models[].name` and OpenAI `model: "<function-id>/<model-name>"`. If any value is missing, ask the user instead of guessing or submitting example placeholders.
- Confirm exact resource names before deletion — match against `cluster list` / `function list` output.
- In CI / non-interactive contexts, use `--non-interactive --token=$JWT`. Never propose interactive `nvcf-cli init` when `$CI` is set.

**NEVER paste these into chat / logs / feedback:**
- Admin tokens (full JWT). Show the first 8 chars + `...` if you must reference one.
- API keys (`nvapi-…`).
- Contents of `~/.nvcf-cli.state` or any kubeconfig.
- Any data marked secret in helmfile values.

> Note (only when operating more than one cluster from the same machine): the CLI's default
> config (`~/.nvcf-cli.yaml`) and state (`~/.nvcf-cli.state`) are a single shared slot, not
> per-cluster. With a single control plane (the common case) this needs no attention. If you
> manage multiple clusters, pass `--config <cluster>.yaml` on every command (or keep a
> deliberately-switched per-cluster default): with no `--config`, commands target whichever
> cluster was last `init`'d, and `init` / `api-key generate` mutate that shared default, so an
> unscoped `api-key generate` can mint or surface keys against the wrong cluster.

## Output modes (for agent piping)

`nvcf-cli` subcommands that long-run (`up`, `status`, `check`) accept four output modes:

- **`--json`** — JSONL events on stderr; one event per line; stable schema (`schemaVersion: 2`). **Use this when running under an agent.** Parse line-by-line with `jq -c .` or `json.loads()`.
- **`--plain`** — Plain timestamped lines, RFC3339 UTC, `[NN/8]` phase prefix; grep-friendly. Default in non-TTY.
- **`--accessible`** — Plain output without spinners, with verbose state markers (`[completed]`, `[running]`, `[pending]`, `[failed]`). For screen readers and constrained terminals.
- **(no flag)** — Bubbletea TTY dashboard. Default in TTY ≥100×30. Don't use under an agent (cursor-up sequences are noisy).

Auto-detect picks the right mode for whatever stdout/stderr is. The CLI also honors `NO_COLOR` (any value → forces plain), `TERM=dumb` (forces plain), and `CI=truthy` (forces plain even on a fake TTY). When the terminal is smaller than 100×30, the bubbletea renderer falls back to a compact layout. Explicit `--json` is the right call for agent piping.

## On failure

`nvcf-cli` failures emit structured `phase_failed` events in JSON:

```json
{"event":"phase_failed","phaseNum":4,"phase":"apply-cp",
 "errCategory":"helm_apply","errMessage":"helm install api-keys: timed out",
 "retryClass":"backoff","retryAfterSec":60,
 "remediation":["kubectl describe pod -n cassandra-system cassandra-0",
                "Re-run with --debug for verbose helmfile output"],
 "raw":{"subprocess":"helmfile","exitCode":1,"stderrTail":"…","kubernetesReason":"FailedScheduling"}}
```

The `retryClass` field tells the agent how to handle the failure:

| `retryClass` | Meaning | Agent action |
|---|---|---|
| `immediate` | Transient blip | Retry the same `up` command now |
| `backoff` | Rate limit / pending operation | Wait `retryAfterSec` seconds, then retry |
| `after_remediation` | Operator must intervene | Surface `errMessage` + `remediation`, STOP. Don't auto-retry. |
| `none` | Non-retryable | Same as `after_remediation` — surface and stop |
| `unknown` | Classifier unsure | Treat conservatively: surface and stop |

Never re-run with `--force` (no command takes one). The `raw` block carries the underlying signal (subprocess exit, HTTP status, K8s reason) — quote relevant fields when explaining failures to the user, but don't dump `stderrTail` verbatim into chat without scanning for secrets first.

## Quick command reference

```sh
nvcf-cli self-hosted check --pre              # pre-flight
nvcf-cli self-hosted up --cluster-name=NAME   # one-shot install
nvcf-cli self-hosted status                   # snapshot
nvcf-cli self-hosted status --watch           # live
nvcf-cli init                                 # mint admin token (clears all saved API keys)
nvcf-cli cluster register …                   # register cluster
nvcf-cli api-key generate --description=…     # mint both function and task API keys (run after every init)
nvcf-cli api-key generate --for function …    # function key only
nvcf-cli api-key generate --for task …        # task key only
nvcf-cli function create --input-file=…       # create function
nvcf-cli function create --function-type=LLM --llm-model=<spec>  # create LLM function metadata
nvcf-cli function create --name=llm-helm --inference-url=/ --inference-port=8000 \
  --function-type=LLM --helm-chart=<chart> --helm-chart-service=<service> --llm-model=<spec>
nvcf-cli function update --llm-model-update=<spec>  # update LLM routing/token limits
nvcf-cli function deploy create --input-file=…
nvcf-cli function invoke --input-file=…

# Task commands — requires NVCF_BASE_NVCT_URL=http://<nvct-gateway>
nvcf-cli task create --name=X --gpu=H100 --instance-type=Y --image=Z
nvcf-cli task create --input-file=task.json   # from JSON config
nvcf-cli task list                            # list all tasks
nvcf-cli task list --status=RUNNING           # filter by status
nvcf-cli task get [taskId]                    # task details
nvcf-cli task events [taskId]                 # lifecycle events
nvcf-cli task results [taskId]                # result artifacts
nvcf-cli task cancel [taskId]
nvcf-cli task delete [taskId]
```

## Feedback

If the user hits a bug or limitation, file an issue through the project tracker. Don't include secrets.
