.PHONY: all deps build install uninstall clean package push-oci bump-version release

# Directory variables
helm_dir := helm
charts_dir := $(helm_dir)/charts

# Chart and namespace
release := openbao-server
namespace := vault-system
values := $(helm_dir)/values.yaml

# Optional additional values files
additional_values ?=

# OCI Registry details
OCI_REGISTRY_HOST ?= <your-registry>
OCI_REGISTRY_NAMESPACE ?= <your-org>


# Automatically determine chart name and version from Chart.yaml
# IMPORTANT: For this setup, CHART_NAME is expected to include any desired OCI prefix (e.g., "helm-yourchart")
# as defined in helm/Chart.yaml's 'name' field.
CHART_NAME := $(shell yq -r .name $(helm_dir)/Chart.yaml)
CHART_VERSION := $(shell yq -r .version $(helm_dir)/Chart.yaml)

# Variable for the new version, to be passed as an argument to make
# Example: make bump-version NEW_VERSION=0.2.0
NEW_VERSION ?=

# used for local dev on k3d based clusters
K3D_CLUSTER_NAME ?= ncp-local

all: install

deps:
	@if ! ls $(charts_dir)/*.tgz 1>/dev/null 2>&1; then \
		helm dependency build $(helm_dir); \
	fi

build: deps

install: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	helm install $(release) $(helm_dir) -n $(namespace) --values $(values) $(if $(additional_values),--values $(additional_values),) --rollback-on-failure --create-namespace --wait --wait-for-jobs --timeout 20m

install-local: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	@helm install $(release) $(helm_dir) -n $(namespace) --values $(values) --values ./values.local.yaml $(if $(additional_values),--values $(additional_values),) $(if $(atomic),--atomic,) --create-namespace --wait --wait-for-jobs --timeout 20m

uninstall:
	@helm uninstall $(release) -n $(namespace) 2>/dev/null || true
	@kubectl delete ns ${namespace} --ignore-not-found

# Publish Chart
clean:
	rm -rf $(charts_dir)/*.tgz
	rm -rf ./packaged-charts

# Ensure .helmignore is set up to exclude dependencies and secrets
ensure-helmignore:
	@echo "secrets/" > $(helm_dir)/.helmignore
	@echo ".DS_Store" >> $(helm_dir)/.helmignore
	@echo "ci/" >> $(helm_dir)/.helmignore
	@echo "[ensure-helmignore] .helmignore configured in $(helm_dir)"

package: build ensure-helmignore
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

bump-version:
ifndef NEW_VERSION
	$(error NEW_VERSION is not set. Usage: make bump-version NEW_VERSION=x.y.z)
endif
	@echo "[bump-version] Current chart version in $(helm_dir)/Chart.yaml (name: $(CHART_NAME)): $(CHART_VERSION)"
	@echo "[bump-version] Bumping chart version to: $(NEW_VERSION)"
	@yq e '.version = "$(NEW_VERSION)"' -i $(helm_dir)/Chart.yaml
	$(eval CHART_VERSION := $(NEW_VERSION)) # Update CHART_VERSION for subsequent messages
	@echo "[bump-version] Successfully bumped chart version in $(helm_dir)/Chart.yaml to $(CHART_VERSION)"
	@echo "[bump-version] You might want to commit this change."

release: bump-version package push-oci
	@echo "[release] Successfully released chart $(CHART_NAME) version $(CHART_VERSION) to OCI registry."
	@echo "[release] You might want to commit this change."

DOCKER_CONFIG_JSON := ./helm/secrets/.dockerconfigjson
prepare-local: deps
	@echo "[prepare-local] Preparing local environment"
	@if [ -f ${DOCKER_CONFIG_JSON} ]; then \
		echo "[prepare-local] ${DOCKER_CONFIG_JSON} file exists, skipping creation"; \
	else \
		echo "[prepare-local] ${DOCKER_CONFIG_JSON} does not exist, creating it. please update the file with your auth token for nvcr.io"; \
		echo '{"auths":{"nvcr.io":{"auth":"<PUT_YOUR_AUTH_HERE>"}}}' > ${DOCKER_CONFIG_JSON}; \
	fi
	@echo "[prepare-local] Successfully prepared local environment"

