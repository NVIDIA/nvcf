# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Variables
release ?= llm-gateway-stack
namespace ?= llm-gateway
helm_dir ?= ./llm-gateway-stack
repo_root := $(abspath $(CURDIR)/../../..)

ci_values ?= $(repo_root)/tools/ci/helm-validate-values/llm-gateway-stack.yaml
values ?=
additional_values ?=

OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>

CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install upgrade uninstall status dependency-build dependency-update lint template validate test clean package push-oci

# The subcharts are file:// dependencies on the sibling chart sources. helm
# dependency build packages them into $(helm_dir)/charts from Chart.lock, so
# every target that renders rebuilds them first and never uses stale copies.
dependency-build:
	helm dependency build --skip-refresh $(helm_dir)

# Regenerate Chart.lock after changing the dependencies in Chart.yaml.
dependency-update:
	helm dependency update $(helm_dir)

install: dependency-build
ifndef values
	$(error "values" variable is not set. Please specify with 'make install values=<path-to-values.yaml>')
endif
	helm install $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(values) \
		$(if $(additional_values),--values $(additional_values),) \
		--atomic \
		--create-namespace \
		--wait \
		--timeout 10m

upgrade: dependency-build
ifndef values
	$(error "values" variable is not set. Please specify with 'make upgrade values=<path-to-values.yaml>')
endif
	helm upgrade $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(values) \
		$(if $(additional_values),--values $(additional_values),) \
		--atomic \
		--wait \
		--timeout 10m

uninstall:
	helm uninstall $(release) --namespace $(namespace)

status:
	helm status $(release) --namespace $(namespace)

lint: dependency-build
	helm lint $(helm_dir) \
		--values $(ci_values) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),)

template: dependency-build
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(ci_values) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),) \
		> bin/manifest.yaml
	@echo "Rendered manifest to bin/manifest.yaml"

validate: template
	@if command -v kubeconform >/dev/null 2>&1; then \
		kubeconform -strict -summary -output pretty -kubernetes-version 1.31.5 -ignore-missing-schemas bin/manifest.yaml; \
	else \
		echo "kubeconform not found on PATH; skipping schema validation of bin/manifest.yaml"; \
	fi

test: dependency-build
	bash ./scripts/check-render.sh $(helm_dir)

clean:
	rm -rf ./packaged-charts bin $(helm_dir)/charts

package: clean lint
	@mkdir -p ./packaged-charts
	helm package $(helm_dir) -d ./packaged-charts/

push-oci:
	@if [ ! -f ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz ]; then \
		echo "Packaged chart ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz not found. Run 'make package' first."; \
		exit 1; \
	fi
	helm push ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz oci://$(OCI_REGISTRY_HOST)/$(OCI_REGISTRY_NAMESPACE)
