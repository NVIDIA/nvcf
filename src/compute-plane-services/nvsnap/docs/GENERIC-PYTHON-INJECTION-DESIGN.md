# Generic Python Library Injection — Design

**Status:** Proposed, 2026-05-11
**Branch:** `feat/rootfs-only-restore`
**Owner:** balaji
**Replaces:** per-manifest bash injection in `vllm-*.yaml`, `sglang-*.yaml`, `trtllm-*.yaml`
**Unblocks:** NIM (`nim-llama-8b.yaml`), future Python 3.13 workloads, BYOC diffusion / generic inference

## Problem

Every workload manifest today carries its own bash block that mutates the container's filesystem before exec'ing the real entrypoint:

```yaml
command: ["/bin/bash", "-lc"]
args:
  - |
    SITE=$(python3 -c "import site; print(site.getsitepackages()[0])")
    cp -a /nvsnap-lib/site-packages/* "$SITE/" 2>/dev/null || true
    cp /nvsnap-lib/libzmq.so* /usr/local/lib/
    ldconfig
    vllm serve ...
```

Three failure modes:

1. **Per-workload coupling** — NIM Llama-8B was never given that block, so its uvloop never got patched and it aborts in `uv__io_poll` after restore. Same risk for any new workload we onboard.
2. **Single Python version** — `/nvsnap-lib/site-packages/` holds a cp312 wheel. The day a workload ships Python 3.13, we crash on import. We cannot keep up with Python release cadence by hand.
3. **BYOC fundamentally broken** — Production diffusion / RAG / custom inference containers ship with their own entrypoint we don't control. We cannot replace `command:` for every customer pod.

We need a mechanism where injection is invisible to the workload's entrypoint, automatic across Python versions, and addable to any pod by a thin mutating webhook.

## Proposal — `PYTHONPATH` + `sitecustomize.py` with runtime version routing

