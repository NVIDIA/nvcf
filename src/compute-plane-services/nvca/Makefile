SHELL := /bin/bash

# Load .env file
# NOTE: env vars in this file always win over local environment variables
ENVFILE := .env
include $(ENVFILE)
export $(shell sed 's/=.*//' $(ENVFILE) | grep '^[[:alpha:]]')

export EGX_TOOLBOX_IMAGE_REPO ?= 
export EGX_TOOLBOX_IMAGE_TAG ?= $(USER)

# Gitlab settings, should work for both local dev and CI env.
export GITLAB_USER ?= $(shell awk -F'=' '/^GITLAB_USER=/{print $$2}' .secrets 2>/dev/null || awk '/github.com/NVIDIA/{getline; print $$2}' $$HOME/.netrc 2>/dev/null || echo "gitlab-ci-token")
export GITLAB_TOKEN ?= $(shell awk -F'=' '/^GITLAB_TOKEN=/{print $$2}' .secrets 2>/dev/null || awk '/github.com/NVIDIA/{getline;getline; print $$2}' $$HOME/.netrc 2>/dev/null || echo "$(CI_JOB_TOKEN)")
export GITHUB_TOKEN ?= $(shell awk -F'=' '/^GITHUB_TOKEN=/{print $$2}' .secrets 2>/dev/null || awk '$$1 == "machine" { in_machine = ($$2 == "github.com") } in_machine && $$1 == "password" { print $$2; exit }' $$HOME/.netrc 2>/dev/null)

# Docker settings
export EGX_DOCKER_CMD ?= docker
export EGX_DOCKER_BUILD_FLAGS ?= $(CI_COMMON_ARGS) \
	--network=host --build-arg 'GITLAB_USER=$(GITLAB_USER)' --build-arg 'GITLAB_TOKEN=$(GITLAB_TOKEN)'

# NOTE: GITLAB_USER and GITLAB_TOKEN need to be configured in .secrets for
# private repo access in go build.
.PHONY: toolbox-update
toolbox-update:
	$(EGX_DOCKER_CMD) pull "$(EGX_TOOLBOX_IMAGE_REPO):$(EGX_TOOLBOX_IMAGE_TAG)"

.PHONY: dev-shell dev-shell-x

#export EGX_DEV_SHELL_DOCKER_NET_ARGS ?=
export EGX_DEV_SHELL_DOCKER_NET_ARGS ?= --net=host

EGX_DEV_SHELL_DOCKER_COMMON_ARGS = \
	--init \
	--rm \
	-it \
	$(EGX_DEV_SHELL_DOCKER_NET_ARGS) \
	-v "/var/run/docker.sock:/var/run/docker.sock" \
	-v "/$(HOME)/.docker/buildx:/root/.docker/buildx:ro" \
	-v $(CURDIR):/nvca:cached \
	-w /nvca \
	-e "USER=$(USER)" \
	-e "UID=$$(id -u)" \
	-e "GID=$$(id -g)" \
	-e "DOCKER_GID=$(shell [[ "$$(uname)" = "Darwin" ]] && echo 1 || getent group docker | cut -d':' -f3)" \
	-e "EGX_STARSHIP_ENABLED=$(EGX_STARSHIP_ENABLED)" \

EGX_DEV_SHELL_TOKEN_ARGS = \
	-e "GITLAB_USER=$(GITLAB_USER)" \
	-e "GITLAB_TOKEN=$(GITLAB_TOKEN)" \
	-e "GITHUB_TOKEN=$(GITHUB_TOKEN)" \


EGX_DEV_SHELL_SSH_ARGS = \
	-v "$(HOME)/.gitconfig:/tmp/host-gitconfig:ro" \
	-v "$(SSH_AUTH_SOCK):/run/host-ssh-agent.sock" \
	-e "SSH_AUTH_SOCK=/run/host-ssh-agent.sock" \

dev-shell:
	@$(EGX_DOCKER_CMD) run $(EGX_DEV_SHELL_DOCKER_COMMON_ARGS) $(EGX_DEV_SHELL_TOKEN_ARGS) \
		"$(EGX_TOOLBOX_IMAGE_REPO):$(EGX_TOOLBOX_IMAGE_TAG)" \
		/toolbox/bin/devshell_entrypoint

