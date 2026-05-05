.PHONY: test docker-build docker-run update-examples update-config-template validate-config

DOCKER_TAG := $(shell whoami)-dev
OTEL_BUILDER_VERSION ?= v0.147.0

test:
	find . -name go.mod -execdir go test -v ./... \;

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

