# =============================================================================
# Makefile - Development Helmfile Operations (Compute Plane)
# =============================================================================
# This is the main development Makefile. It includes the distribution-safe
# targets from Makefile.dist and adds internal/development-only functionality.
#
# For the distributable Makefile, see: Makefile.dist
# =============================================================================

# --- Pre-Include Configuration ---
_MAKEFILE_DIST_INCLUDED := 1

# Determine the absolute path of the directory containing this Makefile
MAKEFILE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

# --- Development Binary Management ---
# Auto-download pinned versions for consistent development environment
HELMFILE_VERSION := 1.1.9
HELM_VERSION := 3.15.4
HELM_DIFF_VERSION := 3.9.11
HELMFILE_BIN := $(MAKEFILE_DIR)/bin/helmfile
HELM_BIN := $(MAKEFILE_DIR)/bin/helm
HELM_PLUGINS_DIR := $(MAKEFILE_DIR)/bin/helm-plugins

# Detect OS and architecture
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

ifeq ($(UNAME_S),Darwin)
	HELMFILE_OS := darwin
endif
ifeq ($(UNAME_S),Linux)
	HELMFILE_OS := linux
endif

ifeq ($(UNAME_M),x86_64)
	HELMFILE_ARCH := amd64
endif
ifeq ($(UNAME_M),aarch64)
	HELMFILE_ARCH := arm64
endif
ifeq ($(UNAME_M),arm64)
	HELMFILE_ARCH := arm64
endif

HELMFILE_DOWNLOAD_URL := https://github.com/helmfile/helmfile/releases/download/v$(HELMFILE_VERSION)/helmfile_$(HELMFILE_VERSION)_$(HELMFILE_OS)_$(HELMFILE_ARCH).tar.gz
HELM_DOWNLOAD_URL := https://get.helm.sh/helm-v$(HELM_VERSION)-$(HELMFILE_OS)-$(HELMFILE_ARCH).tar.gz

# Development mode flag - skip version checks since we use pinned binaries
DEV_MODE := 1

# Use ensure-binaries hook
INSTALL_PRE_HOOKS := ensure-binaries
APPLY_PRE_HOOKS := ensure-binaries

# --- Include Core Targets ---
include Makefile.dist

# --- Include Docker Targets (optional) ---
-include helmfile-docker.mk

# --- Development-Only Targets ---
.PHONY: dist clean-dist ensure-helm ensure-helmfile ensure-binaries render-local test-local generate-golden

# --- Binary Management (Development Only) ---
ensure-helm:
	@if [ ! -f "$(HELM_BIN)" ]; then \
		echo ">>> Helm binary not found. Downloading v$(HELM_VERSION)..."; \
		mkdir -p "$(MAKEFILE_DIR)/bin"; \
		curl -fsSL "$(HELM_DOWNLOAD_URL)" -o "$(MAKEFILE_DIR)/bin/helm.tar.gz"; \
		tar -xzf "$(MAKEFILE_DIR)/bin/helm.tar.gz" -C "$(MAKEFILE_DIR)/bin" --strip-components=1 $(HELMFILE_OS)-$(HELMFILE_ARCH)/helm; \
		rm "$(MAKEFILE_DIR)/bin/helm.tar.gz"; \
		chmod +x "$(HELM_BIN)"; \
		echo "    Downloaded helm v$(HELM_VERSION) to $(HELM_BIN)"; \
	fi
	@# Ensure helm-diff plugin is installed in local plugins directory
	@mkdir -p "$(HELM_PLUGINS_DIR)"
	@if [ ! -d "$(HELM_PLUGINS_DIR)/helm-diff" ]; then \
		echo ">>> Installing helm-diff plugin to local directory..."; \
		PATH="$(MAKEFILE_DIR)/bin:$$PATH" HELM_PLUGINS="$(HELM_PLUGINS_DIR)" \
		helm plugin install https://github.com/databus23/helm-diff --version $(HELM_DIFF_VERSION); \
		echo "    Installed helm-diff v$(HELM_DIFF_VERSION) to $(HELM_PLUGINS_DIR)"; \
	fi

