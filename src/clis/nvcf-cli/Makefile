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

# NVIDIA Cloud Functions CLI - Makefile

.PHONY: build clean test lint install run-tests help license-check sync-agent-skill e2e-self-hosted-down

# Variables
BINARY_NAME=nvcf-cli
BUILD_DIR=./build
GO_FILES=$(shell find . -name "*.go" -type f)
CLUSTER_NAME ?= ncp-local-e2e

# Version information (can be overridden)
VERSION ?= $(shell git describe --tags --exact-match HEAD 2>/dev/null || echo "mr-$$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_USER ?= $(shell whoami 2>/dev/null || echo "unknown")
GO_VERSION ?= $(shell go version | awk '{print $$3}' 2>/dev/null || echo "unknown")

# Enhanced ldflags with all version info
LDFLAGS=-ldflags "-s -w \
	-X 'main.Version=${VERSION}' \
	-X 'main.GitCommit=${GIT_COMMIT}' \
	-X 'main.GitBranch=${GIT_BRANCH}' \
	-X 'main.BuildDate=${BUILD_DATE}' \
	-X 'main.BuildUser=${BUILD_USER}' \
	-X 'main.GoVersion=${GO_VERSION}'"

# Default target
all: build

# Build the binary at top level
build: clean
	@echo "Building ${BINARY_NAME} at top level..."
	@go build ${LDFLAGS} -o ${BINARY_NAME} .
	@echo "✅ Binary built successfully: ./${BINARY_NAME}"

# Build for multiple platforms
build-all: clean
	@echo "Building for multiple platforms..."
	@mkdir -p ${BUILD_DIR}
	
	# Linux AMD64
	@GOOS=linux GOARCH=amd64 go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME}-linux-amd64 .
	@echo "✅ Built: ${BUILD_DIR}/${BINARY_NAME}-linux-amd64"
	
	# Linux ARM64
	@GOOS=linux GOARCH=arm64 go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME}-linux-arm64 .
	@echo "✅ Built: ${BUILD_DIR}/${BINARY_NAME}-linux-arm64"
	
	# macOS AMD64
	@GOOS=darwin GOARCH=amd64 go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME}-darwin-amd64 .
	@echo "✅ Built: ${BUILD_DIR}/${BINARY_NAME}-darwin-amd64"
	
	# macOS ARM64 (Apple Silicon)
	@GOOS=darwin GOARCH=arm64 go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME}-darwin-arm64 .
	@echo "✅ Built: ${BUILD_DIR}/${BINARY_NAME}-darwin-arm64"
	
	# Windows AMD64
	@GOOS=windows GOARCH=amd64 go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME}-windows-amd64.exe .
	@echo "✅ Built: ${BUILD_DIR}/${BINARY_NAME}-windows-amd64.exe"

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy
	@echo "✅ Dependencies installed"

# Run tests
test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@echo "✅ Tests completed"

# Run tests with coverage report
test-coverage: test
	@echo "Generating coverage report..."
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report generated: coverage.html"

# Run linter (requires golangci-lint to be installed)
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
		echo "✅ Linting completed"; \
	else \
		echo "⚠️  golangci-lint not installed. Install it with:"; \
		echo "    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin v1.54.2"; \
	fi

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✅ Code formatted"

# Vet code
vet:
	@echo "Vetting code..."
	@go vet ./...
	@echo "✅ Code vetted"

# Install binary to GOPATH/bin
install:
	@echo "Installing ${BINARY_NAME}..."
	@go install ${LDFLAGS} .
	@echo "✅ ${BINARY_NAME} installed to \$$(go env GOPATH)/bin/"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf ${BUILD_DIR}
	@rm -f ${BINARY_NAME}
	@rm -f coverage.out coverage.html
	@echo "✅ Clean completed"

# Check that all source files have SPDX copyright headers
license-check:
	@echo "Checking copyright headers..."
	@./add-copyright.sh --check
	@echo "✅ Copyright header check passed"

# Run comprehensive checks
check: deps fmt vet lint test license-check
	@echo "✅ All checks passed!"

# Create a release (requires goreleaser)
release:
	@echo "Creating release..."
	@if command -v goreleaser >/dev/null 2>&1; then \
		goreleaser release --rm-dist; \
		echo "✅ Release completed"; \
	else \
		echo "⚠️  goreleaser not installed. Install it with:"; \
		echo "    go install github.com/goreleaser/goreleaser@latest"; \
	fi

