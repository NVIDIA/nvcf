# ESS in Monorepo: Change Report

Branch: feat/ess-in-monorepo
Base: main (merge-base 4311bd12)

## Summary

This branch lands the Encrypted Secret Store (ESS) service natively in the
monorepo as a Bazel-built Java service, alongside the existing Maven build. It
adds 351 new ESS files and touches 10 shared files. The shared-file changes are
limited to what ESS needs from the common Java graph and build rules. No
unintended dependency downgrades remain.

## New ESS subtree

All under src/control-plane-services/ess/ (351 files).

| Category | Count | Notes |
|---|---|---|
| Main Java sources | 193 | ess-core, ess-encryption, ess-service |
| Test Java sources | 116 | includes 18 integration tests (*IT.java) |
| Resources | 19 | application*.yaml, bootstrap*.yaml, spring.factories, logback configs |
| Build files | 7 | BUILD.bazel x3, bazel-java-ci.json, Dockerfile, lombok.config |
| Docs | 6 | AGENTS.md, CLAUDE.md, README.md, BAZEL.md |
| NOTICE | 2 | NOTICE, notice_metadata.json (generated) |
| local_env | 6 | Cassandra schema, docker-compose, local secrets |

## Shared and modified files

| File | +/- | Reason |
|---|---|---|
| MODULE.bazel | +19 / -0 | New ESS root coordinates plus version overrides (details below) |
| maven_install.json | +344 / -58 | Regenerated lockfile from the MODULE.bazel changes |
| rules/java/defs.bzl | +6 / -2 | Parameterized include_classname and added per-target javacopts for nv_boot_library_test and nvct_library_test (ESS test discovery and Error Prone opt-out) |
| rules/java/spring.bzl | +1 / -0 | Emit git.build.version into git.properties for Spring Boot version parity with Maven |
| BAZEL.md | +2 / -0 | ESS onboarding note |
| src/control-plane-services/api-keys/NOTICE | +3 / -3 | logback and nimbus version bumps only |
| src/control-plane-services/cloud-tasks/NOTICE | +3 / -3 | logback and nimbus version bumps only |
| src/control-plane-services/notary/NOTICE | +3 / -3 | logback and nimbus version bumps only |
| src/libraries/java/nv-boot-parent/NOTICE | +3 / -3 | logback and nimbus version bumps only |
| src/libraries/java/nv-boot-parent/notice_metadata.json | +23 / -0 | New license-metadata entries for logback 1.5.37 and nimbus 10.9.1 |

## MODULE.bazel dependency changes

New ESS coordinates:

- com.google.code.gson:gson
- io.opentelemetry.instrumentation:opentelemetry-instrumentation-annotations:2.21.0
- io.projectreactor:reactor-core-micrometer
- io.projectreactor:reactor-test
- io.projectreactor:reactor-tools
- net.logstash.logback:logstash-logback-encoder:7.4
- org.springdoc:springdoc-openapi-starter-webflux-ui:3.0.3
- org.springframework.boot:spring-boot-starter-cache
- org.springframework.boot:spring-boot-starter-data-cassandra-reactive
- org.springframework.boot:spring-boot-testcontainers

Version overrides (intentional, ripple to all Java services):

- ch.qos.logback:logback-classic and logback-core to 1.5.37 (logback security fixes)
- com.nimbusds:nimbus-jose-jwt to 10.9.1
- com.google.errorprone:error_prone_annotations to 2.49.0 (direct pin overriding the Spring Boot BOM's 2.41.0, to prevent the shared-graph downgrade)

## Shared-graph impact vs main

The only cross-service ripple in the sibling NOTICE files (api-keys,
cloud-tasks, notary, nv-boot-parent) is the intended logback 1.5.34 to 1.5.37
and nimbus 10.4 to 10.9.1 bumps. The earlier error_prone_annotations 2.49.0 to
2.41.0 downgrade has been eliminated: it is now 2.49.0 in the lockfile and in
all NOTICE files, and the orphaned 2.41.0 metadata entry was removed.

## Not part of the change

The following untracked items are local artifacts and should not be committed:

- src/control-plane-services/ess/ess-core/target/, ess-encryption/target/, ess-service/target/ (Maven build outputs)
- my_changes.patch (local scratch patch)
- src/control-plane-services/ess/NOTICE.nonapproved (OSRB delta working file)