ensure-helmfile: ensure-helm
	@if [ ! -f "$(HELMFILE_BIN)" ]; then \
		echo ">>> Helmfile binary not found. Downloading v$(HELMFILE_VERSION)..."; \
		mkdir -p "$(MAKEFILE_DIR)/bin"; \
		curl -fsSL "$(HELMFILE_DOWNLOAD_URL)" -o "$(MAKEFILE_DIR)/bin/helmfile.tar.gz"; \
		tar -xzf "$(MAKEFILE_DIR)/bin/helmfile.tar.gz" -C "$(MAKEFILE_DIR)/bin" helmfile; \
		rm "$(MAKEFILE_DIR)/bin/helmfile.tar.gz"; \
		chmod +x "$(HELMFILE_BIN)"; \
		echo "    Downloaded helmfile v$(HELMFILE_VERSION) to $(HELMFILE_BIN)"; \
	fi

ensure-binaries: ensure-helmfile
	@echo ">>> Development mode: Using pinned binaries in bin/"
	@echo "    Helm:     $(HELM_BIN) (v$(HELM_VERSION))"
	@echo "    Helmfile: $(HELMFILE_BIN) (v$(HELMFILE_VERSION))"
	@echo "    PATH will be prepended with: $(MAKEFILE_DIR)/bin"
	@echo ""
	@echo ">>> Verifying binaries will be found:"
	@PATH="$(MAKEFILE_DIR)/bin:$$PATH"; \
	WHICH_HELM=$$(which helm 2>/dev/null || echo "not found"); \
	WHICH_HELMFILE=$$(which helmfile 2>/dev/null || echo "not found"); \
	echo "    which helm:     $$WHICH_HELM"; \
	echo "    which helmfile: $$WHICH_HELMFILE"
	@echo ""

# --- Distribution Package Targets ---
DIST_DIR ?= $(MAKEFILE_DIR)/dist

clean-dist:
	@echo ">>> Cleaning distribution directory: $(DIST_DIR)"
	rm -rf "$(DIST_DIR)"

dist: clean-dist
	@echo ">>> Building distribution package..."
	@mkdir -p "$(DIST_DIR)/environments"

	# Copy Makefile.dist as the distribution Makefile
	@cp "$(MAKEFILE_DIR)/Makefile.dist" "$(DIST_DIR)/Makefile"

	# Copy core files
	@cp "$(MAKEFILE_DIR)/global.yaml.gotmpl" "$(DIST_DIR)/"

	# Copy environments/base.yaml
	@cp "$(MAKEFILE_DIR)/environments/base.yaml" "$(DIST_DIR)/environments/"

	# Copy helmfile.d files
	@cp -r "$(MAKEFILE_DIR)/helmfile.d" "$(DIST_DIR)/"

	# Copy and transform README (remove internal references)
	@sed -e 's|nvcf-ncp-internal/ncp-dev|your-org/your-team|g' \
	     -e 's|0651155215864979/ncp-dev|YOUR_ORG/YOUR_TEAM|g' \
	     -e '/gitlab-master\.nvidia\.com/d' \
	     "$(MAKEFILE_DIR)/README.md" > "$(DIST_DIR)/README.md"

	# --- Validation ---
	# Validate: no internal org references
	@if grep -q "0651155215864979" "$(DIST_DIR)/environments/base.yaml"; then \
		echo "ERROR: base.yaml still contains internal org reference"; \
		exit 1; \
	fi

	@echo ">>> Distribution package ready in $(DIST_DIR)/"
	@echo ""
	@echo "Contents:"
	@find "$(DIST_DIR)" -type f | sed 's|$(DIST_DIR)/|  |'

# --- Test Targets ---

# Golden manifests for `make test-local` (compared against $(DIST_DIR)/out)
GOLDEN_LOCAL_DIR ?= $(MAKEFILE_DIR)/testdata/golden/local

