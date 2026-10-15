# Tear down a self-hosted NVCF deployment

**SAFETY:** Teardown destroys persistent state (Cassandra rows, OpenBao seal keys, registered clusters). Always confirm with the user before any step.

## TL;DR — `down` for one-shot, `uninstall` for per-plane

The CLI provides a two-tier teardown surface symmetric with the install side (REQ-22, M+11):

| Tier | Up side (existing) | Down side |
|---|---|---|
| Per-plane primitive (renders YAML; GitOps-friendly) | `install --control-plane` / `install --compute-plane` | `uninstall --control-plane` / `uninstall --compute-plane` |
| Orchestrator (one-shot, multi-plane + ICMS) | `up --cluster-name=X` | `down --cluster-name=X` / `down --all` |

Use `down` for normal teardown. Use `uninstall` for GitOps (commit YAML; Argo/Flux applies) or scripted pipelines. Always start with a dry-run.

## Dry-run shapes

| Mode | Lives on | Invokes helm/helmfile? | Output |
|---|---|---|---|
| `--plan-only` | `up` / `down` (orchestrators) | **No.** Walks resolved stack entrypoint files, emits phase plan + ETAs + helm uninstall command strings. | Event stream |
| `--no-apply` | `install` / `uninstall` (primitives) | **Yes.** `helmfile template` for install; `helm get manifest` for uninstall. | YAML on stdout |

So:
- `down --plan-only --cluster-name=X` — show me the orchestrator phases without invoking anything.
- `uninstall --no-apply --compute-plane --cluster-name=X` — give me the YAML I'd `kubectl delete -f -`.

```sh
# Step 1: ALWAYS preview first.
nvcf-cli self-hosted down --plan-only --cluster-name=ncp-local --json | jq

# Step 2: Real run.
nvcf-cli self-hosted down --cluster-name=ncp-local
```

## When to use which form

| User intent | Command |
|---|---|
| "Remove this one GPU cluster" | `nvcf-cli self-hosted down --cluster-name=X` |
| "Tear down everything" | `nvcf-cli self-hosted down --all --confirm` |
| "Show me the steps without invoking anything" | `nvcf-cli self-hosted down --plan-only --cluster-name=X` |
| "Give me YAML to commit + Argo deletes later" | `nvcf-cli self-hosted uninstall --no-apply --compute-plane --cluster-name=X > delete.yaml` |
| "Just remove the helm releases on a compute plane (no ICMS unregister, no drain)" | `nvcf-cli self-hosted uninstall --compute-plane --cluster-name=X` |
| "Just remove the control-plane helm releases" | `nvcf-cli self-hosted uninstall --control-plane` |

## Steps for `down --cluster-name=X` (the standard path)

### 1. Confirm scope with the user

Ask:
- "Tearing down everything (compute planes + control plane + ICMS rows) or just one compute plane?"
- "Are there ACTIVE function deployments on this cluster? `nvcf-cli function deploy list` to check." (Important — `down` will refuse without `--drain-active=true|prompt` if any are ACTIVE.)
- "Should we wipe persistent state (Cassandra data, OpenBao seal keys, sr-default user data)? Default is to **preserve** them. `--remove-persistent` opts in to deletion. **Loss is unrecoverable.**"

### 2. Run plan-only first

```sh
nvcf-cli self-hosted down --plan-only --cluster-name=$NAME --json | jq
```

This emits a `planned` event listing each phase, the helm releases that would be uninstalled, the ICMS rows that would be removed, and ETAs. Copy the `willUninstall.commands[]` array to the user — those are the literal `helm uninstall …` invocations the orchestrator will run.

### 3. Execute the down

```sh
nvcf-cli self-hosted down --cluster-name=$NAME --json
```

Phases (1-7, mirrors §6.7.1):

1. Preflight — kubectl access + ICMS reachable
2. Resolve stack bundle
3. Drain active deployments — runs `function deploy remove` for each ACTIVE deployment, polls until STOPPED. Subject to `--drain-active=true|false|prompt` (default is `prompt` in TTY).
4. Uninstall compute plane — delegates to `uninstall --compute-plane`; `helm uninstall <release>` per release in reverse-DAG order
5. Remove persistent (skipped unless `--remove-persistent`)
6. Unregister cluster from ICMS
7. Final verify

### 4. Tear down the control plane (if applicable)

```sh
nvcf-cli self-hosted uninstall --control-plane
```

