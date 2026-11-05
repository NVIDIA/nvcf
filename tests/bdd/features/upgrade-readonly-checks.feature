@upgrade @existing-install
Feature: Repeat the downloadable upgrade checks against an installed stack
  As a self-managed operator,
  I want to capture and compare the installed state without changing it,
  so that I can reuse the upgrade checks at each documented checkpoint.

  Scenario: Check selected packages, workloads, retained identity and receipt
    Given these environment variables are set:
      | name                                 |
      | REPO_ROOT                            |
      | TARGET_BUNDLE                        |
      | CONTROL_STACK                        |
      | COMPUTE_STACK                        |
      | CONTROL_KUBECONFIG                    |
      | CONTROL_CONTEXT                      |
      | COMPUTE_KUBECONFIG                    |
      | COMPUTE_CONTEXT                      |
      | EXPECTED_CONTROL_STACK_VERSION       |
      | EXPECTED_COMPUTE_STACK_VERSION       |
      | EXPECTED_OBSERVABILITY_STACK_VERSION |
      | UPGRADE_RECORD                       |
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/check-upgrade-packages.py"
      """
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/collect-upgrade-state.py" "${UPGRADE_RECORD}/before"
      """
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/wait-upgrade-workloads.py" "${CONTROL_KUBECONFIG}" "${CONTROL_CONTEXT}" nvcf
      """
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/wait-upgrade-workloads.py" "${COMPUTE_KUBECONFIG}" "${COMPUTE_CONTEXT}" nvca-operator
      """
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/check-upgrade-receipt.py"
      """
    When I successfully run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/collect-upgrade-state.py" "${UPGRADE_RECORD}/after"
      """
    When I successfully run command:
      """
      diff -u "${UPGRADE_RECORD}/before/clusters.json" "${UPGRADE_RECORD}/after/clusters.json"
      """
    When I successfully run command:
      """
      diff -u "${UPGRADE_RECORD}/before/identity.json" "${UPGRADE_RECORD}/after/identity.json"
      """
    When I run command:
      """
      env EXPECTED_CONTROL_STACK_VERSION=not-the-selected-release python3 "${REPO_ROOT}/fern/assets/upgrade-checks/check-upgrade-receipt.py"
      """
    Then the command exit code should be 1
    When I run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/wait-upgrade-workloads.py" "${CONTROL_KUBECONFIG}" missing-upgrade-test-context nvcf
      """
    Then the command exit code should be 1
    When I run command:
      """
      python3 "${REPO_ROOT}/fern/assets/upgrade-checks/collect-upgrade-state.py" "${UPGRADE_RECORD}/before"
      """
    Then the command exit code should be 1
    When I successfully run command:
      """
      diff -u "${UPGRADE_RECORD}/before/identity.json" "${UPGRADE_RECORD}/after/identity.json"
      """
