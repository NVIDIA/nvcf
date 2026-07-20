# CI Bazel Handoff

This repository should keep Maven as the default CI build and publish path
during coexistence. Bazel should run only when a project or pipeline opts in
with:

```yaml
variables:
  ENABLE_BAZEL_BUILD: "true"
  BAZEL_CI_IMAGE: "gitlab-master.nvidia.com:5005/nvcf/bazel-ci-templates/bazel-ci:0.11.0"
```

The current `.gitlab-ci.yml` includes the shared
`cds/cicd-pipelines` Java library pipeline and this branch has a project-level
trial job named `bazel-build-test`. The same job shape is intended for the
shared pipeline later; it is not an immediate replacement for Maven build or
deploy jobs.

## Runner Requirements

- Bazel 9.1.1, usually through `.bazelversion` plus Bazelisk or an equivalent
- Bazel 9.1.1 is preferred. If the trial image has a different Bazel version,
  the job warns and continues so the first pipeline can still expose the next
  compatibility issue.
- Java 25. The repo uses Bazel `remotejdk_25`, so `java` does not have to be on
  the image `PATH` for the Bazel trial job.
- The Maven-shaped jar packaging rules use Bazel's Java toolchain `singlejar`,
  so `jar` does not have to be on the image `PATH` for the Bazel trial job.
- Docker available on test runners. The Cassandra/Testcontainers test target
  requires Docker. The Docker CLI is useful for diagnostics but is not required
  by Testcontainers when `DOCKER_HOST` is configured.
- Bazel tests run with a restricted test environment. The CI job must pass the
  Docker/Testcontainers variables through with `--test_env=DOCKER_HOST`,
  `--test_env=DOCKER_TLS_VERIFY`, `--test_env=DOCKER_TLS_CERTDIR`, and
  `--test_env=DOCKER_CERT_PATH`.
- The project-local trial job uses the NVCF `bazel-ci` image through
  `BAZEL_CI_IMAGE`
  (`gitlab-master.nvidia.com:5005/nvcf/bazel-ci-templates/bazel-ci:0.11.0`), which ships
  bazelisk (runs the repo `.bazelversion`, 9.1.1), Temurin JDK 25, and Maven.
  It can be retried with another Bazel-capable image without a code change.
- The project-local trial job uses `BAZEL_CI_IMAGE` with a
  `docker:${DOCKER_VERSION}-dind` service, matching the DIND shape from the
  shared NVCF pipeline.
- Network access to the same Maven repositories used by Maven builds.
- Checked-in Bazel dependency state must be honored:
  - `MODULE.bazel`
  - `MODULE.bazel.lock`
  - `maven_install.json`

## Remote Cache

The `bazel-build-test` job reads and writes a shared Bazel remote cache (the
NVCF EC2 Buildbarn cache) when configured, and falls back to a local build if
the cache is unreachable or unauthenticated. It never hard-fails on a cache
problem.

Configuration comes from CI/CD variables:

- `NVCF_BAZEL_REMOTE`: `1` to enable, `0` to force local-only.
- `NVCF_BAZEL_REMOTE_HOST` / `NVCF_BAZEL_REMOTE_PORT`: the cache endpoint.
- `NVCF_BAZEL_REMOTE_TLS`: `1` for `grpcs://`.
- `BAZEL_REMOTE_CA_PEM`: file variable with the cache TLS CA (the cache uses a
  self-signed cert; this pins it).
- `BAZEL_REMOTE_CACHE_TOKEN`: masked bearer token for the cache.

Before building, the job runs an authenticated `GetCapabilities` probe. On
success it writes a `--bazelrc` with `--remote_cache`, `--tls_certificate`,
`--remote_header=authorization=Bearer ...`, cache compression,
`--remote_download_all`, `--remote_timeout`, `--remote_retries`, and
`--remote_local_fallback`; both `bazel test` and `bazel build` use it. On probe
failure it builds locally. `BAZEL_REMOTE_CACHE_TOKEN` must match the token the
cache currently enforces; if the cache rotates its token, refresh this variable.

## Opt-In GitLab Job Shape

