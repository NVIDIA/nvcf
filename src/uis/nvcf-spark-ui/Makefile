# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Single entry point for every command: `make help` lists them, README.md explains them.
# Targets are thin wrappers: logic lives in scripts and tool config, not here.

SHELL := /bin/bash
BACKEND := backend
UI := ui
REDOCLY := npx --yes @redocly/cli@latest
# corepack runs the pnpm version pinned in ui/package.json (packageManager).
PNPM ?= COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm
ADDLICENSE ?= addlicense
ADDLICENSE_VERSION := v1.2.0
KUBECONFORM_VERSION := v0.8.0
GOVULNCHECK_VERSION := v1.8.0
# Every file addlicense can annotate, minus generated or third-party ones.
LICENSE_ARGS := -f license-header.txt \
	-ignore '$(UI)/node_modules/**' -ignore '$(UI)/vendor/**' \
	-ignore '$(UI)/public/mockServiceWorker.js' -ignore '$(UI)/pnpm-lock.yaml' \
	-ignore '$(UI)/dist/**' -ignore '$(UI)/coverage/**' \
	-ignore '$(UI)/test-results/**' -ignore '$(UI)/playwright-report/**' \
	-ignore '$(BACKEND)/static/**' -ignore '$(BACKEND)/bin/**' \
	Makefile Dockerfile BUILD.bazel $(BACKEND) spec helm $(UI)

# Container image. The default tag is unique per build, like the stack's own
# dev images, so a re-import onto the nodes never reuses a tag.
IMAGE ?= nvcf-spark-ui
PLATFORM ?= linux/arm64
GIT_DIRTY := $(if $(shell git status --porcelain 2>/dev/null),-dirty)
DEFAULT_TAG := dev-$(shell date -u +%Y%m%d%H%M%S)-$(shell git rev-parse --short HEAD 2>/dev/null || echo local)$(GIT_DIRTY)
TAG ?= $(DEFAULT_TAG)

# The recipe catalog ConfigMap (recipes-configmap): NVIDIA/nvcf's index.json at
# RECIPES_REF, or a local copy at RECIPES_INDEX, through helm/recipes/catalog.jq.
RECIPES_REF ?= feat/multi-model-spark-recipes
RECIPES_INDEX ?=
# The OCI repository given to charts the export has none for.
RECIPES_REPOSITORY ?= oci://nvcr.io/org
RECIPES_CONFIGMAP ?= demo-ui-recipes
RECIPES_NAMESPACE ?= demo-ui

.DEFAULT_GOAL := help

.PHONY: help
help: ## List all targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: setup
setup: ## Install Go tools, UI dependencies and Playwright browsers
	go install github.com/google/addlicense@$(ADDLICENSE_VERSION)
	go install github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	cd $(BACKEND) && go mod download
	cd $(UI) && $(PNPM) install
	cd $(UI) && $(PNPM) exec playwright install chromium

.PHONY: ui-dev
ui-dev: ## Vite dev server; MOCK=true serves mock data, SCENARIO=feature:name picks scenarios, DEVTOOLS=true shows the TanStack devtools
	cd $(UI) && VITE_MOCK=$(MOCK) VITE_SCENARIO=$(SCENARIO) VITE_DEVTOOLS=$(DEVTOOLS) $(PNPM) dev

.PHONY: generate
generate: ## Generate the UI's client and mocks from spec/ with Orval, then license headers
	cd $(UI) && $(PNPM) generate
	$(ADDLICENSE) -f license-header.txt $(UI)/src/generated

