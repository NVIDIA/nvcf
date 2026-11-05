# OpenBao runtime verification

Run `sh infra/openbao/tests/verify-runtime-ca-test.sh` from the repository root.
To check a built runtime image, run
`sh infra/openbao/scripts/verify-runtime-ca.sh IMAGE`. Check both architectures
before publishing. Use POSIX shell and preserve tests for all Go-loaded CA paths.
