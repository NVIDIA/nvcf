# =============================================================================
# Makefile.dist - Distribution-Safe Helmfile Operations
# =============================================================================
# This file contains targets suitable for customer distribution.
# It can be used standalone or included by a development Makefile.
#
# Hook Variables (set before including this file to extend behavior):
#   INSTALL_PRE_HOOKS - Prerequisites to run before install
#   APPLY_PRE_HOOKS   - Prerequisites to run before apply
# =============================================================================

# --- Configuration ---
# These variables can be overridden from the command line.
# Example: make template HELMFILE_ENV=myenv

# Helmfile environment to use (maps to environments/<env>.yaml)
HELMFILE_ENV  ?= default

# Determine the absolute path of the directory containing this Makefile.
MAKEFILE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

# Directory where templated manifests will be stored
OUTPUT_DIR    ?= $(MAKEFILE_DIR)/out

# Version requirements
# IMPORTANT: Helmfile v1.2.0+ changed helmfile.d/ processing to parallel mode which breaks
# implicit alphabetical file ordering. Use helmfile v1.1.x to ensure proper deployment order.
# Helm 4 is not supported. Use Helm v3.x.
MIN_HELMFILE_VERSION := 1.1.0
REQUIRED_HELMFILE_MAJOR := 1
REQUIRED_HELMFILE_MINOR := 1
REQUIRED_HELM_MAJOR := 3

# Namespaces created by the NVCF stack (used by destroy target)
NAMESPACES := cassandra-system nats-system vault-system ncp nvcf api-keys ess sis

# Optional kubeconfig file - only used if defined
KUBECONFIG_FILE ?=
KUBECONFIG_FLAG = $(if $(KUBECONFIG_FILE),--kubeconfig $(KUBECONFIG_FILE))

# PATH and HELM_PLUGINS override for development mode (when using local binaries)
# In development, prepend bin/ to PATH so helmfile uses local helm,
# and set HELM_PLUGINS so helm uses local plugins directory
ifdef DEV_MODE
PATH_OVERRIDE = PATH="$(MAKEFILE_DIR)/bin:$$PATH" HELM_PLUGINS="$(MAKEFILE_DIR)/bin/helm-plugins"
else
PATH_OVERRIDE =
endif

# --- Hook Variables ---
# These can be set before including this file to add prerequisites to targets
INSTALL_PRE_HOOKS ?=
APPLY_PRE_HOOKS ?=

# --- Targets ---
.PHONY: check-versions template install sync apply destroy clean help
.DEFAULT_GOAL := help

# --- Prerequisite Checks ---
check-versions:
ifdef DEV_MODE
	@echo ">>> Development mode: Using pinned binaries (skipping system version checks)"