dev-shell-ssh:
	@$(EGX_DOCKER_CMD) run $(EGX_DEV_SHELL_DOCKER_COMMON_ARGS) $(EGX_DEV_SHELL_SSH_ARGS) \
		"$(EGX_TOOLBOX_IMAGE_REPO):$(EGX_TOOLBOX_IMAGE_TAG)" \
		/toolbox/bin/devshell_entrypoint

# Make X11 accessible within container, make running GUI applications such as
# firefox within container possible. Verified working on Ubuntu 20.04.
dev-shell-x:
	@$(EGX_DOCKER_CMD) run $(EGX_DEV_SHELL_DOCKER_COMMON_ARGS) $(EGX_DEV_SHELL_TOKEN_ARGS) \
		-v /tmp/.X11-unix:/tmp/.X11-unix \
		-e "DISPLAY=$(DISPLAY)" \
		"$(EGX_TOOLBOX_IMAGE_REPO):$(EGX_TOOLBOX_IMAGE_TAG)" \
		/toolbox/bin/devshell_entrypoint
# Beyond this point, all targets should run within dev-shell.

.PHONY: codegen-update
codegen-update:
	./scripts/codegen_update

.PHONY: openapigen-update
openapigen-update:
	./scripts/openapigen_update

.PHONY: testdata-update
testdata-update:
	./scripts/testdata_update

.PHONY: vendor-update
vendor-update:
	nv_ci_vendor_update

.PHONY: gitlab-ci-update
gitlab-ci-update:
	nv_components gen-gitlab-ci >$(CURDIR)/pipelines/components.yml

.PHONY: egx-common-update
egx-common-update:
	go get -u github.com/NVIDIA/egx/common

.PHONY: docs-update
docs-update:
	./scripts/docs_update

.PHONY: gen
gen: egx-common-update vendor-update gitlab-ci-update codegen-update openapigen-update testdata-update docs-update

.PHONY: test
# To view coverage results locally:
#   1) cd _output/cover && python3 -m http.server 8000
#   2) Open browser: http://<machine_ip>:8000/coverage.html
# Run individual unit test:
#   nv_gotest ./pkg/core -run TestHTTPServiceRoutes
test:
	source <(./scripts/setup_envtest) && \
	echo KUBEBUILDER_ASSETS=$$KUBEBUILDER_ASSETS && \
	nv_gotest -p "${NVCF_GOMAXPROCS}" \
		-timeout 10m \
		-ldflags '-X github.com/NVIDIA/k8s-dra-driver-gpu/internal/info.version=v25.8.0' \
	 	$(shell go list ./... \
		| grep -v '/pkg/apis/' \
		| grep -v '/pkg/client/' \
		| grep -v '/internal/kubeclients' \
		| grep -v '/internal/mock' \
		| grep -v '/cmd/discover-gpus' \
		| grep -v '/cmd/internal' \
		| grep -v '/cmd/nvca$$' \
		| grep -v '/cmd/nvca-operator' \
		| grep -v '/cmd/nvca-self-managed' \
		| grep -v '/cmd/nvca-mirror' \
		| grep -v '/cmd/webhook-server' \
		| grep -v 'nvca/test/')

test-e2e:
	@$(CURDIR)/test/e2e.sh

.PHONY: lint
lint: lint-go lint-helm

.PHONY: lint-go
lint-go:
	CGO_ENABLED=1 golangci-lint run -c .golangci.yml

.PHONY: dotenv-dependencies-update
dotenv-dependencies-update:
	./scripts/ci_dotenv_dependencies_update


.PHONY: image-dev-local
image-dev-local:
	EGX_DOCKER_MULTI_ARCH_PLATFORMS=$(shell dpkg --print-architecture) nv_ci_build_multiarch_image \
		-t nvcr.io/qtfpt1h0bieu/byocdev/nvca-operator:dev-local \
		-f docker/Dockerfile.nvca-operator .

.PHONY: chart-dev-local
chart-dev-local:
	nv_components build nvca-operator --verbose --fc-version "v0.0.0-dev-local" --charts --yes
	mv _output/charts/nvca-operator-v0.0.0-dev-local.tgz _output/charts/nvca-operator-dev-local.tgz

.PHONY: lint-helm
lint-helm:
	@$(CURDIR)/scripts/lint_helm.sh

# pulse-secret-scanner scans all files in the repository
.PHONY: pulse-secret-scanner
pulse-secret-scanner:
	nv_ci_pulse_secret_scanner .
