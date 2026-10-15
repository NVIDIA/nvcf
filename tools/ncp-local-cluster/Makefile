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

.PHONY: help clean build test test-coverage-html test-manual start stop destroy destroy-all-ncp-local status ensure-cluster ensure-context ensure-docker-config validate-compute-clusters print-compute-clusters start-control-plane deploy-control-plane-addons deploy-control-plane-endpoints build-and-deploy-control-plane-cluster destroy-control-plane start-compute-plane deploy-compute-plane-addons configure-compute-control-plane-dns deploy-compute-control-plane-endpoints build-and-deploy-compute-plane-cluster destroy-compute-plane build-and-deploy-multicluster destroy-multicluster test-multicluster-make setup-gateway-api setup-metallb check-gateway-api deploy-sample deploy-nginx wait-for-nginx wait-for-gateway validate-gateway cleanup-nginx wait-for-deployment validate-deployment cleanup-sample build-and-deploy-cluster build-csi-smb deploy-csi-smb wait-for-csi-smb build-fake-gpu-operator deploy-fake-gpu-operator wait-for-fake-gpu-operator deploy-prometheus-crds uninstall-prometheus-crds deploy-kube-state-metrics wait-for-kube-state-metrics uninstall-kube-state-metrics build-credential-provider-multiarch

# === Cluster Configuration ===
CLUSTER_NAME := ncp-local
K3D_CONFIG_FILE := k3d-config.yaml

# Optional local overrides from tools/ncp-local-cluster/.env.
# Command-line variables (make VAR=value) still win over this file;
# shell-environment variables do NOT; use command-line overrides or remove
# the .env entry to unset a value.
ifneq (,$(wildcard .env))
include .env
export
endif

# === Multi-Cluster Configuration ===
CONTROL_PLANE_CLUSTER_NAME ?= ncp-local-cp
CONTROL_PLANE_CONFIG_FILE ?= k3d-config-control-plane.yaml
CONTROL_PLANE_HTTP_PORT ?= 8080
CONTROL_PLANE_HTTPS_PORT ?= 8443
CONTROL_PLANE_GRPC_PORT ?= 9090
CONTROL_PLANE_GRPC_PROXY_PORT ?= 10081
CONTROL_PLANE_GRPC_WORKER_PORT ?= 10086
CONTROL_PLANE_NATS_PORT ?= 4222
CONTROL_PLANE_DOMAIN ?= nvcf-control-plane.test
COMPUTE_CLUSTER_PREFIX ?= ncp-local-compute
COMPUTE_CLUSTER_COUNT ?= 1
COMPUTE_CLUSTER_NAME ?= $(COMPUTE_CLUSTER_PREFIX)-1
COMPUTE_PLANE_CONFIG_FILE ?= k3d-config-compute-plane.yaml
COMPUTE_CLUSTERS ?=
GATEWAY_HTTP_PORT ?= $(CONTROL_PLANE_HTTP_PORT)

# === Go Credential Provider Configuration ===
GO_PROVIDER_PATH         := ./credential-provider-go
BINARY_NAME              := generic-credential-provider
OUTPUT_DIR               ?= ./bin

# Determine the architecture of the host machine (x86_64 -> amd64, aarch64 -> arm64)
GOARCH           ?= $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

# Resolved output paths for clarity in build commands
PROVIDER_OUTPUT_PATH          := $(OUTPUT_DIR)/$(BINARY_NAME)
PROVIDER_LINUX_AMD64_OUTPUT_PATH := $(OUTPUT_DIR)/$(BINARY_NAME)-linux-amd64
PROVIDER_LINUX_ARM64_OUTPUT_PATH := $(OUTPUT_DIR)/$(BINARY_NAME)-linux-arm64

# === CSI Driver SMB Configuration ===
CSI_SMB_DIR := ./apps/csi-driver-smb
CSI_SMB_STATIC := $(CSI_SMB_DIR)/static/csi-driver-smb.yaml

# === Fake GPU Operator Configuration ===
FAKE_GPU_DIR := ./apps/fake-gpu-operator
FAKE_GPU_STATIC := $(FAKE_GPU_DIR)/static/fake-gpu-operator.yaml
FAKE_GPU_VERSION := 0.0.63

# === Kube Prometheus Stack Configuration (CRDs only) ===
KPS_DIR := ./apps/kube-prometheus-stack
KPS_NAMESPACE := observability
KPS_RELEASE := kube-prometheus-stack-crds
KPS_CHART := prometheus-community/kube-prometheus-stack

