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

# Variables
release ?= global-discovery-mcs-controller
namespace ?= gds-system
helm_dir ?= ./global-discovery-mcs-controller
repo_root := $(abspath $(CURDIR)/../../..)

# The chart has required values with no default. lint, template and validate
# render with the CI values file; values adds your own overrides on top.
ci_values ?= $(repo_root)/tools/ci/helm-validate-values/global-discovery-mcs-controller.yaml
values ?=
additional_values ?=

OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>

CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install upgrade uninstall status lint template validate test sync-crds check-crds clean package push-oci

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

# check-render.sh also compares templates/crds with the module through
# sync-crds.sh --check; without Go it compares the mcs-api version only.
test:
	bash ./scripts/check-render.sh $(helm_dir)

# templates/crds are copies of the CRDs the global-discovery module embeds:
# ServiceExport and ServiceImport from sigs.k8s.io/mcs-api (its go.mod
# version) and config/crd/bases/about.k8s.io_clusterproperties.yaml.
# sync-crds rewrites them; check-crds fails when they drifted. Both need Go.
sync-crds:
	bash ./scripts/sync-crds.sh

check-crds:
	bash ./scripts/sync-crds.sh --check --strict

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
