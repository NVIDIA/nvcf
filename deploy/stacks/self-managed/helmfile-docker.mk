# helmfile-docker.mk
# Contains Dockerized versions of Helmfile operations

# --- Docker Configuration ---
HELMFILE_DOCKER_IMAGE ?= ghcr.io/helmfile/helmfile:v1.1.0
DOCKER_CONFIG_PATH ?= $(HOME)/.docker/config.json
KUBECONFIG_PATH ?= $(HOME)/.kube/config

# --- Docker Targets ---
.PHONY: template-docker

template-docker: clean
	@echo ">>> Templating Helmfile via Docker..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	@echo "    Output Directory: $(OUTPUT_DIR)"
	@echo "    Docker Image: $(HELMFILE_DOCKER_IMAGE)"

	# Create the output directory if it doesn't exist (host side)
	mkdir -p "$(OUTPUT_DIR)"

	# Invoke helmfile template via Docker (auto-discovers helmfile.d/ directory)
	docker run --rm -i \
		--user "$(shell id -u):$(shell id -g)" \
		-v "$(MAKEFILE_DIR):/apps" \
		-v "$(DOCKER_CONFIG_PATH):/tmp/helm_docker_config/config.json:ro" \
		-w "/apps" \
		-e "HELMFILE_ENV=$(HELMFILE_ENV)" \
		-e "DOCKER_CONFIG=/tmp/helm_docker_config" \
		"$(HELMFILE_DOCKER_IMAGE)" \
		helmfile --environment default template --output-dir "$(subst $(MAKEFILE_DIR)/,,$(OUTPUT_DIR))"

	@echo ">>> Dockerized Templating complete. Manifests generated in $(OUTPUT_DIR)"

.PHONY: apply-docker

apply-docker:
	@echo ">>> Applying Helmfile configuration via Docker..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	@echo "    Kubeconfig: $(KUBECONFIG_PATH)"
	@echo "    Docker Image: $(HELMFILE_DOCKER_IMAGE)"

	# Invoke helmfile apply via Docker (auto-discovers helmfile.d/ directory)
	docker run --rm -i \
		--user "$(shell id -u):$(shell id -g)" \
		-v "$(MAKEFILE_DIR):/apps" \
		-v "$(DOCKER_CONFIG_PATH):/tmp/helm_docker_config/config.json:ro" \
		-v "$(KUBECONFIG_PATH):/root/.kube/config:ro" \
		-w "/apps" \
		-e "HELMFILE_ENV=$(HELMFILE_ENV)" \
		-e "DOCKER_CONFIG=/tmp/helm_docker_config" \
		"$(HELMFILE_DOCKER_IMAGE)" \
		helmfile --environment default apply

	@echo ">>> Dockerized Helmfile apply complete."

.PHONY: destroy-docker

destroy-docker:
	@echo ">>> Destroying Helmfile releases via Docker..."
	@echo "    Helmfile Directory: helmfile.d/"
	@echo "    Environment: $(HELMFILE_ENV)"
	@echo "    Kubeconfig: $(KUBECONFIG_PATH)"
	@echo "    Docker Image: $(HELMFILE_DOCKER_IMAGE)"

	# Invoke helmfile destroy via Docker (auto-discovers helmfile.d/ directory)
	docker run --rm -i \
		--user "$(shell id -u):$(shell id -g)" \
		-v "$(MAKEFILE_DIR):/apps" \
		-v "$(DOCKER_CONFIG_PATH):/tmp/helm_docker_config/config.json:ro" \
		-v "$(KUBECONFIG_PATH):/root/.kube/config:ro" \
		-w "/apps" \
		-e "HELMFILE_ENV=$(HELMFILE_ENV)" \
		-e "DOCKER_CONFIG=/tmp/helm_docker_config" \
		"$(HELMFILE_DOCKER_IMAGE)" \
		helmfile --environment default destroy

	@echo ">>> Dockerized Helmfile destroy complete."
