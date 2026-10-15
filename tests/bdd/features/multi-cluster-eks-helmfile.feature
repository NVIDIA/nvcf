@eks @multi-cluster @helmfile
Feature: Install a multi-cluster NVCF stack across two pre-provisioned EKS clusters with Helmfile
  As a self-managed NVCF operator,
  I want to use the documented Helmfile workflow across two pre-provisioned
  EKS clusters, so that I can install the control plane on one cluster, then
  register and install the NVCA operator on a separate compute cluster, and
  verify that the compute cluster agent becomes healthy.

  # Helmfile installs the control plane from the operator-authored
  # environment. Registration exports that installed environment as a
  # control-plane profile, initializes the CLI explicitly, and passes the
  # profile and CLI config to the compute-plane Make target.
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
  #   EKS_GATEWAY_DOMAIN         resolvable wildcard domain derived from the
  #                              gateway address for hostname-based routes
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
    Given these environment variables are set:
      | name                     |
      | NVCF_CLI                 |
      | NGC_API_KEY              |
      | SAMPLE_NGC_ORG           |
      | SAMPLE_NGC_TEAM          |
      | REPO_ROOT                |
      | EKS_CONTEXT              |
      | EKS_COMPUTE_CONTEXT      |
      | EKS_COMPUTE_CLUSTER_NAME |
      | EKS_REGION               |
    # Helmfile pulls OCI charts through helm, so host-side helm
    # Registry authentication must be present before any helmfile sync. The
    # current API key is passed through sensitive stdin.
    And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
    # Create NGC dockerconfig registry credentials.
    And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/eks-bdd-multi-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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
    Then DNS name "${EKS_GATEWAY_ADDR}" should resolve within "180" seconds

    # Route hostnames such as api.<domain> must resolve independently. Derive
    # a nip.io wildcard domain from one NLB address so worker pods on the
    # compute cluster can resolve every control-plane route hostname.
    When I run command "tests/bdd/scripts/resolve-gateway-domain.sh ${EKS_GATEWAY_ADDR}"
    Then the command exit code should be 0
    When I export command output to environment variable "EKS_GATEWAY_DOMAIN"
    Then DNS name "api.${EKS_GATEWAY_DOMAIN}" should resolve within "180" seconds

    # 6. Copy base.yaml -> eks-bdd-multi.yaml and patch with the EKS
    #    knobs, including the resolvable Gateway domain.
    #
    #    The three global.nvcaOperator.selfManaged.*Override rows set the
    #    NVCA service Host-header overrides (helm-nvca-operator >=1.12.0).
    #    The helmfile selfManaged inline-values block passes them into the
    #    operator chart on the compute cluster, which renders them into
    #    the agent config. The agent dials the bare-ELB service URLs
    #    (which DNS-resolve) and sends these hostnames as the HTTP Host
    #    header so the control-plane gateway HTTPRoutes match.
    When I prepare Helmfile environment "eks-bdd-multi" for stack "self-managed" from fixture "deploy/stacks/self-managed/environments/base.yaml" with values:
      | global.helm.sources.repository                                 | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.imagePullSecrets[0].name                                | nvcr-pull-secret                     |
      | global.storageClass                                            | gp3                                  |
      | global.domain                                                  | ${EKS_GATEWAY_DOMAIN}                    |
      | global.workerEndpoints.nvcfServiceURL                          | http://api.${EKS_GATEWAY_DOMAIN}         |
      | global.workerEndpoints.nvcfGrpcServiceURL                      | http://worker-api.${EKS_GATEWAY_DOMAIN}  |
      | global.workerEndpoints.nvcfNatsServiceURL                      | nats://${EKS_GATEWAY_ADDR}:4222          |
      | global.workerEndpoints.nvctServiceURL                          | http://tasks.${EKS_GATEWAY_DOMAIN}       |
      | global.workerEndpoints.nvctGrpcServiceURL                      | http://worker-tasks.${EKS_GATEWAY_DOMAIN} |
      | global.workerEndpoints.invocationServiceURL                    | http://invocation.${EKS_GATEWAY_DOMAIN}  |
      | grpcproxy.nvcfGrpcServiceURL                                   | http://api.nvcf.svc.cluster.local:9090   |
      | global.nvcaOperator.selfManaged.icmsServiceHostHeaderOverride  | sis.${EKS_GATEWAY_DOMAIN}                |
      | global.nvcaOperator.selfManaged.revalServiceHostHeaderOverride | reval.${EKS_GATEWAY_DOMAIN}              |
      | global.nvcaOperator.selfManaged.natsHostOverride               | nats.${EKS_GATEWAY_DOMAIN}               |
      | ingress.gatewayApi.controllerNamespace                         | envoy-gateway-system                 |
      | ingress.gatewayApi.gateways.shared.name                        | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.shared.namespace                   | envoy-gateway                        |
      | ingress.gatewayApi.gateways.grpc.name                          | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.grpc.namespace                     | envoy-gateway                        |
      | ingress.gatewayApi.gateways.nats.name                          | nvcf-gateway                         |
      | ingress.gatewayApi.gateways.nats.namespace                     | envoy-gateway                        |
      | ingress.gatewayApi.routes.nvcfApi.grpc.enabled                 | true                                 |
      | ingress.gatewayApi.routes.nvcfApi.grpc.hostnames[0]            | worker-api.${EKS_GATEWAY_DOMAIN}     |
      | ingress.gatewayApi.routes.nvctApi.grpc.enabled                 | true                                 |
      | ingress.gatewayApi.routes.nvctApi.grpc.hostnames[0]            | worker-tasks.${EKS_GATEWAY_DOMAIN}   |
      | ingress.gatewayApi.routes.nats.enabled                         | true                                 |
      | openbao.migrations.issuerDiscovery.enabled                     | true                                 |
    Then yaml file "deploy/stacks/self-managed/environments/eks-bdd-multi.yaml" key "global.domain" should equal "${EKS_GATEWAY_DOMAIN}"

    When I prepare Helmfile environment "eks-bdd-multi" for stack "nvcf-compute-plane" from fixture "deploy/stacks/nvcf-compute-plane/environments/base.yaml" with values:
      | global.helm.sources.repository                                 | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository                                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.imagePullSecrets[0].name                                | nvcr-pull-secret                     |
      | global.nvcaOperator.selfManaged.icmsServiceURL                 | http://${EKS_GATEWAY_ADDR}           |
      | global.nvcaOperator.selfManaged.icmsServiceHostHeaderOverride  | sis.${EKS_GATEWAY_DOMAIN}            |
      | global.nvcaOperator.selfManaged.revalServiceURL                | http://${EKS_GATEWAY_ADDR}           |
      | global.nvcaOperator.selfManaged.revalServiceHostHeaderOverride | reval.${EKS_GATEWAY_DOMAIN}          |
      | global.nvcaOperator.selfManaged.natsURL                        | nats://${EKS_GATEWAY_ADDR}:4222      |
      | global.nvcaOperator.selfManaged.natsHostOverride               | nats.${EKS_GATEWAY_DOMAIN}           |

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

      # Confirm gateway-routes templated global.domain into the api
      # HTTPRoute hostname on the control-plane cluster.
      Then Kubernetes resource "HTTPRoute/nvcf-api" in namespace "envoy-gateway" using context "${EKS_CONTEXT}" should contain:
        """
        spec:
          hostnames:
            - api.${EKS_GATEWAY_DOMAIN}
        """

      # Confirm the optional API GRPCRoutes are accepted and point at
      # resolved backends. These route flags are authored in the EKS env
      # file because worker pods need externally reachable API gRPC
      # endpoints on the compute cluster.
      Then these Gateway API routes should be accepted and resolved using context "${EKS_CONTEXT}" within "2m":
        | kind      | name          | namespace     | parent       |
        | GRPCRoute | nvcf-api-grpc | envoy-gateway | nvcf-gateway |
        | GRPCRoute | nvct-api-grpc | envoy-gateway | nvcf-gateway |

      # Confirm Helmfile passed the worker-facing endpoints into the
      # environment ConfigMaps consumed by the API deployments.
      Then Kubernetes resource "ConfigMap/nvcf-api-env" in namespace "nvcf" using context "${EKS_CONTEXT}" should contain:
        """
        data:
          NVCF_FQDN: http://api.${EKS_GATEWAY_DOMAIN}
          NVCF_GLOBAL_FQDN_GRPC: http://worker-api.${EKS_GATEWAY_DOMAIN}
          NVCF_NATS_WORKER_URL: nats://${EKS_GATEWAY_ADDR}:4222
        """

      Then Kubernetes resource "ConfigMap/nvct-api-env" in namespace "nvcf" using context "${EKS_CONTEXT}" should contain:
        """
        data:
          NVCT_FQDN: http://tasks.${EKS_GATEWAY_DOMAIN}
          NVCT_GLOBAL_FQDN_GRPC: http://worker-tasks.${EKS_GATEWAY_DOMAIN}
        """

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
      # The pull-secret helper below uses the current kubectl context.
      When I run command "kubectl config use-context ${EKS_COMPUTE_CONTEXT}"
      Then the command exit code should be 0

      # Create a kubeconfig scoped to the compute cluster. register-cluster
      # uses this file to discover the compute cluster's OIDC issuer and
      # JWKS, and install uses the same file so Helmfile targets the compute
      # cluster instead of the control-plane cluster.
      When I run command:
        """
        bash -c 'set -eo pipefail; mkdir -p tests/bdd/out; kubectl --context "${EKS_COMPUTE_CONTEXT}" config view --raw --minify --flatten > tests/bdd/out/eks-compute-kubeconfig.yaml'
        """
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
        | api_host             | api.${EKS_GATEWAY_DOMAIN}        |
        | api_keys_host        | api-keys.${EKS_GATEWAY_DOMAIN}   |
        | invoke_host          | invocation.${EKS_GATEWAY_DOMAIN} |
        | icms_host            | sis.${EKS_GATEWAY_DOMAIN}        |

      # Export the installed control plane with compute-reachable endpoints.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env eks-bdd-multi --control-plane-context ${EKS_CONTEXT} --compute-plane-context ${EKS_COMPUTE_CONTEXT} control-plane profile export --region ${EKS_REGION}
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml init
        """
      Then the command exit code should be 0

      # Register the compute cluster and write the returned Helm values under
      # registration/.
      Then DNS name "${EKS_GATEWAY_ADDR}" should resolve within "180" seconds

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=${EKS_COMPUTE_CLUSTER_NAME} CLUSTER_REGION=${EKS_REGION} CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=${EKS_COMPUTE_CONTEXT} KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/eks-compute-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" should exist
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: ${EKS_REGION}
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/${EKS_COMPUTE_CLUSTER_NAME}-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      # The register-values URLs stay as cluster register's bare-ELB
      # output. Gateway HTTPRoute matching is handled by the chart-native
      # Host-header overrides set on eks-bdd-multi.yaml in @gateway-setup,
      # which the agent sends as the HTTP Host header.
      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=${EKS_COMPUTE_CLUSTER_NAME} HELMFILE_ENV=eks-bdd-multi KUBECONFIG_FILE=${REPO_ROOT}/tests/bdd/out/eks-compute-kubeconfig.yaml NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml
        """
      Then the command exit code should be 0

      Then these Helm releases should be deployed using context "${EKS_COMPUTE_CONTEXT}":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "${EKS_COMPUTE_CONTEXT}" should complete rollout within "10m"

      # Wait for the NVCFBackend on the compute cluster to report the
      # agent healthy. The agent reaching ICMS healthy confirms the
      # cross-cluster Host-header + registration wiring works.
      Then NVCFBackend "${EKS_COMPUTE_CLUSTER_NAME}" in namespace "nvca-operator" using context "${EKS_COMPUTE_CONTEXT}" should report agent status "healthy" within "10m"

      # The pull secret is created only in nvca-operator; confirm the
      # operator propagated it to nvca-system. Asserting propagation here
      # catches a broken propagation that the node image cache would
      # otherwise mask under imagePullPolicy IfNotPresent.
      Then these Kubernetes resources should exist in namespace "nvca-system" using context "${EKS_COMPUTE_CONTEXT}":
        | kind   | name             |
        | Secret | nvcr-pull-secret |

  Rule: Helmfile-installed multi-cluster NVCF can run workloads

    @function-lifecycle
    Scenario: User creates, deploys, and invokes the Load Tester Supreme sample function
      # Function management reaches the control plane through the gateway,
      # and the deployment must execute on the separately registered compute
      # cluster using the worker-facing endpoints configured above.
      Given command has succeeded:
        """
        kubectl wait nvcfbackend ${EKS_COMPUTE_CLUSTER_NAME} -n nvca-operator --context ${EKS_COMPUTE_CONTEXT} --for=jsonpath={.status.agentStatus}=healthy --timeout=10m
        """

      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/out/nvcf-cli-eks-bdd-multi.yaml"

      When I successfully create function "bdd-load-tester-supreme" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option           | value   |
        | --inference-url  | /echo   |
        | --inference-port | 8000    |
        | --health-uri     | /health |
        | --health-port    | 8000    |
        | --health-timeout | PT30S   |

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value                       |
        | --gpu           | H100                        |
        | --instance-type | NCP.GPU.H100_8x             |
        | --backend       | ${EKS_COMPUTE_CLUSTER_NAME} |
        | --regions       | ${EKS_REGION}               |
        | --min-instances | 1                           |
        | --max-instances | 1                           |
        | --timeout       | 900                         |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-load-tester-supreme                                            |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |

      # AWS can briefly return NXDOMAIN for a newly provisioned ELB even after
      # earlier successful lookups. Reconfirm system-resolver stability before
      # the CLI performs its function-details lookup and invocation.
      Then DNS name "${EKS_GATEWAY_ADDR}" should resolve within "180" seconds

      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-echo","repeats":1}
        """
      Then the command output should contain "bdd-echo"

    # Failing until GitHub issue #1098 is resolved and the fix from GitHub
    # issue #1032 is consumed by the self-managed stack.
    @nvct-task-api
    Scenario: User launches an NVCT task on the compute cluster and waits for completion
      Given command has succeeded:
        """
        kubectl wait nvcfbackend ${EKS_COMPUTE_CLUSTER_NAME} -n nvca-operator --context ${EKS_COMPUTE_CONTEXT} --for=jsonpath={.status.agentStatus}=healthy --timeout=10m
        """

      When I run command:
        """
        env NVCT_BDD_STATE_PATH=${HOME}/.nvcf-cli.nvcf-cli-eks-bdd-multi.state NVCT_BDD_API_KEYS_URL=http://${EKS_GATEWAY_ADDR}/v1/keys NVCT_BDD_API_KEYS_HOST=api-keys.${EKS_GATEWAY_DOMAIN} NVCT_BDD_TASKS_URL=http://${EKS_GATEWAY_ADDR}/v1/nvct/tasks NVCT_BDD_TASKS_HOST=tasks.${EKS_GATEWAY_DOMAIN} NVCT_BDD_TASK_BACKEND=${EKS_COMPUTE_CLUSTER_NAME} NVCT_BDD_TASK_INSTANCE_TYPE=NCP.GPU.H100_8x tests/bdd/scripts/run-nvct-task-smoke.sh
        """
      Then the command exit code should be 0
      And the command output should contain "COMPLETED"