Standard Python machinery (CPython's `site.py`) imports `sitecustomize` exactly once during interpreter startup, before any user code. We ship our own `sitecustomize.py` on `PYTHONPATH`. It detects the running interpreter's ABI tag and prepends the matching package directory to `sys.path`.

### Layout in `/nvsnap-lib` after init containers complete

```
/nvsnap-lib/
├── sitecustomize/
│   └── sitecustomize.py             # ~15 lines, ships in nvsnap-agent image
├── site-packages-cp310/uvloop/      # cp310 ABI
├── site-packages-cp311/uvloop/
├── site-packages-cp312/uvloop/
├── site-packages-cp313/uvloop/
├── libzmq.so.5                      # patched C lib, single copy
├── libuv.so.1                       # patched C lib, single copy
└── libnvsnap_intercept.so             # LD_PRELOAD'd
```

**Only uvloop is Python-version-keyed.** Native libraries (`libzmq.so`,
`libuv.so`) are C-ABI singletons — `libnvsnap_intercept.so`'s constructor
dlopens `libzmq.so.5` early (resolved via `LD_LIBRARY_PATH=/nvsnap-lib:...`),
which loads our patched copy into the process. Any later dlopen of
`libzmq.so.5` by `pyzmq` (or its bundled vendored copy) returns the
already-resident instance — the dynamic linker refuses to load a second
library with the same SONAME. So `pyzmq` does not need to be rebuilt
against our libzmq; the patch is delivered at the C-lib layer transparently.
This was decided and validated in commit ff453b7 (Apr 2026); honored here.

### `sitecustomize.py`

```python
import sys, os, importlib.util

_pyver = f"cp{sys.version_info.major}{sys.version_info.minor}"
_pkg_dir = f"/nvsnap-lib/site-packages-{_pyver}"
if os.path.isdir(_pkg_dir):
    sys.path.insert(0, _pkg_dir)

# Chain-load a user-provided sitecustomize.py if one exists further
# down sys.path, so we don't clobber customer behavior.
for _p in sys.path[1:]:
    _candidate = os.path.join(_p, "sitecustomize.py")
    if _p == _pkg_dir or not os.path.isfile(_candidate):
        continue
    try:
        _spec = importlib.util.spec_from_file_location("_nvsnap_user_sitecustomize", _candidate)
        _mod = importlib.util.module_from_spec(_spec)
        _spec.loader.exec_module(_mod)
    except Exception:
        pass
    break
```

### Pod env (uniform across all workloads, no `command:` override)

```yaml
env:
  - name: PYTHONPATH
    value: "/nvsnap-lib/sitecustomize"
  - name: LD_LIBRARY_PATH
    value: "/nvsnap-lib:/usr/local/nvidia/lib64:/usr/local/cuda/lib64"
  - name: LD_PRELOAD
    value: "/nvsnap-lib/libnvsnap_intercept.so"
```

If a workload already sets `PYTHONPATH`, the webhook (or manifest) prepends ours: `"/nvsnap-lib/sitecustomize:${PYTHONPATH}"`.

### How this generalizes

| Workload class | What happens |
|---|---|
| vLLM / SGLang / TRTLLM / NIM | sitecustomize prepends our cp312 dir → `import uvloop` resolves to our patched copy |
| Diffusers / ComfyUI / FastAPI | sitecustomize loads, uvloop import (if any) hits ours; if not imported, no-op |
| Triton C++ server / Go / Rust | sitecustomize never loads (no Python). LD_PRELOAD'd `libnvsnap_intercept.so` still drives io_uring quiesce; LD_LIBRARY_PATH still routes libzmq if linked dynamically |
| Customer BYOC pod | Webhook adds env vars + init containers; never touches `command:` |
| Python 3.10–3.14 | Runtime detection picks the right wheel — same manifest, no rebuild |
| Venv-based image (NIM) | Venv's `python3` is on PATH, its `sys.version_info` drives selection — works inside any layout |

## Mutating admission webhook (follow-up branch)

With sitecustomize doing all runtime wiring, the webhook stays trivial:

```
on Pod CREATE with annotation nvsnap.io/quiesce-enabled=true:
    add volume:       nvsnap-lib (emptyDir)
    add initContainers: get-uvloop, get-libuv, get-libzmq, get-criu
    for each container:
        add volumeMount: nvsnap-lib at /nvsnap-lib
        prepend env: PYTHONPATH, LD_LIBRARY_PATH, LD_PRELOAD
        # never touch command, args, image, securityContext
```

That's ~80 lines of Go. Today's path would require parsing and rewriting customer entrypoints, which we will not do in production.

## Builder changes

### Multi-Python wheel matrix

Switch base from `vllm/vllm-openai:v0.11.2` (cp312 only) to `quay.io/pypa/manylinux_2_28_x86_64`, which ships cp310 / cp311 / cp312 / cp313 in `/opt/python/cp3XX-cp3XX/bin/`. GLIBC 2.28 forward-compatible with the GLIBC 2.35 hosts we run on.

uvloop builder pseudo-Dockerfile:

```dockerfile
FROM quay.io/pypa/manylinux_2_28_x86_64 AS builder
COPY uvloop-src/ /uvloop-src/
WORKDIR /uvloop-src
RUN for py in /opt/python/cp310-cp310 /opt/python/cp311-cp311 \
              /opt/python/cp312-cp312 /opt/python/cp313-cp313; do \
        rm -rf build/ dist/ *.egg-info .eggs/ uvloop/loop.c && \
        "$py/bin/pip" install "setuptools>=60" wheel "Cython>=3.1,<3.2" && \
        "$py/bin/pip" wheel . -w /wheels/ --no-build-isolation; \
    done
RUN ls -la /wheels/  # expect 4 wheels: uvloop-*-cp310-* … cp313-*

FROM python:3.12-slim
COPY --from=builder /wheels/ /wheels/
```

**No pyzmq builder needed.** See "Layout in `/nvsnap-lib`" above for the
rationale (dlopen-once SONAME semantics let our patched libzmq override
pyzmq's bundled vendor copy without any Python-version-specific work).
The standalone `libzmq.so.5` from `docker/libzmq/Dockerfile` remains the
single source of truth for the patched C library; it stays single-version.

### Init container extraction (single shell line per wheel)

```sh
for whl in /wheels/uvloop-*.whl; do
    pytag=$(echo "$whl" | grep -oE 'cp3[0-9]+' | head -1)
    mkdir -p "/nvsnap-lib/site-packages-${pytag}"
    python3 -m zipfile -e "$whl" "/nvsnap-lib/site-packages-${pytag}/"
done
```

### `sitecustomize.py` delivery

Lives at `lib/sitecustomize/sitecustomize.py` in this repo. Copied into `nvsnap-agent` image's `/criu-bundle/sitecustomize/`. The existing `get-criu` init container's command appends:

```sh
cp -r /criu-bundle/sitecustomize /nvsnap-lib/
```

## File changes — sequenced PRs

CLAUDE.md rule #5 caps each PR at three files where practical. Builder Dockerfiles and their build scripts count as one logical unit each.

| PR | Files | Validation |
|---|---|---|
| A. multi-Python uvloop builder | `docker/uvloop/Dockerfile`, `scripts/build-uvloop-wheel.sh`, `scripts/versions.sh` | ✅ Done — `uvloop-builder:v0.22.1-multipy1` |
| ~~B'. multi-Python pyzmq builder~~ | — | **DROPPED.** pyzmq rebuild not needed — see SONAME-singleton note. Re-confirmed Apr 2026 (commit ff453b7) |
| B. `sitecustomize.py` + agent bundling | `lib/sitecustomize/sitecustomize.py` (new), `docker/agent/Dockerfile.app` | ✅ Done — `nvsnap-agent:v0.20.0-sitecustomize`+ |
| C. convert `nim-llama-8b.yaml` | `deploy/k8s/workloads/nim-llama-8b{,-restore}.yaml` | ✅ Done — e2e PASS 5m39s (2026-05-12), commit `2f47577` |
| D. convert `vllm-small.yaml` (regression check) | `deploy/k8s/workloads/vllm-small{,-restore}.yaml` | ✅ Done — sitecustomize pattern landed in workloads/. Source + restore must stay in lockstep — see "Source-restore lockstep" caveat below |
| E. roll across remaining 4 CRIU workloads | `deploy/k8s/workloads/{vllm-8b,sglang-small,sglang-8b,trtllm-small}{,-restore}.yaml` | 🟡 **Deferred.** Workloads pass e2e today on the legacy inline-cp pattern; mechanical migration to sitecustomize will land + be validated when the GKE test cluster is free of QA traffic. Pattern is well-understood (template = vllm-small). |
| F. mutating admission webhook | separate branch | unit tests + e2e on a synthetic BYOC pod |

### Source-restore lockstep caveat

CRIU's restore verifies the size of every mmap'd file on disk against
the checkpoint. If the source pod mmap'd `/nvsnap-lib/site-packages-cp312/uvloop/loop.so`
(sitecustomize layout) but the restore container's init only stages
`/nvsnap-lib/site-packages/uvloop/loop.so` (legacy single-Python layout),
restore aborts with `restore requires file contents but not found in
checkpoint: ...`. When converting a workload, BOTH `<id>.yaml` and
`<id>-restore.yaml` must move to the new pattern in the same commit.
PR-D's first attempt hit this exact failure mode (commit history on
this branch); the fix was making `test-e2e.sh`'s sed substitution
style-agnostic + converting the restore manifest. See the same-commit
note at the head of each migrated `*-restore.yaml`.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Workload entrypoint resets `PYTHONPATH` | Use `PYTHONPATH=/nvsnap-lib/sitecustomize:${PYTHONPATH}` in env; in webhook, splice rather than replace |
| Workload ships its own `sitecustomize.py` | Chain-import their copy from ours (see code above) |
| Patched uvloop has cp312-specific cython source bug after we add cp310/311/313 | Smoke test in builder image must pass for all four ABIs; CI gates on it |
| Wheels picked up by `pip install` somewhere we don't expect | Wheels live at `/nvsnap-lib/site-packages-cpXY/` (extracted, not as `.whl`); pip won't pick them up |
| Restored process loaded stock uvloop at checkpoint time | Patched uvloop must be on `sys.path` before checkpoint — sitecustomize ensures this at cold-start; restore-side injection is no longer needed for Python libs |
| NIM Qwen3-32B passed without injection on `feat/nim-checkpoint` | Verify Qwen3 still passes with injection enabled — it should, but its TRT-LLM backend may exercise different paths |

## Forward / parked items

- **arm64.** Builder matrix is x86_64 only. ARM64 Spot GPU instances (Grace Hopper, etc.) will need:
  - `quay.io/pypa/manylinux_2_28_aarch64` builder base
  - Native ARM build hosts in CI (cross-compile cython is painful)
  - Likely paired with rootfs-only restore across all GPU types (CRIU + arm64 + GPU is largely untested terrain)
  Tracked separately. Not blocking x86_64 work.
- **PyPy / GraalPy.** Out of scope. CPython only.
- **Conda environments.** Should work — Conda's Python still runs site.py at startup and respects PYTHONPATH. Verify with a Conda-based diffusion container before claiming.
- **Static-linked libzmq inside vendored extensions.** Our pyzmq wheel always links system libzmq, so this only affects third-party packages with their own bundled libzmq. Rare in practice.

## Open questions to settle in PR-A

1. **Builder image size.** manylinux_2_28 + 4 Python interpreters is ~2 GB. Use multi-stage to keep the final pushed image small (`FROM scratch` with just `/wheels/`).
2. **uvloop fork cython compatibility with cp313.** Need to confirm our patch compiles against Python 3.13 headers. If not, cherry-pick upstream uvloop changes for 3.13 support before merging.
3. **pyzmq + libsodium on manylinux.** manylinux_2_28 doesn't ship libsodium-dev. Either install from EPEL or build libsodium statically. Validate during PR-B.

## Definition of done

- `./scripts/test-e2e.sh nim-llama-8b` passes on `feat/rootfs-only-restore`.
- `./scripts/test-e2e.sh vllm-small` still passes (no regression).
- A pod manifest with only the three env vars + init containers — no `command:` override — successfully cold-starts, checkpoints, and restores.
- Multi-Python uvloop/pyzmq wheels live in builder image; smoke test imports each under the matching cpython.
- `docs/GENERIC-PYTHON-INJECTION-DESIGN.md` (this doc) merged.
