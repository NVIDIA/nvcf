# Public developer entry point for the self-managed stack.
include Makefile.dist

.PHONY: test
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
	@tests/openbao-published-chart.sh
	@tests/llm-pki-release.sh
	@tests/check-llm-pki-issuer.sh
	@tests/pdb-value-wiring.sh
	@tests/invocation-tracing-baggage.sh
	@tests/image-override-wiring.sh
	@tests/sidecar-registry-wiring.sh
