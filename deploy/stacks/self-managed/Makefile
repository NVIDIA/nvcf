# Public developer entry point for the self-managed stack.
include Makefile.dist

.PHONY: test test-published-charts
test:
	@tests/llm-router-worker-address.sh
	@tests/llm-router-split-cluster.sh
	@tests/llm-router-local-chart.sh
	@tests/llm-servicemonitor-defaults.sh
	@tests/gateway-routes-local-chart.sh
	@tests/grpc-proxy-nats-endpoint.sh
	@tests/llm-pki-openbao-migration.sh
	@tests/api-keys-startup-probe.sh
	@tests/cassandra-openbao-credential-wiring.sh
	@tests/llm-pki-release.sh
	@tests/check-llm-pki-issuer.sh
	@tests/pdb-value-wiring.sh
	@tests/invocation-tracing-baggage.sh
	@tests/image-override-wiring.sh
	@tests/sidecar-registry-wiring.sh
	@tests/api-env-wiring.sh

test-published-charts:
	@: "$${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
	@: "$${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"
	@tests/llm-router-published-chart.sh
	@tests/gateway-routes-published-chart.sh
	@tests/openbao-published-chart.sh
