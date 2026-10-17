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
