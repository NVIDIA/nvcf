@ncp-local @single-cluster @helmfile @pki
Feature: Install a local single-cluster NVCF stack with PKI-secured LLM transport
  As a self-managed NVCF operator,
  I want the Helmfile workflow with the LLM PKI addon enabled,
  so that an LLM function answers invocations over a QUIC tunnel whose
  trust chain is issued by the stack's own PKI.

  # This feature owns its Helmfile environment (local-bdd-pki) so the
  # default fixtures and the non-PKI features stay unchanged. PKI
  # enablement is an install-time value: addons.llm.pki.enabled gates
  # the nvcf-pki release and the stargate Certificate at render.
  # The compute plane's trust configuration is written by
  # tests/bdd/scripts/write-transport-trust-env.sh after the control
  # plane is installed, because the trust bundle (the OpenBao root CA
  # public cert plus its nvcf-trust-bundle-v1 fingerprint) only exists
  # once OpenBao is up. The script replaces the fixture's agentConfig
  # block, which also drops stargateQUICInsecure: the tunnel runs in
  # secure mode and NVCA rejects bundle trust combined with insecure
  # QUIC.

  Rule: Helmfile installs the control plane with the LLM PKI addon

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd-pki.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd-pki.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                   |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                               |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0 |
        | addons.llm.pki.enabled                        | true                                                               |
        | observability.profile                         | disabled                                                           |
      And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd-pki.yaml"
      And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd-pki.yaml" with keys:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-pki-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-pki-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
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

      # The issuer and the stargate leaf are functional gates for the
      # secure tunnel: the router cannot serve TLS before cert-manager
      # writes the stargate-quic-tls Secret.
      When I run command "kubectl wait clusterissuer nvcf-openbao-pki --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0

      When I run command "kubectl wait certificate stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0

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
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist

      # Fetch the root CA public cert from OpenBao, compute the
      # canonical fingerprint, and write the transportTLS bundle block
      # into the compute environment authored by the Background.
      When I run command:
        """
        tests/bdd/scripts/write-transport-trust-env.sh deploy/stacks/nvcf-compute-plane/environments/local-bdd-pki.yaml
        """
      Then the command exit code should be 0
      And the command output should contain "wrote transportTLS bundle config"

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-pki NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --timeout=10m"
      Then the command exit code should be 0

      When I run command "kubectl wait nvcfbackend ncp-local -n nvca-operator --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0

  Rule: An LLM function answers invocations over the secured tunnel

    # Depends on the earlier install and registration scenarios in this
    # feature run; not a standalone tag target. The scenario body is
    # the same as the non-PKI feature's LLM scenario: the invoke
    # succeeding here proves the trust chain end to end, because the
    # worker's tunnel to stargate runs in secure mode and validates
    # the served certificate chain against the injected root.
    @llm-function-type
    Scenario: Operator creates, deploys, and invokes an LLM-type function over the secured tunnel
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-pki-openai-compatible-sample --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/nvcf-openai-compatible-sample:local --function-type LLM --inference-url /v1/chat/completions --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S --llm-model 'name=openai-compatible-sample,uris=/v1/chat/completions|/v1/embeddings,routingMethod=round_robin'
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
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
        curl -s -o /dev/null -w "%{http_code}" -X POST http://llm.localhost:8080/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"unauthenticated/check","messages":[]}'
        """
      Then the command exit code should be 0
      And the command output should contain "401"

      # Leave the GPU capacity free, same as the non-PKI feature.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function delete --deployment-only
        """
      Then the command exit code should be 0
