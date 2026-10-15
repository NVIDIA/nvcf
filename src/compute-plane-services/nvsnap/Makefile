# NVSNAP Makefile

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
REGISTRY ?= nvcr.io/0651155215864979/ncp-dev
IMAGE_NAME ?= nvsnap-agent
FULL_IMAGE ?= $(REGISTRY)/$(IMAGE_NAME):$(VERSION)
CONTAINER_TOOL ?= docker

GO_FLAGS ?= -ldflags="-s -w -X main.version=$(VERSION)"
BUILD_DIR ?= bin

.PHONY: all build agent nvsnap-server clean test test-coverage test-checkpoint test-server deploy install-crds generate check-gpushare

all: build

# Build all binaries
build: agent nvsnap-server

# Build the agent binary
agent:
	@echo "Building nvsnap-agent..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux go build $(GO_FLAGS) -o $(BUILD_DIR)/nvsnap-agent ./cmd/agent

# Build the nvsnap-server binary
nvsnap-server:
	@echo "Building nvsnap-server..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux go build $(GO_FLAGS) -o $(BUILD_DIR)/nvsnap-server ./cmd/nvsnap-server


# Build container image for agent
docker-build: agent
	@echo "Building container image $(FULL_IMAGE)..."
	$(CONTAINER_TOOL) build -t $(FULL_IMAGE) -f docker/agent/Dockerfile .


# Push container image
docker-push: docker-build
	@echo "Pushing $(FULL_IMAGE)..."
	$(CONTAINER_TOOL) push $(FULL_IMAGE)

# Install CRDs
install-crds:
	@echo "Installing CRDs..."
	kubectl apply -f deploy/crds/

# Deploy agent DaemonSet
deploy: install-crds
	@echo "Deploying agent..."
	kubectl apply -f deploy/k8s/agent-daemonset.yaml

# Remove deployment
undeploy:
	kubectl delete -f deploy/k8s/agent-daemonset.yaml --ignore-not-found
	kubectl delete -f deploy/crds/ --ignore-not-found

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)

# Resolve the testable package list once, excluding vendored / node_modules
# trees that happen to ship Go files we don't own. Used by both test
# variants below.
GO_TEST_PKGS = $(shell go list ./... | grep -v '/ui/node_modules/')

# Run all unit tests
test:
	go test -v $(GO_TEST_PKGS)

# Run all unit tests with coverage profile (used by CI).
# Coverage output lands in coverage/coverage.out for Codecov upload.
# Compile the gpushare C code for amd64 and arm64 (Docker, no GPU); same check as CI.
check-gpushare:
	./scripts/check-gpushare-build.sh

test-coverage:
	mkdir -p coverage
	go test -v -race -coverprofile=coverage/coverage.out -covermode=atomic $(GO_TEST_PKGS)
	go tool cover -func=coverage/coverage.out | tail -1

# Run server unit tests only
test-server:
	go test -v ./internal/server

# Run checkpoint unit tests only
test-checkpoint:
	go test -v ./internal/checkpointstore

# Download dependencies
deps:
	go mod download
	go mod tidy

# Generate k8s deepcopy methods
generate:
	controller-gen object paths="./api/..."

# Local development - build and copy to node
dev-deploy: agent
	@echo "Copying agent to node..."
	@if [ -z "$$NODE_IP" ]; then echo "Set NODE_IP env var"; exit 1; fi
	scp $(BUILD_DIR)/nvsnap-agent $$NODE_IP:/tmp/
	ssh $$NODE_IP "sudo cp /tmp/nvsnap-agent /usr/local/bin/ && sudo systemctl restart nvsnap-agent"

# Show help
help:
	@echo "NVSNAP Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  build             - Build all binaries (agent, server)"
	@echo "  agent             - Build agent binary"
	@echo "  docker-build      - Build agent container image"
	@echo "  docker-push       - Push agent container image"
	@echo "  install-crds      - Install CRDs to cluster"
	@echo "  deploy            - Deploy agent DaemonSet"
	@echo "  undeploy          - Remove deployment"
	@echo "  clean             - Clean build artifacts"
	@echo "  test              - Run tests"
	@echo "  test-checkpoint   - Run checkpoint unit tests"
	@echo "  test-server       - Run server unit tests"
	@echo "  dev-deploy        - Build and deploy to NODE_IP"
	@echo "  generate          - Generate deepcopy methods"
	@echo "  CONTAINER_TOOL    - Set to nerdctl for rootless builds"