# Development setup
dev-setup:
	@echo "Setting up development environment..."
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install honnef.co/go/tools/cmd/staticcheck@latest
	@echo "✅ Development setup completed"

# Show version information (without building)
version-info:
	@echo "Version Information:"
	@echo "  VERSION:     ${VERSION}"
	@echo "  GIT_COMMIT:  ${GIT_COMMIT}"
	@echo "  GIT_BRANCH:  ${GIT_BRANCH}"
	@echo "  BUILD_DATE:  ${BUILD_DATE}"
	@echo "  BUILD_USER:  ${BUILD_USER}"
	@echo "  GO_VERSION:  ${GO_VERSION}"

# Create a git tag for release
tag:
	@read -p "Enter version tag (e.g., v1.0.0): " tag; \
	git tag -a $$tag -m "Release $$tag"; \
	echo "Created tag $$tag. Push with: git push origin $$tag"

# Show what version would be used for next build
show-version:
	@echo ${VERSION}

# Build with custom version (e.g., make build-version VERSION=v1.2.3)
build-version:
	@if [ -z "$(VERSION)" ]; then \
		echo "Usage: make build-version VERSION=v1.2.3"; \
		exit 1; \
	fi
	@echo "Building with custom version: $(VERSION)"
	@$(MAKE) build VERSION=$(VERSION)

# Create a snapshot build (version includes timestamp)
build-snapshot:
	@$(eval SNAPSHOT_VERSION := $(shell echo "${VERSION}-snapshot-$(shell date +%Y%m%d-%H%M%S)"))
	@echo "Building snapshot version: $(SNAPSHOT_VERSION)"
	@$(MAKE) build VERSION=$(SNAPSHOT_VERSION)

# Build the binary in build directory (for CI/CD compatibility)
build-dir: clean
	@echo "Building ${BINARY_NAME} in build directory..."
	@mkdir -p ${BUILD_DIR}
	@go build ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME} .
	@echo "✅ Binary built successfully: ${BUILD_DIR}/${BINARY_NAME}"

# sync-agent-skill: copy mcamp/docs/.../agent-skill/ into internal/agentskill/data/
# and verify the manifest is intact. Run before tagging a release.
sync-agent-skill: ## Copy agent-skill from mcamp/docs into internal/agentskill/data/
	@if [ -z "$(MCAMP_DOCS)" ]; then \
		echo "set MCAMP_DOCS=<path-to-mcamp-docs-checkout> (e.g. ~/dev/nvidia/.../mcamp/docs)"; \
		exit 1; \
	fi
	@echo ">>> Syncing agent-skill from $(MCAMP_DOCS)..."
	rm -rf internal/agentskill/data/
	mkdir -p internal/agentskill/data/
	cp -R $(MCAMP_DOCS)/nvcf/one-click-deploy/agent-skill/manifest.json \
	      $(MCAMP_DOCS)/nvcf/one-click-deploy/agent-skill/SKILL.md \
	      $(MCAMP_DOCS)/nvcf/one-click-deploy/agent-skill/prompts \
	      $(MCAMP_DOCS)/nvcf/one-click-deploy/agent-skill/reference \
	      $(MCAMP_DOCS)/nvcf/one-click-deploy/agent-skill/examples \
	      internal/agentskill/data/
	@echo ">>> Verifying embedded manifest..."
	go test ./internal/agentskill/... -run TestVerify_OK -count=1
	@echo ">>> Sync complete."

# Show help
help:
	@echo "Available targets:"
	@echo "  build        - Build the binary at top level"
	@echo "  build-dir    - Build the binary in build/ directory"
	@echo "  build-all    - Build for multiple platforms (in build/ dir)"
	@echo "  clean        - Clean build artifacts"
	@echo "  deps         - Install dependencies"
	@echo "  test         - Run tests"
	@echo "  test-coverage- Run tests with coverage report"
	@echo "  lint         - Run linter"
	@echo "  fmt          - Format code"
	@echo "  vet          - Vet code"
	@echo "  install      - Install binary to GOPATH/bin"
	@echo "  license-check- Check copyright headers on all source files"
	@echo "  check        - Run comprehensive checks"
	@echo "  release      - Create a release"
	@echo "  dev-setup    - Set up development environment"
	@echo "  version-info - Show version information"
	@echo "  show-version - Show version that would be used"
	@echo "  build-version- Build with custom version (VERSION=v1.0.0)"
	@echo "  build-snapshot- Build snapshot with timestamp"
	@echo "  tag                      - Create a git tag for release"
	@echo "  e2e-self-hosted          - Run manual self-hosted E2E (T1-T7) on mcamp-dev-vm"
	@echo "  e2e-self-hosted-progress - Run progress E2E (M+7.9) on mcamp-dev-vm"
	@echo "  e2e-self-hosted-faults   - Run fault-injection E2E (T11-T17, M+8.I) on mcamp-dev-vm"
	@echo "  e2e-self-hosted-split    - Run split-cluster E2E (T8/T9/T10, M+9.H) on mcamp-dev-vm"
	@echo "  e2e-self-hosted-down     - Run T18-T21 down idempotency tests on mcamp-dev-vm"
	@echo "  sync-agent-skill         - Copy agent-skill from mcamp/docs into internal/agentskill/data/"
	@echo "  help         - Show this help message"