# === Kube State Metrics Configuration ===
KSM_DIR := ./apps/kube-state-metrics
KSM_NAMESPACE := observability
KSM_RELEASE := kube-state-metrics
KSM_CHART := prometheus-community/kube-state-metrics

# === Sample workload image (public, multi-arch) ===
SAMPLE_IMAGE ?= registry.k8s.io/e2e-test-images/agnhost:2.53

# === Tool Check Macro ===
# Usage: $(call require,<tool>,<install hint>)
define require
	@command -v $(1) >/dev/null 2>&1 || (echo "ERROR: '$(1)' is required but not found. $(2)"; exit 1)
endef

# --- Cluster Lifecycle Targets ---
ensure-cluster: ## Create cluster if it doesn't exist, otherwise skip
	$(call require,k3d,See https://k3d.io/#installation)
	$(call require,kubectl,See https://kubernetes.io/docs/tasks/tools/)
	@if ! k3d cluster get $(CLUSTER_NAME) >/dev/null 2>&1; then \
		echo "Creating k3d cluster $(CLUSTER_NAME) using config $(K3D_CONFIG_FILE)..."; \
		K3D_CLUSTER_NAME="$(CLUSTER_NAME)" \
			CONTROL_PLANE_HTTP_PORT="$(CONTROL_PLANE_HTTP_PORT)" \
			CONTROL_PLANE_HTTPS_PORT="$(CONTROL_PLANE_HTTPS_PORT)" \
			CONTROL_PLANE_GRPC_PORT="$(CONTROL_PLANE_GRPC_PORT)" \
			CONTROL_PLANE_GRPC_PROXY_PORT="$(CONTROL_PLANE_GRPC_PROXY_PORT)" \
			CONTROL_PLANE_GRPC_WORKER_PORT="$(CONTROL_PLANE_GRPC_WORKER_PORT)" \
			CONTROL_PLANE_NATS_PORT="$(CONTROL_PLANE_NATS_PORT)" \
			k3d cluster create $(CLUSTER_NAME) --config $(K3D_CONFIG_FILE); \
		echo "OK Cluster $(CLUSTER_NAME) created."; \
	else \
		echo "OK Cluster $(CLUSTER_NAME) already exists. Skipping creation."; \
	fi

ensure-context: ## Verify kubectl context matches the cluster, prompt to switch if not
	@EXPECTED_CTX="k3d-$(CLUSTER_NAME)"; \
	CURRENT_CTX=$$(kubectl config current-context 2>/dev/null || echo ""); \
	if [ "$$CURRENT_CTX" = "$$EXPECTED_CTX" ]; then \
		echo "OK kubectl context is already set to $$EXPECTED_CTX"; \
	else \
		echo "WARN kubectl context is '$$CURRENT_CTX' but expected '$$EXPECTED_CTX'"; \
		printf "  Switch to $$EXPECTED_CTX? [Y/n] "; \
		read -r REPLY; \
		if [ "$$REPLY" = "n" ] || [ "$$REPLY" = "N" ]; then \
			echo "ERROR Aborting - kubectl context must be $$EXPECTED_CTX to continue."; \
			exit 1; \
		fi; \
		k3d kubeconfig merge $(CLUSTER_NAME) --kubeconfig-switch-context > /dev/null 2>&1; \
		kubectl config use-context $$EXPECTED_CTX > /dev/null; \
		echo "OK kubectl context switched to $$EXPECTED_CTX"; \
	fi

start: ensure-docker-config
	@echo "========== START CLUSTER =========="
	@$(MAKE) ensure-cluster
	@echo "Ensuring cluster $(CLUSTER_NAME) is ready..."
	@k3d cluster start $(CLUSTER_NAME) 2>/dev/null || echo "Cluster already running."
	@echo "OK Cluster $(CLUSTER_NAME) is running."
	@$(MAKE) ensure-context

stop:
	@echo "Stopping k3d cluster $(CLUSTER_NAME)..."
	@k3d cluster stop $(CLUSTER_NAME)
	@echo "Cluster $(CLUSTER_NAME) stopped."

destroy:
	@if k3d cluster get $(CLUSTER_NAME) >/dev/null 2>&1; then \
		echo "Destroying k3d cluster $(CLUSTER_NAME)..."; \
		k3d cluster delete $(CLUSTER_NAME); \
		echo "Cluster $(CLUSTER_NAME) destroyed."; \
	else \
		echo "Cluster $(CLUSTER_NAME) absent; skipping."; \
	fi

destroy-all-ncp-local: ## Discover and destroy every ncp-local* k3d cluster on the host
	@command -v jq >/dev/null 2>&1 || { \
		echo "destroy-all-ncp-local requires jq; install it and retry." >&2; \
		exit 1; \
	}
	@set -o pipefail; \
	NAMES=$$(k3d cluster list -o json | jq -r '.[] | select(.name|startswith("ncp-local")) | .name'); \
	if [ -z "$$NAMES" ]; then \
		echo "No ncp-local* clusters present."; \
		exit 0; \
	fi; \
	for name in $$NAMES; do \
		echo "Destroying k3d cluster $$name..."; \
		k3d cluster delete "$$name"; \
	done

status:
	@echo "Checking status of k3d cluster $(CLUSTER_NAME)..."
	@k3d cluster get $(CLUSTER_NAME)

ensure-docker-config: ## Verify secrets/docker-config.json exists for credential-provider mounts
	@if [ -d secrets/docker-config.json ]; then \
		echo "ERROR: secrets/docker-config.json is a directory; remove it and create a Docker config file"; \
		exit 1; \
	fi
	@if [ ! -f secrets/docker-config.json ]; then \
		echo "ERROR: secrets/docker-config.json is required for kubelet credential-provider mounts"; \
		echo "Create it from your Docker credentials, for example:"; \
		echo "  mkdir -p secrets"; \
		echo "  cp $$HOME/.docker/config.json secrets/docker-config.json"; \
		exit 1; \
	fi

validate-compute-clusters: ## Validate compute cluster count/name settings without creating clusters
	@if [ -z "$(strip $(COMPUTE_CLUSTERS))" ]; then \
		case "$(COMPUTE_CLUSTER_COUNT)" in \
			""|*[!0-9]*) echo "ERROR: COMPUTE_CLUSTER_COUNT must be a positive integer"; exit 1 ;; \
		esac; \
		if [ "$(COMPUTE_CLUSTER_COUNT)" -lt 1 ]; then \
			echo "ERROR: COMPUTE_CLUSTER_COUNT must be a positive integer"; \
			exit 1; \
		fi; \
	fi

print-compute-clusters: validate-compute-clusters ## Print resolved compute cluster names without creating clusters
	@clusters="$(strip $(COMPUTE_CLUSTERS))"; \
	if [ -z "$$clusters" ]; then \
		for i in $$(seq 1 "$(COMPUTE_CLUSTER_COUNT)"); do \
			clusters="$$clusters $(COMPUTE_CLUSTER_PREFIX)-$$i"; \
		done; \
	fi; \
	echo "$$clusters" | xargs

start-control-plane: ## Start the local control-plane k3d cluster
	@$(MAKE) CLUSTER_NAME=$(CONTROL_PLANE_CLUSTER_NAME) K3D_CONFIG_FILE=$(CONTROL_PLANE_CONFIG_FILE) start

deploy-control-plane-addons: setup-gateway-api check-gateway-api validate-gateway deploy-control-plane-endpoints ## Deploy control-plane addons only
	@echo "OK Control-plane addons deployed"

deploy-control-plane-endpoints: ## Deploy local control-plane Gateway aliases for compute-plane clusters
	@echo "========== CONTROL-PLANE ENDPOINT ROUTES =========="
	@CONTROL_PLANE_DOMAIN="$(CONTROL_PLANE_DOMAIN)" \
		bash ./scripts/render-control-plane-endpoints.sh | kubectl apply -f -
	@echo "OK Control-plane endpoint routes deployed for $(CONTROL_PLANE_DOMAIN)"

build-and-deploy-control-plane-cluster: clean build start-control-plane deploy-control-plane-addons ## Build and deploy the local control-plane cluster
	@echo "========== COMPLETED =========="
	@echo "OK Control-plane cluster $(CONTROL_PLANE_CLUSTER_NAME) is up and ready."
	@echo "OK Context is k3d-$(CONTROL_PLANE_CLUSTER_NAME)"

destroy-control-plane: ## Destroy the local control-plane cluster
	@$(MAKE) CLUSTER_NAME=$(CONTROL_PLANE_CLUSTER_NAME) destroy

start-compute-plane: ## Start one local compute-plane k3d cluster
	@$(MAKE) CLUSTER_NAME=$(COMPUTE_CLUSTER_NAME) K3D_CONFIG_FILE=$(COMPUTE_PLANE_CONFIG_FILE) start

deploy-compute-plane-addons: deploy-compute-control-plane-endpoints configure-compute-control-plane-dns deploy-csi-smb wait-for-csi-smb deploy-fake-gpu-operator wait-for-fake-gpu-operator validate-deployment cleanup-sample ## Deploy compute-plane addons only
	@echo "OK Compute-plane addons deployed"

configure-compute-control-plane-dns: ## Configure compute-cluster DNS for local control-plane endpoints
	$(call require,kubectl,See https://kubernetes.io/docs/tasks/tools/)
	@CONTROL_PLANE_DOMAIN="$(CONTROL_PLANE_DOMAIN)" \
		bash ./scripts/configure-control-plane-dns.sh

deploy-compute-control-plane-endpoints: ## Deploy compute-cluster aliases for control-plane services
	$(call require,docker,See https://docs.docker.com/get-docker/)
	$(call require,kubectl,See https://kubernetes.io/docs/tasks/tools/)
	@echo "========== COMPUTE CONTROL-PLANE ENDPOINT ALIASES =========="
	@kubectl kustomize ./apps/compute-control-plane-endpoints | kubectl apply -f -
	@# CONTROL_PLANE_LB_HTTP_PORT / CONTROL_PLANE_LB_NATS_PORT are the
	@# load-balancer CONTAINER ports (k3d-config-control-plane.yaml maps
	@# host CONTROL_PLANE_HTTP_PORT -> container 80, host
	@# CONTROL_PLANE_NATS_PORT -> container 4222). Cross-cluster traffic
	@# from the compute cluster reaches the control-plane LB container
	@# over the docker network on the container ports, not the host
	@# ports. Pass the literal container ports; do NOT forward
	@# CONTROL_PLANE_HTTP_PORT / CONTROL_PLANE_GRPC_PORT /
	@# CONTROL_PLANE_NATS_PORT, which would make compute aliases point
	@# at host-port:8080 (refused on the container side).
	@CONTROL_PLANE_DOMAIN="$(CONTROL_PLANE_DOMAIN)" \
		CONTROL_PLANE_CLUSTER_NAME="$(CONTROL_PLANE_CLUSTER_NAME)" \
		CONTROL_PLANE_LB_HTTP_PORT=80 \
		CONTROL_PLANE_LB_GRPC_PORT=9090 \
		CONTROL_PLANE_LB_GRPC_WORKER_PORT=10086 \
		CONTROL_PLANE_LB_NATS_PORT=4222 \
		CLUSTER_NAME="$(COMPUTE_CLUSTER_NAME)" \
		bash ./scripts/configure-control-plane-endpoints.sh
	@echo "OK Compute-plane endpoint aliases deployed for $(CONTROL_PLANE_DOMAIN)"

build-and-deploy-compute-plane-cluster: clean build start-compute-plane deploy-compute-plane-addons ## Build and deploy one local compute-plane cluster
	@echo "========== COMPLETED =========="
	@echo "OK Compute-plane cluster $(COMPUTE_CLUSTER_NAME) is up and ready."
	@echo "OK Context is k3d-$(COMPUTE_CLUSTER_NAME)"

destroy-compute-plane: ## Destroy one local compute-plane cluster
	@$(MAKE) CLUSTER_NAME=$(COMPUTE_CLUSTER_NAME) destroy

build-and-deploy-multicluster: ensure-docker-config validate-compute-clusters clean build ## Build one control-plane cluster and one or more compute-plane clusters
	@echo "========== BUILD AND DEPLOY MULTI-CLUSTER =========="
	@$(MAKE) CLUSTER_NAME=$(CONTROL_PLANE_CLUSTER_NAME) K3D_CONFIG_FILE=$(CONTROL_PLANE_CONFIG_FILE) \
		start deploy-control-plane-addons
	@clusters="$$( $(MAKE) --no-print-directory -s print-compute-clusters )"; \
	for cluster in $$clusters; do \
		echo "========== BUILD AND DEPLOY COMPUTE CLUSTER $$cluster =========="; \
		if ! $(MAKE) COMPUTE_CLUSTER_NAME="$$cluster" start-compute-plane deploy-compute-plane-addons; then \
			echo "ERROR: compute cluster $$cluster failed"; \
			exit 1; \
		fi; \
	done; \
	echo "========== COMPLETED =========="; \
	echo "OK Control-plane context: k3d-$(CONTROL_PLANE_CLUSTER_NAME)"; \
	for cluster in $$clusters; do \
		echo "OK Compute-plane context: k3d-$$cluster"; \
	done

destroy-multicluster: validate-compute-clusters ## Destroy the default control-plane cluster and resolved compute-plane clusters
	@$(MAKE) CLUSTER_NAME=$(CONTROL_PLANE_CLUSTER_NAME) destroy || true
	@clusters="$$( $(MAKE) --no-print-directory -s print-compute-clusters )"; \
	for cluster in $$clusters; do \
		$(MAKE) CLUSTER_NAME="$$cluster" destroy || true; \
	done

test-multicluster-make: ## Run dry Makefile tests for multi-cluster name derivation
	@tests/test-multicluster-make.sh

# --- Load Balancer & Gateway API Setup ---
setup-gateway-api:
	$(call require,kubectl,See https://kubernetes.io/docs/tasks/tools/)
	$(call require,helm,See https://helm.sh/docs/intro/install/)
	@echo "========== SETUP GATEWAY API =========="
	@echo ">>> Setting up Gateway API infrastructure..."
	@bash ./scripts/setup-gateway-api.sh

# Optional MetalLB setup (not required for default k3d setup with klipper)
setup-metallb:
	@echo "MetalLB support removed in this branch"

check-gateway-api:
	@echo ">>> Checking Gateway API infrastructure..."
	@echo -n "  Gateway API CRDs: "
	@if kubectl get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1; then \
		echo "OK Installed"; \
	else \
		echo "ERROR Not found. Run 'make setup-gateway-api'"; \
		exit 1; \
	fi
	@echo -n "  GatewayClass: "
	@if kubectl get gatewayclass eg >/dev/null 2>&1; then \
		echo "OK Available"; \
	else \
		echo "ERROR Not found. Run 'make setup-gateway-api'"; \
		exit 1; \
	fi
	@echo ">>> Gateway API infrastructure is ready!"

# --- Application & Provider Deployment ---
deploy-sample: # Depends on the cluster being up
	@echo "========== SAMPLE WORKLOAD DEPLOYMENT =========="
	@echo "Building and deploying sample application from ./sample directory..."
	@SAMPLE_IMAGE="$(SAMPLE_IMAGE)" \
		bash ./scripts/render-sample.sh | kubectl apply -f -
	@echo "Sample application deployment complete."

validate-deployment: deploy-sample wait-for-deployment ## Deploy public sample and validate the cluster runs it
	@echo "OK Sample deployment is ready"
	@echo ""
	@echo "OK The sample pod is serving HTTP using $(SAMPLE_IMAGE)"
	@echo "OK This confirms that:"
	@echo "   - The cluster can schedule and run a multi-arch sample workload"
	@echo "   - Readiness and liveness probes against the HTTP server are passing"
	@echo ""
	@echo "INFO Deployment Details:"
	@kubectl get deployment/sample -n sample -o wide
	@echo ""
	@kubectl get pods -l app=sample -n sample -o wide

deploy-nginx:
	@echo "Building and deploying nginx application from ./apps/nginx directory..."
	@kubectl kustomize ./apps/nginx | kubectl apply -f -
	@echo "Nginx application deployment complete."

wait-for-nginx: ## Wait for nginx deployment to be ready
	@echo "Waiting for nginx deployment to be ready..."
	@timeout=300; \
	elapsed=0; \
	while [ $$elapsed -lt $$timeout ]; do \
		if kubectl rollout status deployment/nginx -n sample --timeout=10s >/dev/null 2>&1; then \
			echo "OK Nginx deployment is ready!"; \
			break; \
		fi; \
		echo "INFO Nginx not ready yet, waiting... ($$elapsed/$$timeout seconds)"; \
		sleep 5; \
		elapsed=$$((elapsed + 5)); \
	done; \
	if [ $$elapsed -ge $$timeout ]; then \
		echo "ERROR Timeout waiting for nginx deployment"; \
		exit 1; \
	fi

wait-for-deployment: ## Wait for sample deployment to be ready (with polling loop)
	@echo "Waiting for sample deployment to be ready..."
	@timeout=300; \
	elapsed=0; \
	while [ $$elapsed -lt $$timeout ]; do \
		if kubectl rollout status deployment/sample -n sample --timeout=10s >/dev/null 2>&1; then \
			echo "OK Sample deployment is ready!"; \
			break; \
		fi; \
		echo "INFO Deployment not ready yet, waiting... ($$elapsed/$$timeout seconds)"; \
		sleep 5; \
		elapsed=$$((elapsed + 5)); \
	done; \
	if [ $$elapsed -ge $$timeout ]; then \
		echo "ERROR Timeout waiting for deployment to be ready"; \
		exit 1; \
	fi

wait-for-gateway: ## Wait for Gateway to be ready and HTTPRoute to be attached
	@echo "Waiting for Gateway to be ready..."
	@timeout=300; \
	elapsed=0; \
	while [ $$elapsed -lt $$timeout ]; do \
		if kubectl get gateway shared-gw -n envoy-gateway-system -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}' 2>/dev/null | grep -q "True"; then \
			if kubectl get gateway shared-gw -n envoy-gateway-system -o jsonpath='{.status.listeners[0].attachedRoutes}' 2>/dev/null | grep -q "[1-9]"; then \
				echo "OK Gateway is ready and HTTPRoute is attached!"; \
				break; \
			fi; \
		fi; \
		echo "INFO Gateway not ready yet, waiting... ($$elapsed/$$timeout seconds)"; \
		sleep 5; \
		elapsed=$$((elapsed + 5)); \
	done; \
	if [ $$elapsed -ge $$timeout ]; then \
		echo "ERROR Timeout waiting for Gateway to be ready"; \
		kubectl get gateway shared-gw -n envoy-gateway-system -o yaml; \
		exit 1; \
	fi

validate-gateway: deploy-nginx wait-for-nginx wait-for-gateway ## Validate Gateway route from host
	@echo "========== VALIDATE GATEWAY =========="
	@echo ">>> Checking Gateway status..."
	@kubectl get gateway shared-gw -n envoy-gateway-system -o jsonpath='{.status}' | grep -q "Programmed" || (echo "Gateway not programmed"; exit 1)
	@echo ">>> Checking HTTPRoute status..."
	@kubectl get httproute nginx-route -n sample -o jsonpath='{.status}' || echo "HTTPRoute status not available"
	@echo ">>> Validating Gateway route (nginx.localhost)..."
	@if curl -sSf http://nginx.localhost:$(GATEWAY_HTTP_PORT)/ >/dev/null 2>&1; then \
		echo "OK Gateway route reachable at http://nginx.localhost:$(GATEWAY_HTTP_PORT)"; \
	else \
		echo "ERROR Gateway route validation failed"; \
		echo "Debugging info:"; \
		kubectl get gateway shared-gw -n envoy-gateway-system -o yaml | grep -A 10 "status:"; \
		kubectl get httproute nginx-route -n sample -o yaml | grep -A 10 "status:" || true; \
		kubectl get svc -n envoy-gateway-system | grep LoadBalancer; \
		echo "Note: Ensure nginx.localhost resolves to 127.0.0.1 (check /etc/hosts)"; \
		exit 1; \
	fi
	$(MAKE) cleanup-nginx

cleanup-sample: ## Remove the sample deployment and associated resources
	@echo "Cleaning up sample deployment..."
	@kubectl delete deployment/sample -n sample 2>/dev/null || echo "Sample deployment not found (already cleaned up)"
	@echo "OK Sample cleanup complete."

cleanup-nginx: ## Remove the nginx deployment and associated resources
	@echo "Cleaning up nginx deployment..."
	@kubectl delete deployment/nginx -n sample 2>/dev/null || echo "Nginx deployment not found (already cleaned up)"
	@echo "OK Nginx cleanup complete."

# --- CSI Driver SMB Targets ---
build-csi-smb: ## Build CSI SMB driver manifests from remote kustomize sources
	$(call require,kustomize,See https://kubectl.docs.kubernetes.io/installation/kustomize/)
	@echo "========== BUILD CSI SMB =========="
	@echo "Building CSI SMB driver manifests..."
	@mkdir -p $(CSI_SMB_DIR)/static
	@kustomize build $(CSI_SMB_DIR) > $(CSI_SMB_STATIC)
	@echo "OK Static manifests written to $(CSI_SMB_STATIC)"

deploy-csi-smb: ## Deploy CSI SMB driver to the cluster
	@echo "========== DEPLOY CSI SMB =========="
	@echo "Deploying CSI SMB driver..."
	@kubectl apply -f $(CSI_SMB_STATIC)
	@echo "OK CSI SMB driver deployed"

wait-for-csi-smb: ## Wait for CSI SMB pods to be ready
	@echo "Waiting for CSI SMB controller..."
	@kubectl -n kube-system rollout status deployment/csi-smb-controller --timeout=300s
	@echo "OK CSI SMB driver ready"

# --- Fake GPU Operator Targets ---
build-fake-gpu-operator: ## Build fake GPU operator manifests from helm chart
	$(call require,helm,See https://helm.sh/docs/intro/install/)
	@echo "========== BUILD FAKE GPU OPERATOR =========="
	@echo "Building fake GPU operator manifests..."
	@mkdir -p $(FAKE_GPU_DIR)/static
	@helm template gpu-operator oci://ghcr.io/run-ai/fake-gpu-operator/fake-gpu-operator \
		--version $(FAKE_GPU_VERSION) \
		--namespace gpu-operator \
		-f $(FAKE_GPU_DIR)/values.yaml > $(FAKE_GPU_STATIC)
	@echo "OK Static manifests written to $(FAKE_GPU_STATIC)"

deploy-fake-gpu-operator: ## Deploy fake GPU operator to the cluster
	@echo "========== DEPLOY FAKE GPU OPERATOR =========="
	@kubectl delete runtimeclass nvidia --ignore-not-found
	@echo "Deploying fake GPU operator..."
	@kubectl create namespace gpu-operator --dry-run=client -o yaml | kubectl apply -f -
	@kubectl apply -f $(FAKE_GPU_STATIC) -n gpu-operator
	@echo "OK Fake GPU operator deployed"

wait-for-fake-gpu-operator: ## Wait for fake GPU operator pods to be ready
	@echo "Waiting for fake GPU operator deployments..."
	@kubectl -n gpu-operator rollout status deployment/kwok-gpu-device-plugin --timeout=120s
	@kubectl -n gpu-operator rollout status deployment/status-updater --timeout=120s
	@echo "OK Fake GPU operator ready"

# --- Kube Prometheus Stack Targets (CRDs only) ---
deploy-prometheus-crds: ## Deploy prometheus-operator CRDs only
	$(call require,helm,See https://helm.sh/docs/intro/install/)
	$(call require,kubectl,See https://kubernetes.io/docs/tasks/tools/)
	@echo "========== DEPLOY PROMETHEUS OPERATOR CRDS =========="
	@echo "Creating namespace $(KPS_NAMESPACE) if not exists..."
	@kubectl create namespace $(KPS_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "Adding prometheus-community Helm repo..."
	@helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
	@helm repo update
	@echo "Deploying prometheus-operator CRDs only..."
	@helm upgrade --install $(KPS_RELEASE) $(KPS_CHART) \
		--namespace $(KPS_NAMESPACE) \
		--values $(KPS_DIR)/values.yaml \
		--wait --timeout=120s
	@echo "OK Prometheus-operator CRDs deployed"

uninstall-prometheus-crds: ## Uninstall prometheus-operator CRDs
	@echo "========== UNINSTALL PROMETHEUS OPERATOR CRDS =========="
	@echo "Uninstalling prometheus-operator CRDs..."
	@helm uninstall $(KPS_RELEASE) --namespace $(KPS_NAMESPACE) 2>/dev/null || echo "Release not found (already uninstalled)"
	@echo "OK Prometheus-operator CRDs uninstalled"

# --- Kube State Metrics Targets ---
deploy-kube-state-metrics: deploy-prometheus-crds ## Deploy kube-state-metrics to the cluster (depends on prometheus CRDs)
	@echo "========== DEPLOY KUBE STATE METRICS =========="
	@echo "Creating namespace $(KSM_NAMESPACE) if not exists..."
	@kubectl create namespace $(KSM_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "Adding prometheus-community Helm repo..."
	@helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
	@helm repo update
	@echo "Deploying kube-state-metrics..."
	@helm upgrade --install $(KSM_RELEASE) $(KSM_CHART) \
		--namespace $(KSM_NAMESPACE) \
		--values $(KSM_DIR)/values.yaml \
		--wait --timeout=120s
	@echo "OK Kube-state-metrics deployed"

wait-for-kube-state-metrics: ## Wait for kube-state-metrics deployment to be ready
	@echo "Waiting for kube-state-metrics deployment..."
	@kubectl -n $(KSM_NAMESPACE) rollout status deployment/$(KSM_RELEASE) --timeout=120s
	@echo "OK Kube-state-metrics ready"

uninstall-kube-state-metrics: ## Uninstall kube-state-metrics from the cluster
	@echo "========== UNINSTALL KUBE STATE METRICS =========="
	@echo "Uninstalling kube-state-metrics..."
	@helm uninstall $(KSM_RELEASE) --namespace $(KSM_NAMESPACE) 2>/dev/null || echo "Release not found (already uninstalled)"
	@echo "OK Kube-state-metrics uninstalled"

# -- Main composite target --
build-and-deploy-cluster: ensure-docker-config clean build start deploy-csi-smb wait-for-csi-smb deploy-fake-gpu-operator wait-for-fake-gpu-operator setup-gateway-api check-gateway-api validate-gateway validate-deployment cleanup-sample
	@echo "========== COMPLETED =========="
	@echo "Cluster $(CLUSTER_NAME) is up and ready to use."
	@echo "OK Context is k3d-$(CLUSTER_NAME)"

# --- Go Credential Provider Build & Test Targets ---
build:
	$(call require,go,See https://go.dev/doc/install)
	@echo "========== BUILD =========="
	@echo "Building $(BINARY_NAME) for $(GOARCH) into $(PROVIDER_OUTPUT_PATH)..."
	@mkdir -p $(OUTPUT_DIR)
	cd $(GO_PROVIDER_PATH) && CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags='-s -w -extldflags "-static"' -o ../$(PROVIDER_OUTPUT_PATH) ./cmd/$(BINARY_NAME)/main.go

test: ## Run Go tests with coverage summary
	$(call require,go,See https://go.dev/doc/install)
	@echo "Running Go tests with coverage inside $(GO_PROVIDER_PATH)..."
	cd $(GO_PROVIDER_PATH) && go test -race -cover -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

test-coverage-html: ## Generate and show test coverage in HTML
	@echo "Generating HTML test coverage report..."
	@if [ ! -f "$(GO_PROVIDER_PATH)/coverage.out" ]; then \
		$(MAKE) test; \
	fi
	cd $(GO_PROVIDER_PATH) && go tool cover -html=coverage.out -o coverage.html
	@echo "HTML coverage report generated at $(GO_PROVIDER_PATH)/coverage.html"
	@echo "Open with your browser, e.g., 'open $(GO_PROVIDER_PATH)/coverage.html' on macOS."

test-manual: build ## Run the manual test harness script (builds first)
	@echo "Running manual test harness (from $(GO_PROVIDER_PATH)/hack)..."
	cd $(GO_PROVIDER_PATH)/hack && chmod +x ./manual-test.sh && ./manual-test.sh

build-credential-provider-multiarch: ## Build multi-arch credential provider binaries locally without publishing (outputs to bin/)
	$(call require,go,See https://go.dev/doc/install)
	@echo "========== BUILD MULTI-ARCH =========="
	@mkdir -p $(OUTPUT_DIR)
	@echo "Building linux/amd64..."
	@cd $(GO_PROVIDER_PATH) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-ldflags='-s -w -extldflags "-static"' \
		-o ../$(PROVIDER_LINUX_AMD64_OUTPUT_PATH) ./cmd/$(BINARY_NAME)/main.go
	@echo "Building linux/arm64..."
	@cd $(GO_PROVIDER_PATH) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags='-s -w -extldflags "-static"' \
		-o ../$(PROVIDER_LINUX_ARM64_OUTPUT_PATH) ./cmd/$(BINARY_NAME)/main.go
	@echo "OK Binaries:"
	@ls -lh $(PROVIDER_LINUX_AMD64_OUTPUT_PATH) $(PROVIDER_LINUX_ARM64_OUTPUT_PATH)

# Optional release helpers are loaded only when present.
NCP_LOCAL_INTERNAL_RELEASE_MK ?= ../../../nvcf-internal/tools/ncp-local-cluster/release.mk
-include $(NCP_LOCAL_INTERNAL_RELEASE_MK)

# --- Cleanup & Help ---
clean:
	@echo "Cleaning built binaries and coverage files..."
	rm -f $(PROVIDER_OUTPUT_PATH) $(PROVIDER_LINUX_AMD64_OUTPUT_PATH) $(PROVIDER_LINUX_ARM64_OUTPUT_PATH)
	rm -f $(OUTPUT_DIR)/$(BINARY_NAME)_*_linux_* $(OUTPUT_DIR)/$(BINARY_NAME)_*_checksums.txt
	rm -f $(GO_PROVIDER_PATH)/coverage.out $(GO_PROVIDER_PATH)/coverage.html

help:
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-25s %s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

# Default target: help
.DEFAULT_GOAL := help
