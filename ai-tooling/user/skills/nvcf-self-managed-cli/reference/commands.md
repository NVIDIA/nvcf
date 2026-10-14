# `nvcf-cli` command reference

Full subcommand list. Always pair with [flags.md](flags.md) for global flags and [exit-codes.md](exit-codes.md) for return codes.

## Self-hosted lifecycle

| Command | Purpose | Output | Notes |
|---|---|---|---|
| `self-hosted check --pre [flags]` | Pre-flight before install (no admin creds): local tools, registry credentials, and both planes' cluster checks | streaming events on stderr | Split clusters need both `--control-plane-context` and `--compute-plane-context`; `--local-only` makes no cluster or registry contact |
| `self-hosted check --control-plane [--wait DUR]` | Installed control plane: leftover namespaces and the control-plane cluster-validator | streaming events on stderr | `--wait` polls until pass or DUR elapses |
| `self-hosted check --compute-plane [--wait DUR]` | Installed compute plane: leftover namespaces, node inotify limits, SIS reachability, and the compute-plane cluster-validator | streaming events on stderr | SIS is probed only at a configured `--icms-url`. Without context flags it checks the current context |
| `self-hosted check --all [--wait DUR]` | Both of the above | streaming events on stderr | Gate on the `final` event; see [exit-codes.md](exit-codes.md) |
| `self-hosted install --control-plane` | Render control-plane manifests | YAML on stdout | Pipe to `kubectl apply -f -` |
| `self-hosted install --compute-plane --cluster-name=X` | Register cluster + render compute manifests | YAML on stdout | One invocation per GPU cluster |
| `self-hosted up --cluster-name=X` | One-shot first install (both planes) | (empty) stdout, progress on stderr | Always installs both planes; for compute-only use `compute-plane register` and `compute-plane install` |
| `self-hosted up --plan-only --cluster-name=X` | Dry-run preview | JSONL on stderr | Agent-friendly, no cluster changes |
| `self-hosted compute-plane register --control-plane-profile=P --cluster-name=X --kube-context=Y [--output=V]` | Register a compute cluster with an existing control plane and write its NVCA operator values | summary on stdout | For the 2nd, 3rd, ... GPU cluster after the first install. CP not touched |
| `self-hosted compute-plane install --values=V --kube-context=Y` | Install a compute plane from the values `compute-plane register` wrote | progress on stderr | Run after `compute-plane register`. Installs V whatever its name, never another values file beside it |
| `self-hosted status [--cluster-name=X] [--watch]` | Snapshot cluster health | TTY/plain/JSONL | `--watch` for live re-render |
| `self-hosted uninstall --compute-plane --cluster-name=X` | Per-plane primitive: `helmfile destroy` on one compute plane (no ICMS unregister, no drain) | progress | Just the helm releases on that compute plane |
| `self-hosted uninstall --control-plane [--force-with-registered-clusters]` | Per-plane primitive: `helmfile destroy` on the control plane | progress | Refuses if compute planes still registered unless `--force-with-registered-clusters` |
| `self-hosted uninstall --no-apply <plane>` | Render delete YAML via `helm get manifest` per release | YAML on stdout | GitOps; pipe to `kubectl delete -f -` or commit + Argo applies. Mirrors `install --no-apply` |
| `self-hosted down --cluster-name=X [--drain-active] [--remove-persistent]` | Orchestrator: drain → uninstall --compute-plane → cluster delete in ICMS | progress | Symmetric companion to `up --cluster-name=X` |
| `self-hosted down --all [--confirm]` | Orchestrator: tear down every registered compute plane + control plane | progress | Bounded parallelism via `--all-concurrency` (default 4); `--confirm` required in non-interactive |
| `self-hosted down --plan-only ...` | Orchestrator dry-run: phases + helm releases + ICMS rows + ETAs (no helm/helmfile contact) | JSONL | **Always run before any actual `down`** |

