@ncp-local @multi-cluster @helmfile @llm-last-cluster-affinity
Feature: Route returning LLM sessions to the cluster that served their last request
  As a self-managed NVCF operator,
  I want the LLM gateway and request router to keep each session on the cluster
  that served its last request,
  so that a returning session keeps its KV cache, a new session routes to a
  cluster that can take it now, and a session fails over when its cluster goes away.

  # The gateway stores the x-stargate-cluster-id of each session's last 2xx
  # response and sends it to the router as x-stargate-last-cluster-id. The
  # router reads it only for a wait-and-widen or pulsar-wait-and-widen model
  # configuration that sets last_cluster_affinity in the router configuration
  # file. Routing expressions cannot set it. Here a session is the
  # prompt_cache_key of a chat completion request.
  #
  # NVCF starts Pylon without --cluster-id, so the router cluster ID of a
  # backend is its function instance ID. Each function version below runs one
  # instance in its own compute cluster, so the last cluster is that compute
  # cluster.
  #
  # The gateway removes x-stargate-* headers from client responses, so these
  # scenarios observe routing through router and gateway metrics.

  Rule: Helmfile installs last-cluster affinity across two compute clusters

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | NVCF_CLI        |
        | REPO_ROOT       |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      # LLM_API_GATEWAY_IMAGE_TAG and LLM_REQUEST_ROUTER_IMAGE_TAG are optional.
      # Leave them unset to use the stack-pinned images. Set them to published
      # tags when the pinned images predate last-cluster affinity; the router
      # rejects the configuration below at startup otherwise.
      And I prepare Helmfile environment "local-bdd-last-cluster" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name              | nvcr-pull-secret                                                                                                                                                      |
        | global.helm.sources.repository               | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                                                                                                                  |
        | global.image.repository                      | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                                                                                                                  |
        | addons.llm.gateway.image.tag                 | ${LLM_API_GATEWAY_IMAGE_TAG}                                                                                                                                          |
        | addons.llm.requestRouter.image.tag           | ${LLM_REQUEST_ROUTER_IMAGE_TAG}                                                                                                                                       |
        | addons.llm.requestRouter.loadBalancer.config | {"default":"power-of-n","models":{"openai-compatible-sample":{"algorithm":"wait-and-widen","cache_affinity_backend_selection_count":1,"last_cluster_affinity":true}}} |
      And I prepare Helmfile environment "local-bdd-last-cluster" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-last-cluster-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # Conflict precheck: the single-cluster topology owns the same host
      # ports. Run make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local
      # before retrying. k3d v5 exits 1 when the cluster is absent.
      When I run command "k3d cluster get ncp-local"
      Then the command exit code should be 1
      And multi-cluster ncp-local compute clusters are running:
        | ncp-local-compute-1 |
        | ncp-local-compute-2 |
      And command has succeeded:
        """
        kubectl config use-context k3d-ncp-local-cp
        """
      And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local-cp":
        | cassandra-system |
        | nats-system      |
        | nvcf             |
        | api-keys         |
        | ess              |
        | sis              |
        | vault-system     |
        | nvca-operator    |
        | cert-manager     |

    @llm-last-cluster-install
    Scenario: Operator turns on last-cluster affinity and registers two compute clusters
      When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-last-cluster"
      Then these Kubernetes workloads should complete rollout using context "k3d-ncp-local-cp" within "10m":
        | kind       | name               | namespace |
        | Deployment | llm-request-router | nvcf      |
        | Deployment | llm-api-gateway    | nvcf      |
      When I successfully run command "kubectl --context k3d-ncp-local-cp get configmap llm-request-router-lb --namespace nvcf --output yaml"
      Then the command output should contain all:
        | text                     |
        | openai-compatible-sample |
        | last_cluster_affinity    |

      # The self-managed stack does not map the gateway last-cluster settings,
      # so set the documented chart values on the installed release. The
      # lookup timeout is raised from its default so a busy local cluster does
      # not drop hints.
      When I successfully run command:
        """
        helm --kube-context k3d-ncp-local-cp upgrade llm-api-gateway ${REPO_ROOT}/deploy/helm/llm-api-gateway/llm-api-gateway --namespace nvcf --reuse-values --set llmApiGateway.config.lastCluster.enabled=true --set llmApiGateway.config.lastCluster.lookupTimeout=250ms --wait --timeout 10m
        """
      Then Kubernetes resource "ConfigMap/llm-api-gateway" in namespace "nvcf" using context "k3d-ncp-local-cp" should contain:
        """yaml
        data:
          STARGATE_LAST_CLUSTER_ENABLED: "true"
          STARGATE_LAST_CLUSTER_LOOKUP_TIMEOUT: 250ms
        """

      # Both compute clusters reach the control plane through the same
      # compute-cluster aliases, so one exported profile registers both.
      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-last-cluster --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 control-plane profile export --cluster-name ncp-local-cp
        """
      Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist
      When command has succeeded:
        """
        /bin/sh -c '${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init >/dev/null'
        """

      When I successfully run command "kubectl config use-context k3d-ncp-local-compute-1"
      And I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local-compute-1":
        | nvca-operator |
      And I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd-last-cluster COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI}
        """
      Then NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

      When I successfully run command "kubectl config use-context k3d-ncp-local-compute-2"
      And I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-2 CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-2 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      And the "nvcr-pull-secret" image pull secret exists in namespaces using context "k3d-ncp-local-compute-2":
        | nvca-operator |
      And I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-2 HELMFILE_ENV=local-bdd-last-cluster COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-2 NVCF_CLI=${NVCF_CLI}
        """
      Then NVCFBackend "ncp-local-compute-2" in namespace "nvca-operator" using context "k3d-ncp-local-compute-2" should report agent status "healthy" within "10m"

  Rule: A session returns to its last cluster until that cluster goes away

    Background:
      Given these environment variables are set:
        | name                       |
        | NVCF_CLI                   |
        | REPO_ROOT                  |
        | SAMPLE_NGC_ORG             |
        | SAMPLE_NGC_TEAM            |
        | LAST_CLUSTER_INSTANCE_TYPE |
      # LAST_CLUSTER_INSTANCE_TYPE defaults to NCP.GPU.H100_1x in the live
      # entry point. Hosts too small for the fake GPU sizing register only
      # NCP.GPU.H100_2x and set it to that.
      # This rule depends on the install scenario above and is not a
      # standalone tag target.

    # Version A runs on compute-1 first, so session A's last cluster is known.
    # Version B of the same function then runs on compute-2. Versions share
    # the function ID routing key, so the router routes the model across both
    # clusters. Every turn asserts a 2xx completion; the router counters then
    # show how each turn was classified and where it went.
    @llm-last-cluster-sessions
    Scenario: A returning session stays on its last cluster, a new session routes now, and a session fails over
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      # Without routingMethod the gateway sends no x-routing-method, so the
      # router uses the model's configured wait-and-widen with
      # last_cluster_affinity.
      When I successfully create function "bdd-last-cluster-affinity" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local" with CLI options:
        | option           | value                                                                   |
        | --function-type  | LLM                                                                     |
        | --inference-url  | /v1/chat/completions                                                    |
        | --inference-port | 8000                                                                    |
        | --health-uri     | /health                                                                 |
        | --health-port    | 8000                                                                    |
        | --health-timeout | PT30S                                                                   |
        | --llm-model      | name=openai-compatible-sample,uris=/v1/chat/completions\|/v1/embeddings |
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I export the function selected by NVCF CLI to environment variables "LAST_CLUSTER_FUNCTION_ID" and "LAST_CLUSTER_VERSION_A_ID"
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value               |
        | --gpu           | H100                |
        | --instance-type | ${LAST_CLUSTER_INSTANCE_TYPE} |
        | --backend       | ncp-local-compute-1 |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |
      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-last-cluster-affinity                                           |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |
      Then every Pylon for function "bdd-last-cluster-affinity" using container "llm-worker" and context "k3d-ncp-local-compute-1" should report metrics within "10m":
        | metric                              | comparison | count |
        | pylon_registration_stream_connected | exactly    | 3     |
        | pylon_reverse_tunnel_connected      | exactly    | 3     |

      # Session A starts on compute-1, the only cluster serving the model.
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 1"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 2"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 3"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      Then Prometheus metrics on port "9090" of pods selected by "app.kubernetes.io/name=helm-nvcf-llm-request-router,app.kubernetes.io/instance=llm-request-router" in namespace "nvcf" using context "k3d-ncp-local-cp" should total within "2m":
        | metric                                    | labels                                                                                                     | comparison | total |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=new                         | exactly    | 1     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=returning,selection=primary | exactly    | 2     |

      # Add version B on compute-2 through the NVCF API. The CLI creates only
      # new functions and deploys one specification at a time.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/functions/${LAST_CLUSTER_FUNCTION_ID}/versions" with body:
        """
        {"name":"bdd-last-cluster-affinity","containerImage":"nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local","functionType":"LLM","inferenceUrl":"/v1/chat/completions","inferencePort":8000,"health":{"protocol":"HTTP","uri":"/health","port":8000,"timeout":"PT30S","expectedStatusCode":200},"models":[{"name":"openai-compatible-sample","llmConfig":{"uris":["/v1/chat/completions","/v1/embeddings"]}}]}
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "function.versionId" to environment variable "LAST_CLUSTER_VERSION_B_ID"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "DELETE" request to "http://api.localhost:8080/v2/nvcf/deployments/functions/${LAST_CLUSTER_FUNCTION_ID}/versions/${LAST_CLUSTER_VERSION_B_ID}" at scenario teardown expecting status 200
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/deployments/functions/${LAST_CLUSTER_FUNCTION_ID}/versions/${LAST_CLUSTER_VERSION_B_ID}" with body:
        """
        {"deploymentSpecifications":[{"gpu":"H100","instanceType":"${LAST_CLUSTER_INSTANCE_TYPE}","backend":"ncp-local-compute-2","regions":["us-west-1"],"minInstances":1,"maxInstances":1}]}
        """
      Then the HTTP status should be 200 or 202
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I poll "GET" "http://api.localhost:8080/v2/nvcf/deployments/functions/${LAST_CLUSTER_FUNCTION_ID}/versions/${LAST_CLUSTER_VERSION_B_ID}" for up to 900 seconds until the HTTP response body contains "ACTIVE"
      Then every Pylon for function "bdd-last-cluster-affinity" using container "llm-worker" and context "k3d-ncp-local-compute-2" should report metrics within "10m":
        | metric                              | comparison | count |
        | pylon_registration_stream_connected | exactly    | 3     |
        | pylon_reverse_tunnel_connected      | exactly    | 3     |

      # A returning session stays on compute-1 although compute-2 can now
      # serve it. For a returning session, selection primary means the router
      # chose the session's last cluster.
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 4"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 5"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"

      # A new session has no last cluster. The router classifies it new and
      # sends it to a cluster that can take it now instead of waiting for
      # affinity.
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session B turn 1"}],"prompt_cache_key":"bdd-last-cluster-session-b"}
        """
      Then the command output should contain "chat.completion"
      Then Prometheus metrics on port "9090" of pods selected by "app.kubernetes.io/name=helm-nvcf-llm-request-router,app.kubernetes.io/instance=llm-request-router" in namespace "nvcf" using context "k3d-ncp-local-cp" should total within "2m":
        | metric                                    | labels                                                                                                      | comparison | total |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=new                          | exactly    | 2     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=returning,selection=primary  | exactly    | 4     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=returning,selection=fallback | exactly    | 0     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=stale                        | exactly    | 0     |
      And Prometheus metrics on port "9464" of pods selected by "app.kubernetes.io/name=helm-nvcf-llm-api-gateway,app.kubernetes.io/instance=llm-api-gateway" in namespace "nvcf" using context "k3d-ncp-local-cp" should total within "2m":
        | metric                                     | labels         | comparison | total |
        | llm_api_gateway_last_cluster_lookups_total | result=hit     | at least   | 4     |
        | llm_api_gateway_last_cluster_lookups_total | result=miss    | at least   | 2     |
        | llm_api_gateway_last_cluster_lookups_total | result=timeout | exactly    | 0     |
        | llm_api_gateway_last_cluster_writes_total  | result=error   | exactly    | 0     |

      # Session A's last cluster goes away: force-terminate version A on
      # compute-1. The router no longer lists that instance, so the next turn
      # is classified stale and fails over to a cluster that can serve it.
      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml cluster agent kill-function ${LAST_CLUSTER_FUNCTION_ID} ${LAST_CLUSTER_VERSION_A_ID} --compute-plane-context k3d-ncp-local-compute-1 --reason bdd-last-cluster-failover --yes
        """
      Then the command output should contain "[deleted]"
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 6"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-last-cluster session A turn 7"}],"prompt_cache_key":"bdd-last-cluster-session-a"}
        """
      Then the command output should contain "chat.completion"
      Then Prometheus metrics on port "9090" of pods selected by "app.kubernetes.io/name=helm-nvcf-llm-request-router,app.kubernetes.io/instance=llm-request-router" in namespace "nvcf" using context "k3d-ncp-local-cp" should total within "2m":
        | metric                                    | labels                                                                                                      | comparison | total |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=new                          | exactly    | 2     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=stale                        | exactly    | 1     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=returning,selection=primary  | exactly    | 5     |
        | stargate_routing_session_selections_total | routing_key=${LAST_CLUSTER_FUNCTION_ID},algorithm=wait-and-widen,session_state=returning,selection=fallback | exactly    | 0     |

      And I successfully undeploy the function selected by NVCF CLI
