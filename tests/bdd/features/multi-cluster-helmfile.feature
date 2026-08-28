@ncp-local @multi-cluster @helmfile
Feature: Install a local multi-cluster NVCF stack with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow across a local multi-cluster
  ncp-local topology,
  so that I can install the control plane on one cluster and register and
  install the NVCA operator on a separately registered compute cluster.

  # Helmfile installs the control plane from the operator-authored
  # environment. Registration then exports that installed environment as a
  # control-plane profile and passes it to the compute-plane Make target.
  # The feature runs `nvcf-cli init` explicitly before registration.
  # See tests/bdd/AGENTS.md "CLI vs Helmfile install paths".

  Rule: Helmfile installs the control plane on the control-plane cluster

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      # The multi-cluster fixture starts from local service-DNS
      # endpoint values, then the Background overlays
      # operator-specific registry values before the first Helmfile
      # install. Later scenarios reuse that install instead of
      # reinstalling with different secrets or URLs.
      And I prepare Helmfile environment "local-bdd" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | observability.profile                          | disabled                                                            |
      And I prepare Helmfile environment "local-bdd" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/10081, and ncp-local-cp also
      # needs NATS on 4222 plus the worker callback port 10086.
      # Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      And I run command "k3d cluster get ncp-local"
      And the command exit code should be 1
      And multi-cluster ncp-local compute clusters are running:
        | ncp-local-compute-1 |
      # The Helmfile install runs against whatever ambient kubectl
      # context is set. Switch to the control-plane cluster so the
      # subsequent pull-secret applies and the install target both
      # land on k3d-ncp-local-cp.
      And command has succeeded:
        """
        kubectl config use-context k3d-ncp-local-cp
        """
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

    @control-plane @llm-gateway
    Scenario: Operator installs the control plane through Helmfile on the control-plane cluster
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd"
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local-cp":
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

      # NVCF API must advertise the compute-reachable alias, not the
      # control-plane pod or headless Service address. The same DNS name is a
      # normal Service on the control plane and an Endpoints-backed alias on
      # the compute plane.
      When I run command "kubectl --context k3d-ncp-local-cp get configmap/nvcf-api-remote-config -n nvcf -o yaml"
      Then the command exit code should be 0
      And the command output should contain "worker-address: https://llm-request-router.nvcf.svc.cluster.local:50071"
      And the command output should contain "llm-router-client-image: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/pylon:0.14.1"

      # The dial address chooses the cross-cluster network path. Stargate's
      # per-pod authority remains the gRPC authority and reverse QUIC SNI, so
      # the managed certificate covers the stable Service and headless pod
      # wildcard without relying on public DNS.
      Then Kubernetes resource "Certificate/stargate-quic-tls" in namespace "nvcf" using context "k3d-ncp-local-cp" should contain:
        """
        spec:
          secretName: stargate-quic-tls
          dnsNames:
            - llm-request-router.nvcf.svc.cluster.local
            - "*.llm-request-router-headless.nvcf.svc.cluster.local"
        """

      # gRPC TLS is a separate listener identity from reverse QUIC. The
      # Certificate is written in the Gateway namespace because the HTTPS
      # listener consumes its Secret there.
      Then Kubernetes resource "Certificate/llm-request-router-grpc-tls" in namespace "envoy-gateway-system" using context "k3d-ncp-local-cp" should contain:
        """
        spec:
          secretName: llm-request-router-grpc-tls
          dnsNames:
            - llm-request-router.nvcf.svc.cluster.local
          issuerRef:
            kind: ClusterIssuer
            name: nvcf-openbao-pki
        """

      Then Kubernetes resource "Gateway/grpc-gw" in namespace "envoy-gateway-system" using context "k3d-ncp-local-cp" should contain:
        """
        spec:
          gatewayClassName: eg
          listeners:
            - name: tcp
              protocol: TCP
              port: 10081
              allowedRoutes:
                namespaces:
                  from: All
            - name: worker-tcp
              protocol: TCP
              port: 10086
              allowedRoutes:
                namespaces:
                  from: All
            - name: llm-grpc
              protocol: HTTPS
              port: 50071
              tls:
                mode: Terminate
                certificateRefs:
                  - group: ""
                    kind: Secret
                    name: llm-request-router-grpc-tls
              allowedRoutes:
                namespaces:
                  from: All
            - name: llm-quic
              protocol: UDP
              port: 50072
              allowedRoutes:
                namespaces:
                  from: All
        """

      Then Kubernetes resource "BackendTrafficPolicy/llm-worker-grpc-streams" in namespace "envoy-gateway-system" using context "k3d-ncp-local-cp" should contain:
        """
        spec:
          targetRefs:
            - group: gateway.networking.k8s.io
              kind: GRPCRoute
              name: llm-worker-grpc
          timeout:
            http:
              requestTimeout: 0s
        """
      # These routes are installed by ncp-local before the Helmfile
      # stack, then become fully resolved once the control-plane
      # Services exist. Check route status here so Gateway wiring
      # failures point at the route layer instead of surfacing only
      # during function invocation.
      Then these Gateway API routes should be accepted and resolved using context "k3d-ncp-local-cp" within "2m":
        | kind      | name                        | namespace            | parent      |
        | GRPCRoute | llm-worker-grpc             | envoy-gateway-system | grpc-gw     |
        | UDPRoute  | llm-worker-quic             | envoy-gateway-system | grpc-gw     |
        | HTTPRoute | nvcf-api-control-plane      | nvcf                 | shared-gw   |
        | HTTPRoute | invocation-control-plane    | nvcf                 | shared-gw   |
        | HTTPRoute | reval-control-plane         | nvcf                 | shared-gw   |
        | HTTPRoute | ess-control-plane           | ess                  | shared-gw   |
        | HTTPRoute | sis-control-plane           | sis                  | shared-gw   |
        | GRPCRoute | nvcf-api-control-plane-grpc | nvcf                 | api-grpc-gw |

      When I run command "kubectl --context k3d-ncp-local-cp wait certificate llm-request-router-grpc-tls -n envoy-gateway-system --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0

  Rule: Helmfile registers and installs NVCA on the compute cluster

    Background:
      Given these environment variables are set:
        | name      |
        | NVCF_CLI  |
        | REPO_ROOT |
      # This rule depends on the earlier control-plane scenario in the
      # same feature run. That scenario authors local-bdd.yaml with
      # the compute-reachable endpoints, creates the pull secrets, and
      # installs the control plane. Do not repeat that setup here.

    @nvca-registration
    Scenario: Operator registers the compute cluster and installs the NVCA operator there
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 control-plane profile export --cluster-name ncp-local-cp
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      Then the command exit code should be 0

      # nvcf-cli cluster register auto-discovers the target cluster's
      # OIDC issuer + JWKS by running a probe Job in the CURRENT
      # kubectl context, then POSTs that identity to ICMS so future
      # PSAT tokens from this cluster can be validated. The compute
      # cluster (not the control plane) is the target, so switch the
      # context to it BEFORE register-cluster runs. If we registered
      # from the cp context, ICMS would record the cp cluster's JWKS
      # for the compute cluster row and the compute agent's tokens
      # would 401 against ICMS at runtime.
      #
      # compute-plane install that follows also runs helm against the
      # ambient context, so this single switch covers both steps.
      When I run command "kubectl config use-context k3d-ncp-local-compute-1"
      Then the command exit code should be 0

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should exist
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local-compute-1":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should complete rollout within "10m"

      # The default NVCFBackend CR is created on the compute cluster
      # by the nvca-operator helm chart at install time (helm reports
      # this in its post-install output), and the NVCA agent updates
      # its own .status.agentStatus locally. The NVCFBackend CRD is
      # therefore only registered on k3d-ncp-local-compute-1, not on
      # k3d-ncp-local-cp. Wait on the compute cluster.
      Then NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

  Rule: Helmfile-installed multi-cluster NVCF can run workloads

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenarios in
    # this feature run, and is not a standalone tag target.
    # Failing until GitHub issue #1098 is resolved and the fix from GitHub
    # issue #1032 is consumed by the self-managed stack.
    @nvct-task-api
    Scenario: Operator launches an NVCT task and waits for it to complete
      When I run command:
        """
        env NVCT_BDD_TASK_INSTANCE_TYPE=NCP.GPU.H100_1x tests/bdd/scripts/run-nvct-task-smoke.sh
        """
      Then the command exit code should be 0
      And the command output should contain "COMPLETED"

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
        | --backend       | ncp-local-compute-1 |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-load-tester-supreme                                            |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-echo","repeats":1}
        """
      Then the command output should contain "bdd-echo"

      # Keep the simulated GPU capacity available for the next scenario.
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
        | --backend       | ncp-local-compute-1 |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-grpc-load-tester-supreme                                       |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      When I successfully invoke the function selected by NVCF CLI over plaintext gRPC service "Echo" method "EchoMessage" with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-grpc-echo"}
        """
      Then the command output should contain "bdd-grpc-echo"

      # Keep the simulated GPU capacity available for the LLM scenario.
      And I successfully undeploy the function selected by NVCF CLI

    # This fixed-response sample proves the multi-cluster LLM routing and
    # request/response contract. It is not a token-generation capacity test.
    # The compute fixture consumes the exported profile bundle with secure
    # Stargate QUIC transport.
    # The scenario depends on the earlier control-plane install and compute
    # registration scenarios and is not a standalone tag target.
    @llm-function-type
    Scenario: Operator creates, deploys, and invokes an LLM-type OpenAI-compatible sample function
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

      When I successfully create function "bdd-multi-openai-compatible-sample" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local" with CLI options:
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
        | --backend       | ncp-local-compute-1 |
        | --regions       | us-west-1           |
        | --min-instances | 1                   |
        | --max-instances | 1                   |
        | --timeout       | 900                 |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-multi-openai-compatible-sample                                  |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-multi-llm-echo"}]}
        """
      Then the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"

      # Authentication remains enforced at the LLM gateway in the split
      # topology. Only the status code is captured for a stable assertion.
      When I run command:
        """
        curl -s --connect-timeout 5 --max-time 30 -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -H "traceparent: 00-00000000000000000000000000001019-0000000000001019-01" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command exit code should be 0
      And the command output should contain "401"

      And I successfully undeploy the function selected by NVCF CLI