When the control plane runs the Event Ledger, `self-hosted up`, `self-hosted install --compute-plane`, and `compute-plane register` turn on the NVCA OTel collector for the new cluster. `up` and `install` detect it from the `event-ledger` Helm release on the control-plane context. `compute-plane register` reads `controlPlane.addons.eventLedger` from the control-plane profile. If the check fails, the collector stays off.

## Cluster management

| Command | Purpose | Notes |
|---|---|---|
| `cluster register --name=X --nca-id=Y [--region=Z] [--ignore-existing]` | Register a JWKS+OIDC with ICMS | Used by `up` Phase 5 internally; standalone for manual registration |
| `cluster list-registered --nca-id=Y [--icms-url=URL]` | List self-hosted cluster registrations from ICMS | Uses the admin token; `--json` for machine-readable output |
| `cluster rotate --cluster-id=ID` | Re-fetch JWKS from K8s and PUT to ICMS | After K8s API server signing key rotation |
| `cluster delete --cluster-id=ID` | Remove ICMS row | **DESTRUCTIVE: confirm with user** |

## Functions

| Command | Purpose | Notes |
|---|---|---|
| `function create --input-file=FILE` | Create function metadata | Returns Function ID + Version ID; LLM functions use `functionType: "LLM"` and `models[].llmConfig.uris` for `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings` |
| `function list` / `function list-ids` | List functions / IDs only | |
| `function get --function-id=ID --version-id=VID` | Function metadata | |
| `function update --function-id=ID --version-id=VID --tags=TAG[,TAG]` | Update function tags | |
| `function update --function-id=ID --version-id=VID --llm-model-update=SPEC` | Update LLM model routing config | `SPEC` uses `name=<model>,routingMethod=<method>,tokenRateLimit=<limit>`; the routing method format is in [flags.md](flags.md) |
| `function deploy create --input-file=FILE` | Schedule a deployment | Blocks until ACTIVE (default 900s) |
| `function deploy get --function-id=ID --version-id=VID [--json]` | Deployment status | `functionStatus: ACTIVE\|DEPLOYING\|ERROR\|FAILED` |
| `function deploy update --input-file=FILE` | Modify a deployment in place | |
| `function deploy remove --function-id=ID --version-id=VID` | Stop a deployment, keep function record | |
| `function invoke --input-file=FILE` | Call a regular function | Requires API key (not admin token); LLM functions are invoked through `llm.invocation.<domain>` with `model: "<function-id>/<model-name>"` |
| `function delete --function-id=ID --version-id=VID` | Remove function + deployment | **DESTRUCTIVE: confirm with user** |

## Tasks

Task commands require `NVCF_API_KEY` set to a task-scoped key and `NVCF_BASE_NVCT_URL` set to the NVCT gateway endpoint. In a fresh session, read `nvctApiKey` from `~/.nvcf-cli.state` and inject it as `NVCF_API_KEY` without logging the value; check `nvctApiKeyExpiration` first and regenerate with `api-key generate --for task --validate` if expired. Run `api-key generate --for task` after `init` to mint a new key.

| Command | Purpose | Notes |
|---|---|---|
| `task create --name=X --gpu=GPU --instance-type=TYPE --image=IMG` | Submit a container task | Saves task ID to CLI state; use `--input-file` for repeatable configs |
| `task create --name=X --gpu=GPU --instance-type=TYPE --helm-chart=CHART` | Submit a Helm task | `--helm-chart` replaces `--image` |
| `task create --input-file=FILE` | Submit a task from a JSON config file | Recommended for repeatable configurations |
| `task list [--status=STATUS] [--limit=N]` | List tasks, optionally filtered by status | Statuses: `QUEUED`, `LAUNCHED`, `RUNNING`, `COMPLETED`, `ERRORED`, `CANCELED`, `EXCEEDED_MAX_RUNTIME_DURATION`, `EXCEEDED_MAX_QUEUED_DURATION` |
| `task get [taskId]` | Get task details | Uses saved task ID from state if omitted |
| `task events [taskId] [--limit=N]` | List lifecycle events for a task | |
| `task results [taskId]` | List result artifacts for a completed task | Returns empty list when `resultHandlingStrategy` is `NONE`; result upload not yet supported |
| `task cancel [taskId]` | Cancel a running task | No-op if already in terminal state |
| `task delete [taskId]` | Delete a task record | **Stop, state task ID + current status, and wait for a subsequent user reply explicitly confirming deletion of that specific task. Do not treat the original delete request as confirmation.** |
| `task update-secrets [taskId] --secrets NAME=value` | Update secrets on a task; supplied secrets are added or updated by name, existing secrets not in the request are preserved | Values are encrypted at rest |
| `task bulk --task-ids=ID1[,ID2,…]` | Fetch details for multiple tasks in one request | |

