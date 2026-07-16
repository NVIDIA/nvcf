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
release ?= nvca-operator
namespace ?= nvca-operator
helm_dir ?= ./nvca-operator
values := $(helm_dir)/values.yaml
stack_repo ?= ../../nvcf-self-managed-stack
stack_env ?= local
stack_env_file ?= $(stack_repo)/environments/$(stack_env).yaml
generated_values ?= ./bin/nvca-operator-values.$(stack_env).yaml

# Load environment variables from .env file if it exists
-include .env

# OPTIONAL for install targets: Path to an additional Helm values file.
# Example: make install values=my-values.yaml additional_values=override.yaml
additional_values ?= 
self_managed_endpoint_values ?= \
	--set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
	--set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
	--set-string selfManaged.natsURL=nats://nats.example.invalid:4222

# OCI Registry details
OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>

# Automatically determine chart name and version from Chart.yaml
# IMPORTANT: For this setup, CHART_NAME is expected to include any desired OCI prefix (e.g., "helm-yourchart")
# as defined in helm/Chart.yaml's 'name' field.
CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install uninstall status lint template validate clean package push-oci sync-chart check-synced-chart render-values-from-stack install-from-stack test-render-values test-build-release-assets test-release-image-manifest test-release-artifact-permissions test-package-release-assets test-attach-release-assets test-release-sbom-wrapper test-self-managed-nvca-image-reference

install:
ifndef values
		$(error "values" variable is not set. Please specify with 'make install values=<path-to-your-values.yaml>')
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

render-values-from-stack:
	@echo "Rendering $(generated_values) from $(stack_env_file)..."
	@mkdir -p "$(dir $(generated_values))"
	@STACK_ENV_FILE="$(stack_env_file)" \
	OUTPUT_FILE="$(generated_values)" \
	RELEASE="$(release)" \
	NAMESPACE="$(namespace)" \
	NCA_ID="$(NCA_ID)" \
	CLUSTER_NAME="$(CLUSTER_NAME)" \
	CLUSTER_ID="$(CLUSTER_ID)" \
	IMAGE_PULL_SECRET_NAME="$(IMAGE_PULL_SECRET_NAME)" \
	./scripts/render_values_from_stack_env.sh
	@echo "Rendered values to $(generated_values)"

install-from-stack: render-values-from-stack
	@echo "Deploying $(release) to namespace $(namespace) using generated values '$(generated_values)'..."
	@echo "Additional values file: '$(if $(additional_values),$(additional_values),N/A)'"
	helm upgrade --install $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(generated_values) \
		$(if $(additional_values),--values $(additional_values),) \
		--atomic \
		--create-namespace \
		--wait \
		--wait-for-jobs \
		--timeout 20m

test-render-values:
	@bash ./tests/render_values_from_stack_env_test.sh

test-build-release-assets:
	@bash ./tests/build_release_assets_test.sh

test-release-image-manifest:
	@bash ./tests/release_image_manifest_test.sh

test-release-artifact-permissions:
	@bash ./tests/release_asset_permissions_test.sh

test-package-release-assets:
	@bash ./tests/package_release_assets_test.sh

test-attach-release-assets:
	@bash ./tests/attach_release_assets_test.sh

test-release-sbom-wrapper:
	@bash ./tests/generate_release_sbom_test.sh

test-self-managed-nvca-image-reference:
	@bash ./tests/self_managed_nvca_image_reference_test.sh

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
		$(if $(additional_values),--values $(additional_values),) \
		$(self_managed_endpoint_values)

template:
	@echo "Templating chart $(helm_dir)..."
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),) \
		$(self_managed_endpoint_values) \
		> bin/manifest.yaml
	@echo "Rendered manifest to bin/manifest.yaml"

validate: template
	@echo "Validating manifest with kubeconform..."
	@kubeconform -strict -summary -output pretty -kubernetes-version 1.31.5 -ignore-missing-schemas bin/manifest.yaml

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

# Sync NVCA Operator chart from monorepo source chart
vendor-chart:
	@echo "🚀 [vendor-chart] Vendor NVCA Operator chart from local monorepo source..."
	@./scripts/ci_vendor_nvca_operator_chart
	@echo "🎉 [vendor-chart] Vendor complete. Deployments available in $(helm_dir)/"

check-vendor-chart: vendor-chart
	@echo "🚀 [check-vendor-chart] Checking if chart is synced..."
	@if ! git diff --ignore-space-at-eol --exit-code -- .; then \
		echo ""; \
		echo "❌ [check-vendor-chart] Chart is NOT synced with source chart!"; \
		echo ""; \
		echo "📋 To fix this issue:"; \
		echo "   1. Run: make vendor-chart"; \
		echo "   2. Review and commit the changes"; \
		echo "   3. Update your merge request"; \
		echo ""; \
		echo "💡 This ensures your deployment matches the monorepo source NVCA Operator chart."; \
		exit 1; \
	fi
	@echo "🎉 [check-vendor-chart] Chart is synced."
