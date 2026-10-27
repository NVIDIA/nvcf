# AGENTS.md - worker-callback-proxy chart

This chart deploys upstream Envoy to route NVCF worker callbacks to the
grpc-proxy or invocation-service pod that holds the request. It has no
service source in `src/`. `appVersion` is the Envoy version and is bumped by
hand.

## Validate

Run from this directory:

```bash
make test             # helm lint and tests/chart-render/verify-render.sh
make validate         # kubeconform with CRD schemas; needs kubeconform
make validate-envoy   # tests/chart-render/validate-envoy.sh; needs Docker
```

`workerCallbackProxy.workerDomain` and the frontend issuer have no default,
so the targets use `tools/ci/helm-validate-values/worker-callback-proxy.yaml`.

`verify-render.sh` checks how resources fit together: Service ports and
selectors against the pod, Envoy listeners against container ports, and SDS
paths against mounts. Extend it when adding a port, listener, or mount.
`validate-envoy.sh` runs the rendered config in the `appVersion` Envoy image
and rotates the grpc-proxy CA to prove it reloads without a restart. It also
runs a worker curl through the TLS invocation listener to a TLS upstream.

## Style

Follow the repository root `AGENTS.md` for repository-wide code style and
public snapshot rules.

- Use two-space indentation in YAML.
- Keep the Envoy configuration in `templates/_envoy.tpl`. Other templates
  only include it.
- Validate required values with `required` and quote rendered strings.
  Put checks that span several values in `workerCallbackProxy.validate`, and
  keep `values.schema.json` in step with `values.yaml`.
- Keep the container non-root with a read-only root filesystem. Envoy writes
  nothing to disk.

## Safety

- The grpc-proxy route accepts any `Host` under `*.<workerDomain>`. It is safe
  only because upstream TLS requires the grpc-proxy pod certificate. Do not
  relax `match_typed_subject_alt_names` or the `grpc_proxy_ca` SDS secret.
- Keep the admin API on `127.0.0.1`. It can stop Envoy and dump its config;
  only `/ready` and `/stats/prometheus` are exposed, on their own listeners.
- The invocation route must keep the authority regex. Without it the listener
  is an open forward proxy. Keep `invocation.tls.enabled` and the 8443
  `invocation.port` as defaults; plaintext is for test setups only.
- Never commit certificates or private keys. `bin/` is ignored for local
  renders.