.PHONY: spec-lint
spec-lint: ## Lint the OpenAPI specs with Redocly
	$(REDOCLY) lint spec/*.yaml

.PHONY: typecheck
typecheck: ## Type check the UI
	cd $(UI) && $(PNPM) typecheck

.PHONY: ui-lint
ui-lint: ## Biome check of the UI
	cd $(UI) && $(PNPM) check

.PHONY: license-check
license-check: ## Check that every source file carries the license header
	$(ADDLICENSE) -check $(LICENSE_ARGS)

.PHONY: lint
lint: ui-lint backend-lint helm-lint license-check ## Biome, golangci-lint, the chart and the license check

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart and validate its manifests against the Kubernetes schemas
	helm lint helm
	helm template lint helm | kubeconform -strict -summary -
	@# Each cluster override file next to the chart (values-<cluster>.yaml), if any.
	@for values in $(wildcard helm/values-*.yaml); do \
		echo "helm lint and kubeconform with $$values"; \
		helm lint helm -f $$values && helm template lint helm -f $$values | kubeconform -strict -summary - || exit 1; \
	done

.PHONY: lint-fix
lint-fix: ## Fix lint issues and add missing license headers
	cd $(UI) && $(PNPM) check:fix
	cd $(BACKEND) && golangci-lint run --fix ./...
	$(ADDLICENSE) $(LICENSE_ARGS)

.PHONY: ui-test
ui-test: ## Vitest once, with the coverage gate
	cd $(UI) && $(PNPM) test --coverage

.PHONY: ui-test-watch
ui-test-watch: ## Vitest in watch mode
	cd $(UI) && $(PNPM) exec vitest

.PHONY: e2e
e2e: ## Playwright headless, every project
	cd $(UI) && $(PNPM) e2e

.PHONY: e2e-ui
e2e-ui: ## Playwright UI mode
	cd $(UI) && $(PNPM) e2e --ui

.PHONY: e2e-update
e2e-update: ## Playwright, refreshing visual snapshot baselines
	cd $(UI) && $(PNPM) e2e --update-snapshots

.PHONY: e2e-prod
e2e-prod: ## Playwright on the production build, served by the real BFF in front of a fake gateway
	cd $(UI) && $(PNPM) build --outDir dist
	cd $(UI) && $(PNPM) e2e -c playwright.prod.config.ts

.PHONY: audit
audit: ## Check UI and BFF dependencies for known vulnerabilities
	cd $(UI) && $(PNPM) audit
	cd $(BACKEND) && govulncheck ./...

.PHONY: check
check: lint audit typecheck ui-test backend-test e2e e2e-prod ## Everything required before calling a change done

.PHONY: ui-build
ui-build: ## Build the UI into backend/static
	cd $(UI) && $(PNPM) build --outDir ../$(BACKEND)/static

.PHONY: ui-vendor-css
ui-vendor-css: ## Refresh the vendored KUI CSS and fonts
	cd $(UI) && $(PNPM) vendor-css

.PHONY: build
build: ui-build backend-build ## UI build into backend/static, plus the Go binary

.PHONY: image-build
image-build: ## Build the container image IMAGE:TAG for PLATFORM (default linux/arm64, DGX Spark)
	docker buildx build --platform $(PLATFORM) -t $(IMAGE):$(TAG) --load .
	@echo "Built $(IMAGE):$(TAG)"

.PHONY: image-tar
image-tar: image-build ## Build the image and save it to backend/bin/, for importing into k3s nodes
	mkdir -p $(BACKEND)/bin
	docker save $(IMAGE):$(TAG) -o $(BACKEND)/bin/image-$(TAG).tar
	@echo "Saved $(BACKEND)/bin/image-$(TAG).tar, for a cluster that pulls from no registry"

.PHONY: recipes-configmap
recipes-configmap: ## Print the recipe catalog ConfigMap for kubectl apply (RECIPES_REF or RECIPES_INDEX; see README.md)
	@$(if $(RECIPES_INDEX),cat $(RECIPES_INDEX),gh api -H 'Accept: application/vnd.github.raw' 'repos/NVIDIA/nvcf/contents/deploy/helm/llm-routing/recipes/index.json?ref=$(RECIPES_REF)') \
		| jq --arg repository $(RECIPES_REPOSITORY) -f helm/recipes/catalog.jq \
		| kubectl create configmap $(RECIPES_CONFIGMAP) -n $(RECIPES_NAMESPACE) --from-file=index.json=/dev/stdin --dry-run=client -o yaml

.PHONY: backend-build
backend-build: ## Compile the BFF server into backend/bin/server
	cd $(BACKEND) && go build -o bin/server ./cmd/server

.PHONY: backend-run
backend-run: ## Run the BFF locally; configure it through the environment (README.md)
	cd $(BACKEND) && go run ./cmd/server

.PHONY: backend-test
backend-test: ## Run Go tests with coverage
	cd $(BACKEND) && go test -race -cover ./...

.PHONY: backend-lint
backend-lint: ## Run golangci-lint on the BFF
	cd $(BACKEND) && golangci-lint run ./...
