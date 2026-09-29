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

SHELL := /bin/bash

GO ?= go
# Resolve against this module's go.mod even when a repository go.work exists.
GOWORK ?= off
export GOWORK

# controller-tools v0.19 is built against Kubernetes 1.34, the same minor as
# controller-runtime v0.22 in go.mod.
CONTROLLER_TOOLS_VERSION ?= v0.19.0
export CONTROLLER_TOOLS_VERSION

SETUP_ENVTEST_VERSION ?= release-0.22
ENVTEST_K8S_VERSION ?= 1.34.x
ENVTEST_BIN_DIR ?= $(CURDIR)/_output/envtest
GOLANGCI_LINT ?= golangci-lint

.PHONY: all
all: build

.PHONY: build
build:
	$(GO) build -o _output/bin/pylon-operator ./cmd/pylon-operator

.PHONY: codegen-update
codegen-update:
	hack/codegen.sh

.PHONY: codegen-check
codegen-check:
	hack/codegen.sh --check

# Unit tests. The envtest suite in internal/integration skips unless
# KUBEBUILDER_ASSETS is set; use test-envtest to run it. The reported total is
# the statement coverage of internal/.
.PHONY: test
test:
	@mkdir -p _output/cover
	$(GO) test -race -covermode=atomic -coverpkg=./internal/... -coverprofile=_output/cover/cover.txt ./...
	$(GO) tool cover -func=_output/cover/cover.txt | tail -1

.PHONY: envtest-assets
envtest-assets:
	@$(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION) \
		use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN_DIR) -p path

.PHONY: test-envtest
test-envtest:
	KUBEBUILDER_ASSETS="$$($(MAKE) -s envtest-assets)" $(GO) test ./internal/integration/... -count=1 -v

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run -c .golangci.yml --timeout 10m

.PHONY: vet
vet:
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: files above need formatting" >&2; exit 1; }
