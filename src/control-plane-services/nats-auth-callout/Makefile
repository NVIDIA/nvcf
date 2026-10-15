# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build variables
BINARY_NAME = nvcf-nats-auth-callout-service
MAIN_PATH = ./cmd/nvcf-nats-auth-callout-service
LDFLAGS = -ldflags "-X github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/version.Version=$(VERSION) \
                   -X github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/version.GitCommit=$(GIT_COMMIT) \
                   -X github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/version.BuildDate=$(BUILD_DATE)"

# Docker build variables
DOCKER_IMAGE_NAME ?= $(BINARY_NAME)
DOCKERFILE_PATH ?= build/package/Dockerfile
DOCKER_BUILD_PLATFORMS ?= linux/amd64,linux/arm64
DOCKER_BUILDX_CONTEXT ?= ci-buildx-context
DOCKER_BUILDX_BUILDER ?= multi-platform-builder
DOCKER_PUSH_SKIPPED_FILE ?= /tmp/$(BINARY_NAME)-docker-push-ngc-skipped$(if $(CI_JOB_ID),-$(CI_JOB_ID))
CONTAINER_IMAGE ?= $(CI_REGISTRY_IMAGE)/$(DOCKER_IMAGE_NAME):$(IMAGE_TAG)
TARGET_NGC_IMAGE ?= $(NGC_IMAGE_PATH)/$(DOCKER_IMAGE_NAME):$(IMAGE_TAG)

gen-api:
	./scripts/install_proto.bash
	@protoc -I.\
        -I.:$$GOPATH/src \
        -I./api/third_party/ \
        --grpc-gateway_out=:. \
        --grpc-gateway_opt=paths=source_relative \
        --openapiv2_out=. \
        --go_out=.  \
        --go_opt=paths=source_relative \
        --go-grpc_out=. \
        --go-grpc_opt=paths=source_relative \
        api/nvcf-nats-auth-callout-service/v1/nvcf-nats-auth-callout-service.proto

go-install-deps:
	@go install \
        github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway \
        github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2 \
        google.golang.org/protobuf/cmd/protoc-gen-go \
        google.golang.org/grpc/cmd/protoc-gen-go-grpc

run:
	@go run ./cmd/nvcf-nats-auth-callout-service server

# Development targets
.PHONY: dev dev-debug dev-debug-suspend dev-test dev-lint dev-format dev-swagger

# Start with hot reloading using Air
dev:
	@echo "🔥 Starting with hot reloading..."
	@echo "   Application will restart automatically when you make changes"
	@mkdir -p tmp
	@air

# Start with debugger (continues immediately)
dev-debug:
	@echo "→ Starting debugger on port 5005 (continues immediately)..."
	@echo "  → Application will start running right away"
	@echo "  → Connect your debugger to localhost:5005 anytime"
	@echo "  → Use VS Code 'Connect to DevSpace Debugger' configuration"
	@echo "  → Use Ctrl+C to stop"
	@mkdir -p tmp
	@bash -c '\
		cleanup() { \
			echo "→ Stopping debugger..."; \
			if [ ! -z "$$DELVE_PID" ]; then \
				kill -TERM $$DELVE_PID 2>/dev/null || true; \
				sleep 1; \
				kill -KILL $$DELVE_PID 2>/dev/null || true; \
			fi; \
			exit 0; \
		}; \
		trap cleanup INT TERM; \
		dlv debug --headless --listen=:5005 --api-version=2 --accept-multiclient --continue ./cmd/nvcf-nats-auth-callout-service/main.go -- server & \
		DELVE_PID=$$!; \
		echo "✓ Delve started (PID: $$DELVE_PID) - application running"; \
		wait $$DELVE_PID; \
	'

# Start with debugger (waits for debugger attachment)
dev-debug-suspend:
	@echo "→ Starting debugger on port 5005 (suspended)..."
	@echo "  → Application will wait for debugger connection before starting"
	@echo "  → Connect your debugger to localhost:5005 to begin execution"
	@echo "  → Use VS Code 'Connect to DevSpace Debugger' configuration"
	@echo "  → Use Ctrl+C to stop"
	@mkdir -p tmp
	@bash -c '\
		cleanup() { \
			echo "→ Stopping debugger..."; \
			if [ ! -z "$$DELVE_PID" ]; then \
				kill -TERM $$DELVE_PID 2>/dev/null || true; \
				sleep 1; \
				kill -KILL $$DELVE_PID 2>/dev/null || true; \
			fi; \
			exit 0; \
		}; \
		trap cleanup INT TERM; \
		dlv debug --headless --listen=:5005 --api-version=2 --accept-multiclient ./cmd/nvcf-nats-auth-callout-service/main.go -- server & \
		DELVE_PID=$$!; \
		echo "✓ Delve started (PID: $$DELVE_PID) - waiting for debugger connection"; \
		wait $$DELVE_PID; \
	'

