.PHONY: test perf-test docker-build docker-run update-examples update-config-template validate-config

DOCKER_TAG := $(shell whoami)-dev
OTEL_BUILDER_VERSION ?= v0.157.0

# Run tests for every Go module in this subtree. The perf module is a
# standalone module deliberately kept out of go.work (it carries its own
# replace directives and k8s pins), so it must be tested with GOWORK=off;
# running every module that way keeps each on its own go.mod, which is the
# correct behavior for per-module testing and avoids the "directory prefix .
# does not contain modules listed in go.work" failure from the perf dir.
test:
	find . -name go.mod -execdir env GOWORK=off go test -v ./... \;

# Build, vet, and test the standalone perf module. The perf module is not part
# of go.work and has no Bazel targets, so it is invisible to the Bazel CI lane;
# this target is wired into the required byoo-otel-collector CI workflow so the
# perf tests actually run on every change to this subtree.
perf-test:
	cd perf && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...

update-examples:
	./scripts/update-examples.sh

update-config-template:
	./scripts/start-generator.sh

validate-otelconfig:
	./scripts/validate-otelconfig.sh

docker-build:
	DOCKER_BUILDKIT=1 docker build --network=host --build-arg OTEL_BUILDER_VERSION=$(OTEL_BUILDER_VERSION) -f ./Dockerfile -t byoo-otel-collector:$(DOCKER_TAG) .

docker-run:
	docker run --network=host -v./accounts-secrets.json:/var/secrets/accounts-secrets.json -v./secrets:/etc/byoo-otel-collector/secrets/ -v./test/local/otelconfig.yaml:/etc/byoo-otel-collector/config.yaml byoo-otel-collector:$(DOCKER_TAG) $(EXTRA_ARGS)

