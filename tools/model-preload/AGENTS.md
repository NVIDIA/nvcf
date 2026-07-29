# AGENTS.md - tools/model-preload

Prototype tool that preloads an NGC model into a shared ReadOnlyMany volume so
worker pods mount one read-only copy instead of each downloading it. See
`README.md` for the flow and design gaps.

## Layout

- `model-preload-rox.py` - the orchestrator (kubectl + NGC REST + python stdlib).

## Run

    export NGC_API_KEY=<nvapi-...>
    python3 model-preload-rox.py --model <org>/[<team>/]<model>:<version> \
      --namespace <ns> --name <short> --ngc-secret <secret> --image <img>

## Conventions

- Standard library only. Do not add third-party python deps for this prototype.
- kubectl is the k8s client. Keep the tool CSI-agnostic via `--csi-driver`;
  default `nvmesh-csi.excelero.com` (nvcf-sc).
- Every source file needs the SPDX Apache-2.0 header (check-license CI).
- This is public OSS. No internal hostnames, cluster names, or private IDs.
