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

# Metadata about this makefile and position
MKFILE_PATH := $(lastword $(MAKEFILE_LIST))
CURRENT_DIR := $(patsubst %/,%,$(dir $(realpath $(MKFILE_PATH))))

# Tags specific for building
GOTAGS ?=

# added for nv
# use "ess-agent" for binary name and its references
BIN_NAME=ess-agent

# Get the project metadata
OWNER := "hashicorp"
NAME := consul-template
PROJECT := $(shell go list -m | awk "/${NAME}/ {print $0}" )
GIT_COMMIT ?= $(shell git rev-parse --short HEAD || echo release)
VERSION := $(shell awk -F\" '/^[ \t]+Version/ { print $$2; exit }' "${CURRENT_DIR}/version/version.go")
PRERELEASE := $(shell awk -F\" '/^[ \t]+VersionPrerelease/ { print $$2; exit }' "${CURRENT_DIR}/version/version.go")

# Current system information
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# List of ldflags
LD_FLAGS ?= \
	-s -w \
	-X ${PROJECT}/version.GitCommit=${GIT_COMMIT}

# for CRT build process
version:
	@echo ${VERSION}${PRERELEASE}
.PHONY: version

# added for nv
# add target "build" for building executable and for configuring target OS and architecture.
build:
	@echo "==> Building ${BIN_NAME} for ${GOOS}/${GOARCH}"
	@env \
		GOOS="${GOOS}" \
		GOARCH="${GOARCH}" \
		CGO_ENABLED="0" \
		go build \
			-ldflags "${LD_FLAGS}" \
			-tags "${GOTAGS}" \
			-o bin/${BIN_NAME} .
.PHONY: build

# dev builds and installs the project locally.
dev:
	@echo "==> Installing ${BIN_NAME} for ${GOOS}/${GOARCH}"
	@env \
		CGO_ENABLED="0" \
		go install \
			-ldflags "${LD_FLAGS}" \
			 -tags "${GOTAGS}"
	@mv $(shell go env GOPATH)/bin/${NAME} $(shell go env GOPATH)/bin/$(BIN_NAME)
.PHONY: dev

# dev docker builds
docker:
	@env CGO_ENABLED="0" go build -ldflags "${LD_FLAGS}" -o $(BIN_NAME)
	mkdir -p dist/linux/amd64/
	cp ${BIN_NAME} dist/linux/amd64/
	env DOCKER_BUILDKIT=1 docker build -t ${BIN_NAME} .
.PHONY: docker

# test runs the test suite.
test:
	@echo "==> Testing ${BIN_NAME}"
	@go test -count=1 -timeout=30s -parallel=20 -failfast -tags="${GOTAGS}" ./... ${TESTARGS}
.PHONY: test

# test-race runs the test suite.
test-race:
	@echo "==> Testing ${BIN_NAME} (race)"
	@go test -timeout=60s -race -tags="${GOTAGS}" ./... ${TESTARGS}
.PHONY: test-race

# _cleanup removes any previous binaries
clean:
	@rm -rf "${CURRENT_DIR}/dist/"
	@rm -f "${BIN_NAME}"
.PHONY: clean

# Add/Update the "Table Of Contents" in the README.md
toc:
	@./scripts/readme-toc.sh
.PHONY: toc

# noop command to get build pipeline working
dev-tree:
	@true
.PHONY: dev-tree

# build-distroless builds the distroless image
build-distroless:
	@echo "==> Building distroless image for integration test"
	@./nv_releases/scripts/build_distroless.sh latest
.PHONY: build-distroless

# integration-test runs integration tests to verify ess-agent behavior in different modes
# Usage: make integration-test [TEST=01|02|03]
# If TEST is not specified, all tests will run
integration-test:
	@if [ "$(SKIP_BUILD)" != "true" ]; then \
		echo "==> Building distroless image for integration test"; \
		./nv_releases/scripts/build_distroless.sh latest; \
	fi
	@if [ -n "$(TEST)" ]; then \
		echo "==> Running integration test $(TEST) for ${BIN_NAME}"; \
		test_file="nv_releases/test/integration-tests/$(TEST)-*.sh"; \
		if ls $$test_file 1> /dev/null 2>&1; then \
			for test in $$test_file; do \
				echo "==> Running test: $$(basename $$test)"; \
				if ! ./$$test; then \
					echo "==> ❌ Test failed: $$(basename $$test)"; \
					exit 1; \
				fi; \
				echo "==> ✅ Test passed: $$(basename $$test)"; \
			done; \
		else \
			echo "==> ❌ Test $(TEST) not found"; \
			exit 1; \
		fi; \
	else \
		echo "==> Running all integration tests for ${BIN_NAME}"; \
		set -e; \
		for test in nv_releases/test/integration-tests/*.sh; do \
			echo "==> Running test: $$(basename $$test)"; \
			if ! ./$$test; then \
				echo "==> ❌ Test failed: $$(basename $$test)"; \
				exit 1; \
			fi; \
			echo "==> ✅ Test passed: $$(basename $$test)"; \
		done; \
	fi
.PHONY: integration-test
