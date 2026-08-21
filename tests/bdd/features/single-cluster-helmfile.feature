@ncp-local @single-cluster @helmfile
Feature: Install a local single-cluster NVCF stack with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow against a local k3d cluster,
  so that I can install a single-cluster control plane with NVCA and the LLM
  gateway add-on enabled.

  Rule: Operator authors the local Helmfile environment file

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      # The fixture is a copy of deploy/stacks/self-managed/environments/local.yaml,
      # which already carries every ncp-local local-mode override (storageClass,
      # replica counts, NVCA self-managed endpoints, addons.llm.*, agentConfig,
      # ingress.gatewayApi.*). The Background only overlays the operator-specific
      # values that vary per NGC org and pull-secret name.
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                   |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0 |
        | observability.profile                         | disabled                                                           |
      And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      # Only ${VAR} is interpolated; bare $oauthtoken stays literal.
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"

    Scenario: Operator validates the authored Helmfile environment renders
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd"

      Then the command exit code should be 0
      And the command output should not contain "Error:"

  Rule: Helmfile installs the local control plane with LLM gateway add-ons

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
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd"

      Then the command exit code should be 0

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
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
      # The single-cluster Makefile passes CLUSTER_NAME separately to
      # helmfile, so the register-values file does not carry clusterName
      # at the top level. Assert the deterministic block that is
      # present plus the non-empty fields.
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
          icmsServiceURL: http://sis.localhost:8080
          revalServiceURL: http://reval.localhost:8080
          natsURL: nats://nats.localhost:4222
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" key "clusterGroupID" should not be empty

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --timeout=10m"
      Then the command exit code should be 0

      When I run command "kubectl wait nvcfbackend ncp-local -n nvca-operator --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0

  Rule: Helmfile-installed local NVCF can run a sample function

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenario in
    # this feature run, and is not a standalone tag target.
    @function-lifecycle
    Scenario: Operator creates, deploys, and invokes the Load Tester Supreme sample function
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-load-tester-supreme --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8 --inference-url /echo --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-load-tester-supreme --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{"message":"bdd-echo","repeats":1}' --timeout 120 --poll-duration 5
        """
      Then the command exit code should be 0
      And the command output should contain "bdd-echo"

      # Remove the deployment so its worker pod (whose utils sidecar
      # requests 4 CPU) frees the GPU node for the later scenarios.
      # The local sizing cannot hold every scenario's deployment at
      # once.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function delete --deployment-only
        """
      Then the command exit code should be 0

    @function-lifecycle @grpc
    Scenario: Operator creates, deploys, and invokes the gRPC Load Tester Supreme sample function
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-grpc-load-tester-supreme --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8 --inference-url /grpc --inference-port 8001 --health-protocol GRPC --health-uri / --health-port 8001 --health-timeout PT30S
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-grpc-load-tester-supreme --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --grpc --grpc-plaintext --grpc-service Echo --grpc-method EchoMessage --request-body '{"message":"bdd-grpc-echo"}' --timeout 120 --poll-duration 5
        """
      Then the command exit code should be 0
      And the command output should contain "bdd-grpc-echo"

      # Free the GPU node for the LLM scenario; see the HTTP scenario's
      # cleanup comment.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function delete --deployment-only
        """
      Then the command exit code should be 0

    # The LLM scenario proves the serve path the @llm-gateway scenario
    # only installs: an LLM-type function is routed through the
    # llm-api-gateway and llm-request-router instead of the standard
    # invocation path. For LLM functions the CLI overrides the request
    # Host header with the LLM gateway host derived from the profile's
    # invoke host (invocation.localhost -> llm.localhost) and rewrites
    # the request body model to <functionId>/<model>, so the scenario
    # only supplies the OpenAI-compatible path and the model name.
    # Depends on the earlier control-plane install and NVCA
    # registration scenarios in this feature run; not a standalone
    # tag target.
    @llm-function-type
    Scenario: Operator creates, deploys, and invokes an LLM-type OpenAI-compatible sample function
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-openai-compatible-sample --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local --function-type LLM --inference-url /v1/chat/completions --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S --llm-model 'name=openai-compatible-sample,uris=/v1/chat/completions|/v1/embeddings,routingMethod=round_robin'
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-openai-compatible-sample --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      # The gateway path answers synchronously, so no --poll-duration
      # (the echo scenarios poll the 202 queue path). The sample always
      # answers with its fixed load-testing message, so the assertion
      # checks that content rather than only the envelope shape.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --inference-url /v1/chat/completions --model-name openai-compatible-sample --request-body '{"messages":[{"role":"user","content":"bdd-llm-echo"}]}' --timeout 120
        """
      Then the command exit code should be 0
      And the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"

      # The gateway must reject requests that carry no API key. curl
      # reports only the status code so the assertion cannot match
      # response-body noise.
      When I run command:
        """
        curl -s -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command exit code should be 0
      And the command output should contain "401"

      # Leave the cluster's GPU capacity free, same as the echo
      # scenarios.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function delete --deployment-only
        """
      Then the command exit code should be 0
