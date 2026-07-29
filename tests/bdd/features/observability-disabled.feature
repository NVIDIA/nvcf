@observability @disabled @render-only
Feature: Render local Helmfile stacks with observability disabled
  As a self-managed NVCF operator,
  I want to render both local Helmfile stacks with observability disabled,
  so that I can verify the profile does not add observability resources.

  Background:
    Given environment variable "NGC_API_KEY" is set
    And environment variable "SAMPLE_NGC_ORG" is set
    And environment variable "SAMPLE_NGC_TEAM" is set
    And environment variable "REPO_ROOT" is set
    # Helmfile pulls OCI charts during rendering. Keep $NGC_API_KEY unbraced
    # so the BDD runner does not expand it into command logs.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | helm registry login nvcr.io --username "\$oauthtoken" --password-stdin'
      """
    # The public stack ships Makefile.dist. Copy it for this render-only run so
    # the ledger restores the untracked development Makefile state afterward.
    And I copy the file "deploy/stacks/self-managed/Makefile.dist" to "deploy/stacks/self-managed/Makefile"
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-observability-disabled.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-observability-disabled.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | disabled                             |
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-observability-disabled-secrets.yaml"
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-observability-disabled-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-disabled.yaml"
    And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-disabled.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | disabled                             |
    # The Make target checks registration/ while the Helmfile reads out/.
    # Rendering does not contact ICMS, so seed both handoff paths from a stable
    # local fixture and let the ledger restore them after the scenario.
    And I copy the file "tests/bdd/fixtures/ncp-local-register-values.yaml" to "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml"
    And I copy the file "tests/bdd/fixtures/ncp-local-register-values.yaml" to "deploy/stacks/nvcf-compute-plane/out/ncp-local-register-values.yaml"

  Scenario: Disabled profile renders no observability resources
    When I run command:
      """
      make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-observability-disabled OUTPUT_DIR=${REPO_ROOT}/tests/bdd/out/observability-disabled/control-plane
      """
    Then the command exit code should be 0

    When I run command:
      """
      make -C deploy/stacks/nvcf-compute-plane template CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-observability-disabled OUTPUT_DIR=${REPO_ROOT}/tests/bdd/out/observability-disabled/compute-plane
      """
    Then the command exit code should be 0

    When I run command "rg --fixed-strings 'name: function-autoscaler' tests/bdd/out/observability-disabled"
    Then the command exit code should be 1

    When I run command "rg --fixed-strings 'kind: OpenTelemetryCollector' tests/bdd/out/observability-disabled"
    Then the command exit code should be 1

    When I run command "rg --fixed-strings 'kind: ServiceMonitor' tests/bdd/out/observability-disabled"
    Then the command exit code should be 1

    When I run command "rg --fixed-strings 'kind: PodMonitor' tests/bdd/out/observability-disabled"
    Then the command exit code should be 1

    When I run command "rg --fixed-strings 'BYOObservability' tests/bdd/out/observability-disabled"
    Then the command exit code should be 1
