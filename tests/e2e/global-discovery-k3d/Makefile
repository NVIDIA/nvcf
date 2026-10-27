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

# The hub cluster (gds-server, phase 1). Phases 2 and 3 add
# $(E2E_CLUSTER)-a and $(E2E_CLUSTER)-b on the Docker network
# $(E2E_CLUSTER)-net. run.sh only uses names that start with gds-e2e.
E2E_CLUSTER ?= gds-e2e
export E2E_CLUSTER

# Phases to run, for example E2E_PHASES=1 for the single-cluster checks.
E2E_PHASES ?= 1 2 3
export E2E_PHASES

# Where the images come from. local builds them with Bazel from this
# checkout; it is the only source until images are published.
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
	@echo "make test          run phases $(E2E_PHASES); creates the k3d clusters when missing, builds and imports the images, installs the charts"
	@echo "make test-cleanup  run the phases, then delete what run.sh created (its clusters and network, or the releases and namespaces)"
	@echo "make clean         delete what earlier runs left: clusters and networks run.sh created, or the releases and namespaces"
	@echo "make deploy        install everything the phases need and stop after the setup checks, print the commands"
	@echo "make dry-run       render the charts with the phase values; no cluster, no build"
	@echo "make check         syntax-check run.sh with bash -n, and shellcheck when installed"
	@echo "make cluster       create k3d cluster $(E2E_CLUSTER) if it does not exist; changes no kube context"
	@echo "make images        build the images of the phases with Bazel and load them into Docker; BAZEL_FLAGS"
	@echo "make test-unit     bazel test the global-discovery module"
	@echo "make test-charts   run the chart render tests of both GDS charts and tools/ci/check-helm-charts"
	@echo "make all           images, then the test without a second build"
	@echo "make destroy       print the k3d cluster delete commands; CONFIRM=1 runs them"
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

# shellcheck from PATH, else the pinned image when Docker already has it.
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.10.0

check:
	bash -n ./run.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -x ./run.sh; \
	elif docker image inspect $(SHELLCHECK_IMAGE) >/dev/null 2>&1; then \
		docker run --rm -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) -x run.sh; \
	else \
		echo "shellcheck not found on PATH and $(SHELLCHECK_IMAGE) not in Docker; skipping"; \
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
	E2E_BUILD_ONLY=1 ./run.sh

test-unit:
	cd "$(REPO_ROOT)" && $(BAZEL) test $(BAZEL_FLAGS) //src/control-plane-services/global-discovery/... --test_output=errors

test-charts:
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/global-discovery" lint test
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/global-discovery-mcs-controller" lint test
	"$(REPO_ROOT)/tools/ci/check-helm-charts"

all: images
	E2E_SKIP_BUILD=1 ./run.sh

destroy:
	@case "$(E2E_CLUSTER)" in gds-e2e|gds-e2e-*) ;; *) echo "E2E_CLUSTER must start with gds-e2e"; exit 1 ;; esac
	@echo "k3d cluster delete $(E2E_CLUSTER) $(E2E_CLUSTER)-a $(E2E_CLUSTER)-b"
	@if [ "$(CONFIRM)" = "1" ]; then \
		for c in $(E2E_CLUSTER) $(E2E_CLUSTER)-a $(E2E_CLUSTER)-b; do \
			if k3d cluster get $$c >/dev/null 2>&1; then k3d cluster delete $$c; fi; \
		done; \
	else \
		echo "Not deleted. Run 'make destroy CONFIRM=1' to delete these k3d clusters."; \
	fi
