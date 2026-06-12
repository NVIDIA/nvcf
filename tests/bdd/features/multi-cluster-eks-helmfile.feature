@eks @multi-cluster @helmfile
Feature: Install a multi-cluster NVCF stack across two pre-provisioned EKS clusters with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow across two pre-provisioned
  EKS clusters, so that I can install the control plane on one cluster, then
  register and install the NVCA operator on a separate compute cluster, and
  deploy and invoke a sample function end to end.

  # This feature is values-driven (not profile-driven); see
  # AGENTS.md "CLI vs Helmfile install paths". It authors the nvcf-cli
  # config and runs init + cluster register explicitly (like the
  # single-cluster EKS feature) because the gateway address is only
  # known at runtime.
  #
  # Required environment variables (user-supplied):
  #   NVCF_CLI                   built CLI path (harness)
  #   NGC_API_KEY                NGC API key
  #   SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM
  #                              NGC org/team for nvcr.io image + chart pulls
  #   REPO_ROOT                  repo root (harness)
  #   EKS_CONTEXT                control-plane cluster kubectl context
  #   EKS_COMPUTE_CONTEXT        compute (GPU) cluster kubectl context
  #   EKS_COMPUTE_CLUSTER_NAME   compute cluster name used in registration
  #   EKS_REGION                 AWS region
  #
  # NOT user-supplied (feature exports it):
  #   EKS_GATEWAY_ADDR           captured from the control-plane
  #                              gateway/nvcf-gateway .status.addresses[0].value
  #
  # Cluster prerequisites (feature does NOT install these):
  #   - EBS CSI driver (gp3 StorageClass) on both clusters
  #   - fake GPU operator on the compute cluster
  #
  # The compute NVCA agent reaches the control plane through the
  # control-plane gateway ELB. cluster register emits the bare-ELB
  # service URLs (which DNS-resolve); the three
  # global.nvcaOperator.selfManaged.*Override rows on the env file
  # carry the gateway-matching Host headers so the control-plane
  # gateway HTTPRoutes match (helm-nvca-operator >=1.12.0).
  #
  # Isolation: this feature uses HELMFILE_ENV=eks-bdd-multi so its
  # environment, secrets, and CLI-config files do not collide with the
  # single-cluster EKS feature's eks-bdd files. Cluster identities are
  # the user-supplied EKS contexts; register-values are keyed by
  # ${EKS_COMPUTE_CLUSTER_NAME}.
  #
  # Pre-suite cleanup (operator-run):
  # tests/bdd/scripts/destroy-nonlocal-stack.sh
  #   --control-plane-context ${EKS_CONTEXT}
  #   --compute-context ${EKS_COMPUTE_CONTEXT}

  Background:
    Given environment variable "NVCF_CLI" is set
    And environment variable "NGC_API_KEY" is set
    And environment variable "SAMPLE_NGC_ORG" is set
    And environment variable "SAMPLE_NGC_TEAM" is set
    And environment variable "REPO_ROOT" is set
    And environment variable "EKS_CONTEXT" is set
    And environment variable "EKS_COMPUTE_CONTEXT" is set
    And environment variable "EKS_COMPUTE_CLUSTER_NAME" is set
    And environment variable "EKS_REGION" is set
    # Helmfile pulls OCI charts through helm, so host-side helm
    # registry auth must be present before any helmfile sync.
    # Keep $NGC_API_KEY unbraced so the BDD runner does not expand
    # the secret into command logs; bash expands it at execution time.
    And command has succeeded:
      """
      bash -c 'set -eo pipefail; printf %s "$NGC_API_KEY" | helm registry login nvcr.io --username "\$oauthtoken" --password-stdin'
      """
    # Create NGC dockerconfig registry credentials.
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/eks-bdd-multi-secrets.yaml"
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/eks-bdd-multi-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    # Both clusters must be reachable before we start.
    And I run command "kubectl --context ${EKS_CONTEXT} get nodes -o name"
    And the command exit code should be 0
    And I run command "kubectl --context ${EKS_COMPUTE_CONTEXT} get nodes -o name"
    And the command exit code should be 0

  @gateway-setup
  Scenario: Install the control-plane gateway, capture the ELB address, and author the env file
    # Set up the gateway on the CONTROL-PLANE cluster: install the
    # envoy-gateway controller, apply the nvcf-gateway Gateway, wait for
    # AWS to provision the NLB, capture the assigned hostname into
    # EKS_GATEWAY_ADDR, and patch eks-bdd-multi.yaml with the
    # EKS-specific values.

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
    #    downstream scenarios.
    When I run command "kubectl --context ${EKS_CONTEXT} get gateway nvcf-gateway -n envoy-gateway -o jsonpath={.status.addresses[0].value}"
    Then the command exit code should be 0
    When I export command output to environment variable "EKS_GATEWAY_ADDR"

    # 5. Wait for the ELB hostname to be resolvable from the host's
    #    DNS resolver before installing.
    When I run command "tests/bdd/scripts/wait-for-dns.sh ${EKS_GATEWAY_ADDR} 180"
    Then the command exit code should be 0

    # 6. Copy base.yaml -> eks-bdd-multi.yaml and patch with the EKS
    #    knobs (including global.domain from the just-exported
    #    EKS_GATEWAY_ADDR).
    #
    #    The three global.nvcaOperator.selfManaged.*Override rows set the
    #    NVCA service Host-header overrides (helm-nvca-operator >=1.12.0).
    #    The helmfile selfManaged inline-values block passes them into the
    #    operator chart on the compute cluster, which renders them into
    #    the agent config. The agent dials the bare-ELB service URLs
    #    (which DNS-resolve) and sends these hostnames as the HTTP Host
    #    header so the control-plane gateway HTTPRoutes match.
    When I copy the file "deploy/stacks/self-managed/environments/base.yaml" to "deploy/stacks/self-managed/environments/eks-bdd-multi.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/eks-bdd-multi.yaml" with keys:
      | global.helm.sources.repository                                 | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.imagePullSecrets[0].name                                | nvcr-pull-secret                     |
      | global.storageClass                                            | gp3                                  |
      | global.domain                                                  | ${EKS_GATEWAY_ADDR}                  |
      | global.nvcaOperator.selfManaged.icmsServiceHostHeaderOverride  | sis.${EKS_GATEWAY_ADDR}   |
      | global.nvcaOperator.selfManaged.revalServiceHostHeaderOverride | reval.${EKS_GATEWAY_ADDR} |
      | global.nvcaOperator.selfManaged.natsHostOverride               | nats.${EKS_GATEWAY_ADDR}  |
      | ingress.gatewayApi.controllerNamespace                         | envoy-gateway-system                 |
      | ingress.gatewayApi.gateways.shared.name                        | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.shared.namespace                   | envoy-gateway                        |
      | ingress.gatewayApi.gateways.grpc.name                          | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.grpc.namespace                     | envoy-gateway                        |
      | ingress.gatewayApi.gateways.nats.name                          | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.nats.namespace                     | envoy-gateway                        |
      | ingress.gatewayApi.routes.nats.enabled                         | true                                 |
      | openbao.migrations.issuerDiscovery.enabled                     | true                                 |
    Then yaml file "deploy/stacks/self-managed/environments/eks-bdd-multi.yaml" key "global.domain" should equal "${EKS_GATEWAY_ADDR}"

  Rule: Helmfile installs the control plane on the control-plane EKS cluster

    Background:
      # Install and assertions target the control-plane cluster.
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
        | cert-manager     |

    Scenario: User installs the control plane through Helmfile on the control-plane cluster
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=eks-bdd-multi"
      Then the command exit code should be 0

      When I run command "helm list --all-namespaces --kube-context ${EKS_CONTEXT} -o json"
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

      # Confirm gateway-routes templated global.domain into the api
      # HTTPRoute hostname on the control-plane cluster.
      When I run command "kubectl --context ${EKS_CONTEXT} get httproute nvcf-api -n envoy-gateway -o jsonpath={.spec.hostnames[0]}"
      Then the command output should contain "api.${EKS_GATEWAY_ADDR}"

  Rule: Helmfile registers and installs NVCA on the compute EKS cluster

    Background:
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
        | cert-manager     |
      And command has succeeded:
        """
        make -C deploy/stacks/self-managed install HELMFILE_ENV=eks-bdd-multi
        """

    @nvca-registration
    Scenario: User registers the compute cluster and installs the NVCA operator there
      # cluster register auto-discovers the target cluster's OIDC issuer
      # + JWKS by probing the CURRENT kubectl context, then POSTs that
      # identity to ICMS. The compute cluster (not the control plane) is
      # the target, so switch context to it before register-cluster runs.
      # install-nvca-operator that follows also runs helm against the
      # ambient context, so this single switch covers both steps.
      When I run command "kubectl config use-context ${EKS_COMPUTE_CONTEXT}"
      Then the command exit code should be 0

      # Pull secret in the operator namespace on the compute cluster. The
      # operator chart propagates it to the namespaces it manages.
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |

      # Author the nvcf-cli config from the gateway address. The URL +
      # Host fields point at the control-plane gateway ELB.
      And I copy the file "tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template" to "tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml"
      And I update yaml file "tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml" with keys:
        | base_http_url        | http://${EKS_GATEWAY_ADDR}     |
        | invoke_url           | http://${EKS_GATEWAY_ADDR}     |
        | base_grpc_url        | ${EKS_GATEWAY_ADDR}:10081      |
        | api_keys_service_url | http://${EKS_GATEWAY_ADDR}     |
        | icms_url             | http://${EKS_GATEWAY_ADDR}     |
        | api_host             | api.${EKS_GATEWAY_ADDR}        |
        | api_keys_host        | api-keys.${EKS_GATEWAY_ADDR}   |
        | invoke_host          | invocation.${EKS_GATEWAY_ADDR} |
        | icms_host            | sis.${EKS_GATEWAY_ADDR}        |

      # Mint the admin JWT against the control-plane gateway.
      When I run command "${NVCF_CLI} --config tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml init"
      Then the command exit code should be 0

      # Register the compute cluster with the control plane. tee mirrors
      # the CLI's full stdout to stderr for log visibility; the slice
      # helper extracts the YAML body from the mixed stdout before
      # redirecting it to the worker-layer values file.
      When I run command:
        """
        bash -c 'set -eo pipefail; mkdir -p deploy/stacks/self-managed/out; ${NVCF_CLI} --config tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml cluster register --name ${EKS_COMPUTE_CLUSTER_NAME} --nca-id nvcf-default --region ${EKS_REGION} --icms-url http://${EKS_GATEWAY_ADDR} --ignore-existing | tee /dev/stderr | tests/bdd/scripts/slice-yaml-body.sh > deploy/stacks/self-managed/out/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml'
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" should exist
      And yaml file "deploy/stacks/self-managed/out/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: ${EKS_REGION}
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/self-managed/out/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" key "clusterGroupID" should not be empty

      # The register-values URLs stay as cluster register's bare-ELB
      # output. Gateway HTTPRoute matching is handled by the chart-native
      # Host-header overrides set on eks-bdd-multi.yaml in @gateway-setup,
      # which the agent sends as the HTTP Host header.
      When I run command:
        """
        make -C deploy/stacks/self-managed install-nvca-operator CLUSTER_NAME=${EKS_COMPUTE_CLUSTER_NAME} HELMFILE_ENV=eks-bdd-multi NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml
        """
      Then the command exit code should be 0

      When I run command "helm list -n nvca-operator --kube-context ${EKS_COMPUTE_CONTEXT} -o json"
      Then the json output should contain rows:
        | name          | namespace     | status   |
        | nvca-operator | nvca-operator | deployed |

      When I run command "kubectl rollout status deployment/nvca-operator -n nvca-operator --context ${EKS_COMPUTE_CONTEXT} --timeout=10m"
      Then the command exit code should be 0

      # Wait for the NVCFBackend on the compute cluster to report the
      # agent healthy. The agent reaching ICMS healthy confirms the
      # cross-cluster Host-header + registration wiring works.
      When I run command "kubectl wait nvcfbackend ${EKS_COMPUTE_CLUSTER_NAME} -n nvca-operator --context ${EKS_COMPUTE_CONTEXT} --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0

      # The pull secret is created only in nvca-operator; confirm the
      # operator propagated it to nvca-system. Asserting propagation here
      # catches a broken propagation that the node image cache would
      # otherwise mask under imagePullPolicy IfNotPresent.
      When I run command "kubectl --context ${EKS_COMPUTE_CONTEXT} get secret nvcr-pull-secret -n nvca-system"
      Then the command exit code should be 0

    @function-lifecycle @skip
    Scenario: User creates, deploys, and invokes the Load Tester Supreme sample function
      # SKIPPED in the live run via ~@skip (still exercised by the wiring
      # test). Known gap: cross-cluster function EXECUTION is
      # not supported by the current nvca-operator chart/agent. The agent
      # injects the control plane's IN-CLUSTER service names into worker
      # pods (NVCF_FQDN=http://api.nvcf.svc.cluster.local:8080,
      # NVCF_FQDN_GRPC=...:9090, NVCF_FQDN_NATS=nats://nats.nats-system...,
      # ESS_FQDN=http://ess-api.ess...), which do not resolve on a separate
      # compute cluster, so the worker init container cannot fetch
      # artifacts and the deployment ERRORs. The selfManaged.*Override
      # values only configure the agent's own connections, not the worker
      # pod env; there is no chart knob to externalize the worker FQDNs.
      # Registration + agent health (the scenario above) work cross-region.
      # Re-enable this scenario once worker FQDNs can be externalized.
      #
      # Smoke the full path: function management hits the control-plane
      # API through the gateway; the deployment lands on the registered
      # compute cluster. Relies on the @nvca-registration scenario above
      # having registered the compute cluster and brought the agent
      # healthy in this suite; re-assert health first so this scenario
      # fails clearly if registration did not complete.
      Given command has succeeded:
        """
        kubectl wait nvcfbackend ${EKS_COMPUTE_CLUSTER_NAME} -n nvca-operator --context ${EKS_COMPUTE_CONTEXT} --for=jsonpath={.status.agentStatus}=healthy --timeout=10m
        """

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml function create --name bdd-load-tester-supreme --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8 --inference-url /echo --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ${EKS_COMPUTE_CLUSTER_NAME} --regions ${EKS_REGION} --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml api-key generate --description bdd-load-tester-supreme --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml function invoke --request-body '{"message":"bdd-echo","repeats":1}' --timeout 120 --poll-duration 5
        """
      Then the command exit code should be 0
      And the command output should contain "bdd-echo"
