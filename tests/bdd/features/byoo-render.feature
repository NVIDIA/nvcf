@byoo @observability @render-only
Feature: Render the BYOO feature gate in the compute-plane stack
  As a self-managed NVCF operator,
  I want the compute and all observability profiles to enable BYOO,
  so that tenants can attach telemetry endpoints without extra NVCA settings.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
      | REPO_ROOT       |
    # Helmfile pulls OCI charts during rendering. Authenticate before render
    # without exposing the current API key in command arguments or logs.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Seed the compute stack's registration handoff without contacting ICMS.
    # The Make target copies this input to OUTPUT_DIR before Helmfile evaluates it.
    And I copy the file "tests/bdd/fixtures/ncp-local-register-values.yaml" to "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml"

  Scenario Outline: The <profile> profile renders NVCA with the BYOObservability gate <state>
    Given I prepare Helmfile environment "local-bdd-byoo-<profile>" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | <profile>                            |
    When I successfully run command:
      """
      make -C deploy/stacks/nvcf-compute-plane template CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-byoo-<profile> OUTPUT_DIR=${REPO_ROOT}/tests/bdd/out/byoo-render/<profile>
      """
    Then the rendered manifests in "tests/bdd/out/byoo-render/<profile>" should <assertion>:
      | text             |
      | BYOObservability |

    Examples:
      | profile  | state | assertion   |
      | compute  | on    | contain     |
      | all      | on    | contain     |
      | control  | off   | not contain |
      | disabled | off   | not contain |
