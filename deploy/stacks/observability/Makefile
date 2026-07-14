# =============================================================================
# Makefile - Helmfile operations for the shared NVCF observability stack
# =============================================================================

HELMFILE_ENV ?= local
OUTPUT_DIR ?= $(CURDIR)/out
KUBECONFIG_FILE ?=
KUBECONFIG_FLAG = $(if $(KUBECONFIG_FILE),--kubeconfig $(KUBECONFIG_FILE))
HELMFILE_SELECTOR ?=
HELMFILE_EXTRA_ARGS ?= --allow-no-matching-release

.PHONY: template install sync apply destroy clean help
.DEFAULT_GOAL := help

template: clean
	@echo ">>> Templating observability stack"
	@echo "    Environment: $(HELMFILE_ENV)"
	@echo "    Output:      $(OUTPUT_DIR)"
	mkdir -p "$(OUTPUT_DIR)"
	HELMFILE_ENV="$(HELMFILE_ENV)" \
		helmfile --environment default $(KUBECONFIG_FLAG) $(HELMFILE_EXTRA_ARGS) \
		$(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) \
		template --output-dir "$(OUTPUT_DIR)"

install:
	@echo ">>> Installing observability stack"
	HELMFILE_ENV="$(HELMFILE_ENV)" \
		helmfile --environment default $(KUBECONFIG_FLAG) $(HELMFILE_EXTRA_ARGS) \
		$(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) \
		sync

sync: install

apply:
	@echo ">>> Applying observability stack"
	HELMFILE_ENV="$(HELMFILE_ENV)" \
		helmfile --environment default $(KUBECONFIG_FLAG) $(HELMFILE_EXTRA_ARGS) \
		$(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) \
		apply

destroy:
	@echo ">>> Destroying observability stack releases"
	HELMFILE_ENV="$(HELMFILE_ENV)" \
		helmfile --environment default $(KUBECONFIG_FLAG) $(HELMFILE_EXTRA_ARGS) \
		$(if $(HELMFILE_SELECTOR),--selector $(HELMFILE_SELECTOR)) \
		destroy

clean:
	rm -rf "$(OUTPUT_DIR)"

help:
	@echo "NVCF Observability Helmfile"
	@echo ""
	@echo "Usage: make [target] HELMFILE_ENV=<env> [VAR=value]"
	@echo ""
	@echo "Targets:"
	@echo "  template   Generate Kubernetes manifests without applying"
	@echo "  install    Install the stack with helmfile sync"
	@echo "  apply      Apply changes with helmfile apply"
	@echo "  destroy    Remove stack releases"
	@echo "  clean      Remove generated output"
	@echo ""
	@echo "Configuration:"
	@echo "  HELMFILE_ENV       Environment name (default: $(HELMFILE_ENV))"
	@echo "  HELMFILE_SELECTOR  Optional release selector"
	@echo "  KUBECONFIG_FILE    Optional kubeconfig path"
	@echo "  OUTPUT_DIR         Template output directory"
