# `nvcf-cli` exit codes

Stable across subcommands. Use these to drive agent retry / surfacing logic.

| Code | Meaning | Example causes |
|---|---|---|
| `0` | Success | All checks passed; install completed; deployment ACTIVE |
| `1` | Generic error | Helm render failed; network unreachable; file not found; YAML parse error |
| `2` | Pre-flight check failed | A `check` result at error severity: Gateway API CRDs missing; kubectl not on PATH with `--pre`; default StorageClass absent; a cluster the CLI cannot reach (missing kubeconfig or context, or a first API call that fails for any reason: refused or dropped connection, rejected credentials, a failing credential plugin, a server error); a cluster-validator that could not run (RBAC denied, image pull failure, its own timeout, no tag found for an untagged image); a `stale-namespaces` row naming a namespace `stuck Terminating`. Such a result is exit `2` even when the time budget cut another check short. Warning-severity results exit `0`, including a namespace still deleting, a Helm release left mid-operation, a namespace with no Helm release, and a `cluster-validator` row saying no `cluster_validator_image` is set |
| `3` | Admin auth failed | No token + `--non-interactive` set; ICMS rejected JWT; init endpoint unreachable |
| `5` | Manifest apply or `--wait` timed out | Helm install timeout; check polled but did not pass before DURATION, including a rollout still in progress; the check's time budget stopped a check that can fail the run before it finished (its row says not run or cut short) and no other check failed. A cut-short row keeps the worst severity its check can report, so a check that can only warn, such as a registry that is not critical, stays a warning. A check that already had its result keeps it, so a validator's own failure is exit `2` |
| `130` | Cancelled by SIGINT, SIGTERM or SIGHUP, or by a quit key in the `check --wait` dashboard | User Ctrl-C; `q`, `Esc` or `Ctrl-C` in the dashboard; CI budget exceeded; pod evicted; terminal or SSH session closed |

## How an agent should react

| Code | Agent behavior |
|---|---|
| `0` | Continue / report success. |
| `1` | Surface stderr to user; ask before any retry. Generic errors usually need a human to read the message. |
| `2` | Surface failed checks + their `hintURL`. Don't propose `up` until the user fixes prereqs. |
| `3` | Suggest `nvcf-cli init` (interactive) OR `--token=$JWT` (CI). Don't auto-mint without user OK. |
| `5` | Ask user if they want to wait longer, dig into the stalled component, or cancel. |
| `130` | Don't retry automatically; user explicitly cancelled. Confirm before re-running. |

## Where to find more detail

`check` reports its outcome in one `final` event (`success`, `verdict`, `totalChecks`, `passedCount`, `failedCount`, `warningCount`) and never emits `phase_failed`; see `examples/ci-pipelines.md`. A usage error, such as a malformed `--wait`, exits `1` before any event. Its exit `5` has `success: false` and `verdict: "timeout"`. A `check_completed` event for a warning that `--wait` polls on carries `transient: true`, and one for a check the time budget stopped carries `cutShort: true`. A cancellation (exit `130`) emits `final` with `cancelled: true` instead of `phase_failed`; `up` emits `phase_cancelled` before it. When a cluster-validator run left objects in the cluster (`--no-cleanup`, a pod that would not stop, or a cleanup that failed), `check` lists the commands that remove them in the `final` event's `cleanup` array and in that row's `cleanup` field, on every exit code, and prints them again as its last stderr lines. On an interrupt it prints them at once, before its own cleanup, which a second Ctrl-C cuts short. Every other non-zero exit emits a structured `phase_failed` JSON event with `errCategory`, `errMessage`, `remediation` (array), `retryClass` (enum: `none|immediate|backoff|after_remediation|unknown`), `retryAfterSec` (int, optional), and `raw` (subprocess + HTTP + Kubernetes signal). Always consume these in `--json` mode rather than parsing English from stderr.
