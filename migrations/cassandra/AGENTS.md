# Cassandra migrations

This native subtree owns the migration image and schema scripts.

Run from `migrations/cassandra`:

- Shell contracts: `sh tests/test-execute-sqls.sh`.
- Local image: `docker buildx build --platform linux/arm64 --load -t cassandra-migrations:test .`.
- Image smoke and locked library checks: `sh tests/test-image.sh cassandra-migrations:test`.
- Shell lint: `shellcheck execute_sqls.sh tests/*.sh`.

Use POSIX shell. Keep dependency archives, builder images, and Java libraries
checksum-pinned. Preserve numeric non-root image users and runtime tool coverage.
Security backports must not introduce unrelated schema migrations. Verify both
platforms and the consuming migration Job before releasing a replacement image.
