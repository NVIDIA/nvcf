<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# AGENTS.md

## Repo Role

- Repo: `llm-api-gateway`
- Workspace(s): `self-hosted-nvcf`
- Tier: `image-source`
- Team: `@NVIDIA/nvcf-dev`
- Default owner: `@NVIDIA/nvcf-dev`
- Manifest description: LLM API gateway application source

## Use `nvcf-agentic-dev` As The Routing Layer

Before making changes, use the `nvcf-agentic-dev` workspace repo to confirm whether this repo is actually the right place for the task. Treat that repo as the source of truth for workspace membership, repo ownership, deployment dependencies, and available agent skills.

Check these files first when they exist in your local workspace:

- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/repos.yaml`: repo ownership and workspace membership
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/skills.yaml`: related agent skills and sourced commands
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-sequence.md`: deployment order and stage gates
- `nvcf-agentic-dev/workspaces/self-hosted-nvcf/docs/deployment-dependencies-with-links.yaml`: release dependencies and upstream/downstream links

## Routing Rules

- Stay in this repo for HTTP API handlers, OpenAI-compatible request and response models, prompt templating, tokenization, telemetry, rate limiting, runtime config, Docker image logic, local Kustomize assets, and tests for the shipped binaries.
- Helm packaging lives in [`llm-api-gateway-colocated-deploy`](/Users/jcameron/Code/llm-api-gateway-colocated-deploy). Route Helm values, templates, hook jobs, and chart packaging work there unless the deployment ownership model changes.
- Self-hosted orchestration lives in [`nvcf-self-managed-stack`](/Users/jcameron/Code/nvcf-self-managed-stack). Route environment composition, multi-service rollout ordering, and stack-level release sequencing there.
- Keep deploy-repo chart values and this repo's runtime env/config names aligned.

## Working Rules

- Read this repo's top-level `README*`, `go.mod`, `Dockerfile`, and workflow/task files before making assumptions about language or tooling.
- **Go module:** `github.com/NVIDIA/nvcf/llm-api-gateway`; **entrypoints:** `cmd/llm-api-gateway/main.go` and `cmd/llm-api-gateway-rate-limit-sync-worker/main.go`.
- Search for existing patterns with `rg` before adding new structure.
- `github.com/nvidia-lpu/*` uses full module trees under `nvidia-lpu-vendor/` via `replace` in `go.mod`. `bin/sync-nvidia-lpu-vendor.sh` reads `go.mod`, downloads each module into the cache, and copies the tree into `nvidia-lpu-vendor/`. `mise run bootstrap` runs `bootstrap:nvidia-lpu-vendor` first (so replace targets exist), then `bootstrap:go-deps`, then `bootstrap:tokenizers`. Commit `nvidia-lpu-vendor/` when LPU module versions change.
- `github.com/olric-data/olric` is redirected via `replace` in `go.mod` to a fork at [`github.com/max007-008/olric`](https://github.com/max007-008/olric), branch `cas`. The fork is `olric-data/olric@v0.7.3` plus a `CompareAndSwap` primitive that the rate limiter depends on (see `internal/dmap/atomic.go` and `atomic_handlers.go` in the fork). The fork keeps upstream's module path (`github.com/olric-data/olric`) so the `replace` is a drop-in: no import sites in this repo need to change. To pick up new commits on the fork, run `go get github.com/max007-008/olric@cas && go mod tidy` (this re-resolves the pseudo-version in `go.mod`). Because the replace targets a remote repo rather than a local tree, teammates don't need any local clone. If the CAS work is ever upstreamed into `olric-data/olric`, drop the `replace` line and bump the `require` version. This arrangement is explicitly marked `TEMPORARY` in `go.mod`.
- Do not hand-edit generated `*.pb.go`, `*_grpc.pb.go`, or generated mocks; change `.proto` files or source interfaces and regenerate with the owning generator command. New hand-authored files should use the same SPDX Apache-2.0 header style as the rest of the tree.
- Never commit real secrets; use placeholders in examples and templates.

## Completion Expectations

- Validate with the repo-native command set: `go build ./...`, `go test ./...`, and `gofmt` / `go fmt ./...` after substantive Go edits; `kustomize build kustomize/overlays/local` when changing local manifests; `docker build` when changing the image.
- If you change cross-repo behavior, mention the adjacent repo(s) that may also need follow-up.
- In your final summary, state that routing was confirmed through `nvcf-agentic-dev` and name the workspace context used (`self-hosted-nvcf`) when that routing check was available.

## References

- [CONTRIBUTING.md](CONTRIBUTING.md) — DCO (`git commit -s`)
- [README.md](README.md) — behavior, configuration, and local development
- [LICENSE](LICENSE), [NOTICE](NOTICE) — Apache-2.0 and third-party attribution

## Current Naming

- GitLab repo: `nvcf/llm-api-gateway`
- Go module: `github.com/NVIDIA/nvcf/llm-api-gateway`
- Runtime service name: `llm-api-gateway`
