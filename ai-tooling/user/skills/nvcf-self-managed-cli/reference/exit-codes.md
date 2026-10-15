# `nvcf-cli` exit codes

Stable across subcommands. Use these to drive agent retry / surfacing logic.

| Code | Meaning | Example causes |
|---|---|---|
| `0` | Success | All checks passed; install completed; deployment ACTIVE |
| `1` | Generic error | Helm render failed; network unreachable; file not found; YAML parse error |
| `2` | Pre-flight check failed | Gateway API CRDs missing; kubectl not on PATH; default StorageClass absent |
| `3` | Admin auth failed | No token + `--non-interactive` set; ICMS rejected JWT; init endpoint unreachable |
| `5` | Manifest apply or `--wait` timed out | Helm install timeout; check polled but didn't pass before duration |
| `130` | Cancelled by SIGINT/SIGTERM | User Ctrl-C; CI budget exceeded; pod evicted |

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

Every non-zero exit emits a structured `phase_failed` JSON event with `errCategory`, `errMessage`, `remediation` (array), `retryClass` (enum: `none|immediate|backoff|after_remediation|unknown`), `retryAfterSec` (int, optional), and `raw` (subprocess + HTTP + Kubernetes signal). Always consume these in `--json` mode rather than parsing English from stderr.
