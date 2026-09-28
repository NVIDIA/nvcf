@ncp-local @single-cluster @helmfile @ha
Feature: Install a local single-cluster NVCF stack with high availability enabled
  As a self-managed NVCF operator,
  I want highAvailability.mode to scale, spread, and protect the control plane,
  so that a single node loss or drain does not take a replica-safe service down.

  # Runtime check of the HA value layer on the ncp-local topology. The
  # render-level wiring is covered by deploy/stacks/self-managed/tests; this
  # feature proves the installed objects and the scheduler agree. nvcf-api is
  # the representative replica-safe Deployment. Draining nodes to prove the PDB
  # blocks the last eviction is intentionally out of scope here because it
  # would disrupt the shared stack.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
    # Start from the single-cluster local fixture, which pins HA off, and
    # turn it on. preferred keeps placement soft so the whole stack still
    # schedules on the local agents.
    And I prepare Helmfile environment "local-bdd-ha" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | highAvailability.mode           | preferred                            |
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-ha-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
    # Conflict precheck: ncp-local-cp's k3d serverlb claims
    # 0.0.0.0:8080/8443/10081, NATS on 4222, and the worker
    # callback port 10086, overlapping host ports single-cluster
    # ncp-local needs. Fail loudly so the operator runs
    # `make -C tools/ncp-local-cluster destroy-multicluster`
    # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
    And I run command "k3d cluster get ncp-local-cp"
    And the command exit code should be 1
    And a single-cluster ncp-local cluster is running
    And the "nvcr-pull-secret" image pull secret exists in namespaces:
      | cassandra-system |
      | nats-system      |
      | nvcf             |
      | api-keys         |
      | ess              |
      | sis              |
      | vault-system     |
      | nvca-operator    |
      | cert-manager     |

  Scenario: Operator installs the control plane with highAvailability.mode preferred
    When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-ha"

    Then deployment "nvcf-api" in namespace "nvcf" using context "k3d-ncp-local" should complete rollout within "10m"
    And Kubernetes resource "deployment/nvcf-api" in namespace "nvcf" using context "k3d-ncp-local" should contain:
      """
      spec:
        replicas: 2
        strategy:
          type: RollingUpdate
          rollingUpdate:
            maxUnavailable: 0
            maxSurge: 1
      """
    And Kubernetes resource "poddisruptionbudget/nvcf-api" in namespace "nvcf" using context "k3d-ncp-local" should contain:
      """
      spec:
        minAvailable: 1
      """

    # Soft hostname anti-affinity must still land each replica on its own node.
    When I successfully run command:
      """
      /bin/bash -c 'kubectl --context k3d-ncp-local get pods --namespace nvcf -l app.kubernetes.io/instance=api,app.kubernetes.io/name=nvcf-api -o json | bash tests/bdd/scripts/assert-ha-placement.sh 2'
      """
    Then the command output should contain "ha-placement=ok replicas=2 nodes=2"
