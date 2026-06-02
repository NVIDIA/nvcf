@ncp-local @multi-cluster @helmfile
Feature: Install a local multi-cluster NVCF stack with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow across a local multi-cluster
  ncp-local topology,
  so that I can install the control plane on one cluster and register and
  install the NVCA operator on a separately registered compute cluster.

  # The register-cluster Make target runs `nvcf-cli init` internally
  # before the cluster register call, so unlike the CLI features this
  # feature does not need a separate init step. The CLI state file
  # (~/.nvcf-cli.nvcf-cli-local.state) the init writes is snapshotted
  # by harness.NewSuite through the Ledger and restored at suite
  # teardown.
  #
  # This feature is values-driven (not profile-driven). The CLI
  # multi-cluster feature uses `self-hosted install --control-plane`
  # which writes a profile with both inCluster and computeReachable
  # URLs, then `compute-plane register --control-plane-profile`
  # picks the right URL block by kube-context. This Helmfile path
  # has no profile; the URLs come from the operator-authored env
  # file (here: fixtures/self-managed-local-bdd-multi.yaml). The
  # fixture's .test hostnames must match the CLI feature's
  # computeReachable assertion in features/multi-cluster-up.feature.
  # See tests/bdd/AGENTS.md "CLI vs Helmfile install paths".

  Rule: Helmfile installs the control plane on the control-plane cluster

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      # The multi-cluster fixture overrides global.nvcaOperator.selfManaged.*
      # to the .test hostnames that configure-compute-control-plane-endpoints
      # aliases to the control-plane LB. The single-cluster fixture's
      # in-cluster URLs (api.sis.svc.cluster.local etc.) only resolve
      # inside the control-plane k3d cluster and crash the NVCA agent
      # on the compute cluster with ICMS 401.
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/4222, the same host ports
      # ncp-local-cp needs. Fail loudly so the operator runs
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
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |

    @control-plane @llm-gateway
    Scenario: Operator installs the control plane through Helmfile on the control-plane cluster
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd"
      Then the command exit code should be 0

      When I run command "helm list --all-namespaces --kube-context k3d-ncp-local-cp -o json"
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

  Rule: Helmfile registers and installs NVCA on the compute cluster

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      And environment variable "NVCF_CLI" is set
      And environment variable "REPO_ROOT" is set
      # The multi-cluster fixture overrides global.nvcaOperator.selfManaged.*
      # to the .test hostnames that configure-compute-control-plane-endpoints
      # aliases to the control-plane LB. The single-cluster fixture's
      # in-cluster URLs (api.sis.svc.cluster.local etc.) only resolve
      # inside the control-plane k3d cluster and crash the NVCA agent
      # on the compute cluster with ICMS 401.
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/4222, the same host ports
      # ncp-local-cp needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      And I run command "k3d cluster get ncp-local"
      And the command exit code should be 1
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
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |
      # Reuse the previous scenario's install rather than re-running it
      # if it already succeeded in this suite. Cache key is the resolved
      # command text, identical to scenario 1's When step.
      And command has succeeded:
        """
        make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd
        """

    @nvca-registration
    Scenario: Operator registers the compute cluster and installs the NVCA operator there
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
      # install-nvca-operator that follows also runs helm against the
      # ambient context, so this single switch covers both steps.
      When I run command "kubectl config use-context k3d-ncp-local-compute-1"
      Then the command exit code should be 0

      When I run command:
        """
        make -C deploy/stacks/self-managed register-cluster CLUSTER_NAME=ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" should exist
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" key "clusterGroupID" should not be empty

      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |
        | nvca-system   |
        | nvcf-backend  |

      When I run command:
        """
        make -C deploy/stacks/self-managed install-nvca-operator CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      When I run command "helm list -n nvca-operator --kube-context k3d-ncp-local-compute-1 -o json"
      Then the json output should contain rows:
        | name          | namespace     | status   |
        | nvca-operator | nvca-operator | deployed |

      When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --context k3d-ncp-local-compute-1 --timeout=10m"
      Then the command exit code should be 0

      # The default NVCFBackend CR is created on the compute cluster
      # by the nvca-operator helm chart at install time (helm reports
      # this in its post-install output), and the NVCA agent updates
      # its own .status.agentStatus locally. The NVCFBackend CRD is
      # therefore only registered on k3d-ncp-local-compute-1, not on
      # k3d-ncp-local-cp. Wait on the compute cluster.
      When I run command "kubectl wait nvcfbackend ncp-local-compute-1 -n nvca-operator --context k3d-ncp-local-compute-1 --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0
