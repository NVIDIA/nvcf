# Public developer entry point for the self-managed stack.
include Makefile.dist

.PHONY: test

# Offline render checks for stack values. No cluster or chart registry needed.
test:
	@tests/llm-request-router-worker-address.sh
	@tests/llm-request-router-external-exposure.sh
