@ncp-local @multi-cluster @helmfile @pki @llm-registration @multi-region
Feature: Register an LLM worker securely with routers in two local regions
  As a self-managed NVCF operator,
  I want recursive router discovery to retain explicit HTTPS transport,
  so that one worker can register with routable Deployment and StatefulSet routers across regions.

  Rule: A secure remote Watch URI expands the registered router topology

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | NVCF_CLI        |
        | REPO_ROOT       |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      And I prepare Helmfile environment "local-bdd-registration-multiregion" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name                                  | nvcr-pull-secret                                                                       |
        | global.helm.sources.repository                                   | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                                  |
        | global.image.repository                                          | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                                  |
        | global.workerEndpoints.llmRequestRouterAddress                   | https://llm-request-router.nvcf.svc.cluster.local:50071                                |
        | addons.llm.requestRouter.workload.kind                            | Deployment                                                                             |
        | addons.llm.requestRouter.discovery.remoteWatchUrls[0]             | https://region-b-watch.nvcf.svc.cluster.local:50071                                    |
        | addons.llm.requestRouter.grpcTls.dnsNames[1]                      | region-b-watch.nvcf.svc.cluster.local                                                  |
        | addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress       | https://llm-request-router.nvcf.svc.cluster.local:50071                                |
        | addons.llm.pki.dnsNames[2]                                        | *.llm-request-router-region-b-headless.nvcf.svc.cluster.local                          |
        | observability.profile                                             | disabled                                                                               |
      And I prepare Helmfile environment "local-bdd-registration-multiregion" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-multiregion-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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

    @llm-registration-multiregion-install
    Scenario: Operator installs two secure regions with distinct router workload identities
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-multiregion"
      Then the command exit code should be 0
      And the rendered manifests in "deploy/stacks/self-managed/out" should contain:
        | text                                                                       |
        | kind: Deployment                                                           |
        | --remote-stargate-url=https://region-b-watch.nvcf.svc.cluster.local:50071 |

      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-registration-multiregion"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp wait certificate llm-request-router-grpc-tls -n envoy-gateway-system --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp rollout status deployment/llm-request-router -n nvcf --timeout=10m"
      Then the command exit code should be 0

      When I run command "tests/bdd/scripts/install-llm-region-b.sh"
      Then the command exit code should be 0

      # The initial region advertises an explicit HTTPS recursive seed while
      # retaining every concrete Deployment pod identity.
      When I run command:
        """
        /bin/bash -c 'set -eu; output=$(grpcurl -max-time 3 -cacert <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) -authority llm-request-router.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates 2>&1 || true); pods=$(kubectl --context k3d-ncp-local-cp get pods -n nvcf -l app.kubernetes.io/instance=llm-request-router,app.kubernetes.io/name=llm-request-router -o jsonpath="{range .items[*]}{.metadata.name}{\"\\n\"}{end}"); count=0; while IFS= read -r pod; do [ -z "$pod" ] && continue; printf "%s" "$output" | grep -Fq "$pod"; count=$((count + 1)); done <<<"$pods"; [ "$count" -eq 3 ]; printf "%s" "$output" | grep -Fq "https://region-b-watch.nvcf.svc.cluster.local:50071"; printf "region-a-deployment=%s remote-watch=https\n" "$count"'
        """
      Then the command exit code should be 0
      And the command output should contain "region-a-deployment=3 remote-watch=https"

      # The remote HTTPS authority resolves to two stable StatefulSet router
      # identities and never relies on a dashed-IP SRV alias.
      When I run command:
        """
        /bin/bash -c 'set -eu; output=$(grpcurl -max-time 3 -cacert <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) -authority region-b-watch.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates 2>&1 || true); printf "%s" "$output" | grep -Fq "llm-request-router-region-b-0"; printf "%s" "$output" | grep -Fq "llm-request-router-region-b-1"; printf "region-b-statefulset=2 tls=https\n"'
        """
      Then the command exit code should be 0
      And the command output should contain "region-b-statefulset=2 tls=https"

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-registration-multiregion --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 control-plane profile export --cluster-name ncp-local-cp
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
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd-registration-multiregion COMPUTE_KUBE_CONTEXT=k3d-ncp-local-compute-1 NVCF_CLI=${NVCF_CLI}
        """
      Then the command exit code should be 0
      Then NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

    @llm-registration-multiregion-runtime
    Scenario: Pylon recursively registers with both regions and serves an authenticated request
      Given I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"
      When I successfully create function "bdd-registration-multiregion" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local" with CLI options:
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
        | --description | bdd-registration-multiregion                                        |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      When I run command:
        """
        /bin/sh -c 'set -eu; for attempt in $(seq 1 120); do row=$(kubectl --context k3d-ncp-local-compute-1 get pods -A -o json | jq -r "[.items[] | select(any(.spec.containers[]?; .name == \"llm-worker\")) | [.metadata.namespace,.metadata.name] | @tsv] | first // empty"); if [ -n "$row" ]; then ns=$(printf "%s" "$row" | cut -f1); pod=$(printf "%s" "$row" | cut -f2); metrics=$(kubectl --context k3d-ncp-local-compute-1 get --raw "/api/v1/namespaces/$ns/pods/$pod:9089/proxy/metrics" 2>/dev/null || true); registration=$(printf "%s\n" "$metrics" | grep -c "^pylon_registration_stream_connected.* 1$" || true); reverse=$(printf "%s\n" "$metrics" | grep -c "^pylon_reverse_tunnel_connected.* 1$" || true); if [ "$registration" -eq 5 ] && [ "$reverse" -ge 3 ]; then printf "registration=%s reverse=%s regions=2\n" "$registration" "$reverse"; exit 0; fi; fi; sleep 5; done; exit 1'
        """
      Then the command exit code should be 0
      And the command output should contain "registration=5"
      And the command output should contain "regions=2"

      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-registration-multiregion"}]}
        """
      Then the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"
      And I successfully undeploy the function selected by NVCF CLI