# Run tests (alias for existing test target)
dev-test: test

# Run linter
dev-lint:
	@echo "🔍 Running linter..."
	@golangci-lint run

# Format imports
dev-format:
	@echo "✨ Formatting imports..."
	@goimports -w .

# Generate swagger documentation
dev-swagger:
	@echo "📝 Generating swagger documentation..."
	@swag init -g ./cmd/nvcf-nats-auth-callout-service/main.go

gen-localhost-certs:
	pushd; cd ./scripts/certs/localhost/; ./gen-localhost-certs.sh; popd;

all: go-install-deps gen-api build

run-client-test:
	@go run ./cmd/nvcf-nats-auth-callout-service client --config ./cmd/nvcf-nats-auth-callout-service/client-config.yaml foo

# Build targets
.PHONY: build build-dev clean version docker-buildx-setup docker-build-ci docker-push-ngc-ci docker-push-ngc-fail-if-skipped

# Build the binary with version information
build:
	@echo "Building $(BINARY_NAME) version $(VERSION)..."
	@go build $(LDFLAGS) -o $(BINARY_NAME) $(MAIN_PATH)
	@echo "Build completed: ./$(BINARY_NAME)"

# Build for development (with race detection)
build-dev:
	@echo "Building $(BINARY_NAME) for development..."
	@go build -race $(LDFLAGS) -o $(BINARY_NAME) $(MAIN_PATH)
	@echo "Development build completed: ./$(BINARY_NAME)"

docker-buildx-setup:
	@docker context create "$(DOCKER_BUILDX_CONTEXT)" 2>/dev/null || docker context use "$(DOCKER_BUILDX_CONTEXT)"
	@docker buildx create --name "$(DOCKER_BUILDX_BUILDER)" --driver docker-container "$(DOCKER_BUILDX_CONTEXT)" --use 2>/dev/null || docker buildx use "$(DOCKER_BUILDX_BUILDER)"

docker-build-ci: docker-buildx-setup
	@set -eu; \
	: "$${IMAGE_TAG:?IMAGE_TAG must be set}"; \
	: "$${CI_REGISTRY:?CI_REGISTRY must be set}"; \
	: "$${CI_REGISTRY_USER:?CI_REGISTRY_USER must be set}"; \
	: "$${CI_JOB_TOKEN:?CI_JOB_TOKEN must be set}"; \
	: "$${CI_REGISTRY_IMAGE:?CI_REGISTRY_IMAGE must be set}"; \
	echo "$${CI_JOB_TOKEN}" | docker login -u "$${CI_REGISTRY_USER}" --password-stdin "$${CI_REGISTRY}"; \
	docker buildx build \
		--platform "$(DOCKER_BUILD_PLATFORMS)" \
		--push \
		-t "$(CONTAINER_IMAGE)" \
		$${EXTRA_BUILD_ARGS:-} \
		-f "$(DOCKERFILE_PATH)" \
		.; \
	echo "Pushed multi-arch image $(CONTAINER_IMAGE) for $(DOCKER_BUILD_PLATFORMS)"

docker-push-ngc-ci:
	@rm -f "$(DOCKER_PUSH_SKIPPED_FILE)"
	@set -eu; \
	if [ "$${SKIP_SEMANTIC_RELEASE:-}" = "true" ] && [ -z "$${CI_MERGE_REQUEST_IID:-}" ]; then \
		echo "Non semantic-release detected and not an MR build, skipping docker push"; \
		exit 0; \
	fi; \
	if [ -z "$${PUSH_TO_NGC:-}" ] || [ "$${PUSH_TO_NGC}" = "false" ]; then \
		echo "PUSH_TO_NGC is false or undefined, won't push to NGC"; \
		exit 0; \
	fi; \
	: "$${IMAGE_TAG:?IMAGE_TAG must be set}"; \
	: "$${NGC_REGISTRY:?NGC_REGISTRY must be set}"; \
	: "$${NGC_REGISTRY_TOKEN:?NGC_REGISTRY_TOKEN must be set}"; \
	: "$${NGC_IMAGE_PATH:?NGC_IMAGE_PATH must be set}"; \
	echo "$${NGC_REGISTRY_TOKEN}" | docker login -u '$$oauthtoken' --password-stdin "$${NGC_REGISTRY}"; \
	if docker manifest inspect "$(TARGET_NGC_IMAGE)" >/dev/null 2>&1; then \
		echo "$(TARGET_NGC_IMAGE) already exists on the registry."; \
		touch "$(DOCKER_PUSH_SKIPPED_FILE)"; \
	else \
		$(MAKE) docker-buildx-setup; \
		docker buildx build \
			--platform "$(DOCKER_BUILD_PLATFORMS)" \
			--push \
			-t "$(TARGET_NGC_IMAGE)" \
			$${EXTRA_BUILD_ARGS:-} \
			-f "$(DOCKERFILE_PATH)" \
			.; \
	fi; \
	echo "=========================================================================="; \
	echo "Displaying docker image:tags"; \
	echo "--------------------------------------------------------------------------"; \
	echo "  - $(TARGET_NGC_IMAGE)"

