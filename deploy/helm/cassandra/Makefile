.PHONY: all deps build install upgrade uninstall clean ensure-helmignore package push-oci bump-version release prepare-local template

# Directory variables
helm_dir := helm
charts_dir := $(helm_dir)/charts

# Chart and namespace
release_name := cassandra
namespace := cassandra-system
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

all: install

deps:
	@if ! ls $(charts_dir)/*.tgz 1>/dev/null 2>&1; then \
		helm dependency build $(helm_dir); \
	else \
		echo "[deps] helm dependencies already built, skipping"; \
	fi

build: deps
	@echo "[build] nothing to do"

install: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	helm install $(release_name) $(helm_dir) -n $(namespace) --values $(values) $(if $(additional_values),--values $(additional_values),) --atomic --create-namespace --wait --wait-for-jobs --timeout 20m --set global.security.allowInsecureImages=true

install-local: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	helm upgrade --install $(release_name) $(helm_dir) -n $(namespace) --values $(values) --values ./values.local.yml $(if $(atomic),--atomic,) --create-namespace --wait --wait-for-jobs --timeout 20m --set global.security.allowInsecureImages=true

debug-local: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	helm install --debug --dry-run $(release_name) $(helm_dir) -n $(namespace) --values $(values) --values ./values.local.yml $(if $(atomic),--atomic,) --create-namespace --wait --wait-for-jobs --timeout 20m --set global.security.allowInsecureImages=true

template: build
	helm template $(release_name) $(helm_dir) -n $(namespace) --values $(values) $(if $(additional_values),--values $(additional_values),)

upgrade: build
	@cd helm/secrets && kustomize edit set namespace ${namespace} && kustomize build . | kubectl apply -f -
	helm upgrade $(release_name) $(helm_dir) -n $(namespace) --values $(values) $(if $(additional_values),--values $(additional_values),) --atomic --wait --wait-for-jobs --timeout 20m --force

uninstall:
	helm uninstall $(release_name) -n $(namespace) || true
	kubectl delete ns ${namespace}

clean:
	rm -rf $(charts_dir)/*.tgz
	rm -rf ./packaged-charts

# Ensure .helmignore is set up to exclude dependencies and secrets
ensure-helmignore:
	# Do not ignore the charts/ directory itself, as helm package needs to access
	# dependencies placed there by 'helm dependency build'.
	@echo "secrets/" > $(helm_dir)/.helmignore
	@echo ".DS_Store" >> $(helm_dir)/.helmignore
	@echo "ci/" >> $(helm_dir)/.helmignore
	@echo "[ensure-helmignore] .helmignore configured in $(helm_dir)"

package: deps ensure-helmignore
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
