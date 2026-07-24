# `nvcf-cli` command reference

Full subcommand list. Always pair with [flags.md](flags.md) for global flags and [exit-codes.md](exit-codes.md) for return codes.

## Self-hosted lifecycle

| Command | Purpose | Output | Notes |
|---|---|---|---|
| `self-hosted check --pre [flags]` | Pre-flight validation (no admin creds) | streaming events | Per-role with `--control-plane-context` / `--compute-plane-context`; `--local-only` skips kubectl |
| `self-hosted check --control-plane [--wait DUR]` | Every CP release healthy | streaming events | Polls until pass or `--wait` elapses |
| `self-hosted check --compute-plane --cluster-name=X [--wait DUR]` | Per-cluster worker state | streaming events | NVCA + operator + JWKS-fingerprint match |
| `self-hosted check --all [--wait DUR]` | Fan-out over all of the above | streaming events | |
| `self-hosted install --control-plane` | Render control-plane manifests | YAML on stdout | Pipe to `kubectl apply -f -` |
| `self-hosted install --compute-plane --cluster-name=X` | Register cluster + render compute manifests | YAML on stdout | One invocation per GPU cluster |
| `self-hosted up --cluster-name=X` | One-shot first install (both planes) | (empty) stdout, progress on stderr | Always installs both planes; for compute-only use `add-compute-plane` |
| `self-hosted up --plan-only --cluster-name=X` | Dry-run preview | JSONL on stderr | Agent-friendly, no cluster changes |
| `self-hosted add-compute-plane --cluster-name=X --compute-plane-context=Y --icms-url=URL --token=$JWT` | Add a compute plane to an existing control plane | progress on stderr | For 2nd, 3rd, … GPU cluster after the first install. CP not touched. |
| `self-hosted status [--cluster-name=X] [--watch]` | Snapshot cluster health | TTY/plain/JSONL | `--watch` for live re-render |
| `self-hosted uninstall --compute-plane --cluster-name=X` | Per-plane primitive: `helmfile destroy` on one compute plane (no ICMS unregister, no drain) | progress | Just the helm releases on that compute plane |
| `self-hosted uninstall --control-plane [--force-with-registered-clusters]` | Per-plane primitive: `helmfile destroy` on the control plane | progress | Refuses if compute planes still registered unless `--force-with-registered-clusters` |
| `self-hosted uninstall --no-apply <plane>` | Render delete YAML via `helm get manifest` per release | YAML on stdout | GitOps; pipe to `kubectl delete -f -` or commit + Argo applies. Mirrors `install --no-apply` |
| `self-hosted down --cluster-name=X [--drain-active] [--remove-persistent]` | Orchestrator: drain → uninstall --compute-plane → cluster delete in ICMS | progress | Symmetric companion to `up --cluster-name=X` |
| `self-hosted down --all [--confirm]` | Orchestrator: tear down every registered compute plane + control plane | progress | Bounded parallelism via `--all-concurrency` (default 4); `--confirm` required in non-interactive |
| `self-hosted down --plan-only ...` | Orchestrator dry-run: phases + helm releases + ICMS rows + ETAs (no helm/helmfile contact) | JSONL | **Always run before any actual `down`** |

## Cluster management

| Command | Purpose | Notes |
|---|---|---|
| `cluster register --name=X --nca-id=Y [--region=Z] [--ignore-existing]` | Register a JWKS+OIDC with ICMS | Used by `up` Phase 5 internally; standalone for manual registration |
| `cluster list` | List registered clusters | Output is YAML by default; `--json` for machine |
| `cluster rotate --cluster-id=ID` | Re-fetch JWKS from K8s and PUT to ICMS | After K8s API server signing key rotation |
| `cluster delete --cluster-id=ID` | Remove ICMS row | **DESTRUCTIVE: confirm with user** |

## Functions

| Command | Purpose | Notes |
|---|---|---|
| `function create --input-file=FILE` | Create function metadata | Returns Function ID + Version ID; LLM functions use `functionType: "LLM"` and `models[].llmConfig.uris` for `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings` |
| `function list` / `function list-ids` | List functions / IDs only | |
| `function get --function-id=ID --version-id=VID` | Function metadata | |
| `function update --function-id=ID --version-id=VID --tags=TAG[,TAG]` | Update function tags | |
| `function update --function-id=ID --version-id=VID --llm-model-update=SPEC` | Update LLM model routing config | `SPEC` uses `name=<model>,routingMethod=<round_robin|power_of_two|random>,tokenRateLimit=<limit>`; use JSON for combined token limits |
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
| `task delete [taskId]` | Delete a task record | **Confirm with user** |
| `task update-secrets [taskId] --secrets NAME=value` | Update secrets on a task; supplied secrets are added or updated by name, existing secrets not in the request are preserved | Values are encrypted at rest |
| `task bulk --task-ids=ID1[,ID2,…]` | Fetch details for multiple tasks in one request | |

## Auth

| Command | Purpose | Notes |
|---|---|---|
| `init` | Mint admin token from API Keys via public api gateway | Idempotent; writes `~/.nvcf-cli.state`; clears saved function state and all API keys (function and task) |
| `status` | Show CLI state (token, last endpoints, function context) | Distinct from `self-hosted status` |
| `api-key generate [--for function\|task] [--description=…] [--expires-in=…]` | Mint API keys | Default (no `--for`): generates both a function key and a task key; `--for function` or `--for task` generates one only; `--scopes` requires `--for` |
| `api-key generate --validate` | Mint and immediately validate each generated key | Validates each key against its correct audience (NVCF for function keys, NVCT for task keys) |
| `api-key list` | List API keys for current owner | Lists NVCF-scoped keys only |
| `api-key delete KEY-ID` / `api-key revoke KEY-ID` | Remove an API key | **Confirm with user** |
| `api-key show` | Show currently saved API key from state | |

## Agent skill

| Command | Purpose | Notes |
|---|---|---|
| `agent-skill install [--target=DIR]` | Install all bundled public user skills into `~/.claude/skills/` and `~/.agents/skills/` | Default: both base skills directories |
| `agent-skill uninstall [--target=DIR]` | Remove only bundled public user skills from the target base skills directories | Idempotent; unrelated skills remain |
| `agent-skill show [--file=REL]` | Print bundled `nvcf-self-managed-cli/SKILL.md` or another embedded file | For debugging |
| `agent-skill version` | Print build SHA and embedded public user skill summary | |
