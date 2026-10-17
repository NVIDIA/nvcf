# AGENTS.md - worker-callback-proxy chart

This chart deploys upstream Envoy to route NVCF worker callbacks to the
grpc-proxy or invocation-service pod that holds the request. It has no
service source in `src/`. `appVersion` is the Envoy version and is bumped by
hand.

## Validate

Run from this directory:

```bash
helm lint worker-callback-proxy -f ../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
helm template worker-callback-proxy worker-callback-proxy -f ../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
bash tests/chart-render/verify-render.sh
```

`workerCallbackProxy.workerDomain` has no default, so a values override is
used for validation.

To check the rendered Envoy configuration, extract `envoy.yaml` from the
ConfigMap and run it through `envoy --mode validate` with the image matching
`appVersion`.

## Style

Follow the repository root `AGENTS.md` for repository-wide code style and
public snapshot rules.

- Use two-space indentation in YAML.
- Keep the Envoy configuration in `templates/_envoy.tpl`. Other templates
  only include it.
- Validate required values with `required` and quote rendered strings.
- Keep the container non-root with a read-only root filesystem. Envoy writes
  nothing to disk.

## Safety

- The grpc-proxy route accepts any `Host` under `*.<workerDomain>`. It is safe
  only because upstream TLS requires the grpc-proxy pod certificate. Do not
  relax `match_typed_subject_alt_names` or `trusted_ca`.
- The invocation route must keep the authority regex. Without it the listener
  is an open forward proxy.
- Never commit certificates or private keys. `bin/` is ignored for local
  renders.
