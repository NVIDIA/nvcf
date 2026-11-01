# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

IMAGE ?= reval-server
TAG   ?= local

.PHONY: build
build:
	go build -o _output/bin/reval ./cmd/reval-service

.PHONY: test
test:
	go test ./...

.PHONY: lint
lint:
	golangci-lint run ./...

COVERAGE_THRESHOLD ?= 80

.PHONY: test-coverage
test-coverage:
	go test -coverprofile=coverage.out -covermode=set -v ./... | tee test-results.txt
	go tool cover -func=coverage.out
	@go tool cover -func=coverage.out | awk -v threshold="$(COVERAGE_THRESHOLD)" \
	  '/^total:/ { gsub(/%/, "", $$NF); if ($$NF + 0 < threshold + 0) { \
	    printf "FAIL: coverage %.1f%% is below %s%% threshold\n", $$NF, threshold; exit 1 } \
	    printf "PASS: coverage %.1f%% meets %s%% threshold\n", $$NF, threshold }'

# Run only fast, self-contained unit tests (no external deps).
# Uses the local module cache to avoid fetching private NVIDIA modules.
.PHONY: test-unit
test-unit:
	GOPROXY="file://$(shell go env GOMODCACHE)/cache/download,off" \
		go test ./pkg/authorizers/... ./pkg/httpapi/... ./pkg/reval/service/... ./reval/cache/...

# Verify every source file carries the full Apache 2.0 license header.
.PHONY: check-license-headers
check-license-headers:
	@echo "Checking source files for license headers..."
	@missing=$$(grep -rL "Licensed under the Apache License" \
	  --include="*.go" --include="*.yaml" --include="*.yml" --include="*.sh" \
	  . | grep -v '.git/' | grep -v 'vendor/'); \
	if [ -n "$$missing" ]; then \
	  printf "FAIL: the following files are missing the Apache 2.0 license header:\n$$missing\n"; \
	  exit 1; \
	fi
	@echo "PASS: all source files have the required license header."

# Check that dependency licenses are acceptable (go-licenses ratchet).
.PHONY: check-licenses
check-licenses:
	./scripts/ci_check_licenses

.PHONY: update-license-baseline
update-license-baseline:
	./scripts/ci_check_licenses --update-baseline

.PHONY: container
container:
	DOCKER_BUILDKIT=1 docker build \
		-f docker/Dockerfile \
		--build-arg REVAL_VERSION=$(shell git describe --tags --always 2>/dev/null || echo dev) \
		--build-arg REVAL_GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo dev) \
		-t $(IMAGE):$(TAG) .

# ── Local development ────────────────────────────────────────────────────────

# Run the server using the local test config (mock JWKS on :8888, local registries on :8282/:8383).
.PHONY: run
run: build
	./_output/bin/reval --config test/config.yaml

# Start local helm (:8282) and image (:8383) test registries.
.PHONY: run-test-regs
run-test-regs:
	go run ./test/registry

# Start a mock JWKS server on :8888 and write a signed test JWT to /tmp/reval-test-token.
.PHONY: mock-jwks
mock-jwks:
	go run ./test/mockjwks

# Run the full test suite (starts mock JWKS, registries, and server, then validates).
.PHONY: test-server
test-server:
	bash test/test_server.sh
