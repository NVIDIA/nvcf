# Public developer entry point for the self-managed stack.
include Makefile.dist

.PHONY: test
test:
	@tests/llm-router-worker-address.sh
	@tests/llm-pki-release.sh
	@tests/pdb-value-wiring.sh
