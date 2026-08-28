@ncp-local @multi-cluster @helmfile @pki @llm-registration
Feature: Register an LLM worker securely with every router in a local split-cluster stack
  As a self-managed NVCF operator,
  I want Pylon registration to use the stack-issued TLS identity across clusters,
  so that plaintext or untrusted registration cannot silently enter the routing plane.

  Rule: Secure registration is observable from the operator boundary

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
      And I prepare Helmfile environment "local-bdd-registration-tls" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-tls-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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

    @llm-registration-tls-install
    Scenario: Operator installs a TLS-only registration endpoint with three concrete routers
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-tls"
      Then the command exit code should be 0
      And the rendered manifests in "deploy/stacks/self-managed/out" should contain:
        | text                                                                                 |
        | https://llm-request-router.nvcf.svc.cluster.local:50071                              |
        | --grpc-pylon-dial-addr=https://llm-request-router.nvcf.svc.cluster.local:50071 |

      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-registration-tls"
      Then the command exit code should be 0

      When I run command "kubectl --context k3d-ncp-local-cp wait clusterissuer nvcf-openbao-pki --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp wait certificate stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp rollout status statefulset/llm-request-router -n nvcf --timeout=10m"
      Then the command exit code should be 0

      When I run command "kubectl --context k3d-ncp-local-cp get configmap/nvcf-api-remote-config -n nvcf -o yaml"
      Then the command exit code should be 0
      And the command output should contain "worker-address: https://llm-request-router.nvcf.svc.cluster.local:50071"

      # openssl verifies the externally reachable listener against the same
      # stack-issued CA and DNS identity that a compute-plane Pylon uses.
      When I run command:
        """
        /bin/bash -c 'openssl s_client -connect 127.0.0.1:50071 -servername llm-request-router.nvcf.svc.cluster.local -alpn h2 -verify_return_error -CAfile <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) </dev/null 2>&1'
        """
      Then the command exit code should be 0
      And the command output should contain "Verify return code: 0 (ok)"
      And the command output should contain "ALPN protocol: h2"

      When I run command:
        """
        grpcurl -plaintext -max-time 5 -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates
        """
      Then the command exit code should be 1

      # WatchStargates is a long-lived stream. Normalize grpcurl's deadline
      # exit after it prints the initial snapshot.
      When I run command:
        """
        /bin/bash -c 'grpcurl -max-time 3 -cacert <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) -authority llm-request-router.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates; rc=$?; [ "$rc" -ne 0 ]'
        """
      Then the command exit code should be 0
      And the command output should contain "llm-request-router-0"
      And the command output should contain "llm-request-router-1"
      And the command output should contain "llm-request-router-2"
      And the command output should contain "https://llm-request-router.nvcf.svc.cluster.local:50071"

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-registration-tls --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 control-plane profile export --cluster-name ncp-local-cp
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should have non-empty keys:
        | key                                 |
        | managementTls.caBundlePem           |
        | transportTls.trustBundleFingerprint |
        | transportTls.trustBundlePem         |

      And command has succeeded:
        """
        /bin/sh -c '${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init >/dev/null'
        """
      When I run command "kubectl config use-context k3d-ncp-local-compute-1"
      Then the command exit code should be 0
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should exist
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd-registration-tls COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI}
        """
      Then the command exit code should be 0
      Then NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

    @llm-registration-tls-runtime
    Scenario: Pylon registers with every router and serves an authenticated LLM request
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

      # Discover the workload pod by its public sidecar name, then poll the
      # Pylon metrics endpoint until all three router streams and reverse
      # tunnels are connected.
      When I run command:
        """
        /bin/sh -c 'set -eu; for attempt in $(seq 1 120); do row=$(kubectl --context k3d-ncp-local-compute-1 get pods -A -o json | jq -r "[.items[] | select(any(.spec.containers[]?; .name == \"llm-worker\")) | [.metadata.namespace,.metadata.name] | @tsv] | first // empty"); if [ -n "$row" ]; then ns=$(printf "%s" "$row" | cut -f1); pod=$(printf "%s" "$row" | cut -f2); metrics=$(kubectl --context k3d-ncp-local-compute-1 get --raw "/api/v1/namespaces/$ns/pods/$pod:9089/proxy/metrics" 2>/dev/null || true); registration=$(printf "%s\n" "$metrics" | grep -c "^pylon_registration_stream_connected.* 1$" || true); tunnels=$(printf "%s\n" "$metrics" | grep -c "^pylon_reverse_tunnel_connected.* 1$" || true); if [ "$registration" -eq 3 ] && [ "$tunnels" -eq 3 ]; then printf "registration=%s reverse=%s\n" "$registration" "$tunnels"; exit 0; fi; fi; sleep 5; done; exit 1'
        """
      Then the command exit code should be 0
      And the command output should contain "registration=3 reverse=3"

      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-registration-tls"}]}
        """
      Then the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"
      And I successfully undeploy the function selected by NVCF CLI
