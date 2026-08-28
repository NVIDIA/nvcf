@ncp-local @single-cluster @helmfile @pki
Feature: Install a local single-cluster NVCF stack with PKI-secured LLM transport
  As a self-managed NVCF operator,
  I want the Helmfile workflow with the LLM PKI addon enabled,
  so that an LLM function answers invocations over a QUIC tunnel whose
  trust chain is issued by the stack's own PKI.

  # Owns its own Helmfile environment (local-bdd-pki) for install-time PKI
  # values. The exported profile carries the OpenBao root CA and fingerprint
  # into registration values. The shared compute fixture uses secure QUIC so
  # the registered bundle trust remains active.

  Rule: Helmfile installs the control plane with the LLM PKI addon

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | NVCF_CLI        |
        | REPO_ROOT       |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-pki.yaml"
      # PKI render requirements: dnsNames must cover the router's
      # advertised hostname (a single replica advertises its plain
      # service DNS name), allowedDomains constrains the OpenBao
      # signing role, and the provisioning hook needs the
      # nvcf-openbao-migrations tag (no default propagates from env).
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-pki.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                         |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                     |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                     |
        | api.remoteConfig.configData.nvcf.sidecars.llm-router-client-image | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/pylon:0.14.1                |
        | addons.llm.requestRouter.chartPath             | ../../../helm/llm-request-router/llm-request-router                      |
        | addons.llm.pki.enabled                         | true                                                                      |
        | addons.llm.pki.dnsNames[0]                     | llm-request-router.nvcf.svc.cluster.local                                |
        | addons.llm.pki.allowedDomains                  | nvcf.svc.cluster.local                                                    |
        | addons.llm.pki.image.tag                       | 0.16.2                                                                    |
        | observability.profile                          | disabled                                                                  |
      And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd-pki.yaml"
      And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd-pki.yaml" with keys:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-pki-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/10081, NATS on 4222, and the worker
      # callback port 10086, overlapping host ports single-cluster
      # ncp-local needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      When I run command "k3d cluster get ncp-local-cp"
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

    @llm-pki-render
    Scenario: Operator validates the PKI-enabled environment renders
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-pki"
      Then the command exit code should be 0
      And the rendered manifests in "deploy/stacks/self-managed/out" should contain Kubernetes resource "ClusterIssuer/nvcf-openbao-pki"
      And the rendered manifests in "deploy/stacks/self-managed/out" should contain:
        | text                                               |
        | name: ADDONS_LLM_ENABLED                           |
        | value: "true"                                     |
        | llm-request-router.nvcf.svc.cluster.local          |
        | name: NVCF_SERVICE_PKI_ALLOWED_DOMAINS              |
        | value: "nvcf.svc.cluster.local"                   |
        | nvcf-openbao-migrations:0.16.2                     |
      # A colocated worker uses the in-cluster h2c Service directly. The
      # dedicated HTTPS identity and route belong only to an explicitly
      # enabled remote-worker ingress.
      And the rendered manifests in "deploy/stacks/self-managed/out" should not contain:
        | text                                |
        | name: llm-worker-grpc               |
        | name: llm-request-router-grpc-tls   |
        | name: llm-worker-grpc-streams       |

    @llm-pki-install
    Scenario: Operator installs the control plane with the PKI addon enabled
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-pki"

      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name                      | namespace            |
        | nats                      | nats-system          |
        | cert-manager              | cert-manager         |
        | openbao-server            | vault-system         |
        | nvcf-pki                  | cert-manager         |
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
      And the command output should contain "worker-address: llm-request-router.nvcf.svc.cluster.local:50071"
      And the command output should contain "llm-router-client-image: nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/pylon:0.14.1"

      Then these Kubernetes resources should not exist in namespace "envoy-gateway-system" using context "k3d-ncp-local":
        | kind                 | name                         |
        | GRPCRoute            | llm-worker-grpc              |
        | Certificate          | llm-request-router-grpc-tls  |
        | BackendTrafficPolicy | llm-worker-grpc-streams      |

      # The issuer and the stargate leaf are functional gates for the
      # secure tunnel: the router cannot serve TLS before cert-manager
      # writes the stargate-quic-tls Secret.
      When I run command "kubectl --context k3d-ncp-local wait clusterissuer nvcf-openbao-pki --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0

      When I run command "kubectl --context k3d-ncp-local wait certificate stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0

      # Registration consumes the generated profile. Export it only after the
      # PKI is ready so the profile carries both management and transport trust.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-pki control-plane profile export --cluster-name ncp-local
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should contain:
        """
        managementTls:
          trustMode: bundle
        transportTls:
          trustMode: bundle
        """
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should have non-empty keys:
        | key                                 |
        | managementTls.caBundlePem           |
        | transportTls.trustBundleFingerprint |
        | transportTls.trustBundlePem         |

      # The profile carries endpoints and trust, not credentials. Initialize
      # the harness-isolated, config-scoped CLI state before compute-plane
      # registration. Discard stdout because init prints the minted token;
      # stderr and the exit status remain available for diagnostics.
      And command has succeeded:
        """
        /bin/sh -c '${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init >/dev/null'
        """

  Rule: The compute plane installs with bundle trust distributed from OpenBao

    Background:
      Given these environment variables are set:
        | name      |
        | NVCF_CLI  |
        | REPO_ROOT |
      # This rule depends on the earlier control-plane install scenario
      # in the same feature run. The @llm-pki-nvca scenario is not a
      # standalone tag target.

    @llm-pki-nvca
    Scenario: Operator registers the cluster and installs NVCA with bundle trust
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-pki COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI}
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      When I run command "helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local -o yaml"
      Then the command exit code should be 0
      And the command output should contain "stargateQUICInsecure: false"
      And the command output should contain "trustMode: bundle"
      And the command output should contain "trustBundleFingerprint: sha256:"

      When I run command "kubectl --context k3d-ncp-local rollout status deployment/nvca-operator -n nvca-operator --timeout=10m"
      Then the command exit code should be 0

      When I run command "kubectl --context k3d-ncp-local wait nvcfbackend ncp-local -n nvca-operator --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0

  Rule: An LLM function answers invocations over the secured tunnel

    Background:
      Given these environment variables are set:
        | name            |
        | NVCF_CLI        |
        | REPO_ROOT       |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |

    # Depends on the earlier scenarios in this feature run; not a
    # standalone tag target. Same body as the non-PKI LLM scenario:
    # the invoke succeeding over the secure tunnel is the trust-chain
    # proof.
    @llm-function-type
    Scenario: Operator creates, deploys, and invokes an LLM-type function over the secured tunnel
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-pki-openai-compatible-sample --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local --function-type LLM --inference-url /v1/chat/completions --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S --llm-model 'name=openai-compatible-sample,uris=/v1/chat/completions|/v1/embeddings,routingMethod=round_robin'
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_1x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-pki-openai-compatible-sample --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --inference-url /v1/chat/completions --model-name openai-compatible-sample --request-body '{"messages":[{"role":"user","content":"bdd-pki-llm"}]}' --timeout 120
        """
      Then the command exit code should be 0
      And the command output should contain "chat.completion"
      And the command output should contain "fixed 128-byte response"

      # curl reports only the status code so the assertion cannot
      # match response-body noise.
      When I run command:
        """
        curl -s --connect-timeout 5 --max-time 30 -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -H "traceparent: 00-00000000000000000000000000001076-0000000000001076-01" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command exit code should be 0
      And the command output should contain "401"

      # Leave the GPU capacity free, same as the non-PKI feature.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function delete --deployment-only
        """
      Then the command exit code should be 0