## Auth

| Command | Purpose | Notes |
|---|---|---|
| `init` | Mint admin token from API Keys via public api gateway | Idempotent; writes `~/.nvcf-cli.state`; clears saved function state and all API keys (function and task) |
| `status` | Show CLI state (token, last endpoints, function context) | Distinct from `self-hosted status` |
| `api-key generate [--for function\|task\|event-ledger] [--description=…] [--expires-in=…]` | Mint API keys | Default (no `--for`): generates a function key, a task key and an Event Ledger key; `--for function`, `--for task` or `--for event-ledger` generates one only (the Event Ledger key has the `fnds:getEvents` scope and is stored as `eventLedgerApiKey`); `--scopes` requires `--for` |
| `api-key generate --validate` | Mint and immediately validate each generated key | Validates each key against its correct audience (NVCF for function keys, NVCT for task keys) |
| `api-key list` | List API keys for current owner | Lists NVCF-scoped keys only |
| `api-key delete KEY-ID` / `api-key revoke KEY-ID` | Remove an API key | **Confirm with user** |
| `api-key show` | Show the saved function, task and Event Ledger keys from state | |

## Event Ledger

Event Ledger commands need the Event Ledger addon on the control plane and an API key with the `fnds:getEvents` read scope (`api-key generate --for event-ledger`). The URL comes from `--event-ledger-url`, `EVENT_LEDGER_URL`, or `event_ledger_url`. Without one, it is derived from `base_http_url` by replacing the leading `api.` with `events.`. Set `event_ledger_host` (or `EVENT_LEDGER_HOST`) when the gateway routes by Host header.

| Command | Purpose | Notes |
|---|---|---|
| `stats get [--function-version-id=VID] [--event-filter=NAME[,NAME]]` | Latest event for every instance of a function version | The version ID must be a UUID; uses the saved version ID when the flag is omitted; `--json` for machine-readable output |
| `stages events list [--function-version-id=VID] [--instance-id=ID] [--deployment-id=ID] [--gpu-specification-id=ID] [--cluster-id=ID] [--resource-id=ID] [--attribute-key=K --attribute-value=V]` | Event history of one instance, most recent first | Context filters (`--instance-id`, `--deployment-id`, `--gpu-specification-id`, `--cluster-id`, `--resource-id`) are optional and must match how the events were ingested; with none, it returns the events stored without a context. `--attribute-key` and `--attribute-value` go together and keep only events with that attribute value. Run `stats get` first to see the contexts |

## Agent skill

| Command | Purpose | Notes |
|---|---|---|
| `agent-skill install [--target=DIR]` | Install all bundled public user skills into `~/.claude/skills/` and `~/.agents/skills/` | Default: both base skills directories |
| `agent-skill uninstall [--target=DIR]` | Remove only bundled public user skills from the target base skills directories | Idempotent; unrelated skills remain |
| `agent-skill show [--file=REL]` | Print bundled `nvcf-self-managed-cli/SKILL.md` or another embedded file | For debugging |
| `agent-skill version` | Print build SHA and embedded public user skill summary | |
