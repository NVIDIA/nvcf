# Encrypted Secret Store

Encrypted Secret Store (ESS) provides a reactive REST API for storing secrets
with tenant-scoped encryption and authorization. Its API design follows the
Vault KV v2 model to support workload secret injection.

## Modules

| Module | Purpose |
|---|---|
| `ess-encryption` | Namespace encryption key management and cryptography |
| `ess-core` | Secret storage, authorization, and REST API logic |
| `ess-service` | Spring Boot executable application |

ESS is a Bazel-only component in the root `nvcf` module. See [BAZEL.md](BAZEL.md)
for build, test, NOTICE, Docker, and local startup commands.

Unit tests run without external services. Integration tests require a running
Docker daemon and use Testcontainers to start an ephemeral Cassandra instance.

## License

This component is licensed under the
[Apache License 2.0](../../../LICENSE). See [NOTICE](NOTICE) for its
third-party dependency attribution and
[CONTRIBUTING.md](../../../CONTRIBUTING.md) for contribution guidance.
