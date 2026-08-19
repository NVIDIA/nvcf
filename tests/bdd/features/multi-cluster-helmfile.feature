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
  # fixture's service-DNS hostnames must match the local stack values
  # used by the CLI feature.
  # See tests/bdd/AGENTS.md "CLI vs Helmfile install paths".

  Rule: Helmfile installs the control plane on the control-plane cluster

    Background:
      Given environment variable "NGC_API_KEY" is set
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      # The multi-cluster fixture starts from local service-DNS
      # endpoint values, then the Background overlays
      # operator-specific registry values before the first Helmfile
      # install. Later scenarios reuse that install instead of
      # reinstalling with different secrets or URLs.
      And I copy the file "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE | nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/stargate-client:0.2.0  |
      And I copy the file "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" to "deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml"
      And I update yaml file "deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml" with keys:
        | global.imagePullSecrets[0].name               | nvcr-pull-secret                                                    |
        | global.helm.sources.repository                | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                       | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/10081, and ncp-local-cp also
      # needs NATS on 4222 plus the worker callback port 10086.
      # Fail loudly so the operator runs
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
        | cert-manager     |

    @control-plane @llm-gateway @split-cluster-llm @llm-pki
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

      # These routes are installed by ncp-local before the Helmfile
      # stack, then become fully resolved once the control-plane
      # Services exist. Check route status here so Gateway wiring
      # failures point at the route layer instead of surfacing only
      # during function invocation.
      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/nvcf-api-control-plane httproute/invocation-control-plane httproute/reval-control-plane -n nvcf --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/nvcf-api-control-plane httproute/invocation-control-plane httproute/reval-control-plane -n nvcf --for=jsonpath='{.status.parents[0].conditions[?(@.type=="ResolvedRefs")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/ess-control-plane -n ess --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/ess-control-plane -n ess --for=jsonpath='{.status.parents[0].conditions[?(@.type=="ResolvedRefs")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/sis-control-plane -n sis --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait httproute/sis-control-plane -n sis --for=jsonpath='{.status.parents[0].conditions[?(@.type=="ResolvedRefs")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait grpcroute/nvcf-api-control-plane-grpc -n nvcf --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp wait grpcroute/nvcf-api-control-plane-grpc -n nvcf --for=jsonpath='{.status.parents[0].conditions[?(@.type=="ResolvedRefs")].status}'=True --timeout=2m
        """
      Then the command exit code should be 0

      # Keep the Helmfile path values-driven. Read only the public CA from the
      # cert-manager Secret, compute its canonical fingerprint, and merge the
      # trust settings into the compute environment authored in the Background.
      When I run command:
        """
        set -euo pipefail
        kubectl --context k3d-ncp-local-cp wait certificate/stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m
        trust_dir=deploy/stacks/nvcf-compute-plane/out/bdd-split-llm
        mkdir -p "$trust_dir"
        rm -f "$trust_dir"/certificate-*.pem "$trust_dir/certificate-hashes"
        encoded_ca="$(kubectl --context k3d-ncp-local-cp get secret/stargate-quic-tls -n nvcf -o jsonpath='{.data.ca\.crt}')"
        test -n "$encoded_ca"
        printf '%s' "$encoded_ca" | openssl base64 -d -A >"$trust_dir/ca.pem"
        openssl x509 -in "$trust_dir/ca.pem" -noout -subject -issuer
        awk -v output_dir="$trust_dir" '/-----BEGIN CERTIFICATE-----/ { certificate++; in_certificate=1 } in_certificate { print > (output_dir "/certificate-" certificate ".pem") } /-----END CERTIFICATE-----/ { in_certificate=0 }' "$trust_dir/ca.pem"
        for certificate_file in "$trust_dir"/certificate-*.pem; do
          openssl x509 -in "$certificate_file" -outform DER | openssl dgst -sha256 -r | awk '{print $1}'
        done | sort -u >"$trust_dir/certificate-hashes"
        trust_fingerprint="$({ printf 'nvcf-trust-bundle-v1\n'; cat "$trust_dir/certificate-hashes"; } | openssl dgst -sha256 -r | awk '{print "sha256:" $1}')"
        export TRUST_BUNDLE_PEM="$(sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p' "$trust_dir/ca.pem")"
        export TRUST_BUNDLE_FINGERPRINT="$trust_fingerprint"
        yq -i '.agentConfig.mergeConfig = (((.agentConfig.mergeConfig | from_yaml) * {"workload": {"stargateQUICInsecure": false, "transportTLS": {"trustMode": "bundle", "trustBundleFingerprint": strenv(TRUST_BUNDLE_FINGERPRINT), "trustBundlePem": strenv(TRUST_BUNDLE_PEM)}}}) | to_yaml)' deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml
        """
      Then the command exit code should be 0

  Rule: Helmfile registers and installs NVCA on the compute cluster

    Background:
      Given environment variable "NVCF_CLI" is set
      And environment variable "REPO_ROOT" is set
      # This rule depends on the earlier control-plane scenario in the
      # same feature run. That scenario authors local-bdd.yaml with
      # the compute-reachable endpoints, creates the pull secrets, and
      # installs the control plane. Do not repeat that setup here.

    @nvca-registration @split-cluster-llm @llm-pki
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
      # compute-plane install that follows also runs helm against the
      # ambient context, so this single switch covers both steps.
      When I run command "kubectl config use-context k3d-ncp-local-compute-1"
      Then the command exit code should be 0

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local-compute-1 NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should exist
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" should contain:
        """
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          identitySource: psat
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml" key "clusterGroupID" should not be empty

      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | nvca-operator |

      When I run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local-compute-1 HELMFILE_ENV=local-bdd NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then the command exit code should be 0

      When I run command:
        """
        set -euo pipefail
        expected_fingerprint="$({ printf 'nvcf-trust-bundle-v1\n'; cat deploy/stacks/nvcf-compute-plane/out/bdd-split-llm/certificate-hashes; } | openssl dgst -sha256 -r | awk '{print "sha256:" $1}')"
        merge_config="$(kubectl --context k3d-ncp-local-compute-1 get configmap/agent-config-merge -n nvca-operator -o jsonpath='{.data.config\.yaml}')"
        test "$(printf '%s' "$merge_config" | yq -r '.workload.transportTLS.trustMode')" = bundle
        test "$(printf '%s' "$merge_config" | yq -r '.workload.transportTLS.trustBundleFingerprint')" = "$expected_fingerprint"
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

  Rule: Helmfile-installed multi-cluster NVCF can run workloads

    # This scenario intentionally has no Background. It depends on the
    # earlier control-plane install and NVCA registration scenarios in
    # this feature run, and is not a standalone tag target.
    @nvct-task-api
    Scenario: Operator launches an NVCT task and waits for it to complete
      When I run command:
        """
        tests/bdd/scripts/run-nvct-task-smoke.sh
        """
      Then the command exit code should be 0
      And the command output should contain "COMPLETED"

    @function-lifecycle
    Scenario: Operator creates, deploys, and invokes the Load Tester Supreme sample function
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-load-tester-supreme --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8 --inference-url /echo --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local-compute-1 --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-load-tester-supreme --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --request-body '{"message":"bdd-echo","repeats":1}' --timeout 120 --poll-duration 5
        """
      Then the command exit code should be 0
      And the command output should contain "bdd-echo"

    @function-lifecycle @grpc
    Scenario: Operator creates, deploys, and invokes the gRPC Load Tester Supreme sample function
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-grpc-load-tester-supreme --image nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8 --inference-url /grpc --inference-port 8001 --health-protocol GRPC --health-uri / --health-port 8001 --health-timeout PT30S
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local-compute-1 --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-grpc-load-tester-supreme --for function --scopes invoke_function,list_functions,queue_details,list_functions_details
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --grpc --grpc-plaintext --grpc-service Echo --grpc-method EchoMessage --request-body '{"message":"bdd-grpc-echo"}' --timeout 120 --poll-duration 5
        """
      Then the command exit code should be 0
      And the command output should contain "bdd-grpc-echo"

    # This tagged scenario uses the local implementation chart and image. It
    # depends on the control-plane and compute-plane scenarios above, including
    # the managed OpenBao trust bundle in the compute environment.
    @function-lifecycle @split-cluster-llm @llm-pki
    Scenario: Operator invokes two mock LLM replicas through two split-cluster Stargates
      # Function creation validates the registry manifest even when k3d has
      # local image bytes. The operator supplies an existing mock-dynamo tag;
      # the build and import below replace only the local cluster image.
      Given environment variable "NVCF_BDD_MOCK_DYNAMO_TAG" is set

      When I run command:
        """
        docker build --file src/libraries/rust/stargate/Dockerfile --target stargate-runtime --tag nvcf-stargate-per-replica:bdd src/libraries/rust/stargate
        """
      Then the command exit code should be 0

      When I run command:
        """
        k3d image import nvcf-stargate-per-replica:bdd --cluster ncp-local-cp
        """
      Then the command exit code should be 0

      When I run command:
        """
        helm upgrade llm-request-router deploy/helm/llm-request-router/llm-request-router --namespace nvcf --kube-context k3d-ncp-local-cp --reuse-values --set-string llmRequestRouter.image.registry= --set-string llmRequestRouter.image.repository=nvcf-stargate-per-replica --set-string llmRequestRouter.image.tag=bdd --wait --timeout 10m
        """
      Then the command exit code should be 0

      When I run command:
        """
        make -C tools/ncp-local-cluster configure-compute-llm-router-endpoints CONTROL_PLANE_CLUSTER_NAME=ncp-local-cp COMPUTE_CLUSTER_NAME=ncp-local-compute-1
        """
      Then the command exit code should be 0

      When I run command:
        """
        kubectl --context k3d-ncp-local-cp rollout status statefulset/llm-request-router -n nvcf --timeout=10m
        kubectl --context k3d-ncp-local-cp rollout status deployment/llm-api-gateway -n nvcf --timeout=10m
        test "$(kubectl --context k3d-ncp-local-cp get statefulset llm-request-router -n nvcf -o jsonpath='{.status.readyReplicas}')" = "2"
        test "$(kubectl --context k3d-ncp-local-cp get deployment llm-api-gateway -n nvcf -o jsonpath='{.status.readyReplicas}')" = "2"
        kubectl --context k3d-ncp-local-cp wait certificate/stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m
        kubectl --context k3d-ncp-local-cp get statefulset/llm-request-router -n nvcf -o json | jq -e '.spec.template.spec.containers[] | select(any(.args[]?; startswith("--grpc-pylon-dial-addr="))) | (.args | index("--grpc-pylon-dial-addr={stargate_id}.nvcf-llm-router.svc.cluster.local:50071")) != null and (.args | index("--reverse-tunnel-pylon-dial-addr=$(POD_NAME).nvcf-llm-router.svc.cluster.local:50072")) != null' >/dev/null
        kubectl --context k3d-ncp-local-cp get certificate/stargate-quic-tls -n nvcf -o json | jq -e '(.spec.dnsNames | index("llm-request-router.nvcf.svc.cluster.local")) != null and (.spec.dnsNames | index("*.llm-request-router-headless.nvcf.svc.cluster.local")) != null and all(.spec.dnsNames[]; contains("nvcf-llm-router.svc.cluster.local") | not)' >/dev/null
        """
      Then the command exit code should be 0

      When I run command:
        """
        MOCK_IMAGE="nvcr.io/$SAMPLE_NGC_ORG/$SAMPLE_NGC_TEAM/mock-dynamo:$NVCF_BDD_MOCK_DYNAMO_TAG"
        docker build --file src/libraries/rust/stargate/Dockerfile --target mock-dynamo-runtime --tag "$MOCK_IMAGE" src/libraries/rust/stargate
        k3d image import "$MOCK_IMAGE" --cluster ncp-local-compute-1
        """
      Then the command exit code should be 0

      When I run command:
        """
        MOCK_IMAGE="nvcr.io/$SAMPLE_NGC_ORG/$SAMPLE_NGC_TEAM/mock-dynamo:$NVCF_BDD_MOCK_DYNAMO_TAG"
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function create --name bdd-split-mock-llm --image "$MOCK_IMAGE" --container-args "--http-listen-addr=0.0.0.0:8000 --model-name=dummy-model --num-tokens=2 --token-delay-ms=0" --inference-url /v1/chat/completions --inference-port 8000 --health-uri /health --health-port 8000 --health-timeout PT30S --function-type LLM --llm-model "name=dummy-model,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=round_robin,tokenRateLimit=1000-S"
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_8x --backend ncp-local-compute-1 --regions us-west-1 --min-instances 2 --max-instances 2 --timeout 900
        """
      Then the command exit code should be 0

      When I run command:
        """
        set -euo pipefail
        pods="$(kubectl --context k3d-ncp-local-compute-1 get pods -n nvcf-backend -o json | jq -r '.items[] | select(any(.spec.containers[]; .name == "llm-worker")) | .metadata.name')"
        test "$(printf '%s\n' "$pods" | sed '/^$/d' | wc -l | tr -d ' ')" = "2"
        while IFS= read -r pod; do
          test -n "$pod"
          kubectl --context k3d-ncp-local-compute-1 wait "pod/$pod" -n nvcf-backend --for=condition=Ready --timeout=10m
          kubectl --context k3d-ncp-local-compute-1 get "pod/$pod" -n nvcf-backend -o json | jq -e '.spec.containers[] | select(.name == "llm-worker") | ((.args | index("--quic-insecure")) == null) and (any(.env[]?; .name == "STARGATE_TLS_CERT_PATH"))' >/dev/null
          router_count="$(kubectl --context k3d-ncp-local-compute-1 logs "pod/$pod" -n nvcf-backend -c llm-worker | grep 'reverse tunnel connected' | sed -n 's/.*router_addr=\([^ ]*\).*/\1/p' | sort -u | wc -l | tr -d ' ')"
          test "$router_count" -eq 2
        done <<<"$pods"
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml api-key generate --description bdd-split-mock-llm --for function --scopes invoke_function,list_functions,queue_details,list_functions_details >/dev/null
        """
      Then the command exit code should be 0

      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --model-name dummy-model --inference-url /v1/chat/completions --request-body '{"messages":[{"role":"user","content":"split cluster smoke"}],"stream":false}' --timeout 30
        """
      Then the command exit code should be 0
      And the command output should contain "choices"
      And the command output should contain "/dummy-model"

      When I run command:
        """
        set -euo pipefail
        for request_number in 1 2 3 4 5 6; do
          ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function invoke --model-name dummy-model --inference-url /v1/chat/completions --request-body "{\"messages\":[{\"role\":\"user\",\"content\":\"split cluster request $request_number\"}],\"stream\":false}" --timeout 30
        done
        """
      Then the command exit code should be 0

      When I run command:
        """
        set -euo pipefail
        pods="$(kubectl --context k3d-ncp-local-compute-1 get pods -n nvcf-backend -o json | jq -r '.items[] | select(any(.spec.containers[]; .name == "llm-worker")) | .metadata.name')"
        backends_with_chat=0
        forward_pid=""
        cleanup_forward() {
          if [ -n "$forward_pid" ]; then
            kill "$forward_pid" >/dev/null 2>&1 || true
            wait "$forward_pid" >/dev/null 2>&1 || true
          fi
        }
        trap cleanup_forward EXIT
        for pod in $pods; do
          log_file="$(mktemp)"
          kubectl --context k3d-ncp-local-compute-1 port-forward "pod/$pod" -n nvcf-backend 28090:8000 >"$log_file" 2>&1 &
          forward_pid=$!
          ready=false
          for attempt in 1 2 3 4 5 6 7 8 9 10; do
            if curl --silent --fail http://127.0.0.1:28090/health >/dev/null 2>&1; then
              ready=true
              break
            fi
            sleep 1
          done
          if [ "$ready" != true ]; then
            kill "$forward_pid" || true
            wait "$forward_pid" || true
            sed -n '1,40p' "$log_file" >&2
            exit 1
          fi
          chat_count="$(curl --silent --fail http://127.0.0.1:28090/test-control | jq '[.counters[]? | select(.endpoint == "chat_completions" and .request_class == "api_gateway") | .count] | add // 0')"
          cleanup_forward
          forward_pid=""
          if [ "$chat_count" -gt 0 ]; then
            backends_with_chat=$((backends_with_chat + 1))
          fi
          printf '%s chat_requests=%s\n' "$pod" "$chat_count"
        done
        test "$backends_with_chat" -eq 2
        """
      Then the command exit code should be 0