Or via the orchestrator's `--all` mode if every compute plane is also going away (see step 5).

`uninstall --control-plane` refuses if any compute plane is still registered in ICMS. Use `--force-with-registered-clusters` to override **only** if the user explicitly wants to orphan compute planes (PSAT auth breaks for those clusters — they will start failing every health check). Always state the consequence before passing the flag.

### 5. Or, nuke everything

```sh
nvcf-cli self-hosted down --all --confirm
```

This iterates every registered compute plane in ICMS, runs the per-cluster down for each in parallel (bounded by `--all-concurrency`, default 4), then runs `uninstall --control-plane`. Interactive mode prompts the operator with the cluster list before proceeding; `--confirm` skips the prompt for non-interactive use.

## GitOps path (if user has Argo / Flux)

```sh
# Render delete YAML for the compute plane:
nvcf-cli self-hosted uninstall --no-apply --compute-plane --cluster-name=$NAME > delete.yaml

# Operator commits delete.yaml to their GitOps repo. Argo/Flux applies the deletion on next sync.
git add delete.yaml; git commit -m "remove compute plane $NAME"; git push

# After Argo sync removes the K8s resources, separately remove the ICMS row:
nvcf-cli cluster delete --cluster-id=$CLUSTER_ID
```

Note: `--no-apply` does NOT include the ICMS `cluster delete` (helm doesn't manage that). Operators following GitOps must run `nvcf-cli cluster delete` separately, or use `down` to orchestrate both.

## Failure recovery

If `down` partial-fails mid-way, **re-run the same command**. The orchestrator is idempotent: `helm uninstall` skips releases that are already gone, and `cluster delete --ignore-missing` skips ICMS rows that are already gone. Phase 4 + 6 emit `phase: skipped (already clean)` on the second run.

If a `phase_failed` event surfaces:
- `errCategory: "helm_pending_upgrade", retryClass: "after_remediation"` → run `helm rollback <release> 0 --kube-context=…` (the message includes the exact release name) then re-run `down`. Do NOT auto-rollback without user confirmation.
- `errCategory: "compute_plane", retryClass: "after_remediation"` with message about ACTIVE deployments → user passed `--drain-active=false` but deployments exist. Re-run with `--drain-active=true` or `--drain-active=prompt` and confirm with the user.
- Other categories → surface `errMessage` + each `remediation` string verbatim. STOP. Do not propose retry without user input.

## Always confirm

For every `nvcf-cli self-hosted down` or `uninstall` invocation, especially with destructive flags:
- State the exact `--cluster-name` (or `--all`).
- State what's lost (e.g. "all function deployments scheduled on `ncp-local` will stop; compute plane will deregister from ICMS").
- If `--remove-persistent` is being considered, state that Cassandra rows + OpenBao seal keys will be deleted and that this is unrecoverable.
- Wait for explicit user confirmation.
- Never propose `--force-with-registered-clusters` without explaining that compute planes will be orphaned.
- Never propose `--remove-persistent` without explaining the loss.

## Manual fallback (if `nvcf-cli self-hosted {uninstall,down}` is unavailable)

If the user is on a CLI version older than M+11, fall back to:

```sh
# Per compute plane (on its kubectl context):
helm uninstall nvca-agent -n nvca-system
helm uninstall nvca-operator -n nvca-operator
helm uninstall image-credential-helper -n nvca-system
helm uninstall sharedstorage -n nvca-system

# Drain the ICMS row:
nvcf-cli cluster delete --cluster-id=<id>

# Control plane teardown (on its kubectl context):
cd nvcf-self-managed-stack
make destroy   # invokes helmfile destroy across all releases

# If using split stack bundles, teardown compute plane releases too:
cd ../nvcf-compute-plane-stack
make destroy
```

This is the manual path; prefer the new subcommands whenever they're installed.

## What `kubectl delete pvc` does

If the user asks "why is my new install seeing the old data?" — the StatefulSet PVCs (Cassandra, OpenBao) are NOT deleted by `helm uninstall` (and so are NOT deleted by `nvcf-cli self-hosted down` either, by default). They persist intentionally so a re-`up` can resume against existing data. To wipe completely on the next `down`:

```sh
nvcf-cli self-hosted down --cluster-name=ncp-local --remove-persistent
```

Or, post-down, manually:

```sh
kubectl delete pvc -n cassandra-system --all
kubectl delete pvc -n vault-system --all
```

**Confirm with user.** This is the destructive step.
