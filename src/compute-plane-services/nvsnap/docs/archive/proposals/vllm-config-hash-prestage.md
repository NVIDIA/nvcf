# Pre-stage the model on the capture pod (vLLM config_hash stability)

Status: RESOLVED — fixed by removing HF_HUB_OFFLINE (v0.0.99); pre-stage moot. See bottom.
Context: ember-cache-env.md Rule 6; gpt-oss-120b cachedir bench, 2026-06-19.

## Problem

vLLM folds the **resolved `model`/`tokenizer` string** into `config_hash`, which keys
its `torch.compile` cache (`~/.cache/vllm/torch_compile_cache/<config_hash>/`). The string
differs between capture and restore:

- **Capture** (cold): the model is not yet in `HF_HOME`, so vLLM keeps the **repo-id**
  `openai/gpt-oss-120b` → `config_hash=…9d8bb3c2d6`.
- **Restore**: the model is present (RO-mounted from rox at `/opt/nvsnap/model`), so vLLM
  **resolves** the repo-id to `/opt/nvsnap/model/hub/models--…/snapshots/<commit>/` → a
  different string → `config_hash=…23aea655ff`.

Result: compile-cache **miss** at restore → ~20s recompile every time, even though the
captured cache is complete and at the right path. Measured: gpt-oss restore would drop
~20s (234s → ~214s). vLLM-only; SGLang (ninja/flashinfer, content+path keyed) is unaffected
and already reuses after the mtime fix.

## Why the obvious alternatives don't work

- **Make restore use the repo-id**: impossible — vLLM resolves to a path whenever the model
  is in `HF_HOME`, and at restore it always is (RO mount). Can't un-resolve.
- **Rewrite `--model` to the literal snapshot path at both ends**: breaks **cold capture**
  (the path doesn't exist until download) and bakes in a commit SHA. Rejected.

So the fix must make the **capture** pod resolve the repo-id the same way restore does —
i.e. the model must be present in `HF_HOME` *before* vLLM starts on the capture pod.

## Design: capture-side pre-stage init container

On capture pods only (cachedir mode, no `restore-from`), inject an init container that
downloads the model into `HF_HOME=/opt/nvsnap/model` before the main container runs. Then
the main vLLM container resolves the repo-id → the same `…/snapshots/<commit>/` path
restore will resolve → identical `config_hash` → compile-cache hit at restore.

Injected by `cacheDirCapturePatches` alongside the existing `/opt/nvsnap` emptyDir:

```
initContainers:
- name: nvsnap-prestage-model
  image: <same image as main container>     # has python + huggingface_hub
  env: <main container's HF_* + HOME + HF_HOME>   # creds/proxy/token, same cache path
  command: ["python","-c","from huggingface_hub import snapshot_download; snapshot_download('<repo-id>')"]
  volumeMounts:
  - {name: nvsnap-cachedir, mountPath: /opt/nvsnap}    # same emptyDir the main container gets
```

### Model-id extraction
Parse the main container's `command`+`args` for the engine's model flag:
`--model <X>` (vLLM), falling back to `--model-path <X>` (SGLang). If no flag is found,
**skip pre-stage** (no regression — capture proceeds exactly as today, just without compile
reuse). Optional override: a `nvsnap.io/model` pod annotation set by the operator.

### Key properties
- **RO restore is untouched.** This is capture-side only. Restore still RO-mounts the model
  from rox; resolution + weight-load are pure reads (already proven). The model dir never
  needs to be writable at restore. (This is the explicit compatibility requirement.)
- **Same commit both ends.** Restore reads the captured snapshot — the exact commit the
  capture pod downloaded — so capture's own vLLM resolves to that same commit. Strings match.
- **Capture time ~neutral.** The cold capture already downloads the model; pre-staging just
  moves that download into a serialized init step. Net wall-clock ≈ unchanged.
- **Image reuse, no new artifact.** Init container uses the main image (already pulled),
  avoiding a new cross-registry dependency (cf. rule 13).

## Hard constraints (non-negotiable)

1. **Nothing may fail.** The pre-stage step must NEVER fail the capture pod. It is an
   optimization; on any error the pod must proceed exactly as today.
2. **Performance must never get worse.** Pre-stage must not add wall-clock to capture and
   must not increase the captured/snapshot byte count.
3. **Workload-agnostic.** Functions are arbitrary — wrapper scripts, `sh -c`, env-driven
   model ids, custom entrypoints, non-vLLM engines, outright garbage. The mechanism must NOT
   parse the serve command for `--model`, must NOT assume the engine is vLLM, and must NOT
   trust function-supplied metadata. It has to work the same across everything or not engage.

Constraint #3 **rejects** the original design below (parse `--model` → metadata pre-fetch):
it depends on reading the model id out of the command, which is exactly the fragile,
per-engine assumption we can't make. Kept for history; see "Revised approach".

A full `snapshot_download` violates #2: it pulls the *entire* repo (e.g. duplicate
`original/` pytorch weights vLLM never loads) → more bytes than vLLM fetches → bigger
snapshot + slower. So the naive "download the model in an init container" is rejected.

## Key insight: metadata-only pre-fetch (byte-neutral)

`config_hash` flips from repo-id to path the moment vLLM **resolves** the repo-id to a local
snapshot — which only requires the HF cache to contain `refs/main` + a `snapshots/<commit>/`
dir, NOT the multi-GB weights. So pre-fetch **only the small metadata** (`config.json`,
`*.json`, tokenizer files — KB–MB), which makes vLLM resolve to the path; vLLM then streams
the **weights** exactly as it does today (same files, same bytes, same time). Net extra
download ≈ a few hundred KB of config that vLLM would fetch anyway → **byte-neutral, not
slower**. The weights are NOT pre-downloaded.

