# Phase 0 Baseline

Date: 2026-07-13
Branch: `feat/bazel`
Repository: `<nv-boot-parent-root>`

## Status

Phase 0 has enough input to begin. No additional product/design information is required before adding the first Bazel scaffold.

The baseline decision is Maven-first coexistence:

- Maven remains the default local and CI path.
- Maven remains the production publish path.
- `com.nvidia.boot:nv-boot-parent:pom` remains published as a parent POM while downstream apps still extend it.
- Bazel starts as an opt-in build/test path.
- Bazel jar publishing is deferred.
- `nv-boot-managed-parent` is explicitly later scope.
- `cloud-tasks` is the first downstream validation target.

## Local Environment

Observed local toolchain:

| Tool | Version |
|---|---|
| Git branch | `feat/bazel` |
| Java | Temurin OpenJDK `25.0.2` |
| Maven | Apache Maven `3.9.9` |
| OS/arch | macOS `26.5.2`, `aarch64` |

Recommended Bazel candidate for Phase 1:

| Component | Candidate |
|---|---|
| Bazel launcher | Bazelisk |
| Bazel version | `9.1.1` |
| Dependency mode | Bzlmod |
| JVM dependency resolver | `rules_jvm_external` `7.0` |
| Java target/runtime | Java 25 |

Why this candidate:

- Bazel `9.1.1` is the current patch LTS release checked on 2026-07-13.
- `rules_jvm_external` `7.0` is current in Bazel Central Registry and marked tested on Bazel 7, 8, and 9.
- Bzlmod is the correct default for new Bazel work; legacy `WORKSPACE` should be avoided unless a rule forces it.

## Commands Run

### Maven verify

Command:

```bash
mvn clean verify
```

Result:

- Success.
- Reactor modules: 13.
- Total time: about `01:58`.
- Warnings observed:
  - Java 25 restricted/native access warnings from Maven/Jansi.
  - terminally deprecated `sun.misc.Unsafe` warning from Maven/Guava.
  - Netty macOS DNS provider fallback log during tests.

### Maven local install

Command:

```bash
mvn clean install
```

Result:

- Success.
- Reactor modules: 13.
- Total time: about `01:43`.
- Installed `0.0.1-SNAPSHOT` POMs/jars/source jars into `$HOME/.m2/repository/com/nvidia/boot/...`.
- Publishing to URM was not performed.

This proves the Maven local artifact workflow that downstream apps currently rely on:

```bash
cd <nv-boot-parent-root>
mvn clean install
```

### License/NOTICE baseline

Command:

```bash
mvn license:aggregate-add-third-party -DskipTests
```

Result:

- Success.
- Detected 28 licenses.
- Wrote third-party file to root `NOTICE`.
- Git status remained clean after the run, so the generated `NOTICE` matched the existing checked-in file.

## Current Maven Output Inventory

After `mvn clean install`, the repository contained:

| Output | Observed |
|---|---:|
| Reactor target directories | 13 |
| Flattened POMs under `target/.flattened-pom.xml` | 13 |
| Jar modules with binary jars | 11 |
| Jar modules with source jars | 11 |
| JaCoCo XML reports | 11 |
| Surefire XML report files | 118 |
| `maven.properties` files | 13 |
| `git.properties` files | 0 |

Important distinction:

- `git-commit-id-maven-plugin` is managed in root `pluginManagement`, but this repo's local Maven build did not generate `git.properties`.
- The Bazel migration still needs a `git.properties` replacement because downstream Spring Boot applications use the parent/plugin contract.

## Current Published Artifact Contract

The Bazel migration must preserve, or intentionally replace with a reviewed compatibility plan, these Maven artifacts:

- `com.nvidia.boot:nv-boot-parent:pom`
- `com.nvidia.boot:nv-boot-bom:pom`
- `com.nvidia.boot:nv-boot-mock-servers-test:jar`
- `com.nvidia.boot:nv-boot-starter-audit:jar`
- `com.nvidia.boot:nv-boot-starter-cassandra:jar`
- `com.nvidia.boot:nv-boot-starter-core:jar`
- `com.nvidia.boot:nv-boot-starter-data-migration-notification:jar`
- `com.nvidia.boot:nv-boot-starter-exceptions:jar`
- `com.nvidia.boot:nv-boot-starter-jwt:jar`
- `com.nvidia.boot:nv-boot-starter-observability:jar`
- `com.nvidia.boot:nv-boot-starter-registries:jar`
- `com.nvidia.boot:nv-boot-starter-reloadable-properties:jar`
- `com.nvidia.boot:nv-boot-starter-telemetry:jar`

Each jar artifact also needs source jar parity before Bazel can become a publishing path.

## Phase 0 Acceptance Checklist

- [x] Confirm branch and repo location.
- [x] Confirm Maven remains default during coexistence.
- [x] Confirm `nv-boot-parent:pom` remains published during coexistence.
- [x] Confirm Bazel publishing is deferred.
- [x] Confirm `nv-boot-managed-parent` migrates later.
- [x] Confirm `cloud-tasks` as first downstream validation target.
- [x] Confirm local Maven artifact workflow remains required.
- [x] Confirm native Bazel source workflow remains required.
- [x] Run local `mvn clean verify`.
- [x] Run local `mvn clean install`.
- [x] Run local `mvn license:aggregate-add-third-party -DskipTests`.
- [x] Capture initial toolchain recommendation.

## Phase 1 Readiness Gates

Before adding broad `BUILD.bazel` coverage, Phase 1 should prove:

- Bazelisk can run the selected Bazel version locally.
- Java 25 compilation works with the chosen Bazel Java toolchain/runtime.
- `rules_jvm_external` can resolve the repo's public and NVIDIA Maven dependencies.
- URM/public repository credentials are available to Bazel without checking secrets into the repo.
- Lombok annotation processing works in one starter module.
- Mockito `-javaagent` can be wired into one `java_test`.
- Spring resources are packaged in a way that preserves Maven jar behavior.

## Local Cross-Repo Workflows To Preserve

### Native Bazel style

For active local development across `nv-boot-parent` and a downstream repo, prefer source overrides:

```bash
bazel test //... --override_module=nv_boot_parent=<nv-boot-parent-root>
```

This requires `nv-boot-parent` to become a Bazel module in Phase 1. It avoids stale jars and gives direct source/test feedback.

### Local Maven artifacts

For workflows that intentionally mimic Maven local snapshot development:

```bash
cd <nv-boot-parent-root>
mvn clean install
```

Then configure the consuming Bazel project to resolve `com.nvidia.boot:*:0.0.1-SNAPSHOT` through `rules_jvm_external` with a local Maven repository such as `file://$HOME/.m2/repository` before remote repositories.

Because Bazel uses lockfiles and stable action inputs, consumers should repin/refresh after reinstalling local snapshots. This is a development-only workflow, not a CI publishing strategy.

## First Downstream Validation Target

Path:

```text
<workspace>/nvct/cloud-tasks
```

Validation must cover:

- consuming `nv-boot-parent` through native Bazel source override;
- consuming `0.0.1-SNAPSHOT` local Maven artifacts;
- protobuf/gRPC generation parity;
- `nvct-core` test-fixture/test-jar equivalent;
- `local_env/cassandra/**` test assets;
- Spring Boot executable jar behavior;
- compatibility with downstream jobs that expect Maven-style paths such as `target/app.jar`.

## Deferred Decisions

These are intentionally not blockers for starting Phase 1:

- final production Bazel publishing mechanism;
- final replacement for `license-maven-plugin`;
- final replacement for `spring-boot-maven-plugin` in downstream apps;
- final Sonar/coverage report ingestion format;
- `nv-boot-managed-parent` migration details.

