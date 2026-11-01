# Kustomize

This directory contains a minimal Kubernetes deployment for `llm-api-gateway`.

## Layout

```text
kustomize/
├── bases/
│   ├── rate-limit-sync-worker/
│   └── server/
└── overlays/
    └── local/
```

## Local Overlay

The `local` overlay deploys:

- the gateway Deployment, Service, and ServiceAccount
- an env-backed ConfigMap for the gateway's runtime configuration

Rate-limit state lives inside the gateway process via an embedded Olric node,
so the overlay no longer bundles a separate Redis workload. Enable it with the
`OLRIC_ENABLED=true` and `OLRIC_*` bind-address env vars in `gateway.env`.

The rate-limit sync worker has its own optional base at
`bases/rate-limit-sync-worker`. Add that base to an overlay when
`RATE_LIMIT_SYNC_TRANSPORT` is enabled and you want remote events applied by a
dedicated worker process instead of the HTTP server.

The overlay does not deploy Stargate. `gateway.env` points the gateway at
`http://stargate:8000`, so you need a Stargate Service in-cluster or an updated
`STARGATE_URL` value before requests will succeed.

Render the manifest:

```bash
kustomize build kustomize/overlays/local
```

Apply it:

```bash
kubectl apply -k kustomize/overlays/local
```
