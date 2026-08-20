@observability @all @ncp-local @single-cluster @helmfile
Feature: Install local Helmfile observability for both planes
  As a self-managed NVCF operator,
  I want to install the all observability profile on one local cluster,
  so that the control and compute planes share one metrics stack and expose
  both monitor families.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
      | NVCF_CLI        |
      | REPO_ROOT       |
    # Helmfile pulls OCI charts during installation. Keep $NGC_API_KEY unbraced
    # so the BDD runner does not expand it into command logs.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | helm registry login nvcr.io --username "\$oauthtoken" --password-stdin'
      """
    # Configure the control-plane stack and its shared observability child.
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-observability-all.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-observability-all.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | all                                  |
      | functionAutoscaler.image.tag    | 1.18.10                              |
    # Give the shared observability Helmfile the same named environment.
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/observability/environments/local-bdd-observability-all.yaml"
    And I update yaml file "deploy/stacks/observability/environments/local-bdd-observability-all.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | all                                  |
    # Configure NVCA to join the same cluster and enable its collector.
    And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-all.yaml"
    And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd-observability-all.yaml" with keys:
      | global.imagePullSecrets[0].name                               | nvcr-pull-secret                                                  |
      | global.helm.sources.repository                                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                              |
      | global.image.repository                                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                              |
      | global.nvcaOperator.selfManaged.otelCollector.imageRepository | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-otel-collector |
      | observability.profile                                         | all                                                               |
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-observability-all-secrets.yaml"
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-observability-all-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    # Conflict precheck: the split topology claims host ports used by the
    # single-cluster topology. From the repository root, run
    # `make -C tools/ncp-local-cluster destroy-all-ncp-local SHELL=/bin/bash`
    # before retrying.
    Given I run command "k3d cluster get ncp-local-cp"
    And the command exit code should be 1
    And a single-cluster ncp-local cluster is running
    # Keep every install and registration operation off the ambient kube context.
    And command has succeeded:
      """
      k3d kubeconfig merge ncp-local --output ${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false
      """
    And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local":
      | cassandra-system |
      | nats-system      |
      | nvcf             |
      | api-keys         |
      | ess              |
      | sis              |
      | vault-system     |
      | nvca-operator    |
      | cert-manager     |
      | monitoring       |

  Scenario: All profile installs one shared stack with both monitor families
    When I run command:
      """
      make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml
      """
    Then the command exit code should be 0

    When I run command:
      """
      make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """
    Then the command exit code should be 0
    And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
    And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" key "clusterID" should not be empty
    And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" key "clusterGroupID" should not be empty

    When I run command:
      """
      make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """
    Then the command exit code should be 0

    # Self-hosted NVCA intentionally creates an empty NGC service-key secret.
    # Supply the existing local credential so the NVCA collector can start,
    # then restart NVCA to consume it. Keep $NGC_API_KEY out of command logs.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | kubectl --context k3d-ncp-local create secret generic ngc-service-api-key --namespace nvca-system --from-file=ngc-service-api-key=/dev/stdin --dry-run=client -o yaml | kubectl --context k3d-ncp-local apply -f -'
      """
    And command has succeeded:
      """
      kubectl --context k3d-ncp-local delete pod --namespace nvca-system --selector app.kubernetes.io/name=nvca --wait=false
      """

    # Revision 1 proves the compute install did not reinstall or upgrade the
    # shared observability releases created by the control-plane install.
    Then these Helm releases should be deployed using context "k3d-ncp-local":
      | name                     | namespace     | revision |
      | prometheus-operator-crds | monitoring    | 1        |
      | opentelemetry-operator   | monitoring    | 1        |
      | victoria-metrics         | monitoring    | 1        |
      | otel-collector           | monitoring    | 1        |
      | default-monitors         | monitoring    | 1        |
      | nvca-operator            | nvca-operator | 1        |

    When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --context k3d-ncp-local --timeout=10m"
    Then the command exit code should be 0
    When I run command "kubectl wait nvcfbackend ncp-local -n nvca-operator --context k3d-ncp-local --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
    Then the command exit code should be 0

    When I run command "kubectl get opentelemetrycollector nvcf-observability -n monitoring --context k3d-ncp-local -o jsonpath='{.spec.targetAllocator.enabled}'"
    Then the command exit code should be 0
    And the command output should contain "true"

    Then these Kubernetes resources should exist in namespace "monitoring" using context "k3d-ncp-local":
      | kind           | name                                             |
      | ServiceMonitor | nvcf-default-monitors-state-metrics              |
      | ServiceMonitor | nvcf-default-monitors-grpc-proxy                  |
      | ServiceMonitor | nvcf-default-monitors-llm-api-gateway             |
      | ServiceMonitor | nvcf-default-monitors-invocation-service          |
      | ServiceMonitor | nvcf-default-monitors-nvca                        |
      | PodMonitor     | nvcf-default-monitors-dcgm                        |
      | PodMonitor     | nvcf-default-monitors-worker                      |

    When I run command:
      """
      bash -c 'set -eo pipefail; helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local -o json | jq -r ".selfManaged.otelCollector.enabled"'
      """
    Then the command exit code should be 0
    And the command output should contain "true"