> Empirical check required before coding: confirm that a metadata-only snapshot (config +
> tokenizer, `allow_patterns` excluding weight files) is sufficient for vLLM to resolve the
> repo-id to the local path at `config_hash` time. If vLLM requires weights present to
> resolve, this approach is reconsidered (we do NOT fall back to full-weight pre-download).

## Design: best-effort metadata pre-fetch init container

Capture-side only (cachedir mode, no `restore-from`), gated on detecting a vLLM `--model
<repo-id>`:

```
initContainers:
- name: nvsnap-prestage-meta
  image: <same image as main container>             # has huggingface_hub already
  env: <main HF_* + HOME + HF_HOME>                  # same creds/proxy/cache path
  command: ["sh","-c","python -c \"from huggingface_hub import snapshot_download;
      snapshot_download('<repo-id>', allow_patterns=['*.json','*.txt','*.model','tokenizer*'])\"
      || true"]                                       # ALWAYS exit 0 (constraint #1)
  volumeMounts: [{name: nvsnap-cachedir, mountPath: /opt/nvsnap}]
```

- **Constraint #1 satisfied**: `|| true` + exception-tolerant — a gated model, missing token,
  or proxy hiccup leaves the cache empty and the main container proceeds as today (correct,
  just no compile reuse that run). Never fails the pod.
- **Constraint #2 satisfied**: metadata-only `allow_patterns` (no `*.safetensors`/`*.bin`)
  → KB-scale, byte-neutral, no weight pre-download, no double-download (vLLM streams weights
  from the Hub as today).

### Model-id extraction
Parse the main container's `command`+`args` for `--model <X>`. If absent → **skip** (no init
injected, today's behavior). Optional `nvsnap.io/model` annotation override.

### Scope
Inject ONLY when a vLLM `--model <repo-id>` is detected. SGLang/NIM pods get nothing (they
don't have this config_hash issue) — no init container added.

## Validation
Re-capture gpt-oss on the patched agent; on restore confirm vLLM logs
`Directly load … from cache` (not `Compiling a graph … / saved AOT`) and `torch.compile`
drops from ~20s to ~1s. Target: gpt-oss restore 234s → ~214s.

## Revised approach & recommendation (after constraints #1–#3)

Walking every option against "never fail / never slower / workload-agnostic":

- **Parse `--model` → metadata pre-fetch** — violates #3 (command parsing, vLLM assumption).
- **Full-weight pre-download** — violates #2 (extra bytes, bigger snapshot, slower capture).
- **Make restore keep the repo-id** (don't resolve) — impossible without abandoning the
  RO-mount/no-download win (vLLM resolves precisely *because* the model is present at restore;
  removing that means re-download → strictly worse).
- **Reproduce vLLM's `config_hash` to alias the cache key at seed time** — needs to
  reimplement vLLM-internal hashing; version-coupled and brittle → violates #3 in spirit.

There is **no fix that is simultaneously general, zero-risk, and never-slower**, because the
root cause is intrinsic: the no-download win *comes from* the model being present at restore,
and that same presence is what flips vLLM's resolved-model string (and thus `config_hash`)
relative to the cold capture. The benefit and the cache-miss share one cause.

**Recommendation: do not special-case this in nvsnap.** gpt-oss restore is already 234s
(~57% vs cold); the ~20s is a vLLM-internal `config_hash` quirk, not a nvsnap defect, and the
underlying inductor FX-graph cache (content-keyed, in `TORCHINDUCTOR_CACHE_DIR`) IS reused —
which is why the recompile is ~20s, not the multi-minute cold compile. Any nvsnap-side hack
risks the known-good number for a marginal gain on one engine.

If a specific customer needs that 20s back, the **operator-side** lever (their command, their
choice, no nvsnap change) is to pin `--model` to a stable string that resolves identically at
capture and restore — i.e. point it at the local snapshot path, or pre-bake the model so the
cold pod already resolves to a path. That keeps nvsnap workload-agnostic.

**Status: shelved.** Kept as analysis; not implementing. The shipped wins stand: mtime-preserve
(SGLang, real fix) + HF_HUB_OFFLINE (warning cleanup).

## RESOLVED (2026-06-19) — not by pre-stage, by removing HF_HUB_OFFLINE

Root cause was narrower than this whole proposal assumed. vLLM (`engine/arg_utils.py`)
rewrites `--model` repo-id → resolved snapshot path **only when `HF_HUB_OFFLINE` is set**
("when use hf offline, replace model ... to local model path"). We had added
`HF_HUB_OFFLINE=1` to the restore env (v0.0.97) purely to silence benign HF `.no_exist`
warnings — and *that* is what flipped restore's model string to the path while capture
(cold, online) kept the repo-id → `config_hash` mismatch → compile miss.

Fix = remove `HF_HUB_OFFLINE` from the restore env (v0.0.99). Capture and restore both keep
the repo-id → same `config_hash` → compile cache HIT. Validated: gpt-oss re-restore, 6
`Directly load … from cache`, 0 recompiles, `torch.compile` 19.8s→4.58s, 234s→171s.

So the pre-stage/metadata-seed designs above are **moot** — no init container, no command
parsing, no model pre-download needed. The `.no_exist` warnings return (harmless, accepted).
Pre-stage is only relevant if a future deployment genuinely needs `HF_HUB_OFFLINE` (air-gap);
then making it a per-deployment toggle + pre-stage would be revisited.
