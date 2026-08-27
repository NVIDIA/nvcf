@observability @compute @ncp-local @multi-cluster @helmfile
Feature: Install local Helmfile observability with the compute profile
  As a self-managed NVCF operator,
  I want to install the compute observability profile on a local split-cluster
  topology,
  so that the compute plane exports workload and NVCA metrics without running
  control-plane-only observability components.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
      | NVCF_CLI        |
      | REPO_ROOT       |
    # Helmfile pulls OCI charts during installation. Authenticate before sync
    # without exposing the current API key in command arguments or logs.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Install only control-plane prerequisites on ncp-local-cp. Shared
    # observability is installed separately on the compute cluster below.
    And I prepare Helmfile environment "local-bdd-observability-compute" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | addons.llm.enabled              | false                                |
      | observability.profile           | disabled                             |
    # Configure the shared observability stack for compute-plane monitors.
    And I prepare Helmfile environment "local-bdd-observability-compute" for stack "observability" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | compute                              |
    # Configure NVCA to use the same compute observability profile.
    And I prepare Helmfile environment "local-bdd-observability-compute" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
      | global.imagePullSecrets[0].name                       | nvcr-pull-secret                     |
      | global.helm.sources.repository                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                               | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.nvcaOperator.selfManaged.otelCollector.enabled | true                                 |
      | observability.profile                                 | compute                              |
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-observability-compute-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
    # Conflict precheck: single-cluster ncp-local claims host ports used by the
    # split topology. From the repository root, run
    # `make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local`
    # before retrying.
    Given I run command "k3d cluster get ncp-local"
    And the command exit code should be 1
    And multi-cluster ncp-local compute clusters are running:
      | ncp-local-compute-1 |
    # Write isolated kubeconfig files so installs and registration never rely
    # on whichever context is current in the operator's default kubeconfig.
    And command has succeeded:
      """
      k3d kubeconfig merge ncp-local-cp --output ${REPO_ROOT}/tests/bdd/out/ncp-local-cp-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false
      """
    And command has succeeded:
      """
      k3d kubeconfig merge ncp-local-compute-1 --output ${REPO_ROOT}/tests/bdd/out/ncp-local-compute-1-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false
      """
    And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local-cp":
      | cassandra-system |
      | nats-system      |
      | nvcf             |
      | api-keys         |
      | ess              |
      | sis              |
      | vault-system     |
      | cert-manager     |
    And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local-compute-1":
      | monitoring    |
      | nvca-operator |

  Scenario: Compute profile installs shared infrastructure and compute monitors
    When I run command:
      """
      make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-compute KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-cp-kubeconfig.yaml
      """
    Then the command exit code should be 0

    When I run command:
      """
      make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-compute-1-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """
    Then the command exit code should be 0
    And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should exist
    And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should have non-empty keys:
      | key            |
      | clusterID      |
      | clusterGroupID |

    When I run command:
      """
      make -C deploy/stacks/observability install HELMFILE_ENV=local-bdd-observability-compute KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-compute-1-kubeconfig.yaml
      """
    Then the command exit code should be 0

    When I run command:
      """
      make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd-observability-compute KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-compute-1-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """
    Then the command exit code should be 0

    # Self-hosted NVCA intentionally creates an empty NGC service-key secret.
    # The local compute-profile test supplies its existing NGC credential so
    # the collector's bearer-token extension can start. Keep $NGC_API_KEY
    # unbraced so the BDD runner does not expand it into command logs.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | kubectl --context k3d-ncp-local-compute-1 create secret generic ngc-service-api-key --namespace nvca-system --from-file=ngc-service-api-key=/dev/stdin --dry-run=client -o yaml | kubectl --context k3d-ncp-local-compute-1 apply -f -'
      """
    And command has succeeded:
      """
      kubectl --context k3d-ncp-local-compute-1 delete pod --namespace nvca-system --selector app.kubernetes.io/name=nvca --wait=false
      """

    Then these Helm releases should be deployed using context "k3d-ncp-local-compute-1":
      | name                     | namespace     |
      | prometheus-operator-crds | monitoring    |
      | opentelemetry-operator   | monitoring    |
      | victoria-metrics         | monitoring    |
      | otel-collector           | monitoring    |
      | default-monitors         | monitoring    |
      | nvca-operator            | nvca-operator |

    Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should complete rollout within "10m"
    Then NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

    Then Kubernetes resource "OpenTelemetryCollector/nvcf-observability" in namespace "monitoring" using context "k3d-ncp-local-compute-1" should contain:
      """
      spec:
        targetAllocator:
          enabled: true
      """

    Then these Kubernetes resources should exist in namespace "monitoring" using context "k3d-ncp-local-compute-1":
      | kind           | name                          |
      | ServiceMonitor | nvcf-default-monitors-nvca    |
      | PodMonitor     | nvcf-default-monitors-dcgm    |
      | PodMonitor     | nvcf-default-monitors-worker  |

    Then these Kubernetes resources should not exist in namespace "monitoring" using context "k3d-ncp-local-compute-1":
      | kind           | name                                             |
      | ServiceMonitor | nvcf-default-monitors-state-metrics              |
      | ServiceMonitor | nvcf-default-monitors-grpc-proxy                  |
      | ServiceMonitor | nvcf-default-monitors-llm-api-gateway             |
      | ServiceMonitor | nvcf-default-monitors-invocation-service          |

    Then Helm release "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should contain values:
      """
      selfManaged:
        otelCollector:
          enabled: true
          imageRepository: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-otel-collector
      """

    When I run command "helm status function-autoscaler --namespace nvcf --kube-context k3d-ncp-local-compute-1"
    Then the command exit code should be 1
    And the command output should contain "release: not found"