render-local: dist
	@echo ">>> Rendering local environment into $(DIST_DIR)/out..."
	@cp -r testdata/environments/* $(DIST_DIR)/environments/
	@cp -r testdata/registration $(DIST_DIR)/
	@cd $(DIST_DIR) && CLUSTER_NAME=ncp-local NCA_ID=ncp-local HELMFILE_ENV=local make template

test-local: render-local
	@echo ">>> Comparing $(DIST_DIR)/out against golden $(GOLDEN_LOCAL_DIR)..."
	@if ! diff -ruN "$(GOLDEN_LOCAL_DIR)" "$(DIST_DIR)/out"; then \
		echo ""; \
		echo "ERROR: rendered output in $(DIST_DIR)/out differs from golden $(GOLDEN_LOCAL_DIR)."; \
		echo "       Review the diff above. If the change is intentional, refresh the golden files:"; \
		echo "         make generate-golden"; \
		exit 1; \
	fi
	@echo ">>> Local render matches golden $(GOLDEN_LOCAL_DIR)."

generate-golden: render-local
	@echo ">>> Updating golden $(GOLDEN_LOCAL_DIR) from $(DIST_DIR)/out..."
	@rm -rf "$(GOLDEN_LOCAL_DIR)"
	@mkdir -p "$(dir $(GOLDEN_LOCAL_DIR))"
	@cp -r "$(DIST_DIR)/out" "$(GOLDEN_LOCAL_DIR)"
	@echo ">>> Golden testdata updated at $(GOLDEN_LOCAL_DIR)."
	@echo "    Review with: git diff -- $(GOLDEN_LOCAL_DIR)"

# --- Override Help to Include All Targets ---
help:
	@echo "NVCF Compute Plane Helmfile (Development)"
	@echo ""
	@echo "Usage: make [target] CLUSTER_NAME=<name> [VAR=value]"
	@echo ""
	@echo "Required:"
	@echo "  CLUSTER_NAME       Name of the GPU cluster to target"
	@echo ""
	@echo "Compute Plane: 2-command deployment flow"
	@echo "  1. make register-cluster CLUSTER_NAME=<name>   Register cluster with ICMS"
	@echo "                               (writes out/<name>-register-values.yaml)"
	@echo "  2. make install CLUSTER_NAME=<name>             Deploy nvca-operator"
	@echo ""
	@echo "  register-cluster is idempotent (nvcf-cli --ignore-existing): re-runs"
	@echo "  each invocation and rewrites the values file."
	@echo ""
	@echo "Core Targets:"
	@echo "  register-cluster   Register/refresh cluster with ICMS (idempotent)"
	@echo "  template           Generate Kubernetes manifests without applying"
	@echo "  install            Initial deployment to cluster (helmfile sync)"
	@echo "  apply              Update existing deployment (helmfile apply)"
	@echo "  destroy            Remove all releases and clean up namespaces"
	@echo "  clean              Remove generated output directory"
	@echo ""
	@echo "Distribution Targets:"
	@echo "  dist               Build distribution package in $(DIST_DIR)/"
	@echo "  clean-dist         Remove the distribution directory"
	@echo ""
	@echo "Testing Targets:"
	@echo "  test-local         Render the local env and diff against golden testdata"
	@echo "  generate-golden    Render the local env and update golden testdata"
	@echo ""
	@echo "Configuration:"
	@echo "  HELMFILE_ENV       Environment name (default: $(HELMFILE_ENV))"
	@echo "                     Maps to: environments/<env>.yaml"
	@echo "  HELMFILE_SELECTOR  Optional release selector (e.g., name=nvca-operator)"
	@echo "  KUBECONFIG_FILE    Path to kubeconfig file (pass to all targets in"
	@echo "                     multi-cluster setups so registration reads JWKS"
	@echo "                     from the correct cluster)"
	@echo "  OUTPUT_DIR         Output directory for template (default: ./out)"
	@echo "  NCA_ID             NCA identifier (default: $(NCA_ID))"
	@echo "  CLUSTER_REGION     Cluster region (default: $(CLUSTER_REGION))"
	@echo "  ICMS_URL           ICMS endpoint (default: $(ICMS_URL))"
	@echo ""
	@echo "Examples:"
	@echo "  make register-cluster CLUSTER_NAME=gpu-east KUBECONFIG_FILE=~/.kube/gpu-east.yaml"
	@echo "  make install CLUSTER_NAME=gpu-east KUBECONFIG_FILE=~/.kube/gpu-east.yaml"
	@echo "  make template CLUSTER_NAME=test-cluster HELMFILE_ENV=local"
	@echo "  make apply CLUSTER_NAME=gpu-east KUBECONFIG_FILE=~/.kube/gpu-east.yaml"
	@echo "  make dist"
