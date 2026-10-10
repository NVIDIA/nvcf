# NVCF Gateway UI

The demo UI for Inference Endpoints in a Box: a React single-page app plus the Go backend-for-frontend (BFF) that serves it. It has three pages:

- **Endpoint registry**: the models registered with the LLM API Gateway, their health, and how to call them.
- **Playground**: streaming chat with one model, opened from the registry in its own tab.
- **Model deployment recipes**: the Helm charts that deploy models, filtered by hardware, each with its `helm install` command.

The browser only ever talks to the BFF. The BFF does three things:

- It forwards four gateway routes, adding its own gateway API key, which never reaches the browser.
- It serves the recipe catalog and the UI's runtime config.
- It serves the UI build.

## Prerequisites

| Tool | Needed for |
| --- | --- |
| Go, at the toolchain version in `backend/go.mod` | The BFF |
| Node.js 22 with corepack | The UI. Corepack runs the pnpm version pinned in `ui/package.json`. |
| Docker with buildx | `image-build`, `image-tar` |
| Helm | `helm-lint`, deploying |
| golangci-lint | `backend-lint`, `lint` |
| `gh`, `jq`, `kubectl` | `recipes-configmap` |

`make setup` installs everything else: addlicense, kubeconform, govulncheck, the UI's dependencies and Playwright's Chromium.

## Quick start

```bash
make setup
make ui-dev MOCK=true    # mock data, no cluster needed: http://localhost:5173
```

To work against a real gateway, run the BFF and point the dev server at it. The dev server proxies `/api` and `/v1` to `BFF_URL`, by default `http://localhost:8300`.

```bash
GATEWAY_URL=https://<gateway-host>:8080 \
GATEWAY_CA_PATH=path/to/ca.crt \
GATEWAY_API_KEY_PATH=path/to/api-key \
  make backend-run
make ui-dev
```

## Make targets

`make help` lists them all. Run every target from this directory, where the `Makefile` is.

### Develop

