@eks @single-cluster @helmfile
Feature: Install a single-cluster NVCF stack on a pre-provisioned EKS cluster with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow against a pre-provisioned
  EKS cluster, so that I can install a single-cluster control plane and
  register and install the NVCA operator on the same cluster, without
  going through the CLI install path.

  # Helmfile installs the control plane from the operator-authored
  # environment. Registration then exports that installed environment as a
  # control-plane profile and passes it to the compute-plane Make target.
  # The feature runs `nvcf-cli init` explicitly before registration.
  #
  # Required environment variables (user-supplied):
  #   REPO_ROOT                      absolute path to this repository
  #   NVCF_CLI                       built CLI path (harness)
  #   NGC_API_KEY                    NGC API key
  #   SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM
  #                                  NGC org/team for nvcr.io image
  #                                  + chart pulls
  #   EKS_CONTEXT                    full kubectl context
  #   EKS_CLUSTER_NAME               Cluster name used in registration
  #   EKS_REGION                     AWS region
  #
  # NOT user-supplied (feature exports it):
  #   EKS_GATEWAY_ADDR               captured from gateway/nvcf-gateway
  #                                  .status.addresses[0].value after
  #                                  the Gateway is Programmed.
  #
  # Cluster prerequisites (feature does NOT install these):
  #   - EBS CSI driver (provisions the gp3 StorageClass)
  #   - fake GPU operator
  #
  # cert-manager is installed by the control-plane helmfile
  # (helmfile.d/01-dependencies.yaml.gotmpl), not the worker layer,
  # so the feature installs it as part of @control-plane.
  #
  # The nvcf-cli config is NOT user-supplied: the @nvca-registration
  # scenario below copies tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template
  # into a runtime path under tests/bdd/out/ and patches the URL + Host
  # fields from ${EKS_GATEWAY_ADDR}. This mimics what a user does after
  # a successful helmfile install: discover the gateway address, then
  # author the CLI config with GATEWAY_ADDR-derived URLs and Host
  # headers (per docs/v0.5/helmfile-installation.md).
  #
  # Pre-suite cleanup:
  # tests/bdd/scripts/destroy-nonlocal-stack.sh
  #   --control-plane-context ${EKS_CONTEXT}
  #   --compute-context ${EKS_CONTEXT}

  Background:
    Given these environment variables are set:
      | name             |
      | REPO_ROOT        |
      | NVCF_CLI         |
      | NGC_API_KEY      |
      | SAMPLE_NGC_ORG   |
      | SAMPLE_NGC_TEAM  |
      | EKS_CONTEXT      |
      | EKS_CLUSTER_NAME |
      | EKS_REGION       |
    # Helmfile pulls OCI charts through helm, so host-side helm
    # Registry authentication must be present before any helmfile sync. The
    # current API key is passed through sensitive stdin.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Create NGC dockerconfig registry credentials
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/eks-bdd-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
    And I run command "kubectl --context ${EKS_CONTEXT} get nodes -o name"
    And the command exit code should be 0

  @gateway-setup
  Scenario: Install gateway, capture ELB address, and author the EKS env file
    # Captures the user's manual setup steps per
    # docs/v0.5/helmfile-installation.md Step 1. Installs the
    # envoy-gateway controller, applies the nvcf-gateway Gateway,
    # waits for AWS to provision the NLB, captures the assigned
    # hostname into EKS_GATEWAY_ADDR, and patches eks-bdd.yaml with
    # all the EKS-specific values (including global.domain).

    # 1. Install the envoy-gateway controller in envoy-gateway-system.
    When I run command "helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version v1.1.3 --kube-context ${EKS_CONTEXT} -n envoy-gateway-system --create-namespace --wait --timeout 5m"
    Then the command exit code should be 0

    # 2. Apply the Gateway, GatewayClass, and envoy-gateway namespace.
    When I run command "kubectl --context ${EKS_CONTEXT} apply -f tests/bdd/fixtures/nvcf-gateway.yaml"
    Then the command exit code should be 0

    # 3. Wait for AWS to provision the NLB and the Gateway to flip
    #    to Programmed=True. Typical NLB-create latency is 3-5min;
    #    timeout is set to 10min for AWS throttling.
    When I run command "kubectl --context ${EKS_CONTEXT} wait --for=condition=Programmed gateway/nvcf-gateway -n envoy-gateway --timeout=10m"
    Then the command exit code should be 0

    # 4. Capture the assigned ELB hostname and export it for
    #    downstream scenarios. The export step asserts the prior
    #    command exited 0 and produced non-empty stdout; the env
    #    Ledger snapshots the prior value (typically unset).
    When I run command "kubectl --context ${EKS_CONTEXT} get gateway nvcf-gateway -n envoy-gateway -o jsonpath={.status.addresses[0].value}"
    Then the command exit code should be 0
    When I export command output to environment variable "EKS_GATEWAY_ADDR"

    # 5. Wait for the ELB hostname to be resolvable from the host's
    #    DNS resolver. AWS DNS propagation typically lags ~30-90s
    #    behind NLB programming; without this wait, subsequent
    #    in-pod connections can fail intermittently.
    When I run command "tests/bdd/scripts/wait-for-dns.sh ${EKS_GATEWAY_ADDR} 180"
    Then the command exit code should be 0

    # 6. Copy base.yaml -> eks-bdd.yaml and patch with the EKS
    #    knobs (including global.domain from the just-exported
    #    EKS_GATEWAY_ADDR).
    #
    #    The three global.nvcaOperator.selfManaged.*Override rows set the NVCA
    #    service Host-header overrides (helm-nvca-operator >=1.12.0).
    #    The helmfile selfManaged inline-values block passes them into the
    #    operator chart, which renders them into the agent config. The agent
    #    dials the bare-ELB service URLs (which DNS-resolve) and sends these
    #    hostnames as the HTTP Host header so the gateway HTTPRoutes match.
    #    This replaces the former @nvca-registration URL-rewrite + hostAliases
    #    workaround.
    When I prepare Helmfile environment "eks-bdd" for stack "self-managed" from fixture "deploy/stacks/self-managed/environments/base.yaml" with values:
      | global.helm.sources.repository                   | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                          | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.imagePullSecrets[0].name                  | nvcr-pull-secret                     |
      | global.storageClass                              | gp3                                  |
      | global.domain                                    | ${EKS_GATEWAY_ADDR}                  |
      | global.nvcaOperator.selfManaged.icmsServiceHostHeaderOverride  | sis.${EKS_GATEWAY_ADDR}   |
      | global.nvcaOperator.selfManaged.revalServiceHostHeaderOverride | reval.${EKS_GATEWAY_ADDR} |
      | global.nvcaOperator.selfManaged.natsHostOverride               | nats.${EKS_GATEWAY_ADDR}  |
      | ingress.gatewayApi.controllerNamespace           | envoy-gateway-system                 |
      | ingress.gatewayApi.gateways.shared.name      | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.shared.namespace | envoy-gateway                        |
      | ingress.gatewayApi.gateways.grpc.name        | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.grpc.namespace   | envoy-gateway                        |
      | ingress.gatewayApi.gateways.nats.name        | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.nats.namespace   | envoy-gateway                        |
      | ingress.gatewayApi.routes.nats.enabled       | true                                 |
      | openbao.migrations.issuerDiscovery.enabled   | true                                 |
    Then yaml file "deploy/stacks/self-managed/environments/eks-bdd.yaml" key "global.domain" should equal "${EKS_GATEWAY_ADDR}"

    # The compute-plane Helmfile is a separate bundle, so give it an
    # environment file with the same registry and control-plane endpoints.
    When I prepare Helmfile environment "eks-bdd" for stack "nvcf-compute-plane" from fixture "deploy/stacks/nvcf-compute-plane/environments/base.yaml" with values:
      | global.helm.sources.repository                                 | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.imagePullSecrets[0].name                                | nvcr-pull-secret                     |
      | global.nvcaOperator.selfManaged.icmsServiceURL                 | http://${EKS_GATEWAY_ADDR}           |
      | global.nvcaOperator.selfManaged.icmsServiceHostHeaderOverride  | sis.${EKS_GATEWAY_ADDR}              |
      | global.nvcaOperator.selfManaged.revalServiceURL                | http://${EKS_GATEWAY_ADDR}           |
      | global.nvcaOperator.selfManaged.revalServiceHostHeaderOverride | reval.${EKS_GATEWAY_ADDR}            |
      | global.nvcaOperator.selfManaged.natsURL                        | nats://${EKS_GATEWAY_ADDR}:4222      |
      | global.nvcaOperator.selfManaged.natsHostOverride               | nats.${EKS_GATEWAY_ADDR}             |

  Rule: Helmfile installs the EKS control plane and NVCA on the same pre-provisioned cluster

    Background:
      # Switch to EKS context for subsequent commands
      Given command has succeeded:
        """
        kubectl config use-context ${EKS_CONTEXT}
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

    Scenario: User installs the control plane through Helmfile on the EKS cluster
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=eks-bdd"
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "${EKS_CONTEXT}":
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

      # Verify gateway-routes templated global.domain into the api
      # HTTPRoute hostname. Confirms the env-file global.domain value
      # propagated through helmfile -> gateway-routes chart -> rendered
      # HTTPRoute. A mismatch here means a future chart change has
      # decoupled global.domain from the hostnames downstream services
      # rely on for SNI/Host routing.
      Then Kubernetes resource "HTTPRoute/nvcf-api" in namespace "envoy-gateway" using context "${EKS_CONTEXT}" should contain:
        """
        spec:
          hostnames:
            - api.${EKS_GATEWAY_ADDR}
        """

    @nvca-registration
    Scenario: User registers the EKS cluster and installs the NVCA operator there
      Given command has succeeded:
        """
        make -C deploy/stacks/self-managed install HELMFILE_ENV=eks-bdd
        """

      # Author the nvcf-cli config now that the gateway address is
      # known and the control plane is up. Copy the static-defaults
      # template, then patch the URL + Host fields with the
      # GATEWAY_ADDR-derived values.
      And I copy the file "tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template" to "tests/bdd/out/nvcf-cli-eks-bdd.yaml"
      And I update yaml file "tests/bdd/out/nvcf-cli-eks-bdd.yaml" with keys:
        | base_http_url        | http://${EKS_GATEWAY_ADDR}     |
        | invoke_url           | http://${EKS_GATEWAY_ADDR}     |
        | base_grpc_url        | ${EKS_GATEWAY_ADDR}:10081      |
        | api_keys_service_url | http://${EKS_GATEWAY_ADDR}     |
        | icms_url             | http://${EKS_GATEWAY_ADDR}     |
        | api_host             | api.${EKS_GATEWAY_ADDR}        |
        | api_keys_host        | api-keys.${EKS_GATEWAY_ADDR}   |
        | invoke_host          | invocation.${EKS_GATEWAY_ADDR} |
        | icms_host            | sis.${EKS_GATEWAY_ADDR}        |

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env eks-bdd control-plane profile export --cluster-name ${EKS_CLUSTER_NAME} --region ${EKS_REGION}
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd.yaml init
        """
      Then the command exit code should be 0

      # Register the EKS cluster from the exported profile and write the Helm
      # handoff under registration/ for the compute-plane install.
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=${EKS_CLUSTER_NAME} CLUSTER_REGION=${EKS_REGION} CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=${EKS_CONTEXT} NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/${EKS_CLUSTER_NAME}-register-values.yaml" should exist
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/${EKS_CLUSTER_NAME}-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: ${EKS_REGION}
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/${EKS_CLUSTER_NAME}-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      # The register-values URLs stay as cluster register's bare-ELB
      # output (http://${EKS_GATEWAY_ADDR}, nats://${EKS_GATEWAY_ADDR}:4222).
      # Those resolve directly, so no hostAliases patch is needed. Gateway
      # HTTPRoute matching is handled by the chart-native Host-header
      # overrides (global.nvcaOperator.selfManaged.*Override) set on eks-bdd.yaml
      # in @gateway-setup, which the agent sends as the HTTP Host header.

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=${EKS_CLUSTER_NAME} HELMFILE_ENV=eks-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "${EKS_CONTEXT}":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "${EKS_CONTEXT}" should complete rollout within "10m"

      # With chart-native Host headers the agent dials the bare-ELB URLs
      # (which DNS-resolve) and sends the gateway-matching Host header, so
      # the former dig + hostAliases patch + nvca rollout restart is no
      # longer needed. The operator brings the agent up directly; wait for
      # the NVCFBackend to report the agent healthy.
      Then NVCFBackend "${EKS_CLUSTER_NAME}" in namespace "nvca-operator" using context "${EKS_CONTEXT}" should report agent status "healthy" within "10m"