else
	@echo ">>> Checking tool versions..."
	@echo ">>> Checking Helmfile version (required v$(REQUIRED_HELMFILE_MAJOR).$(REQUIRED_HELMFILE_MINOR).x)..."
	@HELMFILE_CMD_VERSION=$$(helmfile version --short 2>/dev/null || helmfile version 2>/dev/null); \
	if [ -z "$$HELMFILE_CMD_VERSION" ]; then \
		echo "Error: 'helmfile' command not found."; \
		echo "Please install helmfile v1.1.x from: https://github.com/helmfile/helmfile/releases"; \
		echo ""; \
		echo "IMPORTANT: Do not use helmfile v1.2.0+ as it breaks deployment ordering."; \
		exit 1; \
	fi; \
	CURRENT_HELMFILE_VERSION=$$(echo "$$HELMFILE_CMD_VERSION" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n 1); \
	if [ -z "$$CURRENT_HELMFILE_VERSION" ]; then \
		echo "Error: Could not parse Helmfile version from output: '$$HELMFILE_CMD_VERSION'"; \
		exit 1; \
	fi; \
	echo "    Found Helmfile version: $$CURRENT_HELMFILE_VERSION"; \
	HELMFILE_MAJOR=$$(echo "$$CURRENT_HELMFILE_VERSION" | cut -d. -f1); \
	HELMFILE_MINOR=$$(echo "$$CURRENT_HELMFILE_VERSION" | cut -d. -f2); \
	if [ "$$HELMFILE_MAJOR" -lt "$(REQUIRED_HELMFILE_MAJOR)" ]; then \
		echo "Error: Helmfile version $$CURRENT_HELMFILE_VERSION is too old."; \
		echo "       Required: v$(REQUIRED_HELMFILE_MAJOR).$(REQUIRED_HELMFILE_MINOR).x"; \
		exit 1; \
	elif [ "$$HELMFILE_MAJOR" -eq "$(REQUIRED_HELMFILE_MAJOR)" ] && [ "$$HELMFILE_MINOR" -lt "$(REQUIRED_HELMFILE_MINOR)" ]; then \
		echo "Error: Helmfile version $$CURRENT_HELMFILE_VERSION is too old."; \
		echo "       Required: v$(REQUIRED_HELMFILE_MAJOR).$(REQUIRED_HELMFILE_MINOR).x"; \
		exit 1; \
	elif [ "$$HELMFILE_MAJOR" -eq "$(REQUIRED_HELMFILE_MAJOR)" ] && [ "$$HELMFILE_MINOR" -eq "$(REQUIRED_HELMFILE_MINOR)" ]; then \
		echo "    Helmfile version $$CURRENT_HELMFILE_VERSION is compatible."; \
	else \
		echo "Warning: Helmfile version $$CURRENT_HELMFILE_VERSION may not work correctly."; \
		echo "         Helmfile v1.2.0+ uses parallel processing which breaks deployment ordering."; \
		echo "         Please use helmfile v1.1.x for guaranteed compatibility."; \
		echo ""; \
		echo "Proceed anyway? [y/N] "; \
		read -r response; \
		if [ "$$response" != "y" ] && [ "$$response" != "Y" ]; then \
			exit 1; \
		fi; \
	fi; \
	echo ""; \
	echo ">>> Checking Helm version (required v$(REQUIRED_HELM_MAJOR).x)..."; \
	HELM_CMD_VERSION=$$(helm version --short 2>/dev/null || helm version 2>/dev/null); \
	if [ -z "$$HELM_CMD_VERSION" ]; then \
		echo "Error: 'helm' command not found."; \
		echo "Please install helm v3.x from: https://helm.sh/docs/intro/install/"; \
		exit 1; \
	fi; \
	CURRENT_HELM_VERSION=$$(echo "$$HELM_CMD_VERSION" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n 1); \
	echo "    Found Helm version: $$CURRENT_HELM_VERSION"; \
	HELM_MAJOR=$$(echo "$$CURRENT_HELM_VERSION" | cut -d. -f1); \
	if [ "$$HELM_MAJOR" -ne "$(REQUIRED_HELM_MAJOR)" ]; then \
		echo "Error: Helm version $$CURRENT_HELM_VERSION is not supported."; \
		echo "       This deployment requires Helm v$(REQUIRED_HELM_MAJOR).x (Helm 4 is not supported)."; \
		exit 1; \
	else \
		echo "    Helm version $$CURRENT_HELM_VERSION is compatible."; \
	fi; \
	echo ""; \
	echo ">>> Checking helm-diff plugin..."; \
	if ! helm plugin list 2>/dev/null | grep -q "^diff"; then \
		echo "Warning: helm-diff plugin not found."; \
		echo "         Helmfile requires the helm-diff plugin for diff operations."; \
		echo "         Install with: helm plugin install https://github.com/databus23/helm-diff"; \
		echo ""; \
		echo "Continue without helm-diff? (apply/sync will work, diff will not) [y/N] "; \
		read -r response; \
		if [ "$$response" != "y" ] && [ "$$response" != "Y" ]; then \
			exit 1; \
		fi; \
	else \
		DIFF_VERSION=$$(helm plugin list | grep "^diff" | awk '{print $$2}'); \
		echo "    helm-diff plugin installed ($$DIFF_VERSION)"; \
	fi
endif

# --- Core Targets ---

