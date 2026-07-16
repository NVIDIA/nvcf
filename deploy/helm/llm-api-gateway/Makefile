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
release ?= llm-api-gateway
namespace ?= nvcf
helm_dir ?= ./llm-api-gateway
values := $(helm_dir)/values.yaml

# OPTIONAL for deploy target: Path to an additional Helm values file.
additional_values ?=

# OCI Registry details
OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>

# Automatically determine chart name and version from Chart.yaml
CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

.PHONY: install uninstall status lint template validate clean package push-oci

install:
ifndef values
	$(error "values" variable is not set. Please specify with 'make install values=<path-to-values.yaml>'")
endif
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
	helm uninstall $(release) --namespace $(namespace)

status:
	helm status $(release) --namespace $(namespace)

lint:
	helm lint $(helm_dir) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),)

template:
	@mkdir -p bin
	helm template $(release) $(helm_dir) \
		--namespace $(namespace) \
		$(if $(values),--values $(values),) \
		$(if $(additional_values),--values $(additional_values),) \
		> bin/manifest.yaml

validate: template
	kubeconform -strict -summary -output pretty -kubernetes-version 1.31.5 bin/manifest.yaml

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
