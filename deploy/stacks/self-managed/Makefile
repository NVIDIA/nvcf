# Public developer entry point for the self-managed stack.
include Makefile.dist

.PHONY: test
test:
	@tests/llm-router-worker-address.sh
	@tests/openbao-network-policy-wiring.sh
	@tests/pdb-value-wiring.sh
