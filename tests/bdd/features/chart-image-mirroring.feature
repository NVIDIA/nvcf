@image-catalog @registry-verification
Feature: Prepare verified mirror values for dependency charts
  As a self-managed NVCF operator,
  I want each dependency chart's images mapped to available mirror artifacts,
  so that deployment uses my approved registry while retaining tested versions.

  Background:
    Given these environment variables are set:
      | name                        |
      | REPO_ROOT                   |
      | IMAGE_CATALOG_TOOL          |
      | IMAGE_CATALOG_QA_ROOT       |
      | IMAGE_CATALOG_MIRROR_PREFIX |

  Scenario Outline: Operator generates verified mirror values for <component>
    When I successfully run command:
      """
      ${IMAGE_CATALOG_TOOL} images mirror --chart ${REPO_ROOT}/deploy/helm/<chart> -f ${IMAGE_CATALOG_QA_ROOT}/<profile>-source-values.yaml --mapping ${IMAGE_CATALOG_QA_ROOT}/<profile>-mapping.yaml --registry-cert-dir ${IMAGE_CATALOG_QA_ROOT}/trust
      """
    Then the command output should contain "${IMAGE_CATALOG_MIRROR_PREFIX}"

    Examples:
      | component    | chart          | profile      |
      | NATS         | nats           | nats         |
      | cert-manager | cert-manager   | cert-manager |
      | OpenBao      | openbao/helm   | openbao      |
      | Cassandra    | cassandra/helm | cassandra    |
