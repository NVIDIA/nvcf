@observability @control @ncp-local @single-cluster @helmfile
Feature: Install local Helmfile observability with the control profile
  As a self-managed NVCF operator,
  I want to install the control observability profile on a local k3d cluster,
  so that the control plane has its shared metrics infrastructure and monitors.

  Background:
    Given environment variable "NGC_API_KEY" is set
    And environment variable "SAMPLE_NGC_ORG" is set
    And environment variable "SAMPLE_NGC_TEAM" is set
    # Helmfile pulls OCI charts during installation. Keep $NGC_API_KEY unbraced
    # so the BDD runner does not expand it into command logs.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | helm registry login nvcr.io --username "\$oauthtoken" --password-stdin'
      """
    # Set the self-managed stack environment.
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-observability-control.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-observability-control.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | control                              |
      | functionAutoscaler.image.tag    | 1.18.10                              |
    # Set the shared observability stack environment.
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/observability/environments/local-bdd-observability-control.yaml"
    And I update yaml file "deploy/stacks/observability/environments/local-bdd-observability-control.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | control                              |
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-observability-control-secrets.yaml"
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-observability-control-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    # Conflict precheck: ncp-local-cp claims host ports that overlap with this
    # single-cluster topology. From tools/ncp-local-cluster, run
    # `make destroy CLUSTER_NAME=ncp-local-cp` before retrying.
    Given I run command "k3d cluster get ncp-local-cp"
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
      | monitoring       |

  Scenario: Control profile installs shared infrastructure and control monitors
    When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-control"
    Then the command exit code should be 0

    When I run command "helm list --all-namespaces --kube-context k3d-ncp-local -o json"
    Then the json output should contain rows:
      | name                     | namespace  | status   |
      | prometheus-operator-crds | monitoring | deployed |
      | opentelemetry-operator   | monitoring | deployed |
      | victoria-metrics         | monitoring | deployed |
      | otel-collector           | monitoring | deployed |
      | default-monitors         | monitoring | deployed |

    When I run command "kubectl get opentelemetrycollector nvcf-observability -n monitoring --context k3d-ncp-local -o jsonpath='{.spec.targetAllocator.enabled}'"
    Then the command exit code should be 0
    And the command output should contain "true"

    Then these ServiceMonitors should exist in namespace "monitoring" using context "k3d-ncp-local":
      | name                                             |
      | nvcf-default-monitors-state-metrics              |
      | nvcf-default-monitors-grpc-proxy                  |
      | nvcf-default-monitors-llm-api-gateway             |
      | nvcf-default-monitors-invocation-service          |

    When I run command "kubectl get servicemonitor nvcf-default-monitors-nvca -n monitoring --context k3d-ncp-local"
    Then the command exit code should be 1
    And the command output should contain "NotFound"
    When I run command "kubectl get podmonitor nvcf-default-monitors-dcgm -n monitoring --context k3d-ncp-local"
    Then the command exit code should be 1
    And the command output should contain "NotFound"
    When I run command "kubectl get podmonitor nvcf-default-monitors-worker -n monitoring --context k3d-ncp-local"
    Then the command exit code should be 1
    And the command output should contain "NotFound"
