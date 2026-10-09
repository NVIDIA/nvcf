# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Pylon Operator end-to-end test on a local k3d cluster. See README.md and
# docs/dev/inference-endpoints-quickstart.md.

E2E_KUBE_CONTEXT ?= k3d-pylon-op-e2e
export E2E_KUBE_CONTEXT

REPO_ROOT := $(abspath ../../..)
K3D_CLUSTER ?= $(patsubst k3d-%,%,$(E2E_KUBE_CONTEXT))
SAMPLE_DIR ?= ../../../examples/function-samples/openai-compatible-sample
SAMPLE_IMAGE ?= openai-compatible-sample:e2e

# Extra Bazel flags for images and test-unit, for example --config=remote.
BAZEL ?= bazel
BAZEL_FLAGS ?=
# Absolute path of an empty directory. When set, Bazel repository rules use
# it as DOCKER_CONFIG, so the public base images are pulled anonymously even
# when the local nvcr.io credential is stale (401 at analysis).
DOCKER_CONFIG_DIR ?=
BAZEL_ARGS = $(strip $(BAZEL_FLAGS) $(if $(DOCKER_CONFIG_DIR),--repo_env=DOCKER_CONFIG=$(DOCKER_CONFIG_DIR)))

# Image names loaded by the Bazel image_load targets, plus the sample.
IMAGES = \
	src/compute-plane-services/pylon-operator:latest \
	src/invocation-plane-services/llm-api-gateway:latest \
	src/libraries/rust/stargate/crates/pylon:latest \
	src/libraries/rust/stargate/crates/stargate:latest \
	$(SAMPLE_IMAGE)

UNIT_TEST_TARGETS = \
	//src/compute-plane-services/pylon-operator/... \
	//src/invocation-plane-services/llm-api-gateway/... \
	//src/libraries/rust/stargate/...

CONFIRM ?= 0

# The workflow targets depend on each other's side effects.
.NOTPARALLEL:

.PHONY: help test test-cleanup check sample-image sample-image-build \
	cluster images import deploy test-unit test-charts all destroy

help:
	@echo "make cluster       create k3d cluster $(K3D_CLUSTER) with one agent if it does not exist, then check the kube context"
	@echo "make images        build the four images with Bazel and the sample image; BAZEL_FLAGS, DOCKER_CONFIG_DIR=<empty dir>"
	@echo "make import        import the five images into k3d cluster $(K3D_CLUSTER)"
	@echo "make deploy        install the stack and the InferenceEndpoint, stop when it is ready, print the curl commands"
	@echo "make test          run the end-to-end test against $(E2E_KUBE_CONTEXT); leaves the releases installed"
	@echo "make test-cleanup  run the test, then uninstall the releases and delete the namespaces"
	@echo "make test-unit     bazel test the operator, gateway and Stargate subtrees; BAZEL_FLAGS, DOCKER_CONFIG_DIR"
	@echo "make test-charts   run the render tests of the four charts and tools/ci/check-helm-charts"
	@echo "make all           images, import, deploy and test"
	@echo "make destroy       print the k3d cluster delete command; CONFIRM=1 runs it"
	@echo "make check         syntax-check run.sh and restart-cases.sh with bash -n, and shellcheck when installed"
	@echo "make sample-image  build the in-repo OpenAI-compatible sample image and import it into $(E2E_KUBE_CONTEXT)"

test:
	./run.sh

test-cleanup:
	E2E_CLEANUP=1 ./run.sh

check:
	bash -n ./run.sh
	bash -n ./restart-cases.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -x ./run.sh ./restart-cases.sh; \
	else \
		echo "shellcheck not found on PATH; skipping"; \
	fi

sample-image-build:
	docker build -t $(SAMPLE_IMAGE) $(SAMPLE_DIR)

sample-image: sample-image-build
	k3d image import -c $(K3D_CLUSTER) $(SAMPLE_IMAGE)

cluster:
	@if k3d cluster get $(K3D_CLUSTER) >/dev/null 2>&1; then \
		echo "k3d cluster $(K3D_CLUSTER) exists"; \
	else \
		echo "k3d cluster create $(K3D_CLUSTER) --agents 1"; \
		k3d cluster create $(K3D_CLUSTER) --agents 1; \
	fi
	@current="$$(kubectl config current-context 2>/dev/null || true)"; \
	if [ "$$current" != "$(E2E_KUBE_CONTEXT)" ]; then \
		echo "current kube context is '$${current:-<none>}', expected '$(E2E_KUBE_CONTEXT)'"; \
		echo "switch with: kubectl config use-context $(E2E_KUBE_CONTEXT)"; \
		exit 1; \
	fi
	kubectl --context $(E2E_KUBE_CONTEXT) get nodes

images:
	cd "$(REPO_ROOT)" && $(BAZEL) run $(BAZEL_ARGS) //src/compute-plane-services/pylon-operator:image_load
	cd "$(REPO_ROOT)" && $(BAZEL) run $(BAZEL_ARGS) //src/invocation-plane-services/llm-api-gateway:image_load
	cd "$(REPO_ROOT)" && $(BAZEL) run $(BAZEL_ARGS) //src/libraries/rust/stargate/crates/pylon:image_load
	cd "$(REPO_ROOT)" && $(BAZEL) run $(BAZEL_ARGS) //src/libraries/rust/stargate/crates/stargate:image_load
	$(MAKE) sample-image-build

import:
	k3d image import -c $(K3D_CLUSTER) $(IMAGES)

deploy:
	E2E_DEPLOY_ONLY=1 ./run.sh

test-unit:
	cd "$(REPO_ROOT)" && $(BAZEL) test $(BAZEL_ARGS) $(UNIT_TEST_TARGETS)

test-charts:
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/llm-gateway-stack" test
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/pylon-operator" test
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/llm-request-router" test
	$(MAKE) -C "$(REPO_ROOT)/deploy/helm/llm-api-gateway" test
	"$(REPO_ROOT)/tools/ci/check-helm-charts"

all: images import deploy test

destroy:
	@echo "k3d cluster delete $(K3D_CLUSTER)"
	@if [ "$(CONFIRM)" = "1" ]; then \
		k3d cluster delete $(K3D_CLUSTER); \
	else \
		echo "Not deleted. Run 'make destroy CONFIRM=1' to delete k3d cluster $(K3D_CLUSTER)."; \
	fi
