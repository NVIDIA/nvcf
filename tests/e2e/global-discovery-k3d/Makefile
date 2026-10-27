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
#
# Global Discovery Service end-to-end test on local k3d clusters. See
# README.md.

# Name of the k3d cluster of phase 1. run.sh only uses names that start with
# gds-e2e.
E2E_CLUSTER ?= gds-e2e
export E2E_CLUSTER

# Where the gds-server image comes from. local builds it with Bazel from this
# checkout; it is the only source until an image is published.
IMAGE_SOURCE ?= local
ifneq ($(IMAGE_SOURCE),local)
$(error IMAGE_SOURCE must be local, got '$(IMAGE_SOURCE)')
endif
export IMAGE_SOURCE

REPO_ROOT := $(abspath ../../..)

# Extra Bazel flags for images and test-unit, for example --config=remote.
BAZEL ?= bazel
BAZEL_FLAGS ?=
export BAZEL BAZEL_FLAGS

CONFIRM ?= 0

# The workflow targets depend on each other's side effects.
.NOTPARALLEL:

.PHONY: help check dry-run cluster images deploy test test-cleanup clean \
	test-unit test-charts all destroy

help:
	@echo "make test          run phase 1; creates k3d cluster $(E2E_CLUSTER) when missing, builds and imports the image, installs the chart"
	@echo "make test-cleanup  run phase 1, then delete what run.sh created (the cluster it created, or the release and namespace)"
	@echo "make clean         delete what earlier runs left: clusters run.sh created, or the release and namespace"
	@echo "make deploy        install gds-server and stop after the server checks, print the curl commands"
	@echo "make dry-run       render the chart with the phase 1 values; no cluster, no build"
	@echo "make check         syntax-check run.sh with bash -n, and shellcheck when installed"
	@echo "make cluster       create k3d cluster $(E2E_CLUSTER) if it does not exist; changes no kube context"
	@echo "make images        build and load the gds-server image with Bazel; BAZEL_FLAGS"
	@echo "make test-unit     bazel test the global-discovery module"
	@echo "make test-charts   run the chart render tests and tools/ci/check-helm-charts"
	@echo "make all           images, then the test without a second build"
	@echo "make destroy       print the k3d cluster delete command; CONFIRM=1 runs it"
	@echo ""
	@echo "README.md lists the E2E_* settings, for example E2E_TIMEOUT and E2E_WORK_DIR."

test:
	./run.sh

test-cleanup:
	E2E_CLEANUP=1 ./run.sh

clean:
	E2E_CLEANUP_ONLY=1 ./run.sh

deploy:
	E2E_DEPLOY_ONLY=1 ./run.sh

dry-run:
	E2E_DRY_RUN=1 ./run.sh

check:
	bash -n ./run.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -x ./run.sh; \
	else \
		echo "shellcheck not found on PATH; skipping"; \
	fi

cluster:
	@case "$(E2E_CLUSTER)" in gds-e2e|gds-e2e-*) ;; *) echo "E2E_CLUSTER must start with gds-e2e"; exit 1 ;; esac
	@if k3d cluster get $(E2E_CLUSTER) >/dev/null 2>&1; then \
		echo "k3d cluster $(E2E_CLUSTER) exists"; \
	else \
		echo "k3d cluster create $(E2E_CLUSTER)"; \
		k3d cluster create $(E2E_CLUSTER) --servers 1 --agents 0 --no-lb \
			--k3s-arg '--disable=traefik@server:*' \
			--kubeconfig-update-default=false --kubeconfig-switch-context=false \
			--wait --timeout 180s; \
	fi

images:
	cd "$(REPO_ROOT)" && $(BAZEL) run $(BAZEL_FLAGS) //src/control-plane-services/global-discovery:image_load

test-unit:
	cd "$(REPO_ROOT)" && $(BAZEL) test $(BAZEL_FLAGS) //src/control-plane-services/global-discovery/... --test_output=errors

test-charts:
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/global-discovery" lint test
	"$(REPO_ROOT)/tools/ci/check-helm-charts"

all: images
	E2E_SKIP_BUILD=1 ./run.sh

destroy:
	@case "$(E2E_CLUSTER)" in gds-e2e|gds-e2e-*) ;; *) echo "E2E_CLUSTER must start with gds-e2e"; exit 1 ;; esac
	@echo "k3d cluster delete $(E2E_CLUSTER)"
	@if [ "$(CONFIRM)" = "1" ]; then \
		k3d cluster delete $(E2E_CLUSTER); \
	else \
		echo "Not deleted. Run 'make destroy CONFIRM=1' to delete k3d cluster $(E2E_CLUSTER)."; \
	fi