| Target | What it does |
| --- | --- |
| `make ui-dev` | Vite dev server with hot reload. Without `MOCK`, it proxies `/api` and `/v1` to the BFF at `BFF_URL`. Variables (all optional):<br>- `MOCK=true` serves the UI's mock data (MSW) instead of calling the BFF.<br>- `SCENARIO=feature:name` layers mock scenarios on top, e.g. `registry:empty` or `recipes:unavailable`; separate several with commas.<br>- `DEVTOOLS=true` shows the TanStack devtools. |
| `make backend-run` | Runs the BFF from source, configured through environment variables (see [Configuration](#configuration)). |
| `make generate` | Regenerates the UI's typed API client, React Query hooks and MSW mocks from the OpenAPI specs in `spec/`, with Orval. Run it after editing a spec; the output in `ui/src/generated/` is committed. |
| `make ui-vendor-css` | Refreshes the vendored Kaizen UI CSS and fonts in `ui/vendor/`, so the UI loads nothing from the internet. Run it after bumping `@nvidia/foundations-react-core`. |

### Check quality

| Target | What it does |
| --- | --- |
| `make lint` | Biome on the UI, golangci-lint on the BFF, `helm-lint`, and the license check. |
| `make lint-fix` | Fixes what Biome and golangci-lint can, and adds missing license headers. |
| `make ui-lint`, `make backend-lint` | Lint one side only. |
| `make typecheck` | TypeScript type check of the UI. |
| `make spec-lint` | Lints the OpenAPI specs with Redocly. |
| `make helm-lint` | Runs `helm lint`, then renders the chart and validates the manifests against the Kubernetes schemas with kubeconform. |
| `make license-check` | Fails if a source file lacks the Apache 2.0 header. |
| `make audit` | Checks dependencies for known vulnerabilities: `pnpm audit` for the UI, `govulncheck` for the BFF. |

### Test

| Target | What it does |
| --- | --- |
| `make ui-test` | Vitest unit and integration tests, run once, with an 80% line and branch coverage gate. |
| `make ui-test-watch` | Vitest in watch mode. |
| `make backend-test` | Go tests with the race detector and coverage. These include contract tests, which check the BFF's responses against `spec/bff-openapi.yaml`. |
| `make e2e` | Playwright, headless, against the dev server in mock mode, on four device sizes: phone, tablet, laptop and desktop. |
| `make e2e-ui` | Playwright's UI mode, for writing and debugging tests. |
| `make e2e-update` | Playwright, refreshing the visual snapshot baselines. Run it only on purpose. |
| `make e2e-prod` | Builds the production bundle and runs it behind the real BFF and a fake gateway. It covers security headers, the Content-Security-Policy, compressed assets, key injection and chat streaming. |
| `make check` | Everything to run before calling a change done: `lint`, `audit`, `typecheck`, `ui-test`, `backend-test`, `e2e` and `e2e-prod`. |

### Build and package

| Target | What it does |
| --- | --- |
| `make ui-build` | Production UI build into `backend/static`, with brotli and gzip copies of text assets. |
| `make backend-build` | Compiles the BFF into `backend/bin/server`. |
| `make build` | Both of the above. Run the result with `STATIC_DIR=backend/static backend/bin/server`. |
| `make image-build` | Builds the container image `IMAGE:TAG` for `PLATFORM`, by default `linux/arm64`; it cross-compiles, so no emulation is needed. The image is distroless and runs as a non-root user. `TAG` defaults to a unique `dev-<time>-<commit>`, with `-dirty` for uncommitted changes. |
| `make image-tar` | Runs `image-build`, then saves the image to `backend/bin/image-<tag>.tar`, for clusters that pull from no registry. |

### Deploy helpers

| Target | What it does |
| --- | --- |
| `make recipes-configmap` | Prints the recipe catalog ConfigMap as YAML, for `kubectl apply`. It runs the catalog (below) through `helm/recipes/catalog.jq`, which adds what the UI needs. Variables:<br>- `RECIPES_REF`: the NVIDIA/nvcf branch or commit to fetch `deploy/helm/llm-routing/recipes/index.json` from (needs `gh`).<br>- `RECIPES_INDEX`: a local `index.json` to use instead.<br>- `RECIPES_REPOSITORY`: the OCI repository for charts the catalog names none for; default `oci://nvcr.io/org`.<br>- `RECIPES_CONFIGMAP`, `RECIPES_NAMESPACE`: the ConfigMap's name and namespace; default `demo-ui-recipes` in `demo-ui`. |

```bash
make -s recipes-configmap | kubectl apply --server-side -f -
```

The BFF reads the catalog file again whenever it changes. Re-running this command therefore updates the recipes without restarting anything, once the kubelet syncs the mounted ConfigMap (about a minute).

## Build with Bazel

In the NVCF monorepo, Bazel builds and tests the component, and CI runs it. The Make targets stay for local iteration. Run these from the monorepo root:

```bash
bazel build //src/uis/nvcf-spark-ui/...               # BFF, UI bundle and image layers
bazel test //src/uis/nvcf-spark-ui/...                # Go tests, Biome, TypeScript and Vitest
bazel build //src/uis/nvcf-spark-ui:image_index       # multi-arch image: linux/amd64 and linux/arm64
bazel run //src/uis/nvcf-spark-ui:image_load          # the host-arch image, into the local Docker daemon
bazel run //:gazelle -- src/uis/nvcf-spark-ui/backend  # refresh the BFF's BUILD files after Go changes
```

## Configuration

The BFF reads its configuration from the environment. The Helm chart sets these variables from its values.

| Variable | Default | Purpose |
| --- | --- | --- |
| `GATEWAY_URL` | `https://llm-api-gateway:8080` | The gateway's in-cluster Service |
| `GATEWAY_CA_PATH` | System roots | PEM bundle the BFF trusts for the gateway |
| `GATEWAY_API_KEY_PATH` | `/var/run/secrets/demo-ui/api-key` | File holding the BFF's gateway API key, read once at startup |
| `GATEWAY_TIMEOUT_SECONDS`, `CHAT_TIMEOUT_SECONDS` | `15`, `600` | Limit on gateway reads, and write deadline of one chat stream |
| `GATEWAY_PUBLIC_URL` | None | The gateway's address for clients, shown in the `curl` snippets |
| `GRAFANA_URL` | None | Dashboard each endpoint links to: absolute, or a path behind the same ingress |
| `RECIPE_CATALOG_PATH` | None | The recipe catalog file. The chart mounts it from a ConfigMap. |
| `SERVER_PORT`, `STATIC_DIR` | `8300`, `static` | Listener port and UI build directory |

## Deploying with Helm

The chart is in `helm/`; its values sit under `nvcfSparkUi` in `helm/values.yaml`. Before installing, make the image available to your cluster. The release's namespace must hold:

- a Secret with the BFF's gateway API key (`gateway.apiKey.secret` and `.key`; default `demo-ui-api-key`, field `api-key`);
- the gateway's CA certificate in a ConfigMap (`gateway.ca.configMap` and `.key`; default `llm-gateway-stack-ca`, key `ca.crt`);
- optionally, the recipe catalog ConfigMap (`recipes.configMap`; default `<fullname>-recipes`, from `make recipes-configmap`). The pod starts without it, and the recipes page then says the catalog is unavailable.

```bash
make helm-lint
helm upgrade --install demo-ui helm -n <namespace> \
  --set nvcfSparkUi.image.repository=<image> \
  --set nvcfSparkUi.image.tag=<tag> \
  --set nvcfSparkUi.gateway.url=https://llm-api-gateway.<stack-namespace>.svc.cluster.local:8080
```

Set `ingress.enabled` and `ingress.className` to expose the UI through an ingress controller. `ui.gatewayPublicUrl` and `ui.grafanaUrl` add the gateway's public address and the dashboard links.
