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
release ?= worker-callback-proxy
namespace ?= nvcf
helm_dir ?= ./worker-callback-proxy
# Required for install. lint, template and validate fall back to test_values.
values ?=
test_values := ../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
render_values = $(or $(values),$(test_values))

# OPTIONAL: Path to an additional Helm values file.
# Example: make install values=my-values.yaml additional_values=override.yaml
additional_values ?=

# OCI Registry details — set these to your own OCI-compatible registry
OCI_REGISTRY_HOST ?= registry.example.com
OCI_REGISTRY_NAMESPACE ?= your-org/your-namespace

KUBERNETES_VERSION ?= 1.31.5
# cert-manager Certificate and Prometheus Operator ServiceMonitor schemas,
# generated from the CRD versions NVCF ships; see tests/crd-schemas/PROVENANCE.md.
CRD_SCHEMA_DIR := $(CURDIR)/tests/crd-schemas
CRD_SCHEMA_LOCATION ?= $(CRD_SCHEMA_DIR)/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json

# CHART_NAME is the chart's Chart.yaml name, including any OCI prefix.
CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install uninstall status lint template validate validate-envoy crd-schemas verify-crd-schemas test clean package push-oci

install:
ifndef values
	$(error "values" is not set. Use 'make install values=<path-to-values.yaml>')
endif
	@echo "Installing $(release) to namespace $(namespace) using values file '$(values)'..."
	helm install $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(values) \
		$(if $(additional_values),--values $(additional_values),) \
		--atomic \
		--create-namespace \
		--wait \
		--timeout 20m

uninstall:
	helm uninstall $(release) --namespace $(namespace)

status:
	helm status $(release) --namespace $(namespace)

lint:
	helm lint $(helm_dir) \
		--values $(render_values) \
		$(if $(additional_values),--values $(additional_values),)

template:
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(render_values) \
		$(if $(additional_values),--values $(additional_values),) \
		--set workerCallbackProxy.metrics.serviceMonitor.enabled=true \
		> bin/manifest.yaml
	@echo "Rendered manifest to bin/manifest.yaml"

# Validates every rendered kind, including the Certificate and ServiceMonitor.
validate: template
	kubeconform -strict -summary -output pretty \
		-kubernetes-version $(KUBERNETES_VERSION) \
		-schema-location default \
		-schema-location '$(CRD_SCHEMA_LOCATION)' \
		bin/manifest.yaml

# Regenerates the CRD schemas (needs Docker and network access).
crd-schemas:
	bash tests/crd-schemas/generate.sh

# Fails if the committed CRD schemas differ from a fresh generation.
verify-crd-schemas:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	bash tests/crd-schemas/generate.sh "$$tmp" && \
	diff -r --exclude=generate.sh "$(CRD_SCHEMA_DIR)" "$$tmp" && \
	echo "CRD schemas match their sources"

# Loads the rendered Envoy config into the chart's Envoy image (needs Docker).
validate-envoy:
	bash tests/chart-render/validate-envoy.sh

test: lint
	bash tests/chart-render/verify-render.sh

clean:
	rm -rf ./packaged-charts bin

package: clean lint
	@mkdir -p ./packaged-charts
	helm package $(helm_dir) -d ./packaged-charts/
	@echo "Packaged chart to ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz"

push-oci:
	@if [ ! -f ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz ]; then \
		echo "Packaged chart ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz not found. Run 'make package' first."; \
		exit 1; \
	fi
	helm push ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz oci://$(OCI_REGISTRY_HOST)/$(OCI_REGISTRY_NAMESPACE)
	@rm -rf ./packaged-charts
