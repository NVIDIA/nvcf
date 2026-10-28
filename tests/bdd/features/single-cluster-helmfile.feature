@ncp-local @single-cluster @helmfile
Feature: Install a local single-cluster NVCF stack with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow against a local k3d cluster,
  so that I can install a single-cluster control plane with NVCA, the LLM
  gateway, and Vanity Gateway add-ons enabled.

  Rule: Operator authors the local Helmfile environment file

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      # The fixture is a copy of deploy/stacks/self-managed/environments/local.yaml,
      # which already carries every ncp-local local-mode override (storageClass,
      # replica counts, NVCA self-managed endpoints, addons.llm.*, agentConfig,
      # ingress.gatewayApi.*, addons.vanityGateway.*). The Background only overlays the operator-specific
      # values that vary per NGC org and pull-secret name.
      And I prepare Helmfile environment "local-bdd" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                   |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
      And I prepare Helmfile environment "local-bdd" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential

    Scenario: Operator validates the authored Helmfile environment renders
      When I successfully run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd"

      Then the command output should not contain "Error:"
      And the rendered workloads in "deploy/stacks/self-managed/out" should have valid container images

  Rule: Helmfile installs the local control plane with gateway add-ons

    Background:
      # This rule depends on the earlier environment-authoring
      # scenario in the same feature run. That scenario writes
      # local-bdd.yaml and local-bdd-secrets.yaml. Do not repeat that
      # setup here. The @llm-gateway scenario is not a standalone tag
      # target.
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/10081, NATS on 4222, and the worker
      # callback port 10086, overlapping host ports single-cluster
      # ncp-local needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
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

    @llm-gateway
    Scenario: Operator installs the control plane through the local Helmfile environment
      When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd"

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name                      | namespace            |
        | nats                      | nats-system          |
        | cert-manager              | cert-manager         |
        | openbao-server            | vault-system         |
        | cassandra                 | cassandra-system     |
        | api-keys                  | api-keys             |
        | sis                       | sis                  |
        | api                       | nvcf                 |
        | nvct-api                  | nvcf                 |
        | invocation-service        | nvcf                 |
        | grpc-proxy                | nvcf                 |
        | ess-api                   | ess                  |
        | notary-service            | nvcf                 |
        | admin-issuer-proxy        | api-keys             |
        | reval                     | nvcf                 |
        | nats-auth-callout-service | nats-system          |
        | ingress                   | envoy-gateway-system |
        | llm-request-router        | nvcf                 |
        | llm-api-gateway           | nvcf                 |
        | vanity-gateway            | nvcf                 |

      Then these Gateway API routes should be accepted and resolved using context "k3d-ncp-local" within "2m":
        | kind      | name           | namespace            | parent    |
        | HTTPRoute | vanity-gateway | envoy-gateway-system | shared-gw |

      When I successfully run command "kubectl --context k3d-ncp-local get configmap/nvcf-api-remote-config -n nvcf -o yaml"
      Then the command output should contain "llm-router-client-image: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/pylon:"

      # The fixture intentionally omits these values. Verify the installed
      # gateway received the self-managed plaintext transport default while
      # the disabled observability profile suppressed its ServiceMonitor.
      When I run command "kubectl --context k3d-ncp-local get configmap/llm-api-gateway -n nvcf -o jsonpath={.data.NVCF_GRPC_INSECURE}"
      Then the command exit code should be 0
      And the command output should contain "true"

      When I run command "helm get manifest llm-api-gateway --namespace nvcf --kube-context k3d-ncp-local"
      Then the command exit code should be 0
      And the command output should not contain "kind: ServiceMonitor"

  Rule: Helmfile installs NVCA on the same local cluster after registration via the stack Makefile

    Background:
      Given these environment variables are set:
        | name      |
        | NVCF_CLI  |
        | REPO_ROOT |
      # This rule depends on the earlier control-plane install scenario
      # in the same feature run. That scenario creates the cluster,
      # pull secrets, and Helmfile control-plane releases. The
      # @nvca-registration scenario is not a standalone tag target.

    @nvca-registration
    Scenario: Operator registers the local cluster and installs the NVCA operator
      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd control-plane profile export --cluster-name ncp-local
        """
      Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
      # The target cluster matches controlPlane.clusterName in the exported
      # profile, so registration selects the in-cluster service endpoints.
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should contain:
        """
        clusterName: ncp-local
        ncaID: nvcf-default
        selfManaged:
          region: us-west-1
          identitySource: psat
          icmsServiceURL: http://api.sis.svc.cluster.local:8080
          revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
          natsURL: nats://nats.nats-system.svc.cluster.local:4222
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local" should complete rollout within "10m"

      Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"

  Rule: Helmfile-installed local NVCF lists cluster groups

    # This scenario intentionally has no Background. It depends on the
    # earlier NVCA registration scenario in this feature run, and is not a
    # standalone tag target. The final init clears the narrowly scoped API
    # key from CLI state so later scenarios start from the admin token.
    @cluster-groups
    Scenario: Operator lists cluster groups with the admin token and with a scoped API key
      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml cluster list
        """
      Then the command exit code should be 0
      And the command output should contain "Found"

      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-cluster-groups --for function --scopes list_cluster_groups
        """
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml cluster list
        """
      Then the command exit code should be 0
      And the command output should contain "Found"

      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """

  Rule: Helmfile-installed local NVCF can run a Helm task

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenario in
    # this feature run, and is not a standalone tag target.
    @nvct-task-api @helm-task
    Scenario: Operator sees a Helm task without resource limits fail
      Given environment variable "SAMPLE_HELM_TASK_CHART_WITHOUT_RESOURCES" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully generate a task API key with CLI options:
        | option        | value                                |
        | --description | bdd-nvct-helm-task-missing-resources |
        | --scopes      | launch_task,task_details,delete_task |

      And I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml task create --name bdd-nvct-helm-task-missing-resources --helm-chart ${SAMPLE_HELM_TASK_CHART_WITHOUT_RESOURCES} --gpu H100 --instance-type NCP.GPU.H100_1x --backend ncp-local --result-strategy NONE --max-runtime PT10M --max-queued PT10M --termination-grace PT1M
        """
      Then the command exit code should be 0

      And the task selected by NVCF CLI should reach status "ERRORED" within "900" seconds, polling every "10" seconds

    @nvct-task-api @helm-task
    Scenario: Operator launches an NVCT Helm task and waits for it to complete
      Given environment variable "SAMPLE_HELM_TASK_CHART" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully generate a task API key with CLI options:
        | option        | value                                |
        | --description | bdd-nvct-helm-task-smoke             |
        | --scopes      | launch_task,task_details,delete_task |

      And I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml task create --name bdd-nvct-helm-task-smoke --helm-chart ${SAMPLE_HELM_TASK_CHART} --gpu H100 --instance-type NCP.GPU.H100_1x --backend ncp-local --result-strategy NONE --max-runtime PT10M --max-queued PT10M --termination-grace PT1M
        """
      Then the command exit code should be 0

      And the task selected by NVCF CLI should reach status "COMPLETED" within "900" seconds, polling every "10" seconds

  Rule: Helmfile-installed local NVCF can run a sample function

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenario in
    # this feature run, and is not a standalone tag target.
    @function-lifecycle
    Scenario: Operator invokes the Load Tester Supreme sample function through default and vanity endpoints
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-load-tester-supreme" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
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
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                       |
        | --description | bdd-load-tester-supreme                                                    |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-echo","repeats":1}
        """
      Then the command output should contain "bdd-echo"

      # Vanity Gateway mappings are Helm values, so read the function identity
      # from the operator's CLI and apply only the mapped release.
      When I export the function selected by NVCF CLI to environment variables "BDD_VANITY_FUNCTION_ID" and "BDD_VANITY_VERSION_ID"

      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | addons.vanityGateway.mappingConfig.v2config.vanity.bdd.host                            | vanity.localhost          |
        | addons.vanityGateway.mappingConfig.v2config.vanity.bdd.paths.echo.path                 | /bdd/echo                 |
        | addons.vanityGateway.mappingConfig.v2config.vanity.bdd.paths.echo.functionID           | ${BDD_VANITY_FUNCTION_ID} |
        | addons.vanityGateway.mappingConfig.v2config.vanity.bdd.paths.echo.functionVersionID    | ${BDD_VANITY_VERSION_ID}  |
        | addons.vanityGateway.mappingConfig.v2config.vanity.bdd.paths.echo.outgoingPathOverride | /echo                     |

      When I successfully run command "k3d kubeconfig merge ncp-local --output ${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false"
      And I successfully run command "make -C deploy/stacks/self-managed apply HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=vanity-gateway KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml"
      And I successfully run command "kubectl --context k3d-ncp-local rollout restart deployment/vanity-gateway --namespace nvcf"
      Then deployment "vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should complete rollout within "5m"

      When I successfully invoke the function selected by NVCF CLI through Vanity Gateway host "vanity.localhost" path "/bdd/echo" with timeout "120" seconds:
        """
        {"message":"bdd-vanity-echo","repeats":1}
        """
      Then the command output should contain "bdd-vanity-echo"

      # Remove the deployment: the local sizing cannot hold every
      # scenario's deployment at once.
      And I successfully undeploy the function selected by NVCF CLI

    @function-lifecycle @helm-function
    Scenario: Operator sees a Helm function without resource limits fail
      Given environment variable "SAMPLE_HELM_FUNCTION_CHART_WITHOUT_RESOURCES" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-helm-function-missing-resources" from Helm chart "${SAMPLE_HELM_FUNCTION_CHART_WITHOUT_RESOURCES}" with CLI options:
        | option               | value      |
        | --helm-chart-service | entrypoint |
        | --inference-url      | /echo      |
        | --inference-port     | 8000       |
        | --health-uri         | /health    |
        | --health-port        | 8000       |
        | --health-timeout     | PT30S      |

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --backend ncp-local --gpu H100 --instance-type NCP.GPU.H100_1x --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command should fail
      And the command output should contain "function deployment failed with status:"

      When I successfully undeploy the function selected by NVCF CLI

    @function-lifecycle @helm-function
    Scenario: Operator creates, deploys, and invokes a Helm chart function
      Given environment variable "SAMPLE_HELM_FUNCTION_CHART" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      # Function creation and deployment both cross the ReVal chart-rendering
      # boundary. A deployed ReVal pod alone does not prove this path works.
      When I successfully create function "bdd-helm-function" from Helm chart "${SAMPLE_HELM_FUNCTION_CHART}" with CLI options:
        | option               | value      |
        | --helm-chart-service | entrypoint |
        | --inference-url      | /echo      |
        | --inference-port     | 8000       |
        | --health-uri         | /health    |
        | --health-port        | 8000       |
        | --health-timeout     | PT30S      |

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value               |
        | --gpu           | H100                |
        | --instance-type | NCP.GPU.H100_1x     |
        | --backend       | ncp-local           |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                       |
        | --description | bdd-helm-function                                                           |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-helm-echo","repeats":1}
        """
      Then the command output should contain "bdd-helm-echo"

      And I successfully undeploy the function selected by NVCF CLI

    @function-lifecycle @grpc
    Scenario: Operator creates, deploys, and invokes the gRPC Load Tester Supreme sample function
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-grpc-load-tester-supreme" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option            | value   |
        | --inference-url   | /grpc   |
        | --inference-port  | 8001    |
        | --health-protocol | GRPC    |
        | --health-uri      | /       |
        | --health-port     | 8001    |
        | --health-timeout  | PT30S   |

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value               |
        | --gpu           | H100                |
        | --instance-type | NCP.GPU.H100_1x     |
        | --backend       | ncp-local           |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                       |
        | --description | bdd-grpc-load-tester-supreme                                               |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

      When I successfully invoke the function selected by NVCF CLI over plaintext gRPC service "Echo" method "EchoMessage" with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-grpc-echo"}
        """
      Then the command output should contain "bdd-grpc-echo"

      # Free the GPU node for the LLM scenario.
      And I successfully undeploy the function selected by NVCF CLI

    # Sticky shadow sampling through the Vanity Gateway's OpenAI-compatible
    # routes. The primary and shadow are separate LLM functions built from
    # SAMPLE_LLM_ECHO_IMAGE, which must honor the LOAD_TESTER_DEFAULT_*
    # container settings; the shadow answers after 4 s with its own text. A
    # client never sees whether a request was shadowed, so the checks read
    # the gateway's decision counter (which method decided and the outcome)
    # and its shadow replay server metric. Sampling is 50 percent; buckets
    # are a fixed hash of the key or canonical first message, so the session
    # keys and first messages below are chosen on either side of 50:
    # bdd-shadow-session-a (22) and "bdd shadow conversation a" (24) are
    # admitted, bdd-shadow-session-03 (99) and "bdd shadow conversation c"
    # (85) are rejected. A non-sticky decision would split their turns.
    # Depends on the earlier install and registration scenarios.
    @vanity-shadow-sampling
    Scenario: Vanity Gateway keeps each conversation's shadow decision and never delays the primary
      Given environment variable "SAMPLE_LLM_ECHO_IMAGE" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-vanity-shadow-primary" from image "${SAMPLE_LLM_ECHO_IMAGE}" with CLI options:
        | option           | value |
        | --function-type  | LLM |
        | --inference-url  | /v1/chat/completions |
        | --inference-port | 8000 |
        | --health-uri     | /health |
        | --health-port    | 8000 |
        | --health-timeout | PT30S |
        | --llm-model      | name=bdd-primary,uris=/v1/chat/completions\|/v1/responses,routingMethod=round_robin |
        | --llm-model      | name=bdd-primary-failing-shadow,uris=/v1/chat/completions,routingMethod=round_robin |
        | --container-env  | LOAD_TESTER_DEFAULT_CHUNK=bdd-primary-reply |
      And I export the function selected by NVCF CLI to environment variables "BDD_SHADOW_PRIMARY_FUNCTION_ID" and "BDD_SHADOW_PRIMARY_VERSION_ID"
      And function "${BDD_SHADOW_PRIMARY_FUNCTION_ID}" version "${BDD_SHADOW_PRIMARY_VERSION_ID}" is deleted at scenario teardown
      And function "${BDD_SHADOW_PRIMARY_FUNCTION_ID}" version "${BDD_SHADOW_PRIMARY_VERSION_ID}" is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |

      When I successfully create function "bdd-vanity-shadow-target" from image "${SAMPLE_LLM_ECHO_IMAGE}" with CLI options:
        | option           | value |
        | --function-type  | LLM |
        | --inference-url  | /v1/chat/completions |
        | --inference-port | 8000 |
        | --health-uri     | /health |
        | --health-port    | 8000 |
        | --health-timeout | PT30S |
        | --llm-model      | name=bdd-shadow,uris=/v1/chat/completions\|/v1/responses,routingMethod=round_robin |
        | --container-env  | LOAD_TESTER_DEFAULT_TTFT_MS=4000 |
        | --container-env  | LOAD_TESTER_DEFAULT_CHUNK=bdd-shadow-reply |
      And I export the function selected by NVCF CLI to environment variables "BDD_SHADOW_TARGET_FUNCTION_ID" and "BDD_SHADOW_TARGET_VERSION_ID"
      And function "${BDD_SHADOW_TARGET_FUNCTION_ID}" version "${BDD_SHADOW_TARGET_VERSION_ID}" is deleted at scenario teardown
      And function "${BDD_SHADOW_TARGET_FUNCTION_ID}" version "${BDD_SHADOW_TARGET_VERSION_ID}" is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-vanity-shadow                                                   |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      # Call the shadow function once directly so the LLM gateway has a route
      # to it before the first shadow copy arrives. The 4 s delay lets Pylon
      # finish startup calibration (its first batch sends five requests one at
      # a time within 30 s) and stays under its 5 s canary timeout, so the idle
      # shadow is not marked unavailable.
      When I successfully invoke model "bdd-shadow" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-shadow-warmup"}]}
        """
      Then the command output should contain "bdd-shadow-reply"

      # bdd-missing is not declared on the function, so the LLM gateway
      # answers its shadow copies with 404: a failing shadow target. The
      # gateway matches the Host header exactly, so the host carries the
      # local stack's port.
      When I set yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" key "addons.vanityGateway.mappingConfig.v2config.openai" to:
        """
        host: vanity-openai.localhost:8080
        chatCompletions:
          bdd_primary:
            modelName: bdd-primary
            functionID: ${BDD_SHADOW_PRIMARY_FUNCTION_ID}
            functionType: LLM
            promptCacheKeyHeaders:
              - x-multi-turn-session-id
            shadows:
              - modelName: bdd-shadow
                percentage: 50
                samplingMethod:
                  - promptCacheKey
                  - firstMessageHash
          bdd_primary_failing_shadow:
            modelName: bdd-primary-failing-shadow
            functionID: ${BDD_SHADOW_PRIMARY_FUNCTION_ID}
            functionType: LLM
            shadows:
              - modelName: bdd-missing
                percentage: 100
          bdd_shadow:
            modelName: bdd-shadow
            functionID: ${BDD_SHADOW_TARGET_FUNCTION_ID}
            functionType: LLM
          bdd_missing:
            modelName: bdd-missing
            functionID: ${BDD_SHADOW_PRIMARY_FUNCTION_ID}
            functionType: LLM
        responses:
          bdd_primary:
            modelName: bdd-primary
            functionID: ${BDD_SHADOW_PRIMARY_FUNCTION_ID}
            functionType: LLM
            shadows:
              - modelName: bdd-shadow
                percentage: 50
                samplingMethod:
                  - firstMessageHash
          bdd_shadow:
            modelName: bdd-shadow
            functionID: ${BDD_SHADOW_TARGET_FUNCTION_ID}
            functionType: LLM
        """
      And I set yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" key "ingress.gatewayApi.routes.vanityGateway.hostnames" to:
        """
        - vanity.localhost
        - vanity-openai.localhost
        """
      When I successfully run command "k3d kubeconfig merge ncp-local --output ${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false"
      And I successfully run command "make -C deploy/stacks/self-managed apply HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=ingress KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml"
      And I successfully run command "make -C deploy/stacks/self-managed apply HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=vanity-gateway KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml"
      And I successfully run command "kubectl --context k3d-ncp-local rollout restart deployment/vanity-gateway --namespace nvcf"
      Then deployment "vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should complete rollout within "5m"
      # Decision and replay counters are per pod, and replaced pods keep
      # serving while they drain (up to the 330 s grace period), so wait
      # until only the new pod remains before reading any baseline.
      And I successfully run command:
        """
        /bin/bash -c 'for i in $(seq 1 84); do [ "$(kubectl --context k3d-ncp-local get pods -n nvcf -l app.kubernetes.io/instance=vanity-gateway --no-headers | wc -l | tr -d " ")" = 1 ] && exit 0; sleep 5; done; echo "old vanity-gateway pods still running after 420s" >&2; exit 1'
        """
      And Kubernetes resource "httproute/vanity-gateway" in namespace "envoy-gateway-system" using context "k3d-ncp-local" should contain:
        """
        spec:
          hostnames:
            - vanity.localhost
            - vanity-openai.localhost
        """
      And these Gateway API routes should be accepted and resolved using context "k3d-ncp-local" within "2m":
        | kind      | name           | namespace            | parent    |
        | HTTPRoute | vanity-gateway | envoy-gateway-system | shared-gw |

      # The gateway restarted above and only the new pod remains, so its
      # counters start at zero and the totals below are cumulative. Admitted
      # shadows take 4 s, so a primary that waited for them would exceed each
      # request's 3 s deadline.

      # A prompt_cache_key in the body keeps one decision on every turn.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","prompt_cache_key":"bdd-shadow-session-a","messages":[{"role":"user","content":"bdd body key admitted"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","prompt_cache_key":"bdd-shadow-session-a","messages":[{"role":"user","content":"bdd body key admitted"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","prompt_cache_key":"bdd-shadow-session-a","messages":[{"role":"user","content":"bdd body key admitted"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 3"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      # Read each conversation on its own: decisions that flip in both
      # conversations would still give the same combined totals.
      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                                                   | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=promptCacheKey,outcome=admitted | exactly    | 3     |

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","prompt_cache_key":"bdd-shadow-session-03","messages":[{"role":"user","content":"bdd body key rejected"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","prompt_cache_key":"bdd-shadow-session-03","messages":[{"role":"user","content":"bdd body key rejected"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                                                   | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=promptCacheKey,outcome=admitted | exactly    | 3     |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=promptCacheKey,outcome=rejected | exactly    | 2     |

      # With no body key, the configured session header is the key.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "x-multi-turn-session-id" is "bdd-shadow-session-a"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd header key admitted"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "x-multi-turn-session-id" is "bdd-shadow-session-a"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd header key admitted"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                                                   | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=promptCacheKey,outcome=admitted | exactly    | 5     |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=promptCacheKey,outcome=rejected | exactly    | 2     |

      # With no key, the first message decides; resending the history keeps it.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd shadow conversation a"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd shadow conversation a"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      # Read conversation a on its own, as for the body key above.
      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                                                     | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=firstMessageHash,outcome=admitted | exactly    | 2     |

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd shadow conversation c"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","messages":[{"role":"user","content":"bdd shadow conversation c"},{"role":"assistant","content":"bdd-primary-reply"},{"role":"user","content":"turn 2"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                                                     | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=firstMessageHash,outcome=admitted | exactly    | 2     |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=chatCompletions,method=firstMessageHash,outcome=rejected | exactly    | 2     |

      # Exactly the 7 admitted requests reach the slow shadow, which answers 200.
      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "90s":
        | metric                                     | labels                                         | comparison | total |
        | http_server_request_duration_seconds_count | nvcf_shadow=true,http_response_status_code=200 | exactly    | 7     |

      # A Responses request that continues earlier turns cannot use the first
      # message, so sampling falls back to random.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/responses" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","input":"bdd follow-up one","previous_response_id":"resp_bdd_shadow"}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/responses" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary","input":"bdd follow-up two","previous_response_id":"resp_bdd_shadow"}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"

      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                              | labels                                              | comparison | total |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=responses,method=random           | exactly    | 2     |
        | nvcf_ai_api_gateway_shadow_sampling_decisions_total | sampling_endpoint=responses,method=firstMessageHash | exactly    | 0     |

      # A failing shadow target does not change the primary response.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/chat/completions" with timeout 3 seconds and body:
        """
        {"model":"bdd-primary-failing-shadow","messages":[{"role":"user","content":"bdd failing shadow"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-primary-reply"
      And the HTTP response body should not contain "bdd-shadow-reply"
      Then Prometheus metrics on port "10083" of pods selected by "app.kubernetes.io/name=helm-nvcf-vanity-gateway,app.kubernetes.io/instance=vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should total within "30s":
        | metric                                     | labels                                         | comparison | total |
        | http_server_request_duration_seconds_count | nvcf_shadow=true,http_response_status_code=404 | exactly    | 1     |

    # Proves the serve path the @llm-gateway scenario only installs:
    # LLM-type functions route through llm-api-gateway and
    # llm-request-router. The CLI maps invocation.localhost to the
    # llm.localhost gateway host and rewrites the body model to
    # <functionId>/<model>. Depends on the earlier install and
    # registration scenarios; not a standalone tag target.
    @llm-function-type
    Scenario: Operator creates, deploys, and invokes an LLM-type OpenAI-compatible sample function
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-openai-compatible-sample" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local" with CLI options:
        | option           | value                                                                                               |
        | --function-type  | LLM                                                                                                 |
        | --inference-url  | /v1/chat/completions                                                                                |
        | --inference-port | 8000                                                                                                |
        | --health-uri     | /health                                                                                             |
        | --health-port    | 8000                                                                                                |
        | --health-timeout | PT30S                                                                                               |
        | --llm-model      | name=openai-compatible-sample,uris=/v1/chat/completions\|/v1/responses\|/v1/embeddings,routingMethod=round_robin |

      And the function selected by NVCF CLI is undeployed at scenario teardown

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value               |
        | --gpu           | H100                |
        | --instance-type | NCP.GPU.H100_1x     |
        | --backend       | ncp-local           |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                       |
        | --description | bdd-openai-compatible-sample                                                 |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

      # The gateway answers synchronously (no queue polling). The
      # sample always returns its fixed load-testing message.
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-llm-echo"}]}
        """
      Then the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"

      And I export the function selected by NVCF CLI to environment variables "BDD_LLM_FUNCTION_ID" and "BDD_LLM_VERSION_ID"

      # Use the same authenticated function through each public entry point.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/chat/completions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","messages":[{"role":"user","content":"bdd-stream"}],"stream":true}
        """
      Then the HTTP status should be 200
      And the HTTP response body should contain "chat.completion.chunk"
      And the HTTP response body should end with "data: [DONE]"
      And the HTTP response body should not contain "error"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/responses" with timeout 120 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","input":"bdd-responses","stream":false}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "object" should equal "response"
      And the HTTP response JSON field "status" should equal "completed"
      And the HTTP response JSON field "output.0.content.0.type" should equal "output_text"
      And the HTTP response body should contain "fixed 128-byte response"

      # The sample body controls hold non-streaming output for 90s and
      # stream its 18 words over 76.5s, crossing the 60s regression boundary.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/chat/completions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","messages":[{"role":"user","content":"bdd-long-nonstream"}],"stream":false,"ttft_ms":90000,"repeat_count":1}
        """
      Then the HTTP status should be 200
      And the HTTP response should take at least "60s"
      And the HTTP response JSON field "object" should equal "chat.completion"
      And the HTTP response JSON field "choices.0.finish_reason" should equal "stop"
      And the HTTP response body should contain "fixed 128-byte response"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/chat/completions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","messages":[{"role":"user","content":"bdd-long-stream"}],"stream":true,"ttft_ms":0,"inter_token_delay_ms":4500,"repeat_count":1}
        """
      Then the HTTP status should be 200
      And the HTTP response should take at least "60s"
      And the HTTP response body should contain "chat.completion.chunk"
      And the HTTP response body should end with "data: [DONE]"
      And the HTTP response body should not contain "error"

      # A client deadline well below 60s also rejects the old hanging behavior.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Function-ID" is "${BDD_LLM_FUNCTION_ID}"
      And the HTTP request header "Function-Version-ID" is "${BDD_LLM_VERSION_ID}"
      When I send a "POST" request to "http://invocation.localhost:8080/v1/chat/completions" with timeout 10 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","messages":[{"role":"user","content":"bdd-classic"}],"stream":false}
        """
      Then the HTTP status should be 404

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/pexec/functions/${BDD_LLM_FUNCTION_ID}" with timeout 10 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/openai-compatible-sample","messages":[{"role":"user","content":"bdd-pexec"}],"stream":false}
        """
      Then the HTTP status should be 404

      # The declared model succeeded above, so this cannot pass on a cold route.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/chat/completions" with timeout 10 seconds and body:
        """
        {"model":"${BDD_LLM_FUNCTION_ID}/not-a-model","messages":[{"role":"user","content":"bdd-unlisted"}],"stream":false}
        """
      Then the HTTP status should be 404

      # curl reports only the status code so the assertion cannot
      # match response-body noise.
      When I successfully run command:
        """
        curl -s -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command output should contain "401"

      # Leave the GPU capacity free, same as the echo scenarios.
      And I successfully undeploy the function selected by NVCF CLI

    # The gateway must forward chat fields it does not model unchanged, must
    # not add reasoning options the client omitted, and must forward an audio
    # part instead of rejecting it. SAMPLE_LLM_ECHO_IMAGE is an
    # OpenAI-compatible sample image that supports the x_load_tester_echo
    # body control and replies with the request body it received, so each
    # check reads what reached the model server. The :local sample above
    # cannot echo. Depends on the earlier install and registration scenarios.
    @llm-function-type @llm-field-fidelity
    Scenario: LLM gateway forwards chat request fields to the model server unchanged
      Given environment variable "SAMPLE_LLM_ECHO_IMAGE" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-llm-field-fidelity" from image "${SAMPLE_LLM_ECHO_IMAGE}" with CLI options:
        | option           | value                                                                              |
        | --function-type  | LLM                                                                                |
        | --inference-url  | /v1/chat/completions                                                               |
        | --inference-port | 8000                                                                               |
        | --health-uri     | /health                                                                            |
        | --health-port    | 8000                                                                               |
        | --health-timeout | PT30S                                                                              |
        | --llm-model      | name=openai-compatible-sample,uris=/v1/chat/completions,routingMethod=round_robin |

      And the function selected by NVCF CLI is undeployed at scenario teardown

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value               |
        | --gpu           | H100                |
        | --instance-type | NCP.GPU.H100_1x     |
        | --backend       | ncp-local           |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                       |
        | --description | bdd-llm-field-fidelity                                                      |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details         |

      # Same warm-up as the scenario above: the first gateway call proves
      # the route is ready before the raw HTTP checks.
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-llm-field-fidelity-ready"}]}
        """
      Then the command output should contain "chat.completion"

      And I export the function selected by NVCF CLI to environment variables "BDD_LLM_ECHO_FUNCTION_ID" and "BDD_LLM_ECHO_VERSION_ID"

      # Chat Completions takes audio as audio_url; input_audio is a
      # Responses API part.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/chat/completions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_LLM_ECHO_FUNCTION_ID}/openai-compatible-sample","stream":false,"chat_template_kwargs":{"enable_thinking":false},"ignore_eos":true,"x_load_tester_echo":true,"messages":[{"role":"user","content":[{"type":"text","text":"bdd-field-fidelity"},{"type":"audio_url","audio_url":{"url":"data:audio/wav;base64,AAAA"}}]}]}
        """
      Then the HTTP status should be 200
      # The sample's reply text is the request it received, so these paths
      # read the forwarded request.
      And the HTTP response JSON field "choices.0.message.content.chat_template_kwargs.enable_thinking" should equal "false"
      And the HTTP response JSON field "choices.0.message.content.ignore_eos" should equal "true"
      And the HTTP response JSON field "choices.0.message.content.messages.0.content.0.text" should equal "bdd-field-fidelity"
      And the HTTP response JSON field "choices.0.message.content.messages.0.content.1.type" should equal "audio_url"
      And the HTTP response JSON field "choices.0.message.content.messages.0.content.1.audio_url.url" should equal "data:audio/wav;base64,AAAA"
      And the HTTP response JSON field "choices.0.message.content.include_reasoning" should be absent
      And the HTTP response JSON field "choices.0.message.content.reasoning_format" should be absent

      And I successfully undeploy the function selected by NVCF CLI

    # Beta decision routes, POST /v1/decisions and POST /v1/systemone, through
    # the LLM gateway. Both routes are passthrough, so the sample backend's
    # canned answers and header echo prove what reached it.
    # SAMPLE_LLM_DECISIONS_IMAGE must be an image of the in-repo
    # openai-compatible sample that serves both routes. One function declares
    # two models: bdd-decision-both on both routes and bdd-decision-only on
    # /v1/decisions alone. A model is enabled for a route only when its uris
    # list that route. Depends on the earlier install and registration
    # scenarios.
    @llm-function-type @llm-decision-routes
    Scenario: LLM gateway serves the decisions and systemone routes and keeps each model on its declared routes
      Given environment variable "SAMPLE_LLM_DECISIONS_IMAGE" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-llm-decision-routes" from image "${SAMPLE_LLM_DECISIONS_IMAGE}" with CLI options:
        | option           | value                                                                                    |
        | --function-type  | LLM                                                                                      |
        | --inference-url  | /v1/chat/completions                                                                     |
        | --inference-port | 8000                                                                                     |
        | --health-uri     | /health                                                                                  |
        | --health-port    | 8000                                                                                     |
        | --health-timeout | PT30S                                                                                    |
        | --llm-model      | name=bdd-decision-both,uris=/v1/decisions\|/v1/systemone,routingMethod=round_robin       |
        | --llm-model      | name=bdd-decision-only,uris=/v1/decisions,routingMethod=round_robin                      |
      And I export the function selected by NVCF CLI to environment variables "BDD_DECISION_FUNCTION_ID" and "BDD_DECISION_VERSION_ID"
      And function "${BDD_DECISION_FUNCTION_ID}" version "${BDD_DECISION_VERSION_ID}" is deleted at scenario teardown
      And function "${BDD_DECISION_FUNCTION_ID}" version "${BDD_DECISION_VERSION_ID}" is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-llm-decision-routes                                             |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      # The first gateway call proves the route is ready before the raw checks.
      When I successfully invoke model "bdd-decision-both" at "/v1/decisions" with timeout "120" seconds:
        """
        {"input":"bdd-decision-ready","questions":[{"type":"predicate","text":"Is it ready?"}]}
        """
      Then the command output should contain "answers"

      # Decisions: answers keep the question order, and the vendor headers
      # reach the backend, which echoes them.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://llm.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","input":"bdd-decision-input","questions":[{"type":"predicate","name":"red","text":"Is it red?"},{"type":"choice","name":"animal","text":"Which animal?","choices":["cat","dog"]},{"type":"score","name":"quality","text":"How good is it?"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.0.type" should equal "predicate"
      And the HTTP response JSON field "answers.0.probability" should equal "0.99"
      And the HTTP response JSON field "answers.1.type" should equal "choice"
      And the HTTP response JSON field "answers.1.choice" should equal "cat"
      And the HTTP response JSON field "answers.2.type" should equal "score"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"
      And the HTTP response header "x-mock-echo-typesafe-danger-zone-images" should equal "true"

      # System One: answers are keyed by question name.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://llm.localhost:8080/v1/systemone" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","state":{"text":"bdd-systemone-state"},"questions":{"seen":{"type":"noul","text":"Is it seen?"},"animal":{"type":"choice","choices":["cat","dog"]},"quality":{"type":"score"}}}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.seen.type" should equal "noul"
      And the HTTP response JSON field "answers.seen.noul" should equal "0.99"
      And the HTTP response JSON field "answers.animal.choice" should equal "cat"
      And the HTTP response JSON field "answers.quality.type" should equal "score"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"
      And the HTTP response header "x-mock-echo-typesafe-danger-zone-images" should equal "true"

      # Route isolation: bdd-decision-only declares /v1/decisions alone, so
      # the declared route works and /v1/systemone is a 404 in the System One
      # error shape (error.code, error.message, error.request_id and a
      # top-level request_id that match).
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-only","input":"bdd-decision-only","questions":[{"type":"predicate","text":"Is it declared?"}]}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.0.type" should equal "predicate"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/systemone" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-only","state":{"text":"bdd"},"questions":{"seen":{"type":"noul"}}}
        """
      Then the HTTP status should be 404
      And the HTTP response JSON field "error.code" should equal "not_found"
      And the HTTP response body should contain "is not enabled for /v1/systemone"
      And I export the HTTP response JSON field "error.request_id" to environment variable "BDD_SYSTEMONE_ERROR_REQUEST_ID"
      And the HTTP response JSON field "request_id" should equal "${BDD_SYSTEMONE_ERROR_REQUEST_ID}"

      # A model that is not declared at all fails the same way on /v1/decisions,
      # in the OpenAI error shape.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/not-a-model","input":"bdd-unlisted","questions":[{"type":"predicate","text":"Is it listed?"}]}
        """
      Then the HTTP status should be 404
      And the HTTP response JSON field "error.type" should equal "invalid_request_error"

      # Backend errors pass through unchanged: status, Retry-After and body.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/decisions?mock_error=429" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","input":"bdd-429","questions":[{"type":"predicate","text":"Is it limited?"}]}
        """
      Then the HTTP status should be 429
      And the HTTP response header "Retry-After" should equal "7"
      And the HTTP response JSON field "error.type" should equal "rate_limit_error"
      And the HTTP response JSON field "error.message" should equal "mock rate limit"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/systemone?mock_error=429" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","state":{"text":"bdd-429"},"questions":{"seen":{"type":"noul"}}}
        """
      Then the HTTP status should be 429
      And the HTTP response header "Retry-After" should equal "7"
      And the HTTP response JSON field "detail.0.msg" should equal "mock rate limit"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/decisions?mock_error=400" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","input":"bdd-400","questions":[{"type":"predicate","text":"Is it valid?"}]}
        """
      Then the HTTP status should be 400
      And the HTTP response JSON field "error.type" should equal "invalid_request_error"
      And the HTTP response JSON field "error.message" should equal "mock validation error"

      # The System One backend error is FastAPI style, not the gateway's
      # System One error shape, so the body reached the client unchanged.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://llm.localhost:8080/v1/systemone?mock_error=400" with timeout 120 seconds and body:
        """
        {"model":"${BDD_DECISION_FUNCTION_ID}/bdd-decision-both","state":{"text":"bdd-400"},"questions":{"seen":{"type":"noul"}}}
        """
      Then the HTTP status should be 400
      And the HTTP response JSON field "detail.0.msg" should equal "mock validation error"
      And the HTTP response JSON field "detail.0.type" should equal "value_error"
      And the HTTP response body should not contain "request_id"

    # The same routes through the Vanity Gateway, on the OpenAI host
    # vanity-openai.localhost. Two functions from SAMPLE_LLM_DECISIONS_IMAGE:
    # a default-type function that the gateway calls directly with function
    # ID headers (usePexec false, the path is kept), and an LLM-type function
    # that it reaches through the LLM gateway director (functionType LLM).
    # Mappings: decisions under v2config.openai and typesafeSystemOne under
    # v2config. bdd-decision-direct and bdd-decision-llm are in both sections;
    # bdd-decision-decisions-only and bdd-decision-systemone-only are in one.
    # The Vanity Gateway build must serve these sections before this runs.
    # Depends on the earlier install and registration scenarios.
    @llm-function-type @llm-decision-routes @vanity-decision-routes
    Scenario: Vanity Gateway serves the decisions and systemone routes directly and through the LLM gateway
      Given environment variable "SAMPLE_LLM_DECISIONS_IMAGE" is set
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-vanity-decision-direct" from image "${SAMPLE_LLM_DECISIONS_IMAGE}" with CLI options:
        | option           | value         |
        | --inference-url  | /v1/decisions |
        | --inference-port | 8000          |
        | --health-uri     | /health       |
        | --health-port    | 8000          |
        | --health-timeout | PT30S         |
      And I export the function selected by NVCF CLI to environment variables "BDD_DECISION_DIRECT_FUNCTION_ID" and "BDD_DECISION_DIRECT_VERSION_ID"
      And function "${BDD_DECISION_DIRECT_FUNCTION_ID}" version "${BDD_DECISION_DIRECT_VERSION_ID}" is deleted at scenario teardown
      And function "${BDD_DECISION_DIRECT_FUNCTION_ID}" version "${BDD_DECISION_DIRECT_VERSION_ID}" is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-vanity-decision-routes                                          |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      # The first call proves the direct function answers before the gateway
      # is involved.
      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"input":"bdd-decision-direct-ready","questions":[{"type":"predicate","text":"Is it ready?"}]}
        """
      Then the command output should contain "answers"

      When I successfully create function "bdd-vanity-decision-llm" from image "${SAMPLE_LLM_DECISIONS_IMAGE}" with CLI options:
        | option           | value                                                                              |
        | --function-type  | LLM                                                                                |
        | --inference-url  | /v1/chat/completions                                                               |
        | --inference-port | 8000                                                                               |
        | --health-uri     | /health                                                                            |
        | --health-port    | 8000                                                                               |
        | --health-timeout | PT30S                                                                              |
        | --llm-model      | name=bdd-decision-llm,uris=/v1/decisions\|/v1/systemone,routingMethod=round_robin |
      And I export the function selected by NVCF CLI to environment variables "BDD_DECISION_LLM_FUNCTION_ID" and "BDD_DECISION_LLM_VERSION_ID"
      And function "${BDD_DECISION_LLM_FUNCTION_ID}" version "${BDD_DECISION_LLM_VERSION_ID}" is deleted at scenario teardown
      And function "${BDD_DECISION_LLM_FUNCTION_ID}" version "${BDD_DECISION_LLM_VERSION_ID}" is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |

      # The LLM gateway needs a route to the new function before the first
      # request through the Vanity Gateway.
      When I successfully invoke model "bdd-decision-llm" at "/v1/decisions" with timeout "120" seconds:
        """
        {"input":"bdd-decision-llm-ready","questions":[{"type":"predicate","text":"Is it ready?"}]}
        """
      Then the command output should contain "answers"

      When I set yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" key "addons.vanityGateway.mappingConfig.v2config.openai" to:
        """
        host: vanity-openai.localhost:8080
        decisions:
          bdd_decision_direct:
            modelName: bdd-decision-direct
            functionID: ${BDD_DECISION_DIRECT_FUNCTION_ID}
            functionVersionID: ${BDD_DECISION_DIRECT_VERSION_ID}
            usePexec: false
          bdd_decision_llm:
            modelName: bdd-decision-llm
            functionID: ${BDD_DECISION_LLM_FUNCTION_ID}
            functionType: LLM
          bdd_decision_decisions_only:
            modelName: bdd-decision-decisions-only
            functionID: ${BDD_DECISION_DIRECT_FUNCTION_ID}
            functionVersionID: ${BDD_DECISION_DIRECT_VERSION_ID}
            usePexec: false
        """
      And I set yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" key "addons.vanityGateway.mappingConfig.v2config.typesafeSystemOne" to:
        """
        bdd_decision_direct:
          modelName: bdd-decision-direct
          functionID: ${BDD_DECISION_DIRECT_FUNCTION_ID}
          functionVersionID: ${BDD_DECISION_DIRECT_VERSION_ID}
          usePexec: false
        bdd_decision_llm:
          modelName: bdd-decision-llm
          functionID: ${BDD_DECISION_LLM_FUNCTION_ID}
          functionType: LLM
        bdd_decision_systemone_only:
          modelName: bdd-decision-systemone-only
          functionID: ${BDD_DECISION_DIRECT_FUNCTION_ID}
          functionVersionID: ${BDD_DECISION_DIRECT_VERSION_ID}
          usePexec: false
        """
      And I set yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" key "ingress.gatewayApi.routes.vanityGateway.hostnames" to:
        """
        - vanity.localhost
        - vanity-openai.localhost
        """
      When I successfully run command "k3d kubeconfig merge ncp-local --output ${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml --overwrite --kubeconfig-switch-context=false"
      And I successfully run command "make -C deploy/stacks/self-managed apply HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=ingress KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml"
      And I successfully run command "make -C deploy/stacks/self-managed apply HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=vanity-gateway KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/ncp-local-vanity-kubeconfig.yaml"
      And I successfully run command "kubectl --context k3d-ncp-local rollout restart deployment/vanity-gateway --namespace nvcf"
      Then deployment "vanity-gateway" in namespace "nvcf" using context "k3d-ncp-local" should complete rollout within "5m"
      And Kubernetes resource "httproute/vanity-gateway" in namespace "envoy-gateway-system" using context "k3d-ncp-local" should contain:
        """
        spec:
          hostnames:
            - vanity.localhost
            - vanity-openai.localhost
        """
      And these Gateway API routes should be accepted and resolved using context "k3d-ncp-local" within "2m":
        | kind      | name           | namespace            | parent    |
        | HTTPRoute | vanity-gateway | envoy-gateway-system | shared-gw |

      # Direct models: the gateway keeps the path and selects the function with
      # function ID headers, so each route reaches the function as called.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-direct","input":"bdd-vanity-direct","questions":[{"type":"predicate","name":"red","text":"Is it red?"},{"type":"choice","name":"animal","text":"Which animal?","choices":["cat","dog"]}]}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.0.type" should equal "predicate"
      And the HTTP response JSON field "answers.1.choice" should equal "cat"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/systemone" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-direct","state":{"text":"bdd-vanity-direct"},"questions":{"seen":{"type":"noul"},"animal":{"type":"choice","choices":["cat","dog"]}}}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.seen.noul" should equal "0.99"
      And the HTTP response JSON field "answers.animal.choice" should equal "cat"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"

      # LLM models: the gateway sends the request through the LLM gateway.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-llm","input":"bdd-vanity-llm","questions":[{"type":"predicate","name":"red","text":"Is it red?"},{"type":"choice","name":"animal","text":"Which animal?","choices":["cat","dog"]}]}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.0.type" should equal "predicate"
      And the HTTP response JSON field "answers.1.choice" should equal "cat"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "TypeSafe-Danger-Zone-Images" is "true"
      And the HTTP request header "X-Vendor-Test" is "bdd-vendor-value"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/systemone" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-llm","state":{"text":"bdd-vanity-llm"},"questions":{"seen":{"type":"noul"},"animal":{"type":"choice","choices":["cat","dog"]}}}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "answers.seen.noul" should equal "0.99"
      And the HTTP response JSON field "answers.animal.choice" should equal "cat"
      And the HTTP response JSON field "echo.typesafe-danger-zone-images" should equal "true"
      And the HTTP response JSON field "echo.x-vendor-test" should equal "bdd-vendor-value"

      # A model listed only in the other section is a 404 in the native error
      # shape of the route that was called, and never reaches a function.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/systemone" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-decisions-only","state":{"text":"bdd"},"questions":{"seen":{"type":"noul"}}}
        """
      Then the HTTP status should be 404
      And the HTTP response JSON field "error.code" should equal "not_found"
      And I export the HTTP response JSON field "error.request_id" to environment variable "BDD_VANITY_SYSTEMONE_ERROR_REQUEST_ID"
      And the HTTP response JSON field "request_id" should equal "${BDD_VANITY_SYSTEMONE_ERROR_REQUEST_ID}"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://vanity-openai.localhost:8080/v1/decisions" with timeout 120 seconds and body:
        """
        {"model":"bdd-decision-systemone-only","input":"bdd","questions":[{"type":"predicate","text":"Is it listed?"}]}
        """
      Then the HTTP status should be 404
      And the HTTP response JSON field "error.type" should equal "invalid_request_error"

      # Model discovery lists each decision model with the routes it serves.
      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      When I send a "GET" request to "http://vanity-openai.localhost:8080/v1/models"
      Then the HTTP status should be 200
      And the HTTP response body should contain "bdd-decision-direct"
      And the HTTP response body should contain "bdd-decision-decisions-only"
      And the HTTP response body should contain "bdd-decision-systemone-only"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      When I send a "GET" request to "http://vanity-openai.localhost:8080/v1/models/bdd-decision-direct"
      Then the HTTP status should be 200
      And the HTTP response JSON field "supported_endpoints.0" should equal "/v1/decisions"
      And the HTTP response JSON field "supported_endpoints.1" should equal "/v1/systemone"

      Given the HTTP request header "Authorization" is the NVCF CLI function API key with prefix "Bearer "
      When I send a "GET" request to "http://vanity-openai.localhost:8080/v1/models/bdd-decision-decisions-only"
      Then the HTTP status should be 200
      And the HTTP response JSON field "supported_endpoints.0" should equal "/v1/decisions"
      And the HTTP response body should not contain "/v1/systemone"