# E2E: manual self-hosted idempotency matrix (spec §8.4 T1-T7).
# Run on mcamp-dev-vm only against a working k3d cluster.
.PHONY: e2e-self-hosted
e2e-self-hosted: ## Run manual self-hosted E2E (T1-T7). Run on mcamp-dev-vm only.
	@echo ">>> Building nvcf-cli for E2E..."
	go build -o /tmp/nvcf-cli .
	@echo ">>> Running T1-T7 against k3d cluster $(CLUSTER_NAME)..."
	NVCF_E2E=1 go test -tags=e2e -v ./test/e2e/...

# E2E: progress UX smoke (M+7.9). Verifies --plain mode emits the §6.4.2
# line shape during a real install. Slow (~10 min); runs on mcamp-dev-vm only.
.PHONY: e2e-self-hosted-progress
e2e-self-hosted-progress: ## Run progress E2E (M+7.9). Run on mcamp-dev-vm only.
	@echo ">>> Building nvcf-cli for E2E progress smoke..."
	go build -o /tmp/nvcf-cli .
	@echo ">>> Running TestSelfHostedUp_PlainModeEvents..."
	NVCF_E2E=1 go test -tags=e2e -v -timeout=20m -run TestSelfHostedUp_PlainModeEvents ./test/e2e/...

# E2E: fault-injection matrix (M+8.I, spec §8.4 T11-T17).
# Requires toxiproxy-server on PATH; also needs a live k3d cluster, cqlsh,
# and helmfile for the individual tests that use them. Run on mcamp-dev-vm only.
.PHONY: e2e-self-hosted-faults
e2e-self-hosted-faults: ## Run fault-injection tests T11-T17 (requires toxiproxy-server, live cluster, cqlsh, helmfile)
	@which toxiproxy-server >/dev/null 2>&1 || (echo "ERROR: toxiproxy-server not on PATH. Install from https://github.com/Shopify/toxiproxy/releases" && exit 1)
	@echo ">>> Building nvcf-cli for fault-injection E2E..."
	go build -o /tmp/nvcf-cli .
	@echo ">>> Running T11-T17 fault-injection tests..."
	NVCF_E2E=1 NVCF_E2E_FAULTS=1 go test -tags=e2e -v -timeout=30m -run 'TestT1[1-7]' ./test/e2e/...

# E2E: split-cluster smoke matrix (M+9.H, spec §8.4 T8-T10).
# Requires two or three k3d clusters on PATH. Run on mcamp-dev-vm only.
.PHONY: e2e-self-hosted-split
e2e-self-hosted-split: ## Run split-cluster T8/T9/T10 tests (requires two+ k3d clusters)
	@which k3d >/dev/null 2>&1 || (echo "install k3d first: https://k3d.io/#installation" && exit 1)
	@echo ">>> Building nvcf-cli for split-cluster E2E..."
	go build -o /tmp/nvcf-cli .
	@echo ">>> Running T8/T9/T10 split-cluster tests..."
	NVCF_E2E=1 go test -tags=e2e -v -timeout=60m -run 'TestE2E_T(8|9|10)' ./test/e2e/...

# E2E: down idempotency matrix (M+11.J, spec §8.4 T18-T21).
# Requires live k3d cluster, helm, and helmfile. Run on mcamp-dev-vm only.
.PHONY: e2e-self-hosted-down
e2e-self-hosted-down: ## Run T18-T21 down idempotency tests on mcamp-dev-vm
	@which helm >/dev/null || (echo "install helm first" && exit 1)
	@which helmfile >/dev/null || (echo "install helmfile first" && exit 1)
	NVCF_E2E=1 go test -tags=e2e -timeout=60m -v -run='TestE2E_T(18|19|20|21)' ./test/e2e/...
