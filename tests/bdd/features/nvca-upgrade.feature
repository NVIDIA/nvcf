# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

Feature: Agent reports preserve an NGC-managed NVCA deployment target
  A running agent reports its current version while the operator may have a
  different deployment target. Registration and heartbeats must preserve that
  target, including an explicitly requested downgrade.

  # Run this whole feature against a fresh dedicated lab. Later scenarios use
  # the cluster created by the first scenario. ICMS and Cassandra are real;
  # the identity/NGC route fixture and CPU-only agent probe are test components.
  Background:
    Given these environment variables are set:
      | name                         |
      | REPO_ROOT                    |
      | NVCA_BDD_KUBECONFIG           |
      | NVCA_BDD_GATEWAY              |
      | NVCA_BDD_OPERATOR_REPOSITORY  |
      | NVCA_BDD_PROBE_REPOSITORY     |
      | NVCA_BDD_VERSION_A           |
      | NVCA_BDD_VERSION_B           |

  Scenario: Start an NGC-managed cluster with the older probe version
    When I send a "POST" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters" with body:
      """
      {"clusterName":"bdd-upgrade","clusterGroupName":"bdd-upgrade","ncaId":"bdd-account","cloudProvider":"ON-PREM","region":"us-west-1","nvcaVersion":"${NVCA_BDD_VERSION_A}","clusterSource":"ngc-managed","capabilities":["DynamicGPUDiscovery"],"gpus":[]}
      """
    Then the HTTP status should be 200
    When I export the HTTP response JSON field "clusterId" to environment variable "NVCA_BDD_CLUSTER_ID"
    And I successfully run command:
      """
      helm upgrade --install nvca-upgrade-operator ${REPO_ROOT}/deploy/helm/nvca-operator/nvca-operator --kubeconfig ${NVCA_BDD_KUBECONFIG} --namespace nvca-operator --create-namespace --values ${REPO_ROOT}/tests/bdd/fixtures/nvca-upgrade/operator.yaml --set image.repository=${NVCA_BDD_OPERATOR_REPOSITORY} --set nvcaImage.repositoryOverride=${NVCA_BDD_PROBE_REPOSITORY} --set clusterID=${NVCA_BDD_CLUSTER_ID}
      """
    # The secret-mirroring sidecar is unrelated to this regression. The writable
    # /tmp is needed by the operator's projected-token and webhook setup.
    And I successfully run command:
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator patch deployment nvca-upgrade-operator --type=strategic -p '{"spec":{"template":{"spec":{"terminationGracePeriodSeconds":5,"containers":[{"name":"nvca-mirror","$patch":"delete"},{"name":"nvca-upgrade-operator","env":[{"name":"NVCA_CLUSTER_API_REFRESH_INTERVAL","value":"2s"}],"volumeMounts":[{"name":"lab-tmp","mountPath":"/tmp"}]}],"volumes":[{"name":"lab-tmp","emptyDir":{}}]}}}}'
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator rollout status deployment/nvca-upgrade-operator --timeout=180s"
    And I poll command for up to 180 seconds until output contains "${NVCA_BDD_PROBE_REPOSITORY}:${NVCA_BDD_VERSION_A}":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system get deployment nvca -o 'jsonpath={.spec.template.spec.containers[?(@.name=="agent")].image}'
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system rollout status deployment/nvca --timeout=180s"

  Scenario: An old-agent registration and heartbeat preserve a pending upgrade
    When I run command at scenario teardown with timeout "30s":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=1
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=0"
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator wait --for=delete pod --selector=app.kubernetes.io/instance=nvca-upgrade-operator --timeout=60s"
    And I send a "PUT" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}" with body:
      """
      {"clusterGroupName":"bdd-upgrade","cloudProvider":"ON-PREM","region":"us-west-1","nvcaVersion":"${NVCA_BDD_VERSION_B}","clusterSource":"ngc-managed","capabilities":["DynamicGPUDiscovery"],"gpus":[]}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}"
    Then the HTTP status should be 200
    And the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_B}"
    When I send a "PUT" request to "${NVCA_BDD_GATEWAY}/v1/nvca/clusters/${NVCA_BDD_CLUSTER_ID}/register" with body:
      """
      {"status":"READY","k8sVersion":"v1.30.0","nvcaVersion":"${NVCA_BDD_VERSION_A}","gpus":[{"name":"bdd-probe","capacity":0,"instanceTypes":[{"name":"bdd-probe","value":"bdd-probe","default":true,"cpuCores":1,"systemMemory":"1Gi","gpuMemory":"1Gi","gpuCount":1}]}]}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}"
    Then the HTTP status should be 200
    And the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_B}"
    When I send a "POST" request to "${NVCA_BDD_GATEWAY}/v1/nvca/clusters/${NVCA_BDD_CLUSTER_ID}/heartbeat" with body:
      """
      {"status":"healthy","gpuUsage":{},"nvcaAgentVersion":"${NVCA_BDD_VERSION_A}"}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}"
    Then the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_B}"
    When I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=1"
    And I poll command for up to 180 seconds until output contains "${NVCA_BDD_VERSION_B}":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator get nvcfbackend bdd-upgrade -o jsonpath={.spec.version}
      """
    And I poll command for up to 180 seconds until output contains "${NVCA_BDD_PROBE_REPOSITORY}:${NVCA_BDD_VERSION_B}":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system get deployment nvca -o 'jsonpath={.spec.template.spec.containers[?(@.name=="agent")].image}'
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system rollout status deployment/nvca --timeout=180s"

  Scenario: A newer-agent registration preserves an explicitly requested downgrade
    When I run command at scenario teardown with timeout "30s":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=1
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=0"
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator wait --for=delete pod --selector=app.kubernetes.io/instance=nvca-upgrade-operator --timeout=60s"
    And I send a "PUT" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}" with body:
      """
      {"clusterGroupName":"bdd-upgrade","cloudProvider":"ON-PREM","region":"us-west-1","nvcaVersion":"${NVCA_BDD_VERSION_A}","clusterSource":"ngc-managed","capabilities":["DynamicGPUDiscovery"],"gpus":[]}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}"
    Then the HTTP status should be 200
    And the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_A}"
    When I send a "PUT" request to "${NVCA_BDD_GATEWAY}/v1/nvca/clusters/${NVCA_BDD_CLUSTER_ID}/register" with body:
      """
      {"status":"READY","k8sVersion":"v1.30.0","nvcaVersion":"${NVCA_BDD_VERSION_B}","gpus":[{"name":"bdd-probe","capacity":0,"instanceTypes":[{"name":"bdd-probe","value":"bdd-probe","default":true,"cpuCores":1,"systemMemory":"1Gi","gpuMemory":"1Gi","gpuCount":1}]}]}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_CLUSTER_ID}"
    Then the HTTP status should be 200
    And the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_A}"
    When I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator scale deployment/nvca-upgrade-operator --replicas=1"
    And I poll command for up to 180 seconds until output contains "${NVCA_BDD_VERSION_A}":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-operator get nvcfbackend bdd-upgrade -o jsonpath={.spec.version}
      """
    And I poll command for up to 180 seconds until output contains "${NVCA_BDD_PROBE_REPOSITORY}:${NVCA_BDD_VERSION_A}":
      """
      kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system get deployment nvca -o 'jsonpath={.spec.template.spec.containers[?(@.name=="agent")].image}'
      """
    And I successfully run command "kubectl --kubeconfig ${NVCA_BDD_KUBECONFIG} -n nvca-system rollout status deployment/nvca --timeout=180s"

  Scenario: Helm-managed registration continues reporting the running version
    When I send a "POST" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters" with body:
      """
      {"clusterName":"bdd-helm-report","clusterGroupName":"bdd-helm-report","ncaId":"bdd-account","cloudProvider":"ON-PREM","region":"us-west-1","nvcaVersion":"${NVCA_BDD_VERSION_A}","clusterSource":"helm-managed","capabilities":["DynamicGPUDiscovery"],"gpus":[]}
      """
    Then the HTTP status should be 200
    When I export the HTTP response JSON field "clusterId" to environment variable "NVCA_BDD_HELM_CLUSTER_ID"
    And I send a "PUT" request to "${NVCA_BDD_GATEWAY}/v1/nvca/clusters/${NVCA_BDD_HELM_CLUSTER_ID}/register" with body:
      """
      {"status":"READY","k8sVersion":"v1.30.0","nvcaVersion":"${NVCA_BDD_VERSION_B}","gpus":[{"name":"bdd-probe","capacity":0,"instanceTypes":[{"name":"bdd-probe","value":"bdd-probe","default":true,"cpuCores":1,"systemMemory":"1Gi","gpuMemory":"1Gi","gpuCount":1}]}]}
      """
    Then the HTTP status should be 200
    When I send a "GET" request to "${NVCA_BDD_GATEWAY}/v1/accounts/bdd-account/clusters/${NVCA_BDD_HELM_CLUSTER_ID}"
    Then the HTTP status should be 200
    And the HTTP response JSON field "clusterSource" should equal "helm-managed"
    And the HTTP response JSON field "nvcaVersion" should equal "${NVCA_BDD_VERSION_B}"
