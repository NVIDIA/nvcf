@ncp-local @single-cluster @helmfile
Feature: Install a local single-cluster NVCF stack with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow against a local k3d cluster,
  so that I can install a single-cluster control plane with NVCA and the LLM
  gateway add-on enabled.

  Rule: Operator authors the local Helmfile environment file

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      # The fixture is a copy of deploy/stacks/self-managed/environments/local.yaml,
      # which already carries every ncp-local local-mode override (storageClass,
      # replica counts, NVCA self-managed endpoints, addons.llm.*, agentConfig,
      # ingress.gatewayApi.*). The Background only overlays the operator-specific
      # values that vary per NGC org and pull-secret name.
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      # Only ${VAR} is interpolated; bare $oauthtoken stays literal.
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"

    Scenario: Operator validates the authored Helmfile environment renders
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd"

      Then the command exit code should be 0
      And the command output should not contain "Error:"

  Rule: Helmfile installs the local control plane with LLM gateway add-ons

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/4222, the same host ports single-cluster
      # ncp-local needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      And I run command "k3d cluster get ncp-local-cp"
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
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |

    @llm-gateway
    Scenario: Operator installs the control plane through the local Helmfile environment
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd"

      Then the command exit code should be 0

      When I run command "helm list --all-namespaces -o json"
      Then the json output should contain rows:
        | name                      | namespace            | status   |
        | nats                      | nats-system          | deployed |
        | cert-manager              | cert-manager         | deployed |
        | openbao-server            | vault-system         | deployed |
        | cassandra                 | cassandra-system     | deployed |
        | api-keys                  | api-keys             | deployed |
        | sis                       | sis                  | deployed |
        | api                       | nvcf                 | deployed |
        | nvct-api                  | nvcf                 | deployed |
        | invocation-service        | nvcf                 | deployed |
        | grpc-proxy                | nvcf                 | deployed |
        | ess-api                   | ess                  | deployed |
        | notary-service            | nvcf                 | deployed |
        | admin-issuer-proxy        | api-keys             | deployed |
        | reval                     | nvcf                 | deployed |
        | nats-auth-callout-service | nats-system          | deployed |
        | ingress                   | envoy-gateway-system | deployed |
        | llm-request-router        | nvcf                 | deployed |
        | llm-api-gateway           | nvcf                 | deployed |

  Rule: Helmfile installs NVCA on the same local cluster after registration via the stack Makefile

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      And environment variable "NVCF_CLI" is set
      And environment variable "REPO_ROOT" is set
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/4222, the same host ports single-cluster
      # ncp-local needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      And I run command "k3d cluster get ncp-local-cp"
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
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |
      And command has succeeded:
        """
        make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd
        """

    @nvca-registration
    Scenario: Operator registers the local cluster and installs the NVCA operator
      When I run command:
        """
        make -C deploy/stacks/self-managed register-cluster CLUSTER_NAME=ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" should exist
      # The single-cluster Makefile passes CLUSTER_NAME separately to
      # helmfile, so the register-values file does not carry clusterName
      # at the top level. Assert the deterministic block that is
      # present plus the non-empty fields.
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
          icmsServiceURL: http://sis.localhost:8080
          revalServiceURL: http://reval.localhost:8080
          natsURL: nats://nats.localhost:4222
        """
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" key "clusterGroupID" should not be empty

      When I run command:
        """
        make -C deploy/stacks/self-managed install-nvca-operator CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      When I run command "helm list -n nvca-operator -o json"
      Then the json output should contain rows:
        | name          | namespace     | status   |
        | nvca-operator | nvca-operator | deployed |

      When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --timeout=10m"
      Then the command exit code should be 0

      When I run command "kubectl wait nvcfbackend ncp-local -n nvca-operator --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0
