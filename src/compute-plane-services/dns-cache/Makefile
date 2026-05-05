# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# NVCF DNS Cache + Pod Mutator Webhook
# Combined Makefile for Helm chart and webhook image management

.PHONY: help unit-test lint build-webhook push-webhook deploy test logs status clean buildx-setup verify-manifest

REGISTRY ?= nvcr.io/nvidia
WEBHOOK_IMAGE ?= nvcf-pod-mutator
TAG ?= latest
PLATFORMS ?= linux/amd64,linux/arm64
WEBHOOK_FULL_IMAGE = $(REGISTRY)/$(WEBHOOK_IMAGE):$(TAG)
BUILDER_NAME ?= nvcf-multiarch

help:
	@echo "NVCF DNS Cache + Webhook - Make targets:"
	@echo ""
	@echo "  Webhook Build:"
	@echo "    make build-webhook   - Build webhook Docker image (local)"
	@echo "    make push-webhook    - Build and push webhook image (multi-arch)"
	@echo ""
	@echo "  Webhook Test:"
	@echo "    make unit-test       - Run Go unit tests"
	@echo "    make lint            - Run Go linter (golangci-lint)"
	@echo "    make test            - Run integration tests (requires K8s)"
	@echo ""
	@echo "  Helm Chart:"
	@echo "    make helm-template   - Dry-run Helm template rendering"
	@echo "    make helm-install    - Install Helm chart to cluster"
	@echo ""
	@echo "  Operations:"
	@echo "    make logs            - View webhook logs"
	@echo "    make status          - Show webhook status"
	@echo "    make clean           - Clean local artifacts"
	@echo ""
	@echo "Variables:"
	@echo "  REGISTRY=$(REGISTRY)"
	@echo "  WEBHOOK_IMAGE=$(WEBHOOK_IMAGE) -> $(WEBHOOK_FULL_IMAGE)"
	@echo "  PLATFORMS=$(PLATFORMS)"
	@echo ""

###########################################
# Webhook - Test
###########################################

unit-test:
	@echo "Running unit tests..."
	@go test -v ./...

lint:
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	@golangci-lint run --timeout=5m ./...
	@echo "Lint complete"

###########################################
# Webhook - Build
###########################################

buildx-setup:
	@echo "Setting up docker buildx for multi-arch builds..."
	@docker buildx inspect $(BUILDER_NAME) >/dev/null 2>&1 || \
		docker buildx create --name $(BUILDER_NAME) --driver docker-container --bootstrap
	@docker buildx use $(BUILDER_NAME)
	@echo "Buildx ready: $(BUILDER_NAME)"

build-webhook:
	@echo "Building webhook image (local): $(WEBHOOK_FULL_IMAGE)"
	@docker build -f docker/Dockerfile.webhook -t $(WEBHOOK_FULL_IMAGE) .
	@echo "Webhook build complete"

push-webhook: buildx-setup
	@echo "Building and pushing webhook image (multi-arch): $(WEBHOOK_FULL_IMAGE)"
	@echo "  Platforms: $(PLATFORMS)"
	@docker buildx build \
		--platform $(PLATFORMS) \
		-f docker/Dockerfile.webhook \
		-t $(WEBHOOK_FULL_IMAGE) \
		--push \
		.
	@echo "Webhook push complete (multi-arch)"

verify-manifest:
	@echo "Verifying multi-arch manifest..."
	@docker buildx imagetools inspect $(WEBHOOK_FULL_IMAGE)

###########################################
# Helm Chart
###########################################

helm-template:
	@echo "Rendering Helm templates (dry-run)..."
	@helm template test-release deploy/ -f deploy/values-stage.yaml --dry-run

helm-install:
	@echo "Installing Helm chart..."
	@chmod +x install.sh
	@./install.sh

###########################################
# Integration Tests
###########################################

test:
	@echo "Running integration tests..."
	@kubectl apply -f tests/test-pod.yaml
	@sleep 5
	@kubectl get pod test-webhook -n nvcf-backend -o jsonpath='{.spec.initContainers[*].name}'
	@echo ""
	@kubectl delete pod test-webhook -n nvcf-backend

###########################################
# Operations
###########################################

logs:
	@kubectl logs -n container-caching -l app=nvcf-pod-mutator -f --all-containers

status:
	@echo "Webhook Status:"
	@kubectl get pods -n container-caching -l app=nvcf-pod-mutator
	@echo ""
	@echo "Unbound Status:"
	@kubectl get pods -n container-caching -l app.kubernetes.io/name=nvcf-unbound

clean:
	@echo "Cleaning local artifacts..."
	@rm -f nvcf-pod-mutator
	@docker buildx rm $(BUILDER_NAME) 2>/dev/null || true
	@echo "Clean complete"
