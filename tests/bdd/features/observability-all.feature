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
    # Helmfile pulls OCI charts during installation. Authenticate before sync
    # without exposing the current API key in command arguments or logs.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Configure the control-plane stack and its shared observability child.
    And I prepare Helmfile environment "local-bdd-observability-all" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | all                                  |
    # Give the shared observability Helmfile the same named environment.
    And I prepare Helmfile environment "local-bdd-observability-all" for stack "observability" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | observability.profile           | all                                  |
    # Configure NVCA to join the same cluster and enable its collector.
    And I prepare Helmfile environment "local-bdd-observability-all" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
      | global.imagePullSecrets[0].name                       | nvcr-pull-secret                     |
      | global.helm.sources.repository                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                               | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.nvcaOperator.selfManaged.otelCollector.enabled | true                                 |
      | observability.profile                                 | all                                  |
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-observability-all-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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
    When I successfully run command:
      """
      make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml
      """

    When I successfully run command:
      """
      env KUBECONFIG=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-observability-all control-plane profile export --cluster-name ncp-local
      """
    Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

    When I successfully run command:
      """
      ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
      """

    When I successfully run command:
      """
      make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """
    Then file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
    And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should have non-empty keys:
      | key            |
      | clusterID      |
      | clusterGroupID |

    When I successfully run command:
      """
      make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-observability-all KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
      """

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
      | function-autoscaler      | nvcf          | 1        |
      | nvca-operator            | nvca-operator | 1        |

    Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local" should complete rollout within "10m"
    Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"

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
      | ServiceMonitor | nvcf-default-monitors-nvca                        |
      | PodMonitor     | nvcf-default-monitors-dcgm                        |
      | PodMonitor     | nvcf-default-monitors-worker                      |

    Then Helm release "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local" should contain values:
      """
      selfManaged:
        otelCollector:
          enabled: true
          imageRepository: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-otel-collector
      """

  # The autoscaler is enabled by both control and all profiles. This functional
  # smoke stays in all because it requires the registered compute plane from
  # the preceding scenario. It is not a standalone tag target.
  @function-autoscaler @function-lifecycle
  Scenario: Autoscaler starts an idle function to serve its first request
    Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

    When I successfully create function "bdd-autoscaled-load-tester-supreme" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
      | option           | value   |
      | --inference-url  | /echo   |
      | --inference-port | 8000    |
      | --health-uri     | /health |
      | --health-port    | 8000    |
      | --health-timeout | PT30S   |

    And I successfully deploy the function selected by NVCF CLI with options:
      | option          | value               |
      | --gpu           | H100                |
      | --instance-type | NCP.GPU.H100_1x     |
      | --backend       | ncp-local           |
      | --regions       | us-west-1           |
      | --min-instances | 0                   |
      | --max-instances | 1                   |
      | --timeout       | 900                 |

    And I successfully generate a function API key with CLI options:
      | option        | value                                                                       |
      | --description | bdd-autoscaled-load-tester-supreme                                         |
      | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

    # Prove the function is idle before the first request creates demand. The
    # compute-plane CLI lists only scheduled functions, so no matching entry
    # also represents zero instances.
    Then the function selected by NVCF CLI should have no scheduled compute-plane instances using context "k3d-ncp-local" and kubeconfig "${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml"

    # The invocation plane returns after its short hold-open window while the
    # autoscaler and compute plane complete a cold start. A successful response
    # or the expected 504 proves the first request reached the invocation path.
    When I run command:
      """
      ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{"message":"bdd-autoscaler-echo","repeats":1}' --timeout 60 --poll-duration 5
      """
    Then the command output should contain one of:
      | text                 |
      | bdd-autoscaler-echo  |
      | API error 504         |

    Then the function selected by NVCF CLI should report "1" compute-plane instances with status "running" using context "k3d-ncp-local" and kubeconfig "${REPO_ROOT}/tests/bdd/out/ncp-local-observability-all-kubeconfig.yaml" within "10m"

    And I successfully invoke the function selected by NVCF CLI over HTTP with timeout "600" seconds and poll duration "5" seconds:
      """
      {"message":"bdd-autoscaler-echo","repeats":1}
      """
    Then the command output should contain "bdd-autoscaler-echo"

    And I successfully undeploy the function selected by NVCF CLI
