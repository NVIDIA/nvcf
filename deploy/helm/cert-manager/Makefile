.PHONY: all deps lint template install uninstall status package

CHART_DIR := .
RELEASE_NAME ?= cert-manager
NAMESPACE ?= cert-manager
VALUES_FILE := $(CHART_DIR)/values.yaml
CHART_NAME := helm-nvcf-cert-manager
CHART_VERSION := 0.1.0

all: install

deps:
	helm dependency build $(CHART_DIR)

lint: deps
	helm lint $(CHART_DIR)

template: deps
	helm template $(RELEASE_NAME) $(CHART_DIR) \
		--namespace $(NAMESPACE) \
		--values $(VALUES_FILE)

install: deps
	helm upgrade --install $(RELEASE_NAME) $(CHART_DIR) \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--values $(VALUES_FILE) \
		--atomic \
		--wait \
		--timeout 20m

uninstall:
	helm uninstall $(RELEASE_NAME) --namespace $(NAMESPACE) || true

status:
	kubectl get namespace $(NAMESPACE)
	kubectl -n $(NAMESPACE) get pods
	kubectl get crd -o name | rg 'cert-manager\.io'

package: deps
	mkdir -p packaged-charts
	helm package $(CHART_DIR) -d ./packaged-charts/
	@echo ./packaged-charts/$(CHART_NAME)-$(CHART_VERSION).tgz
