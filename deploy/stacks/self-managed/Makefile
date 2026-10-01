# Public developer entry point for the 0.6 maintenance stack.
include Makefile.dist

.PHONY: test test-published-charts
test:
	@bash tests/openbao-upgrade.sh

test-published-charts:
	@: "$${NVCF_OPENBAO_CHART:?Set NVCF_OPENBAO_CHART to the published chart archive or OCI reference}"
	@bash tests/openbao-upgrade.sh
