@ncp-local @multi-cluster @helmfile @pki @llm-registration @negative
Feature: Reject insecure or invalid LLM worker registration
  As a self-managed NVCF operator,
  I want the Pylon registration endpoint to fail closed,
  so that invalid trust or authority cannot silently enter the routing plane.

  Rule: Every rejected path is observable at the operator boundary

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      And I prepare Helmfile environment "local-bdd-registration-tls-fail-closed" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name                            | nvcr-pull-secret                                                     |
        | global.helm.sources.repository                             | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                                    | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.workerEndpoints.llmRequestRouterAddress             | https://llm-request-router.nvcf.svc.cluster.local:50071              |
        | addons.llm.requestRouter.workload.kind                      | StatefulSet                                                          |
        | addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress | https://llm-request-router.nvcf.svc.cluster.local:50071              |
        | observability.profile                                      | disabled                                                             |
      And I prepare Helmfile environment "local-bdd-registration-tls-invalid-authority" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name                            | nvcr-pull-secret                                                     |
        | global.helm.sources.repository                             | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.image.repository                                    | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}                                |
        | global.workerEndpoints.llmRequestRouterAddress             | https://llm_request_router.nvcf.svc.cluster.local:50071              |
        | addons.llm.requestRouter.workload.kind                      | StatefulSet                                                          |
        | addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress | https://llm_request_router.nvcf.svc.cluster.local:50071              |
        | observability.profile                                      | disabled                                                             |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-tls-fail-closed-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-registration-tls-invalid-authority-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
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

    Scenario: Registration rejects every untrusted client path
      When I run command "make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-tls-fail-closed"
      Then the command exit code should be 0
      When I run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-registration-tls-fail-closed"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp wait clusterissuer nvcf-openbao-pki --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp wait certificate stargate-quic-tls -n nvcf --for=condition=Ready --timeout=5m"
      Then the command exit code should be 0
      When I run command "kubectl --context k3d-ncp-local-cp rollout status statefulset/llm-request-router -n nvcf --timeout=10m"
      Then the command exit code should be 0

      # Establish that the TLS listener is reachable with its issued root,
      # expected DNS identity, and HTTP/2 application protocol.
      When I run command:
        """
        /bin/bash -c 'openssl s_client -connect 127.0.0.1:50071 -servername llm-request-router.nvcf.svc.cluster.local -alpn h2 -verify_return_error -CAfile <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) </dev/null 2>&1'
        """
      Then the command exit code should be 0
      And the command output should contain "Verify return code: 0 (ok)"
      And the command output should contain "ALPN protocol: h2"

      When I run command:
        """
        /bin/bash -c 'set -u; cert_dir=$(mktemp -d); trap '\''rm -rf "$cert_dir"'\'' EXIT; openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=wrong-root -keyout "$cert_dir/key.pem" -out "$cert_dir/ca.pem" -days 1 >/dev/null 2>&1; grpcurl -max-time 5 -cacert "$cert_dir/ca.pem" -authority llm-request-router.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates >/dev/null 2>&1; rc=$?; [ "$rc" -ne 0 ] && printf "wrong-root-rejected\n"'
        """
      Then the command exit code should be 0

      When I run command:
        """
        /bin/bash -c 'grpcurl -max-time 5 -cacert <(kubectl --context k3d-ncp-local-cp get secret stargate-quic-tls -n nvcf -o jsonpath="{.data.ca\.crt}" | base64 -d) -authority wrong-host.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates >/dev/null 2>&1; rc=$?; [ "$rc" -ne 0 ] && printf "wrong-host-rejected\n"'
        """
      Then the command exit code should be 0

      When I run command:
        """
        /bin/bash -c 'grpcurl -max-time 5 -authority llm-request-router.nvcf.svc.cluster.local -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates >/dev/null 2>&1; rc=$?; [ "$rc" -ne 0 ] && printf "missing-trust-rejected\n"'
        """
      Then the command exit code should be 0

      When I run command:
        """
        /bin/bash -c 'grpcurl -plaintext -max-time 5 -import-path src/libraries/rust/stargate/crates/proto/proto -proto stargate.proto 127.0.0.1:50071 stargate.StargateControlPlane/WatchStargates >/dev/null 2>&1; rc=$?; [ "$rc" -ne 0 ] && printf "plaintext-rejected\n"'
        """
      Then the command exit code should be 0

      When I run command:
        """
        /bin/sh -c 'make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd-registration-tls-invalid-authority >/dev/null 2>&1; rc=$?; [ "$rc" -ne 0 ] && printf "invalid-authority-rejected\n"'
        """
      Then the command exit code should be 0
