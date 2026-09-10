# Public developer entry point for the self-managed stack.
MAKEFILE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

include Makefile.dist

.PHONY: test test-published-charts render-local test-local generate-golden test-golden-comparator test-render-structure test-render-structure-detects-violations test-named-control-plane-render-isolation-detects-violations test-control-plane-owner-label-renderer test-control-plane-owner-label-placement test-control-plane-owner-output-dir-env test-control-plane-owner-helmfile-validation test-control-plane-owner-namespace-delete test-control-plane-owner-namespace-marker test-control-plane-owner-label-golden-diff

GOLDEN_LOCAL_DIR ?= $(MAKEFILE_DIR)/testdata/golden/local
GOLDEN_RENDER_DIR ?= $(MAKEFILE_DIR)/.golden/out
GOLDEN_OUTPUT_DIR_TEMPLATE ?= {{ .OutputDir }}/{{ .State.BaseName }}-{{ .Release.Name }}

test:
	@tests/llm-gateway-auth-transport.sh
	@tests/llm-router-worker-address.sh
	@tests/llm-router-split-cluster.sh
	@tests/llm-router-local-chart.sh
	@tests/llm-servicemonitor-defaults.sh
	@tests/gateway-routes-local-chart.sh
	@tests/gateway-routes-enable-wiring.sh
	@tests/gateway-routes-named-isolation.sh
	@tests/admin-nats-named-isolation.sh
	@tests/service-dns-named-isolation.sh
	@tests/grpc-proxy-nats-endpoint.sh
	@tests/llm-pki-openbao-migration.sh
	@tests/api-keys-startup-probe.sh
	@tests/cassandra-openbao-credential-wiring.sh
	@tests/llm-pki-release.sh
	@tests/check-llm-pki-issuer.sh
	@tests/pdb-value-wiring.sh
	@tests/invocation-env-wiring.sh
	@tests/invocation-tracing-baggage.sh
	@tests/image-override-wiring.sh
	@tests/sidecar-registry-wiring.sh
	@tests/api-env-wiring.sh
	@tests/apikeys-env-wiring.sh
	@tests/ess-base-url-wiring.sh
	@tests/icms-condition-wiring.sh
	@tests/nats-placement-tags.sh
	@tests/nats-tls-wiring.sh
	@tests/notary-image-repository.sh

test-published-charts:
	@: "$${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
	@: "$${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"
	@tests/llm-router-published-chart.sh
	@tests/gateway-routes-published-chart.sh
	@tests/openbao-published-chart.sh
	@tests/cassandra-autoscaler-published-compatibility.sh

render-local:
	@tests/render-local-golden.sh "$(GOLDEN_RENDER_DIR)" "$(GOLDEN_OUTPUT_DIR_TEMPLATE)"

test-golden-comparator:
	@tests/verify-golden-detects-change.sh

test-render-structure: render-local
	@tests/verify-render-structure.sh "$(GOLDEN_RENDER_DIR)"

test-render-structure-detects-violations:
	@tests/verify-render-structure-detects-violations.sh

test-named-control-plane-render-isolation-detects-violations:
	@tests/verify-named-control-plane-render-isolation-detects-violations.sh

test-control-plane-owner-label-renderer:
	@tests/verify-control-plane-owner-label-renderer.sh

test-control-plane-owner-label-placement:
	@tests/verify-control-plane-owner-label-placement.sh "$(GOLDEN_RENDER_DIR)"

test-control-plane-owner-output-dir-env:
	@tests/verify-control-plane-owner-output-dir-env.sh

test-control-plane-owner-helmfile-validation:
	@tests/verify-control-plane-owner-helmfile-validation.sh

test-control-plane-owner-namespace-delete:
	@tests/verify-control-plane-owner-namespace-delete.sh

test-control-plane-owner-namespace-marker:
	@tests/verify-control-plane-owner-namespace-marker.sh

test-control-plane-owner-label-golden-diff:
	@tests/verify-control-plane-owner-label-golden-diff.sh "$(GOLDEN_LOCAL_DIR)"

test-local: test-control-plane-owner-label-renderer test-control-plane-owner-output-dir-env test-control-plane-owner-helmfile-validation test-control-plane-owner-namespace-delete test-control-plane-owner-namespace-marker test-named-control-plane-render-isolation-detects-violations render-local test-golden-comparator test-render-structure-detects-violations test-render-structure
	@tests/verify-control-plane-owner-label-placement.sh "$(GOLDEN_RENDER_DIR)"
	@tests/verify-golden.sh "$(GOLDEN_LOCAL_DIR)" "$(GOLDEN_RENDER_DIR)"

generate-golden:
	@tests/render-local-golden.sh "$(GOLDEN_LOCAL_DIR)" "$(GOLDEN_OUTPUT_DIR_TEMPLATE)"
	@echo ">>> Golden testdata updated at $(GOLDEN_LOCAL_DIR)."
	@echo "    Review with: git diff -- $(GOLDEN_LOCAL_DIR)"