docker-push-ngc-fail-if-skipped:
	@if [ -f "$(DOCKER_PUSH_SKIPPED_FILE)" ]; then \
		echo "Some pushes were skipped. (exit code 66)"; \
		exit 66; \
	fi

# Clean build artifacts and development files
clean:
	@echo "Cleaning build artifacts and development files..."
	@rm -f $(BINARY_NAME)
	@rm -rf coverage/
	@rm -rf tmp/
	@rm -f __debug*
	@rm -f build-errors.log
	@rm -f *.out
	@rm -f *.log
	@echo "✓ Cleanup completed"

# Show version information (requires build)
version: build
	@echo "Version information:"
	@./$(BINARY_NAME) version

# Testing and Code Coverage targets
.PHONY: test test-verbose test-coverage coverage-html coverage-report coverage-check clean-coverage

# Run all tests
test:
	@echo "Running tests..."
	@go test ./...

# Run tests with verbose output
test-verbose:
	@echo "Running tests with verbose output..."
	@go test -v ./...

# Run tests with coverage and generate coverage profile
test-coverage:
	@echo "Running tests with coverage..."
	@mkdir -p coverage
	@go test -v -coverprofile=coverage/coverage.out -covermode=atomic ./...
	@go tool cover -func=coverage/coverage.out

# Generate HTML coverage report
coverage-html: test-coverage
	@echo "Generating HTML coverage report..."
	@go tool cover -html=coverage/coverage.out -o coverage/coverage.html
	@echo "Coverage report generated: coverage/coverage.html"

# Display coverage report in terminal
coverage-report: test-coverage
	@echo "Coverage by function:"
	@go tool cover -func=coverage/coverage.out
	@echo ""
	@echo "Total coverage:"
	@go tool cover -func=coverage/coverage.out | grep total: | awk '{print $$3}'

# Check coverage meets minimum threshold (80%)
coverage-check: test-coverage
	@echo "Checking coverage threshold..."
	@COVERAGE=$$(go tool cover -func=coverage/coverage.out | grep total: | awk '{print $$3}' | sed 's/%//'); \
	THRESHOLD=80; \
	echo "Current coverage: $$COVERAGE%"; \
	echo "Required threshold: $$THRESHOLD%"; \
	if [ $$(echo "$$COVERAGE >= $$THRESHOLD" | bc -l) -eq 1 ]; then \
		echo "✓ Coverage check passed!"; \
	else \
		echo "✗ Coverage check failed! Coverage $$COVERAGE% is below threshold $$THRESHOLD%"; \
		exit 1; \
	fi

# Run coverage with race detection
coverage-race:
	@echo "Running tests with coverage and race detection..."
	@mkdir -p coverage
	@go test -v -race -coverprofile=coverage/coverage_race.out -covermode=atomic ./...
	@go tool cover -func=coverage/coverage_race.out

# Clean coverage files
clean-coverage:
	@echo "Cleaning coverage files..."
	@rm -rf coverage/

# Run benchmarks
bench:
	@echo "Running benchmarks..."
	@go test -bench=. -benchmem ./...

# Run benchmarks with coverage
bench-coverage:
	@echo "Running benchmarks with coverage..."
	@mkdir -p coverage
	@go test -bench=. -benchmem -coverprofile=coverage/bench_coverage.out -covermode=atomic ./...
	@go tool cover -func=coverage/bench_coverage.out

# Comprehensive test suite (tests + coverage + race detection)
test-all: clean-coverage test-verbose coverage-race coverage-html coverage-check
	@echo "All tests completed successfully!"

# CI target for continuous integration
ci-test: clean-coverage test-coverage coverage-check
	@echo "CI test suite completed!"

# Swagger Commands
swagger-install:
	@go install github.com/swaggo/swag/cmd/swag@latest

swagger-gen:
	@swag init -g cmd/nvcf-nats-auth-callout-service/main.go -o api --parseDependency --parseInternal --outputTypes go,yaml

swagger-clean:
	@rm -rf api/
