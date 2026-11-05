@image-values @helm
Feature: Deploy dependency charts with ordinary image values
  As a self-managed NVCF operator,
  I want prefilled image settings and ordinary Helm overrides,
  so that I can select my registry without an image configuration generator.

  Background:
    Given these environment variables are set:
      | name                       |
      | IMAGE_VALUES_CHART_ROOT    |
      | IMAGE_VALUES_QA_ROOT       |
      | IMAGE_VALUES_MIRROR_PREFIX |

  Scenario Outline: Operator upgrades <component> with a values file
    When I successfully run command:
      """
      helm upgrade --install <release> ${IMAGE_VALUES_CHART_ROOT}/<chart> --kubeconfig ${IMAGE_VALUES_QA_ROOT}/kubeconfig --namespace <namespace> -f ${IMAGE_VALUES_QA_ROOT}/<profile>-source-values.yaml -f ${IMAGE_VALUES_QA_ROOT}/<profile>-mirror-values.yaml --no-hooks --wait --timeout 5m
      """
    When I successfully run command:
      """
      kubectl --kubeconfig ${IMAGE_VALUES_QA_ROOT}/kubeconfig -n <namespace> get pods -o jsonpath='{range .items[*]}{range .spec.initContainers[*]}{.image}{"\n"}{end}{range .spec.containers[*]}{.image}{"\n"}{end}{end}'
      """
    Then the command output should contain "${IMAGE_VALUES_MIRROR_PREFIX}"
    And the command output should not contain "docker.io/"
    And the command output should not contain "quay.io/"
    And the command output should not contain "nvcr.io/"

    Examples:
      | component    | chart          | profile      | release              | namespace            |
      | NATS         | nats           | nats         | catalog-nats         | catalog-nats         |
      | cert-manager | cert-manager   | cert-manager | catalog-cert-manager | catalog-cert-manager |
      | OpenBao      | openbao/helm   | openbao      | catalog-openbao      | catalog-openbao      |
      | Cassandra    | cassandra/helm | cassandra    | catalog-cassandra    | catalog-cassandra    |
