# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Variables
release ?= pylon-operator
namespace ?= pylon-operator
helm_dir ?= ./pylon-operator
repo_root := $(abspath $(CURDIR)/../../..)

# The chart has required values with no default. lint, template and validate
# render with the CI values file; values adds your own overrides on top.
ci_values ?= $(repo_root)/tools/ci/helm-validate-values/pylon-operator.yaml
values ?=
additional_values ?=

OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>

CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install upgrade uninstall status lint template validate test sync-crd check-crd clean package push-oci

install:
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

upgrade:
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

lint:
	helm lint $(helm_dir) \
		--values $(ci_values) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),)

template:
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		--values $(ci_values) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),) \
		> bin/manifest.yaml
	@echo "Rendered manifest to bin/manifest.yaml"

# kubeconform is optional locally; CI validation runs through
# tools/ci/check-helm-charts.
validate: template
	@if command -v kubeconform >/dev/null 2>&1; then \
		kubeconform -strict -summary -output pretty -kubernetes-version 1.31.5 -ignore-missing-schemas bin/manifest.yaml; \
	else \
		echo "kubeconform not found on PATH; skipping schema validation of bin/manifest.yaml"; \
	fi

test:
	bash ./scripts/check-render.sh $(helm_dir)

# The CRD and ClusterRole templates are copies of the operator's generated
# config/crd/bases and config/rbac/role.yaml. sync-crd rewrites them;
# check-crd fails when they have drifted from the operator module.
sync-crd:
	bash ./scripts/sync-crd.sh

check-crd:
	bash ./scripts/sync-crd.sh --check

clean:
	rm -rf ./packaged-charts bin

package: clean lint
	@mkdir -p ./packaged-charts
	helm package $(helm_dir) -d ./packaged-charts/

push-oci:
	@if [ ! -f ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz ]; then \
		echo "Packaged chart ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz not found. Run 'make package' first."; \
		exit 1; \
	fi
	helm push ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz oci://$(OCI_REGISTRY_HOST)/$(OCI_REGISTRY_NAMESPACE)