template: clean check-versions
	@echo ">>> Templating Helmfile..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	@echo "    Output Directory: $(OUTPUT_DIR)"
	$(if $(KUBECONFIG_FILE),@echo "    Kubeconfig: $(KUBECONFIG_FILE)")

	mkdir -p "$(OUTPUT_DIR)"

	HELMFILE_ENV="$(HELMFILE_ENV)" \
	OUTPUT_DIR="$(OUTPUT_DIR)" \
	$(PATH_OVERRIDE) helmfile --environment default $(KUBECONFIG_FLAG) template --output-dir "$(OUTPUT_DIR)"

	@echo ">>> Templating complete. Manifests generated in $(OUTPUT_DIR)"

install: check-versions $(INSTALL_PRE_HOOKS)
	@echo ">>> Installing Helmfile configuration..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	$(if $(KUBECONFIG_FILE),@echo "    Kubeconfig: $(KUBECONFIG_FILE)")
	$(if $(HELMFILE_SELECTOR),@echo "    Selector: $(HELMFILE_SELECTOR)")

	HELMFILE_ENV="$(HELMFILE_ENV)" \
	$(PATH_OVERRIDE) helmfile --environment default $(KUBECONFIG_FLAG) $(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) sync

	@echo ">>> Helmfile sync complete."

sync: install

apply: check-versions $(APPLY_PRE_HOOKS)
	@echo ">>> Applying Helmfile configuration..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	$(if $(KUBECONFIG_FILE),@echo "    Kubeconfig: $(KUBECONFIG_FILE)")
	$(if $(HELMFILE_SELECTOR),@echo "    Selector: $(HELMFILE_SELECTOR)")

	HELMFILE_ENV="$(HELMFILE_ENV)" \
	$(PATH_OVERRIDE) helmfile --environment default $(KUBECONFIG_FLAG) $(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) apply

	@echo ">>> Helmfile apply complete."

destroy: check-versions
	@echo ">>> Destroying Helmfile releases..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	$(if $(KUBECONFIG_FILE),@echo "    Kubeconfig: $(KUBECONFIG_FILE)")
	$(if $(HELMFILE_SELECTOR),@echo "    Selector: $(HELMFILE_SELECTOR)")

	HELMFILE_ENV="$(HELMFILE_ENV)" \
	$(PATH_OVERRIDE) helmfile --environment default $(KUBECONFIG_FLAG) $(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) destroy

	@echo ">>> Helmfile destroy complete."

ifndef HELMFILE_SELECTOR
	@echo ">>> Deleting namespaces... $(NAMESPACES)"
	@for ns in $(NAMESPACES); do \
		if kubectl get namespace "$$ns" >/dev/null 2>&1; then \
			kubectl delete namespace "$$ns" --wait=true; \
		fi; \
	done
	@echo ">>> Namespace cleanup complete."
else
	@echo ">>> Skipping namespace cleanup (selector was used)"
endif

clean:
	@echo ">>> Cleaning output directory: $(OUTPUT_DIR)"
	rm -rf "$(OUTPUT_DIR)"
	@echo ">>> Clean complete."

# Help target is conditionally defined to avoid warnings when included by dev Makefile
ifndef _MAKEFILE_DIST_INCLUDED
help:
	@echo "NVCF Helmfile Deployment"
	@echo ""
	@echo "Usage: make [target] [VAR=value]"
	@echo ""
	@echo "Targets:"
	@echo "  template    Generate Kubernetes manifests without applying"
	@echo "  install     Initial deployment to cluster (helmfile sync)"
	@echo "  apply       Update existing deployment (helmfile apply)"
	@echo "  destroy     Remove all releases and clean up namespaces"
	@echo "  clean       Remove generated output directory"
	@echo "  help        Show this help message"
	@echo ""
	@echo "Configuration:"
	@echo "  HELMFILE_ENV       Environment name (default: $(HELMFILE_ENV))"
	@echo "                     Must match a file: environments/<env>.yaml"
	@echo "  HELMFILE_SELECTOR  Optional release selector (e.g., name=api-keys)"
	@echo "  KUBECONFIG_FILE    Optional path to kubeconfig file"
	@echo "  OUTPUT_DIR         Output directory for template (default: ./out)"
	@echo ""
	@echo "Examples:"
	@echo "  make template HELMFILE_ENV=myenv"
	@echo "  make install HELMFILE_ENV=myenv"
	@echo "  make apply HELMFILE_ENV=myenv HELMFILE_SELECTOR=name=api-keys"
endif