```yaml
bazel-build-test:
  image: "${BAZEL_CI_IMAGE}"
  stage: test
  tags:
    - prod
    - eks
    - nvcf-cds
    - dind
  services:
    - name: docker:${DOCKER_VERSION}-dind
      alias: docker
      command:
        - "--mtu=1400"
        - "--tls=true"
      variables:
        HEALTHCHECK_TCP_PORT: "2376"
  rules:
    - if: '$ENABLE_BAZEL_BUILD == "true"'
  variables:
    # Keep Bazel's real output/cache tree outside the checkout. A repo-local
    # directory is traversed by `//...` and Bazel will try to load packages from
    # its own embedded tools/external repos.
    BAZEL_OUTPUT_USER_ROOT: "/tmp/nv-boot-parent-bazel-cache"
    DOCKER_HOST: tcp://docker:2376
    DOCKER_DRIVER: overlay2
    DOCKER_TLS_VERIFY: "true"
    DOCKER_TLS_CERTDIR: "/builds/certs"
    DOCKER_CERT_PATH: "/builds/certs/client"
  script:
    - |
      if ! command -v bazel >/dev/null 2>&1; then
        echo "ERROR: bazel is not installed in BAZEL_CI_IMAGE=${BAZEL_CI_IMAGE}"
        echo "Set BAZEL_CI_IMAGE to a CI image that contains Bazel 9.1.1."
        exit 1
      fi
    - bazel --version
    - |
      EXPECTED_BAZEL_VERSION="$(cat .bazelversion)"
      ACTUAL_BAZEL_VERSION="$(bazel --version | awk '{print $2}')"
      if [ "${ACTUAL_BAZEL_VERSION}" != "${EXPECTED_BAZEL_VERSION}" ]; then
        echo "WARNING: .bazelversion requests ${EXPECTED_BAZEL_VERSION}, but BAZEL_CI_IMAGE=${BAZEL_CI_IMAGE} provides ${ACTUAL_BAZEL_VERSION}."
        echo "If analysis or tests fail, rerun with BAZEL_CI_IMAGE set to an image that provides Bazel ${EXPECTED_BAZEL_VERSION}."
      fi
    - |
      if command -v java >/dev/null 2>&1; then
        java -version
      else
        echo "java is not on PATH; continuing because .bazelrc uses Bazel remotejdk_25."
      fi
    - |
      if command -v docker >/dev/null 2>&1; then
        docker version
      else
        echo "docker CLI is not on PATH; continuing because Testcontainers uses DOCKER_HOST=${DOCKER_HOST}."
      fi
    - |
      set +e
      bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test \
        --cache_test_results=no \
        --test_output=errors \
        --test_env=DOCKER_HOST \
        --test_env=DOCKER_TLS_VERIFY \
        --test_env=DOCKER_TLS_CERTDIR \
        --test_env=DOCKER_CERT_PATH \
        //...
      BAZEL_TEST_STATUS=$?
      bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //...
      BAZEL_BUILD_STATUS=$?
      set -e

      mkdir -p bazel-ci-artifacts
      if [ -e bazel-bin ]; then
        cp -RL bazel-bin bazel-ci-artifacts/bazel-bin
      fi
      if [ -e bazel-testlogs ]; then
        cp -RL bazel-testlogs bazel-ci-artifacts/bazel-testlogs
      fi

      BAZEL_JACOCO_XMLS=""
      if [ -d bazel-ci-artifacts/bazel-testlogs ]; then
        BAZEL_JACOCO_XMLS="$(find bazel-ci-artifacts/bazel-testlogs -path '*/test.outputs/jacoco.xml' | sort | paste -sd, -)"
      fi
      echo "Bazel JaCoCo XML reports: ${BAZEL_JACOCO_XMLS}"
      printf 'sonar.coverage.jacoco.xmlReportPaths=%s\n' "${BAZEL_JACOCO_XMLS}" > bazel-sonar.properties

      if [ "${BAZEL_TEST_STATUS}" -ne 0 ]; then
        exit "${BAZEL_TEST_STATUS}"
      fi
      exit "${BAZEL_BUILD_STATUS}"
  artifacts:
    when: always
    expire_in: 7 days
    paths:
      - bazel-ci-artifacts/
      - bazel-sonar.properties
    reports:
      junit:
        - bazel-ci-artifacts/bazel-testlogs/**/test.outputs/junit/TEST-junit-jupiter.xml
