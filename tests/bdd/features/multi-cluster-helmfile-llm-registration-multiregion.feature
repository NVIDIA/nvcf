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
        | addons.llm.pki.dnsNames[2]                                        | region-b-watch.nvcf.svc.cluster.local                                                  |
        | addons.llm.pki.dnsNames[3]                                        | *.llm-request-router-region-b-headless.nvcf.svc.cluster.local                          |
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
      When I run command "kubectl --context k3d-ncp-local-cp get certificate llm-request-router-grpc-tls -n envoy-gateway-system -o jsonpath={.spec.dnsNames}"
      Then the command exit code should be 0
      And the command output should contain "region-b-watch.nvcf.svc.cluster.local"
      When I run command "kubectl --context k3d-ncp-local-cp rollout status deployment/llm-request-router -n nvcf --timeout=10m"
      Then the command exit code should be 0

      # Region B: write visible override values for a second LLM request
      # router with a distinct StatefulSet identity.
      Given I write yaml file "tests/bdd/out/region-b-values.yaml" with values:
        | llmRequestRouter.fullnameOverride                            | llm-request-router-region-b                                                          |
        | llmRequestRouter.replicaCount                                | 2                                                                                    |
        | llmRequestRouter.workload.kind                               | StatefulSet                                                                          |
        | llmRequestRouter.service.headlessName                        | llm-request-router-region-b-headless                                                 |
        | llmRequestRouter.kubernetes.advertisedHostnameTemplate       | {pod_name}.llm-request-router-region-b-headless.nvcf.svc.cluster.local               |
        | llmRequestRouter.discovery.remoteWatchUrls                   | []                                                                                   |
        | llmRequestRouter.backendRouter.enabled                       | true                                                                                 |
        | llmRequestRouter.backendRouter.pylonGrpcDialAddress          | https://region-b-watch.nvcf.svc.cluster.local:50071                                  |
        | llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress | region-b-watch.nvcf.svc.cluster.local:50072                                       |
        | llmRequestRouter.backendRouter.image.pullPolicy              | IfNotPresent                                                                        |
        | llmRequestRouter.serviceAccount.create                       | false                                                                                |
        | llmRequestRouter.serviceAccount.name                         | llm-request-router                                                                   |
        | llmRequestRouter.pki.enabled                                 | false                                                                                |
        | llmRequestRouter.certificate.enabled                         | false                                                                                |
        | llmRequestRouter.tls.mode                                    | existingSecret                                                                       |
        | llmRequestRouter.tls.secretName                              | stargate-quic-tls                                                                    |
        | llmRequestRouter.image.pullPolicy                            | IfNotPresent                                                                         |

      # Export Region A base values so Region B inherits image tags and
      # shared config; then install Region B with the visible overrides.
      When I successfully run command:
        """
        /bin/bash -c 'helm --kube-context k3d-ncp-local-cp get values llm-request-router --namespace nvcf --output json | jq "{llmRequestRouter: .llmRequestRouter}" > ${REPO_ROOT}/tests/bdd/out/region-a-base-values.json'
        """
      When I run command:
        """
        helm --kube-context k3d-ncp-local-cp upgrade --install llm-request-router-region-b ${REPO_ROOT}/deploy/helm/llm-request-router/llm-request-router --namespace nvcf --values ${REPO_ROOT}/tests/bdd/out/region-a-base-values.json --values ${REPO_ROOT}/tests/bdd/out/region-b-values.yaml --wait --timeout 10m
        """
      Then the command exit code should be 0

      # Region B gateway resources: GRPCRoute, BackendTrafficPolicy,
      # and cross-namespace ReferenceGrant.
      When I successfully run command:
        """
        kubectl --context k3d-ncp-local-cp apply -f - <<'YAML'
        apiVersion: gateway.networking.k8s.io/v1
        kind: GRPCRoute
        metadata:
          name: llm-worker-region-b-grpc
          namespace: envoy-gateway-system
        spec:
          parentRefs:
            - name: grpc-gw
              namespace: envoy-gateway-system
              sectionName: llm-grpc
          hostnames:
            - "region-b-watch.nvcf.svc.cluster.local"
            - "*.llm-request-router-region-b-headless.nvcf.svc.cluster.local"
          rules:
            - backendRefs:
                - name: llm-request-router-region-b-backend-router
                  namespace: nvcf
                  port: 50071
        ---
        apiVersion: gateway.envoyproxy.io/v1alpha1
        kind: BackendTrafficPolicy
        metadata:
          name: llm-worker-region-b-grpc-streams
          namespace: envoy-gateway-system
        spec:
          targetRefs:
            - group: gateway.networking.k8s.io
              kind: GRPCRoute
              name: llm-worker-region-b-grpc
          timeout:
            http:
              requestTimeout: 0s
        ---
        apiVersion: gateway.networking.k8s.io/v1beta1
        kind: ReferenceGrant
        metadata:
          name: allow-llm-worker-region-b-route
          namespace: nvcf
        spec:
          from:
            - group: gateway.networking.k8s.io
              kind: GRPCRoute
              namespace: envoy-gateway-system
          to:
            - group: ""
              kind: Service
              name: llm-request-router-region-b-backend-router
        YAML
        """

      # Discover the control-plane endpoint IP for the region-b-watch alias.
      When I run command "kubectl --context k3d-ncp-local-compute-1 get endpoints llm-request-router --namespace nvcf --output jsonpath={.subsets[0].addresses[0].ip}"
      Then the command exit code should be 0
      And I export command output to environment variable "CONTROL_PLANE_IP"

      # Apply the region-b-watch Service and Endpoints alias in both
      # clusters so each cluster can reach the Region B backend-router
      # by name. The discovered control-plane IP is interpolated by the
      # DSL so no external templating tool is needed.
      When I successfully run command:
        """
        kubectl --context k3d-ncp-local-cp apply -f - <<'YAML'
        apiVersion: v1
        kind: Service
        metadata:
          name: region-b-watch
          namespace: nvcf
        spec:
          ports:
            - name: llm-grpc
              port: 50071
              targetPort: llm-grpc
              protocol: TCP
            - name: llm-quic
              port: 50072
              targetPort: llm-quic
              protocol: UDP
        ---
        apiVersion: v1
        kind: Endpoints
        metadata:
          name: region-b-watch
          namespace: nvcf
        subsets:
          - addresses:
              - ip: ${CONTROL_PLANE_IP}
            ports:
              - name: llm-grpc
                port: 50071
                protocol: TCP
              - name: llm-quic
                port: 50072
                protocol: UDP
        YAML
        """
      When I successfully run command:
        """
        kubectl --context k3d-ncp-local-compute-1 apply -f - <<'YAML'
        apiVersion: v1
        kind: Service
        metadata:
          name: region-b-watch
          namespace: nvcf
        spec:
          ports:
            - name: llm-grpc
              port: 50071
              targetPort: llm-grpc
              protocol: TCP
            - name: llm-quic
              port: 50072
              targetPort: llm-quic
              protocol: UDP
        ---
        apiVersion: v1
        kind: Endpoints
        metadata:
          name: region-b-watch
          namespace: nvcf
        subsets:
          - addresses:
              - ip: ${CONTROL_PLANE_IP}
            ports:
              - name: llm-grpc
                port: 50071
                protocol: TCP
              - name: llm-quic
                port: 50072
                protocol: UDP
        YAML
        """

      # Wait for Region B workloads to be ready.
      When I successfully run command "kubectl --context k3d-ncp-local-cp rollout status statefulset/llm-request-router-region-b --namespace nvcf --timeout=10m"
      And deployment "llm-request-router-region-b-backend-router" in namespace "nvcf" using context "k3d-ncp-local-cp" should complete rollout within "10m"

      # The initial region advertises an explicit HTTPS recursive seed while
      # retaining every concrete Deployment pod identity. Three distinct
      # <replicaset-hash>-<pod-suffix> identities prove per-pod discovery
      # rather than a single ClusterIP or headless alias.
      When I successfully observe WatchStargates at "127.0.0.1:50071" with TLS authority "llm-request-router.nvcf.svc.cluster.local" using CA secret "stargate-quic-tls" in namespace "nvcf" and context "k3d-ncp-local-cp" for "3" seconds
      Then the command output should contain "https://region-b-watch.nvcf.svc.cluster.local:50071"
      And the command output should have exactly "3" distinct matches of "llm-request-router-[a-z0-9]{5,10}-[a-z0-9]{5}"
      And the command output should not match "([0-9]{1,3}-){3}[0-9]{1,3}\."

      # The remote HTTPS authority resolves to two stable StatefulSet router
      # identities and never relies on a dashed-IP SRV alias.
      When I successfully observe WatchStargates at "127.0.0.1:50071" with TLS authority "region-b-watch.nvcf.svc.cluster.local" using CA secret "stargate-quic-tls" in namespace "nvcf" and context "k3d-ncp-local-cp" for "3" seconds
      Then the command output should contain all:
        | text                          |
        | llm-request-router-region-b-0 |
        | llm-request-router-region-b-1 |
      And the command output should have exactly "2" distinct matches of "llm-request-router-region-b-[0-9]+"
      And the command output should not match "([0-9]{1,3}-){3}[0-9]{1,3}\."

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
      And NVCFBackend "ncp-local-compute-1" in namespace "nvca-operator" using context "k3d-ncp-local-compute-1" should report agent status "healthy" within "10m"

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

      Then every Pylon for function "bdd-registration-multiregion" using container "llm-worker" and context "k3d-ncp-local-compute-1" should report metrics within "10m":
        | metric                               | comparison | count |
        | pylon_registration_stream_connected | exactly    | 5     |
        | pylon_reverse_tunnel_connected       | at least   | 3     |

      When I successfully invoke model "openai-compatible-sample" at "/v1/chat/completions" with timeout "120" seconds:
        """
        {"messages":[{"role":"user","content":"bdd-registration-multiregion"}]}
        """
      Then the command output should contain all:
        | text                    |
        | chat.completion         |
        | fixed 128-byte response |
      And I successfully undeploy the function selected by NVCF CLI
