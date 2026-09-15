# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

.PHONY: all lint deps build template test test-render install uninstall clean ensure-helmignore package push-oci bump-version release

# Variables
CHART_DIR := .
CHARTS_SUBDIR := $(CHART_DIR)/charts # Subcharts are downloaded here

# Helm release configuration
RELEASE_NAME ?= nvcf-nats
NAMESPACE ?= nats-system
VALUES_FILE := $(CHART_DIR)/values.yaml

# Optional: specify additional values files, e.g., make install additional_values=my-custom-values.yaml
ADDITIONAL_VALUES ?=
ADDITIONAL_VALUES_CMD := $(if $(ADDITIONAL_VALUES),--values $(ADDITIONAL_VALUES),)

# OCI Registry details
OCI_REGISTRY_HOST ?=
OCI_REGISTRY_NAMESPACE ?=

# Automatically determine chart name and version from Chart.yaml
# IMPORTANT: For this setup, CHART_NAME is expected to include any desired OCI prefix (e.g., "helm-yourchart")
# as defined in helm/Chart.yaml's 'name' field.
CHART_NAME := $(shell yq -r .name $(CHART_DIR)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(CHART_DIR)/Chart.yaml)

# Variable for the new version, to be passed as an argument to make
# Example: make bump-version NEW_VERSION=0.2.0
NEW_VERSION ?=

# Default target
all: install

lint:
	helm lint $(CHART_DIR)

deps:
	@echo "Building dependencies for chart in $(CHART_DIR)..."
	helm dependency build $(CHART_DIR)

# The build target might be used if you have pre-packaging steps for your scripts or other assets.
# For now, it ensures the scripts directory exists. The generate-nkey.sh is assumed to be in place.
build: deps
	@echo "[build] nothing to do"
	
template: build
	@echo "Templating chart $(CHART_DIR) for release $(RELEASE_NAME) in namespace $(NAMESPACE)..."
	helm template $(RELEASE_NAME) $(CHART_DIR) --namespace $(NAMESPACE) --values $(VALUES_FILE) $(ADDITIONAL_VALUES_CMD)

test: lint test-render

test-render: deps
	@echo "[test-render] Verifying rendered auth-callout secret wiring..."
	@CHART_DIR=$(CHART_DIR) RELEASE_NAME=$(RELEASE_NAME) NAMESPACE=$(NAMESPACE) sh ./scripts/test-render-auth-callout.sh

install: build
	@echo "Installing chart $(CHART_DIR) as release $(RELEASE_NAME) in namespace $(NAMESPACE)..."
	helm install $(RELEASE_NAME) $(CHART_DIR) \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--values $(VALUES_FILE) \
		$(ADDITIONAL_VALUES_CMD) \
		--atomic \
		--wait \
		--wait-for-jobs \
		--timeout 20m

uninstall:
	@echo "Uninstalling release $(RELEASE_NAME) from namespace $(NAMESPACE)..."
	helm uninstall $(RELEASE_NAME) --namespace $(NAMESPACE) || true
	# Consider if deleting the namespace is always desired. It can be disruptive.
	# kubectl delete ns $(NAMESPACE)

clean:
	@echo "Cleaning up downloaded chart dependencies..."
	rm -rf $(CHARTS_SUBDIR)/*.tgz
	rm -f helm-nvcf-nats-*.tgz # Remove packaged chart
	
# Ensure .helmignore is set up to exclude dependencies and secrets
ensure-helmignore:
	# Do not ignore the charts/ directory itself, as helm package needs to access
	# dependencies placed there by 'helm dependency build'.
	@echo "secrets/" > $(CHART_DIR)/.helmignore
	@echo ".DS_Store" >> $(CHART_DIR)/.helmignore
	@echo "ci/" >> $(CHART_DIR)/.helmignore
	@echo ".git/" >> $(CHART_DIR)/.helmignore
	@echo "[ensure-helmignore] .helmignore configured in $(CHART_DIR)"

package: deps ensure-helmignore
	@echo "[package] Packaging Helm chart $(CHART_NAME) version $(CHART_VERSION)..."
	@mkdir -p ./packaged-charts
	@helm package $(CHART_DIR) -d ./packaged-charts/
	@echo "[package] Packaged chart to ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz"

push-oci:
ifndef OCI_REGISTRY_HOST
	$(error OCI_REGISTRY_HOST is not set. Usage: make push-oci OCI_REGISTRY_HOST=example.com OCI_REGISTRY_NAMESPACE=charts)
endif
ifndef OCI_REGISTRY_NAMESPACE
	$(error OCI_REGISTRY_NAMESPACE is not set. Usage: make push-oci OCI_REGISTRY_HOST=example.com OCI_REGISTRY_NAMESPACE=charts)
endif
	@echo "[push-oci] Pushing chart $(CHART_NAME) version $(CHART_VERSION) to oci://$(OCI_REGISTRY_HOST)/$(OCI_REGISTRY_NAMESPACE)/$(CHART_NAME):$(CHART_VERSION)"
	@echo "[push-oci] Note: You must be logged into oci://$(OCI_REGISTRY_HOST) for the push to succeed."
	@if [ ! -f ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz ]; then \
		echo "[push-oci] Error: Packaged chart ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz not found. Run 'make package' first."; \
		exit 1; \
	fi
	@helm push ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz oci://$(OCI_REGISTRY_HOST)/$(OCI_REGISTRY_NAMESPACE)
	@echo "[push-oci] Successfully pushed chart to OCI registry."
	@echo "[push-oci] Cleaning up temporary package directory..."
	@rm -rf ./packaged-charts
	@echo "[push-oci] Cleanup complete."

bump-version:
ifndef NEW_VERSION
	$(error NEW_VERSION is not set. Usage: make bump-version NEW_VERSION=x.y.z)
endif
	@echo "[bump-version] Current chart version in $(CHART_DIR)/Chart.yaml (name: $(CHART_NAME)): $(CHART_VERSION)"
	@echo "[bump-version] Bumping chart version to: $(NEW_VERSION)"
	@yq e '.version = "$(NEW_VERSION)"' -i $(CHART_DIR)/Chart.yaml
	$(eval CHART_VERSION := $(NEW_VERSION)) # Update CHART_VERSION for subsequent messages
	@echo "[bump-version] Successfully bumped chart version in $(CHART_DIR)/Chart.yaml to $(CHART_VERSION)"
	@echo "[bump-version] You might want to commit this change."

release: bump-version package push-oci
	@echo "[release] Successfully released chart $(CHART_NAME) version $(CHART_VERSION) to OCI registry."
	@echo "[release] You might want to commit this change."