```

`bazel test //...` builds and runs the test targets. `bazel build //...` is
still required when CI also wants non-test outputs, including Maven-shaped jars,
sources jars, and generated POMs under `bazel-bin/`.

## Sonar Coverage Wiring

For this repository, Sonar should consume the JaCoCo XML reports generated by
ordinary `bazel test` runs:

```text
bazel-testlogs/<module>/tests/test.outputs/jacoco.xml
```

Use:

```text
sonar.coverage.jacoco.xmlReportPaths=<comma-separated-jacoco.xml-paths>
```

The job snippet writes that value to `bazel-sonar.properties` so the shared
Sonar job can source it, append it to its scanner arguments, or publish it as a
debug artifact.

Do not use `bazel coverage //...` as the primary Java coverage path for
`nv-boot-parent`. The shared `nv_boot_library_test` macro runs JUnit
ConsoleLauncher directly with `use_testrunner = False`, so Bazel native Java
coverage does not collect meaningful coverage for those targets. The
`tools/bazel/lcov_to_sonar_generic.py` converter remains useful for future
standard Bazel `java_test` targets or mixed workspaces.

## Artifact Expectations

The Bazel job should copy Bazel's symlinked output trees into
`bazel-ci-artifacts/` and publish these paths as CI artifacts:

```text
bazel-ci-artifacts/bazel-testlogs/**/test.log
bazel-ci-artifacts/bazel-testlogs/**/test.outputs/junit/TEST-junit-jupiter.xml
bazel-ci-artifacts/bazel-testlogs/**/test.outputs/index.html
bazel-ci-artifacts/bazel-testlogs/**/test.outputs/jacoco.xml
bazel-ci-artifacts/bazel-testlogs/**/test.outputs/jacoco.exec
bazel-ci-artifacts/bazel-bin/**/*.jar
bazel-ci-artifacts/bazel-bin/**/*.pom
bazel-ci-artifacts/bazel-bin/**/*-sources.jar
```

Do not publish `bazel-testlogs/**/test.xml` as JUnit. It reports the one outer
`sh_test` wrapper, while ConsoleLauncher writes the real testcases and counts
to `test.outputs/junit/TEST-junit-jupiter.xml`.

Do not set `BAZEL_OUTPUT_USER_ROOT` under `$CI_PROJECT_DIR`. Unlike Bazel's
standard `bazel-*` symlinks, a real repo-local cache directory is included by
the `//...` package walk. The failure mode is Bazel trying to load packages from
its own installation or external repositories, for example
`.bazel-cache/install/.../embedded_tools/...` or
`.bazel-cache/.../external/rules_python+...`.

The HTML coverage entry point for each module is:

```text
bazel-testlogs/<module>/tests/test.outputs/index.html
```

## Publishing During Coexistence

Maven deploy remains the publish path for Maven consumers during coexistence.
Do not add a Bazel job that publishes Maven-shaped jars to URM/Artifactory.

The corrected Bazel model is:

- `bazel-build-test` proves the Bazel target graph, tests, coverage reports,
  and local artifact generation.
- Existing Maven jobs continue to publish Maven artifacts for Maven consumers.
- Downstream Bazel consumers use Bzlmod source dependencies, such as
  `git_override`, to consume `nv-boot-parent` Bazel targets directly.

A temporary Bazel remote-publish experiment did prove that generated artifacts
could be consumed by `cloud-tasks` as Maven artifacts with version `15665e3b`,
but that bridge has been removed and should not be recreated unless the
migration strategy changes again.

## Maven Coexistence Guardrails

- Existing Maven build/test/publish jobs should continue to run by default.
- `ENABLE_BAZEL_BUILD: "true"` should add the Bazel job; it should not disable
  Maven jobs unless a later cutover explicitly changes that behavior.
- For the temporary branch-only `feat/bazel` validation path, the workflow rule
  sets `NEXT_VERSION` to `${CI_COMMIT_SHORT_SHA}`. That mirrors the shared
  `compute-next-release-version` fallback for non-default branches and prevents
  inherited Maven jobs from failing before Maven starts.
- Bazel failures should be blocking only for opt-in pipelines at first.
- For merge requests, the Bazel job can start as manual or allowed-to-fail if
  the CI team wants a softer rollout. Once runner/tooling stability is proven,
  make the opt-in job required when the flag is set.
