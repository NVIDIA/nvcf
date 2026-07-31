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
    # Create the self-managed stack environment used by the control-plane render.
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-observability-disabled.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-observability-disabled.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | disabled                             |
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-observability-disabled-secrets.yaml"
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-observability-disabled-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    # Create the compute-plane stack environment used by the worker render.
    And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-disabled.yaml"
    And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-disabled.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | disabled                             |
    # Seed the Make target's required registration input without contacting ICMS.
    # The target validates this path before copying it to OUTPUT_DIR.
    And I copy the file "tests/bdd/fixtures/ncp-local-register-values.yaml" to "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml"
    # Seed the separate fixed path read by the NVCA Helmfile during evaluation.
    # The Helmfile does not use OUTPUT_DIR to locate this registration handoff.
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

    Then the rendered manifests in "tests/bdd/out/observability-disabled" should not contain:
      | text                         |
      | name: function-autoscaler    |
      | kind: OpenTelemetryCollector |
      | kind: ServiceMonitor         |
      | kind: PodMonitor             |
      | BYOObservability             |
