@observability @control @ncp-local @single-cluster @helmfile
Feature: Install local Helmfile observability with the control profile
  As a self-managed NVCF operator,
  I want to install the control observability profile on a local k3d cluster,
  so that the control plane has its shared metrics infrastructure and monitors.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
    # Helmfile pulls OCI charts during installation. Authenticate before sync
    # without exposing the current API key in command arguments or logs.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Set the self-managed stack environment.
    And I prepare Helmfile environment "local-bdd-observability-control" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | control                              |
    # Set the shared observability stack environment.
    And I prepare Helmfile environment "local-bdd-observability-control" for stack "observability" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | control                              |
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-observability-control-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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
    When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-control"

    Then these Helm releases should be deployed using context "k3d-ncp-local":
      | name                     | namespace  |
      | prometheus-operator-crds | monitoring |
      | opentelemetry-operator   | monitoring |
      | victoria-metrics         | monitoring |
      | otel-collector           | monitoring |
      | default-monitors         | monitoring |

    Then Kubernetes resource "OpenTelemetryCollector/nvcf-observability" in namespace "monitoring" using context "k3d-ncp-local" should contain:
      """
      spec:
        targetAllocator:
          enabled: true
      """

    Then these Kubernetes resources should exist in namespace "monitoring" using context "k3d-ncp-local":
      | kind           | name                                             |
      | ServiceMonitor | nvcf-default-monitors-state-metrics              |
      | ServiceMonitor | nvcf-default-monitors-grpc-proxy                  |
      | ServiceMonitor | nvcf-default-monitors-llm-api-gateway             |
      | ServiceMonitor | nvcf-default-monitors-invocation-service          |

    Then these Kubernetes resources should not exist in namespace "monitoring" using context "k3d-ncp-local":
      | kind           | name                          |
      | ServiceMonitor | nvcf-default-monitors-nvca    |
      | PodMonitor     | nvcf-default-monitors-dcgm    |
      | PodMonitor     | nvcf-default-monitors-worker  |
