@event-ledger @ncp-local @single-cluster @helmfile
Feature: Install a local single-cluster NVCF stack and check the event ledger
  As a self-managed NVCF operator,
  I want to install the stack with the event ledger enabled,
  so that I can check it is reachable, protected, and fed by SIS.

  Rule: Helmfile installs the local control plane with the event ledger enabled

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | NVCF_CLI        |
        | REPO_ROOT       |
      # The fixture is a copy of deploy/stacks/self-managed/environments/local.yaml,
      # which already carries every ncp-local local-mode override. The Background
      # only overlays the values that vary per NGC org and pull-secret name. The
      # event ledger add-on is on by default in the stack's base environment.
      And I prepare Helmfile environment "local-bdd-event-ledger" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      # The NVCA OTel collector is off by default. It publishes NVCA's Kubernetes
      # events to the ledger, so the NVCA scenarios need it on; the image must be
      # published under global.image.
      And I prepare Helmfile environment "local-bdd-event-ledger" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name                       | nvcr-pull-secret                     |
        | global.helm.sources.repository                        | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository                               | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.nvcaOperator.selfManaged.otelCollector.enabled | true                                 |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-event-ledger-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/10081, NATS on 4222, and the worker callback port
      # 10086, overlapping host ports single-cluster ncp-local needs. Fail loudly
      # so the operator runs `make -C tools/ncp-local-cluster destroy-multicluster`
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
        | cert-manager     |

    Scenario: Operator installs the control plane with the event ledger enabled
      When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-event-ledger"

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name           | namespace            |
        | nats           | nats-system          |
        | cert-manager   | cert-manager         |
        | openbao-server | vault-system         |
        | cassandra      | cassandra-system     |
        | api-keys       | api-keys             |
        | sis            | sis                  |
        | api            | nvcf                 |
        | event-ledger   | nvcf                 |
        | ingress        | envoy-gateway-system |

      # The checks below create API keys, which needs the admin token.
      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init"

    Scenario: SIS is configured to send events to the event ledger
      When I successfully run command "kubectl --context k3d-ncp-local --namespace sis get configmaps -o yaml"
      Then the command output should contain all:
        | text                                  |
        | ICMS_FNDS_MESSAGES_ENABLED: "true"    |
        | ICMS_FNDS_MESSAGES_V3_ENABLED: "true" |

  Rule: The event ledger is reachable, protected, and readable

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | REPO_ROOT       |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

    # These scenarios depend on the earlier control-plane install scenario in the
    # same feature run and are not standalone tag targets.
    Scenario: Event ledger is running and routed through the shared gateway
      Then deployment "event-ledger" in namespace "nvcf" using context "k3d-ncp-local" should complete rollout within "2m"
      And these Kubernetes resources should exist in namespace "nvcf" using context "k3d-ncp-local":
        | kind    | name         |
        | service | event-ledger |
      And these Gateway API routes should be accepted and resolved using context "k3d-ncp-local" within "2m":
        | kind      | name         | namespace            | parent    |
        | HTTPRoute | event-ledger | envoy-gateway-system | shared-gw |

      When I successfully run command "kubectl --context k3d-ncp-local --namespace nvcf get service event-ledger -o jsonpath={.spec.ports[*].name}"
      Then the command output should contain "api-port"

      When I send a "GET" request to "http://events.localhost:8080/health"
      Then the HTTP status should be 200

    # A missing or JWT-shaped invalid credential is rejected with 401. An opaque
    # token is not a JWT, so it goes to the api-keys policy evaluator, which
    # denies an unknown key with 403.
    Scenario: Requests without valid credentials are rejected
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/cloudevents" with body:
        """
        {"specversion":"1.0"}
        """
      Then the HTTP status should be 401

      Given the HTTP request header "Authorization" is "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/cloudevents"
      Then the HTTP status should be 401

      Given the HTTP request header "Authorization" is "Bearer not-a-token"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/k8s-events"
      Then the HTTP status should be 403

      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-auth/stats"
      Then the HTTP status should be 401

      Given the HTTP request header "Authorization" is "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-auth/events"
      Then the HTTP status should be 401

      Given the HTTP request header "Authorization" is "Bearer nvapi-not-a-real-key"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-auth/events"
      Then the HTTP status should be 403

      When I send a "GET" request to "http://events.localhost:8080/status"
      Then the HTTP status should be 401

    Scenario: V1 and V2 endpoints are disabled
      When I send a "POST" request to "http://events.localhost:8080/v1/ledger/versions/bdd-version/instances/bdd-instance" with body:
        """
        {}
        """
      Then the HTTP status should be 404

      When I send a "POST" request to "http://events.localhost:8080/v2/ledger/versions/bdd-version/deployments/bdd-deployment/instances/bdd-instance" with body:
        """
        {}
        """
      Then the HTTP status should be 404

    Scenario: A read key can query the ledger and bad queries are rejected
      When I successfully run command:
        """
        /bin/bash -c 'date -u -d "+1 day" +%Y-%m-%dT%H:%M:%S.000Z'
        """
      And I export command output to environment variable "BDD_EL_KEY_EXPIRES"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      When I send a "POST" request to "http://api-keys.localhost:8080/v1/keys" with body:
        """
        {
          "description": "bdd-event-ledger-read",
          "expires_at": "${BDD_EL_KEY_EXPIRES}",
          "authorizations": {
            "policies": [
              {
                "aud": "nvidia-event-ledger-ncp-service-id-ckozoh6f",
                "auds": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"],
                "product": "nv-cloud-functions",
                "resources": [{"id": "*", "type": "account-functions"}],
                "scopes": ["fnds:getEvents", "fnds:getStats"]
              }
            ]
          },
          "audience_service_ids": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"]
        }
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"
      # Delete the key when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      And I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}" at scenario teardown expecting status 204
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-such-namespace/stats"
      Then the HTTP status should be 200
      And the HTTP response JSON field "namespace" should equal "bdd-no-such-namespace"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-such-namespace/events"
      Then the HTTP status should be 200

      # Malformed queries must get a 400 before reaching Cassandra: a half-supplied
      # attribute filter, a disallowed character in a context id, and an unknown stats view.
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-such-namespace/events?attribute_key=only-a-key"
      Then the HTTP status should be 400

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-such-namespace/events?instance_id=bad!id"
      Then the HTTP status should be 400

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-no-such-namespace/stats?view=bogus"
      Then the HTTP status should be 400


    # Write access needs an OpenBao JWT with the FnDS write scope, so a
    # read-scoped api key must be rejected on the write route.
    Scenario: A read-scoped key cannot write events
      When I successfully run command:
        """
        /bin/bash -c 'date -u -d "+1 day" +%Y-%m-%dT%H:%M:%S.000Z'
        """
      And I export command output to environment variable "BDD_EL_KEY_EXPIRES"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      When I send a "POST" request to "http://api-keys.localhost:8080/v1/keys" with body:
        """
        {
          "description": "bdd-event-ledger-read-only-write",
          "expires_at": "${BDD_EL_KEY_EXPIRES}",
          "authorizations": {
            "policies": [
              {
                "aud": "nvidia-event-ledger-ncp-service-id-ckozoh6f",
                "auds": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"],
                "product": "nv-cloud-functions",
                "resources": [{"id": "*", "type": "account-functions"}],
                "scopes": ["fnds:getEvents", "fnds:getStats"]
              }
            ]
          },
          "audience_service_ids": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"]
        }
        """
      When I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"
      # Delete the key when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      And I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}" at scenario teardown expecting status 204
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      And the HTTP request header "Content-Type" is "application/cloudevents+json"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/cloudevents" with body:
        """
        {"specversion":"1.0","id":"bdd-write-attempt-1","type":"BddWriteAttempt","source":"bdd","namespace":"bdd-write-attempt"}
        """
      Then the HTTP status should be 401 or 403

      # The other write route is registered separately, so check it too. The scope
      # check runs before the body is read, so no payload is needed.
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      And the HTTP request header "Content-Type" is "application/x-protobuf"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/k8s-events"
      Then the HTTP status should be 403


    # The positive control for the scope checks: a key with only the write scope
    # may write, which shows the rejections above are specific and not an outage,
    # and it must not be able to read. The event is fixed, so reruns update one row.
    Scenario: A write-scoped key can write events but cannot read them
      When I successfully run command:
        """
        /bin/bash -c 'date -u -d "+1 day" +%Y-%m-%dT%H:%M:%S.000Z'
        """
      And I export command output to environment variable "BDD_EL_KEY_EXPIRES"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      When I send a "POST" request to "http://api-keys.localhost:8080/v1/keys" with body:
        """
        {
          "description": "bdd-event-ledger-write-only",
          "expires_at": "${BDD_EL_KEY_EXPIRES}",
          "authorizations": {
            "policies": [
              {
                "aud": "nvidia-event-ledger-ncp-service-id-ckozoh6f",
                "auds": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"],
                "product": "nv-cloud-functions",
                "resources": [{"id": "*", "type": "account-functions"}],
                "scopes": ["fnds:createEvent"]
              }
            ]
          },
          "audience_service_ids": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"]
        }
        """
      When I export the HTTP response JSON field "id" to environment variable "BDD_EL_WRITE_KEY_ID"
      # Delete the key when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      And I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_WRITE_KEY_ID}" at scenario teardown expecting status 204
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "value" to environment variable "BDD_EL_WRITE_KEY"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_WRITE_KEY}"
      And the HTTP request header "Content-Type" is "application/cloudevents+json"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/cloudevents" with body:
        """
        {"specversion":"1.0","id":"bdd-write-scope-1","type":"BddWriteScope","source":"bdd","namespace":"bdd-write-scope"}
        """
      Then the HTTP status should be 200
      And the HTTP response JSON field "status" should equal "accepted"
      And the HTTP response JSON field "success_count" should be at least 1

      # An empty OTLP body is rejected by the handler, so a 400 here shows the key
      # passed the scope check on this route without writing anything.
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_WRITE_KEY}"
      And the HTTP request header "Content-Type" is "application/x-protobuf"
      When I send a "POST" request to "http://events.localhost:8080/v3/ledger/k8s-events"
      Then the HTTP status should be 400
      And the HTTP response body should contain "No events found in request"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_WRITE_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-write-scope/events"
      Then the HTTP status should be 403

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_WRITE_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/bdd-write-scope/stats"
      Then the HTTP status should be 403

  Rule: nvcf-cli reads the event ledger

    Background:
      Given these environment variables are set:
        | name      |
        | NVCF_CLI  |
        | REPO_ROOT |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

    # The ledger URL is not configured: the CLI derives events.localhost from
    # base_http_url. The key lives until it expires (1h) and in the CLI state file,
    # which suite teardown restores. A fresh version id has no events, so an
    # empty result must still exit 0.
    Scenario: The CLI reads the ledger with a generated Event Ledger key
      When I successfully generate an Event Ledger API key with CLI options:
        | option       | value |
        | --expires-in | 1h    |

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id 00000000-0000-4000-8000-00000000e1de"
      Then the command output should contain "Function version: 00000000-0000-4000-8000-00000000e1de"
      And the command output should contain "Contexts: 0"

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id 00000000-0000-4000-8000-00000000e1de --json"
      Then the command output should contain "00000000-0000-4000-8000-00000000e1de"

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id 00000000-0000-4000-8000-00000000e1de --event-filter InstanceReady,NoSuchEvent"
      Then the command output should contain "Contexts: 0"

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stages events list --function-version-id 00000000-0000-4000-8000-00000000e1de"
      Then the command output should contain "No events found."

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stages events list --function-version-id 00000000-0000-4000-8000-00000000e1de --deployment-id bdd-deployment --gpu-specification-id bdd-gpu --cluster-id bdd-cluster --instance-id bdd-instance --resource-id bdd-resource --json"
      Then the command output should contain "00000000-0000-4000-8000-00000000e1de"

    # Each rejection happens in the CLI before any request is sent, except the
    # missing key, which the config without a saved key reaches.
    Scenario: The CLI rejects bad input and a missing key
      When I run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id not-a-uuid"
      Then the command should fail
      And the command output should contain "must be a UUID"

      When I run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stages events list --function-version-id 00000000-0000-4000-8000-00000000e1de --attribute-key only-a-key"
      Then the command should fail
      And the command output should contain "--attribute-key and --attribute-value must be used together"

      When I run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local-no-key.yaml stats get"
      Then the command should fail
      And the command output should contain "--function-version-id is required"

      When I run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local-no-key.yaml stats get --function-version-id 00000000-0000-4000-8000-00000000e1de"
      Then the command should fail
      And the command output should contain "api-key generate --for event-ledger"

      When I run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local-no-key.yaml stages events list --function-version-id 00000000-0000-4000-8000-00000000e1de"
      Then the command should fail
      And the command output should contain "api-key generate --for event-ledger"

  Rule: Helmfile installs NVCA on the same local cluster after registration

    Background:
      Given these environment variables are set:
        | name      |
        | NVCF_CLI  |
        | REPO_ROOT |
      # This rule depends on the earlier control-plane install scenario in the
      # same feature run. It is only needed for the function deployment scenario.

    @nvca-registration @compute-plane
    Scenario: Operator registers the local cluster and installs the NVCA operator
      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-event-ledger control-plane profile export --cluster-name ncp-local
        """
      Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist
      And yaml file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should have non-empty keys:
        | key            |
        | clusterID      |
        | clusterGroupID |

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-event-ledger NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """

      Then these Helm releases should be deployed using context "k3d-ncp-local":
        | name          | namespace     |
        | nvca-operator | nvca-operator |

      Then deployment "nvca-operator" in namespace "nvca-operator" using context "k3d-ncp-local" should complete rollout within "10m"

      Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"

  Rule: A function deployment produces SIS events

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | REPO_ROOT       |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

    # This scenario depends on the earlier control-plane install and NVCA
    # registration scenarios in the same feature run.
    @compute-plane @function-lifecycle
    Scenario: A function deployment produces SIS events that can be read back
      When I successfully run command:
        """
        /bin/bash -c 'date -u -d "+1 day" +%Y-%m-%dT%H:%M:%S.000Z'
        """
      And I export command output to environment variable "BDD_EL_KEY_EXPIRES"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      When I send a "POST" request to "http://api-keys.localhost:8080/v1/keys" with body:
        """
        {
          "description": "bdd-event-ledger-lifecycle",
          "expires_at": "${BDD_EL_KEY_EXPIRES}",
          "authorizations": {
            "policies": [
              {
                "aud": "nvidia-event-ledger-ncp-service-id-ckozoh6f",
                "auds": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"],
                "product": "nv-cloud-functions",
                "resources": [{"id": "*", "type": "account-functions"}],
                "scopes": ["fnds:getEvents", "fnds:getStats"]
              }
            ]
          },
          "audience_service_ids": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"]
        }
        """
      When I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"
      # Delete the key when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      And I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}" at scenario teardown expecting status 204
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"

      When I successfully create function "bdd-event-ledger" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option           | value   |
        | --inference-url  | /echo   |
        | --inference-port | 8000    |
        | --health-uri     | /health |
        | --health-port    | 8000    |
        | --health-timeout | PT30S   |
      # Delete the function and undeploy it when the scenario ends, even if the
      # deploy fails. The undeploy runs first and does nothing if nothing deployed.
      And the function selected by NVCF CLI is deleted at scenario teardown
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I export the function selected by NVCF CLI to environment variables "BDD_EL_FUNCTION_ID" and "BDD_EL_VERSION_ID"

      # Events are stored per context, and stats list every context in the
      # namespace with its latest event. The NVCA collector, when enabled, adds
      # contexts of its own that sort before SIS's, so find the SIS context with
      # an event filter, then read its events.

      # SIS writes InstanceReady once the instance is running.
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I poll "GET" "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/stats" for up to 300 seconds until the HTTP response body contains "InstanceReady"
      Then the HTTP status should be 200
      And the HTTP response JSON field "namespace" should equal "${BDD_EL_VERSION_ID}"
      And the HTTP response JSON field "summary.by_event.InstanceReady" should be at least 1

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/stats?eventFilter=InstanceReady"
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "contexts.0.context" to environment variable "BDD_EL_CONTEXT"
      And I successfully run command:
        """
        /bin/bash -c 'echo "${BDD_EL_CONTEXT}" | tr "," "&"'
        """
      And I export command output to environment variable "BDD_EL_CONTEXT_QUERY"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/events?${BDD_EL_CONTEXT_QUERY}"
      Then the HTTP status should be 200
      And the HTTP response body should contain "InstanceReady"
      And the HTTP response body should contain "nvidia-spot"
      And the HTTP response body should contain "${BDD_EL_FUNCTION_ID}"
      And the HTTP response body should not contain "InstanceDestroyed"

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/stats?eventFilter=NoSuchEvent"
      Then the HTTP status should be 200
      And the HTTP response body should not contain "InstanceReady"

      # Undeploy removes the instance and SIS then reports it destroyed. Stats keep
      # only the latest event per context, while events keep the whole history.
      When I successfully undeploy the function selected by NVCF CLI
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I poll "GET" "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/stats" for up to 180 seconds until the HTTP response body contains "InstanceDestroyed"
      Then the HTTP status should be 200
      And the HTTP response JSON field "summary.by_event.InstanceDestroyed" should be at least 1

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I send a "GET" request to "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_VERSION_ID}/events?${BDD_EL_CONTEXT_QUERY}"
      Then the HTTP status should be 200
      And the HTTP response body should contain "InstanceReady"
      And the HTTP response body should contain "InstanceDestroyed"

  Rule: NVCA publishes events for a function deployment

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | NVCF_CLI        |
        | REPO_ROOT       |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

    # NVCA's OTel collector forwards Pod events and NVCA's own ICMSRequest events
    # to the ledger, authenticating with the NVCA service account token. The
    # ledger takes cluster_id from that identity, so every NVCA context carries
    # it, while SIS contexts do not. This scenario depends on the earlier
    # control-plane install and NVCA registration scenarios in the same feature
    # run. It checks stats only: stats keep the latest event of each context, so
    # which event names show up depends on how far the deployment got.
    @compute-plane @function-lifecycle
    Scenario: NVCA events from a function deployment reach the ledger and the CLI
      When I successfully run command:
        """
        /bin/bash -c 'date -u -d "+1 day" +%Y-%m-%dT%H:%M:%S.000Z'
        """
      And I export command output to environment variable "BDD_EL_KEY_EXPIRES"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      When I send a "POST" request to "http://api-keys.localhost:8080/v1/keys" with body:
        """
        {
          "description": "bdd-event-ledger-nvca",
          "expires_at": "${BDD_EL_KEY_EXPIRES}",
          "authorizations": {
            "policies": [
              {
                "aud": "nvidia-event-ledger-ncp-service-id-ckozoh6f",
                "auds": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"],
                "product": "nv-cloud-functions",
                "resources": [{"id": "*", "type": "account-functions"}],
                "scopes": ["fnds:getEvents", "fnds:getStats"]
              }
            ]
          },
          "audience_service_ids": ["nvidia-event-ledger-ncp-service-id-ckozoh6f"]
        }
        """
      When I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"
      # Delete the key when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
      And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
      And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
      And I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}" at scenario teardown expecting status 204
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"

      When I successfully run command "kubectl --context k3d-ncp-local --namespace nvca-system get pods --selector app.kubernetes.io/name=nvca -o jsonpath={..name}"
      Then the command output should contain "nvca-otel-collector"

      When I successfully create function "bdd-event-ledger-nvca" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option           | value   |
        | --inference-url  | /echo   |
        | --inference-port | 8000    |
        | --health-uri     | /health |
        | --health-port    | 8000    |
        | --health-timeout | PT30S   |
      # Delete the function and undeploy it when the scenario ends, even if the
      # deploy fails. The undeploy runs first and does nothing if nothing deployed.
      And the function selected by NVCF CLI is deleted at scenario teardown
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I export the function selected by NVCF CLI to environment variables "BDD_EL_NVCA_FUNCTION_ID" and "BDD_EL_NVCA_VERSION_ID"

      # NVCA's ICMSRequest events, then the Pod events of the function's pods.
      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I poll "GET" "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_NVCA_VERSION_ID}/stats" for up to 300 seconds until the HTTP response body contains "ICMSRequest."
      Then the HTTP status should be 200

      Given the HTTP request header "Authorization" is "Bearer ${BDD_EL_READ_KEY}"
      When I poll "GET" "http://events.localhost:8080/v3/ledger/namespace/${BDD_EL_NVCA_VERSION_ID}/stats" for up to 300 seconds until the HTTP response body contains "Pod."
      Then the HTTP status should be 200
      And the HTTP response JSON field "namespace" should equal "${BDD_EL_NVCA_VERSION_ID}"
      And the HTTP response body should contain "cluster_id="

      # The same stats through the CLI, with a key the CLI generated.
      When I successfully generate an Event Ledger API key with CLI options:
        | option       | value |
        | --expires-in | 1h    |
      And I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id ${BDD_EL_NVCA_VERSION_ID}"
      Then the command output should contain "Function version: ${BDD_EL_NVCA_VERSION_ID}"
      And the command output should contain "ICMSRequest."
      And the command output should contain "Pod."
      And the command output should contain "cluster_id="

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id ${BDD_EL_NVCA_VERSION_ID} --json"
      Then the command output should contain "ICMSRequest."

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stats get --function-version-id ${BDD_EL_NVCA_VERSION_ID} --event-filter NoSuchEvent"
      Then the command output should not contain "ICMSRequest."

      # A deployment id that no context carries has no events, which is not an error.
      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml stages events list --function-version-id ${BDD_EL_NVCA_VERSION_ID} --deployment-id 00000000-0000-4000-8000-000000000000"
      Then the command output should contain "No events found."
