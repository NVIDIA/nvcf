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

.PHONY: codegen-update
codegen-update:
	./scripts/codegen_update

.PHONY: update-testdata
update-testdata:
	./testdata/icms-translate/update-testdata.sh

.PHONY: check-testdata
check-testdata:
	./testdata/icms-translate/update-testdata.sh --check

.PHONY: license-update
license-update:
	./scripts/license_update

.PHONY: check-license
check-license:
	./scripts/ci_check_license

.PHONY: lint
# Run via `go run` so the linter is built with the active Go toolchain (>= go.mod).
# A separately installed golangci-lint binary may be built with an older Go and then
# refuse to load this module when go.mod targets a newer language version.
# CI enables a repo-root `go.work`, but this module tracks its own vendor tree.
# Disable workspace mode here and force golangci-lint to resolve packages from
# `vendor/` so analysis matches `go test` and avoids workspace export-data errors.
lint:
	GOWORK=off CGO_ENABLED=1 GOGC=1 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.3.0 run -c .golangci.yml --timeout 10m --modules-download-mode=vendor
