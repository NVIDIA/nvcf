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

GOLANGCI_LINT ?= golangci-lint
# Minimum statement coverage of internal/, in percent.
COVERAGE_MIN ?= 85

# buf and the protoc plugins are installed with `go install` (versions in
# hack/proto.sh) into $(HOME)/go/bin, which is not always on PATH.
# make proto-tools installs the pinned versions into _output/tools, which
# comes first.
PROTO_TOOLS_BIN := $(CURDIR)/_output/tools
PROTO_PATH := $(PROTO_TOOLS_BIN):$(HOME)/go/bin:$(PATH)

.PHONY: all
all: build

.PHONY: build
build:
	$(GO) build ./...
	$(GO) build -o _output/bin/gds-server ./cmd/gds-server
	$(GO) build -o _output/bin/gds-admin ./cmd/gds-admin

# Unit tests with the race detector. The reported total is the statement
# coverage of internal/; the target fails below COVERAGE_MIN percent.
.PHONY: test
test:
	@mkdir -p _output/cover
	$(GO) test -race -covermode=atomic -coverpkg=./internal/... -coverprofile=_output/cover/cover.txt ./...
	@total="$$($(GO) tool cover -func=_output/cover/cover.txt | tail -1)"; \
	echo "$${total}"; \
	pct="$$(awk '{ sub(/%/, "", $$NF); print $$NF }' <<<"$${total}")"; \
	if awk -v p="$${pct}" -v m="$(COVERAGE_MIN)" 'BEGIN { exit !(p < m) }'; then \
		echo "coverage of internal/ is $${pct}%, below $(COVERAGE_MIN)%" >&2; exit 1; \
	fi

# Integration tests (build tag "integration"): the DynamoDB store
# conformance and multi-replica tests and gds-admin create-table against
# amazon/dynamodb-local, started through testcontainers-go. They skip when no Docker daemon is reachable. DOCKER_HOST defaults to the endpoint of the
# current docker context, and colima gets the Ryuk socket override it needs.
# GDS_TEST_DYNAMODB_ENDPOINT=http://host:port uses a running endpoint instead
# of a container.
INTEGRATION_PKGS ?= ./internal/store/dynamodb/... ./cmd/gds-admin/...

.PHONY: test-integration
test-integration:
	@docker_host="$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null)}"; \
	socket_override="$${TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE:-}"; \
	case "$${docker_host}" in *colima*) socket_override="$${socket_override:-/var/run/docker.sock}" ;; esac; \
	echo "DOCKER_HOST=$${docker_host} $(GO) test -race -count=1 -tags=integration $(INTEGRATION_PKGS)"; \
	DOCKER_HOST="$${docker_host}" TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE="$${socket_override}" \
		$(GO) test -race -count=1 -tags=integration $(INTEGRATION_PKGS)

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run -c .golangci.yml --timeout 10m

.PHONY: vet
vet:
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: files above need formatting" >&2; exit 1; }

# Install buf and the protoc plugins at the versions pinned by the
# "go install" lines of hack/proto.sh. CI runs
# tools/ci/check-go-codegen with "make proto-tools proto".
.PHONY: proto-tools
proto-tools:
	@tools="$$(sed -n 's/^#   go install //p' hack/proto.sh)"; \
	test -n "$${tools}" || { echo "hack/proto.sh pins no tools" >&2; exit 1; }; \
	for tool in $${tools}; do \
		echo "go install $${tool}"; \
		GOBIN="$(PROTO_TOOLS_BIN)" $(GO) install "$${tool}" || exit 1; \
	done

# Lint the proto contract and regenerate api/gen.
.PHONY: proto
proto:
	@echo "hack/proto.sh"
	@PATH="$(PROTO_PATH)" hack/proto.sh

# Lint the proto contract and fail when api/gen is stale.
.PHONY: proto-check
proto-check:
	@echo "hack/proto.sh --check"
	@PATH="$(PROTO_PATH)" hack/proto.sh --check

# Report breaking changes of the proto contract against a git ref, for
# example: make proto-breaking AGAINST=origin/main
AGAINST ?= origin/main
.PHONY: proto-breaking
proto-breaking:
	@PATH="$(PROTO_PATH)" buf breaking --against "$$(git rev-parse --show-toplevel)/.git#ref=$(AGAINST),subdir=src/control-plane-services/global-discovery"

.PHONY: clean
clean:
	rm -rf _output
