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

# Variables
release ?= grpc-proxy
namespace ?= nvcf
helm_dir ?= ./grpc-proxy
values := $(helm_dir)/values.yaml

# OPTIONAL for deploy target: Path to an additional Helm values file.
# Example: make deploy values=my-values.yaml additional_values=override.yaml
additional_values ?= 

# OCI Registry details — set these to your own OCI-compatible registry
OCI_REGISTRY_HOST ?= registry.example.com
OCI_REGISTRY_NAMESPACE ?= your-org/your-namespace

# Automatically determine chart name and version from Chart.yaml
# IMPORTANT: For this setup, CHART_NAME is expected to include any desired OCI prefix (e.g., "helm-yourchart")
# as defined in helm/Chart.yaml's 'name' field.
CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: deploy delete status lint template

install:
ifndef values
	$(error "values" variable is not set. Please specify with 'make deploy values=<path-to-your-values.yaml>')
endif
	@echo "Deploying $(release) to namespace $(namespace) using values file '$(values)'..."
	@echo "Additional values file: '$(if $(additional_values),$(additional_values),N/A)'"
	helm install $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(values) \
		$(if $(additional_values),--values $(additional_values),) \
		--atomic \
		--create-namespace \
		--wait \
		--wait-for-jobs \
		--timeout 20m

uninstall:
	@echo "Deleting $(release) from namespace $(namespace)..."
	helm uninstall $(release) --namespace $(namespace)

status:
	@echo "Checking status of $(release) in namespace $(namespace)..."
	helm status $(release) --namespace $(namespace)

lint:
	@echo "Linting chart $(helm_dir)..."
	helm lint $(helm_dir) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),)

template:
	@echo "Templating chart $(helm_dir)..."
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),) \
		> bin/manifest.yaml
	@echo "Rendered manifest to bin/manifest.yaml"

validate: template
	@echo "Validating manifest with kubeconform..."
	@kubeconform -strict -summary -output pretty -kubernetes-version 1.31.5 bin/manifest.yaml

# Publish Chart
# NOTE: this is manual until the CI pipeline is updated to push the chart to the NVCR OCI registry
clean:
	rm -rf $(charts_dir)/*.tgz
	rm -rf ./packaged-charts

package: clean lint
	@echo "[package] Packaging Helm chart $(CHART_NAME) version $(CHART_VERSION)..."
	@mkdir -p ./packaged-charts
	@helm package $(helm_dir) -d ./packaged-charts/
	@echo "[package] Packaged chart to ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz"

push-oci:
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
