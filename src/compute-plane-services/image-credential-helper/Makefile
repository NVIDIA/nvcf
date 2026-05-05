SHELL := /bin/bash

# Load .env file
# NOTE: env vars in this file always win over local environment variables
ENVFILE := .env
-include $(ENVFILE)
ifneq ($(wildcard $(ENVFILE)),)
export $(shell sed 's/=.*//' $(ENVFILE) 2>/dev/null | grep '^[[:alpha:]]' || true)
endif

# Go compiler envs
export CGO_ENABLED=0
export GO_BUILD_FLAGS=-a -ldflags '-extldflags "-static"' -mod=vendor

# Include toolbox-specific targets if Makefile.toolbox exists
# Must be included early so MAKEFILE_TOOLBOX_INCLUDED is set before ifndef check
-include Makefile.toolbox

# Fallback targets - only defined if Makefile.toolbox is not included
ifndef MAKEFILE_TOOLBOX_INCLUDED

.PHONY: vendor-update
vendor-update:
	@echo "Updating vendor directory using go mod vendor..."
	@go mod vendor

.PHONY: test
test:
	@echo "Running tests using go test..."
	@mkdir -p _output/cover
	@go test -v -race -coverprofile=_output/cover/cover.txt -covermode=atomic \
		-timeout 10m \
		$$(go list ./...)
	@go tool cover -html=_output/cover/cover.txt -o _output/cover/coverage.html
	@echo "Coverage report generated at _output/cover/coverage.html"

endif

.PHONY: build
build:
	go build -o _output/bin/image-credential-helper ./cmd/image-credential-helper

.PHONY: lint
lint: lint-go

.PHONY: lint-go
lint-go:
	CGO_ENABLED=1 golangci-lint run -c .golangci.yml --timeout 10m

.PHONY: container
container:
	docker build -t image-credential-helper:local -f docker/Dockerfile .

.PHONY: shellcheck
shellcheck:
	@if [ -d "./scripts" ]; then \
		shellcheck -x --shell bash $$(find ./scripts -type f); \
	else \
		echo "No scripts directory found, skipping shellcheck"; \
	fi

# Regenerate the third-party licenses list in NOTICE from vendor.
.PHONY: license-update
license-update:
	@echo "Updating NOTICE third-party licenses list..."
	@head -11 NOTICE > NOTICE.new
	@echo "" >> NOTICE.new
	@find vendor -name "LICENSE*" -type f 2>/dev/null | sort | sed 's/^/    /' >> NOTICE.new
	@mv NOTICE.new NOTICE
	@echo "NOTICE updated."
