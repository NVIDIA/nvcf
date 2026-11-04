@upgrade @existing-install
Feature: Export a profile before a compute-plane upgrade
  As a self-managed NVCF operator,
  I want to export the installed control-plane configuration with LLM disabled,
  so that I can upgrade compute without enabling an optional add-on.

  Scenario: A default installation provides a valid compute-reachable profile
    Given these environment variables are set:
      | name                    |
      | NVCF_CLI                |
      | UPGRADE_CLI_CONFIG      |
      | UPGRADE_STACK           |
      | UPGRADE_ENV             |
      | UPGRADE_CLUSTER         |
      | UPGRADE_KUBECONFIG      |
      | UPGRADE_CONTEXT         |
    And yaml file "${UPGRADE_STACK}/environments/${UPGRADE_ENV}.yaml" key "addons.llm.enabled" should equal "false"
    When I successfully run command:
      """
      /bin/bash -eu -c 'test "$(kubectl --kubeconfig "$1" config current-context)" = "$2"' upgrade-profile "${UPGRADE_KUBECONFIG}" "${UPGRADE_CONTEXT}"
      """
    And I successfully run command:
      """
      kubectl --kubeconfig "${UPGRADE_KUBECONFIG}" --context "${UPGRADE_CONTEXT}" -n vault-system wait --for=condition=Ready pod --selector app.kubernetes.io/instance=openbao-server,component=server --timeout=120s
      """
    And I successfully run command:
      """
      /usr/bin/env KUBECONFIG="${UPGRADE_KUBECONFIG}" "${NVCF_CLI}" --config "${UPGRADE_CLI_CONFIG}" self-hosted --control-plane-stack "${UPGRADE_STACK}" --env "${UPGRADE_ENV}" control-plane profile export --cluster-name "${UPGRADE_CLUSTER}"
      """
    Then I successfully run command:
      """
      "${NVCF_CLI}" --config "${UPGRADE_CLI_CONFIG}" self-hosted control-plane profile validate --file "${UPGRADE_STACK}/out/control-plane-profile.yaml" --require compute-reachable
      """
