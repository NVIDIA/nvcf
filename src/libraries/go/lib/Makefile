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

.PHONY: codegen-update
codegen-update:
	../../../../tools/scripts/update-go-deepcopy \
		--module . \
		--header tools/scripts/boilerplate.go.txt \
		--bounding pkg/types \
		--packages ./pkg/types/nvca/config \
		--replace 'pkg/types/nvca/config/zz_generated.deepcopy.go:package config:package nvcaconfig'
	../../../../tools/scripts/update-go-deepcopy \
		--module . \
		--header tools/scripts/boilerplate.go.txt \
		--bounding pkg/icms-translate/translate \
		--packages './pkg/icms-translate/translate/common ./pkg/icms-translate/translate/function ./pkg/icms-translate/translate/task'

.PHONY: update-testdata
update-testdata:
	./testdata/icms-translate/update-testdata.sh

.PHONY: check-testdata
check-testdata:
	./testdata/icms-translate/update-testdata.sh --check

.PHONY: lint
# Run via `go run` so the linter is built with the active Go toolchain (>= go.mod).
# A separately installed golangci-lint binary may be built with an older Go and then
# refuse to load this module when go.mod targets a newer language version.
# CI enables a repo-root `go.work`, but this module tracks its own vendor tree.
# Disable workspace mode here and force golangci-lint to resolve packages from
# `vendor/` so analysis matches `go test` and avoids workspace export-data errors.
lint:
	GOWORK=off CGO_ENABLED=1 GOGC=1 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.3.0 run -c .golangci.yml --timeout 10m --modules-download-mode=vendor
