@ncp-local @multi-cluster @helmfile @pki @llm-registration
Feature: Register an LLM worker securely with a local split-cluster routing plane
  As a self-managed NVCF operator,
  I want Pylon registration to use the stack-issued TLS identity across clusters,
  so that plaintext or untrusted registration cannot silently enter the routing plane.

  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | NVCF_CLI        |
      | REPO_ROOT       |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
    And I prepare Helmfile environment "local-bdd-registration-tls" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
      | global.imagePullSecrets[0].name                          | nvcr-pull-secret                                                            |
      | global.helm.sources.repository                           | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                       |
      | global.image.repository                                  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                       |
      | global.workerEndpoints.llmRequestRouterAddress           | https://llm-request-router.nvcf.svc.cluster.local:50071                       |
      | addons.llm.requestRouter.workload.kind                    | StatefulSet                                                                 |
      | addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress | https://llm-request-router.nvcf.svc.cluster.local:50071                     |
      | observability.profile                                    | disabled                                                                     |
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-tls-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
    # Explore a shared BDD preflight so feature files need not repeat this check: https://github.com/NVIDIA/nvcf/issues/1411
    When I successfully run command "/bin/sh -c 'command -v grpcurl >/dev/null'"
    # Conflict precheck: the single-cluster topology owns the same host
    # ports. Run make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local
    # before retrying. k3d v5 exits 1 when the cluster is absent.
    When I run command "k3d cluster get ncp-local"
    Then the command exit code should be 1
    And multi-cluster ncp-local compute clusters are running:
      | ncp-local-compute-1 |
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

  Rule: Trusted registration is observable from the operator boundary

    @llm-registration-tls-install @llm-registration-tls-runtime
    Scenario: A trusted Pylon registers with every router and serves an authenticated request
      Given I prepare Helmfile environment "local-bdd-registration-tls" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      When I successfully run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-tls"
      Then the rendered manifests in "deploy/stacks/self-managed/out" should contain:
        | text                                                                                 |
        | https://llm-request-router.nvcf.svc.cluster.local:50071                              |
        | --grpc-pylon-dial-addr=https://llm-request-router.nvcf.svc.cluster.local:50071 |

      When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-registration-tls"

      And I successfully run command "kubectl --context k3d-ncp-local-cp wait clusterissuer nvcf-openbao-pki --for=condition=Ready --timeout=5m"
      And I successfully run command "kubectl --context k3d-ncp-local-cp wait certificate stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m"
      And I successfully run command "kubectl --context k3d-ncp-local-cp rollout status statefulset/llm-request-router -n nvcf --timeout=10m"

      And I successfully run command "kubectl --context k3d-ncp-local-cp get configmap/nvcf-api-remote-config -n nvcf -o yaml"
      Then the command output should contain "worker-address: https://llm-request-router.nvcf.svc.cluster.local:50071"

      # openssl verifies the externally reachable listener against the same
      # stack-issued CA and DNS identity that a compute-plane Pylon uses.
      # Explore a readable shared BDD DSL for this TLS listener probe: https://github.com/NVIDIA/nvcf/issues/1412
      When I successfully run command:
        """
        /bin/bash -c 'openssl s_client -connect 127.0.0.1:50071 -servername llm-request-router.nvcf.svc.cluster.local -alpn h2 -verify_return_error -CAfile <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) </dev/null 2>&1'
        """
      Then the command output should contain "Verify return code: 0 (ok)"
      And the command output should contain "ALPN protocol: h2"

      # grpcurl reports a client-side dial deadline when plaintext HTTP/2 is
      # sent to this verified TLS listener. The trusted Watch below proves
      # that the same endpoint remains healthy.
      # Explore a readable shared BDD DSL for this plaintext rejection probe: https://github.com/NVIDIA/nvcf/issues/1412
      When I successfully run command:
        """
        /bin/bash -c 'set -u; output=$(grpcurl -plaintext -max-time 5 -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates 2>&1); rc=$?; if [ "$rc" -eq 0 ]; then printf "%s\n" "plaintext Watch unexpectedly succeeded" >&2; exit 1; fi; printf "%s\n" "$output" | bash tests/bdd/scripts/assert-grpcurl-plaintext-tls-rejection.sh'
        """
      Then the command output should contain "plaintext-watch-rejected=tls-listener-timeout"

      When I successfully observe WatchStargates at "127.0.0.1:50071" with TLS authority "llm-request-router.nvcf.svc.cluster.local" using CA secret "stargate-quic-tls" in namespace "nvcf" and context "k3d-ncp-local-cp" for "3" seconds
      Then the command output should contain all:
        | text                                                                    |
        | llm-request-router-0                                                    |
        | llm-request-router-1                                                    |
        | llm-request-router-2                                                    |
        | https://llm-request-router.nvcf.svc.cluster.local:50071                |

      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-registration-tls --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 control-plane profile export --cluster-name ncp-local-cp
        """
      Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should have non-empty keys:
        | key                                 |
        | managementTls.caBundlePem           |
        | transportTls.trustBundleFingerprint |
        | transportTls.trustBundlePem         |

      When command has succeeded:
        """
        /bin/sh -c '${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init >/dev/null'
        """
      And I successfully run command "kubectl config use-context k3d-ncp-local-compute-1"
      And I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should exist
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |
      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd-registration-tls COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI}
        """
      And NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"
      When I successfully create function "bdd-registration-tls" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local" with CLI options:
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
        | --description | bdd-registration-tls                                                |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      Then every Pylon for function "bdd-registration-tls" using container "llm-worker" and context "k3d-ncp-local-compute-1" should report metrics within "10m":
        | metric                               | comparison | count |
        | pylon_registration_stream_connected | exactly    | 3     |
        | pylon_reverse_tunnel_connected       | exactly    | 3     |

      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-registration-tls"}]}
        """
      Then the command output should contain all:
        | text                    |
        | chat.completion         |
        | fixed 128-byte response |
      And I successfully undeploy the function selected by NVCF CLI

  @negative
  Rule: Invalid registration authorities fail before installation

    Scenario: Operator cannot render an invalid worker authority
      Given I prepare Helmfile environment "local-bdd-registration-tls-invalid-authority" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name                            | nvcr-pull-secret                                                     |
        | global.helm.sources.repository                             | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                                    | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.workerEndpoints.llmRequestRouterAddress             | https://llm_request_router.nvcf.svc.cluster.local:50071              |
        | addons.llm.requestRouter.workload.kind                      | StatefulSet                                                          |
        | addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress | https://llm_request_router.nvcf.svc.cluster.local:50071              |
        | observability.profile                                      | disabled                                                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-tls-invalid-authority-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-tls-invalid-authority"
      Then the command should fail
      And the command output should contain one of:
        | text                                                                                                                                                 |
        | global.workerEndpoints.llmRequestRouterAddress must use optional http:// or https:// followed by DNS-or-IPv4:port or [IPv6]:port with port 1-65535 |
