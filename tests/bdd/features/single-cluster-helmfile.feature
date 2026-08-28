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
      # The fixture is a copy of deploy/stacks/self-managed/environments/local.yaml,
      # which already carries every ncp-local local-mode override (storageClass,
      # replica counts, NVCA self-managed endpoints, addons.llm.*, agentConfig,
      # ingress.gatewayApi.*). The Background only overlays the operator-specific
      # values that vary per NGC org and pull-secret name.
      And I prepare Helmfile environment "local-bdd" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                   |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | observability.profile                         | disabled                                                           |
      And I prepare Helmfile environment "local-bdd" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential

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

      When I run command "kubectl --context k3d-ncp-local get configmap/nvcf-api-remote-config -n nvcf -o yaml"
      Then the command exit code should be 0
      And the command output should contain "llm-router-client-image: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/pylon:0.14.1"

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
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd control-plane profile export --cluster-name ncp-local
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      Then the command exit code should be 0

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
      # The target cluster matches controlPlane.clusterName in the exported
      # profile, so registration selects the in-cluster service endpoints.
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should contain:
        """
        clusterName: ncp-local
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
          icmsServiceURL: http://api.sis.svc.cluster.local:8080
          revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
          natsURL: nats://nats.nats-system.svc.cluster.local:4222
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local" should complete rollout within "10m"

      Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"

  Rule: Helmfile-installed local NVCF can run a sample function

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenario in
    # this feature run, and is not a standalone tag target.
    @function-lifecycle
    Scenario: Operator creates, deploys, and invokes the Load Tester Supreme sample function
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

      # Remove the deployment: the local sizing cannot hold every
      # scenario's deployment at once.
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
        | --llm-model      | name=openai-compatible-sample,uris=/v1/chat/completions\|/v1/embeddings,routingMethod=round_robin |

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

      # curl reports only the status code so the assertion cannot
      # match response-body noise.
      When I run command:
        """
        curl -s -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command exit code should be 0
      And the command output should contain "401"

      # Leave the GPU capacity free, same as the echo scenarios.
      And I successfully undeploy the function selected by NVCF CLI
