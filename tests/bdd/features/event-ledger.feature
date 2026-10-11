@event-ledger @ncp-local @single-cluster
Feature: Event ledger records SIS deployment events on a running self-managed stack
  As a self-managed NVCF operator,
  I want the event ledger to be reachable, protected, and fed by SIS,
  so that I can read the lifecycle events of my function deployments.

  # This feature does not install anything. It expects the single-cluster
  # ncp-local stack to be running with addons.eventLedger.enabled=true and
  # nvcf-cli initialized for tests/bdd/fixtures/nvcf-cli-local.yaml. It checks
  # the SIS events only and does not depend on the NVCA OTel collector.
  Background:
    Given these environment variables are set:
      | name            |
      | NGC_API_KEY     |
      | SAMPLE_NGC_ORG  |
      | SAMPLE_NGC_TEAM |
      | REPO_ROOT       |
    And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"

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

  Scenario: SIS is configured to send events to the event ledger
    When I successfully run command "kubectl --context k3d-ncp-local --namespace sis get configmaps -o yaml"
    Then the command output should contain all:
      | text                                  |
      | ICMS_FNDS_MESSAGES_ENABLED: "true"    |
      | ICMS_FNDS_MESSAGES_V3_ENABLED: "true" |

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
    When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"
    And I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"

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

    Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
    And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
    And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
    And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
    When I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}"
    Then the HTTP status should be 204

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
    Then the HTTP status should be 200
    When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"
    And I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"

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

    Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
    And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
    And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
    And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
    When I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}"
    Then the HTTP status should be 204

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
    Then the HTTP status should be 200
    When I export the HTTP response JSON field "value" to environment variable "BDD_EL_WRITE_KEY"
    And I export the HTTP response JSON field "id" to environment variable "BDD_EL_WRITE_KEY_ID"

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

    Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
    And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
    And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
    And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
    When I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_WRITE_KEY_ID}"
    Then the HTTP status should be 204

  @function-lifecycle
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
    Then the HTTP status should be 200
    When I export the HTTP response JSON field "value" to environment variable "BDD_EL_READ_KEY"
    And I export the HTTP response JSON field "id" to environment variable "BDD_EL_READ_KEY_ID"

    When I successfully create function "bdd-event-ledger" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
      | option           | value   |
      | --inference-url  | /echo   |
      | --inference-port | 8000    |
      | --health-uri     | /health |
      | --health-port    | 8000    |
      | --health-timeout | PT30S   |
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

    Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
    And the HTTP request header "Key-Issuer-Service" is "nvcf-api"
    And the HTTP request header "Key-Issuer-Id" is "nvidia-event-ledger-ncp-service-id-ckozoh6f"
    And the HTTP request header "Key-Owner-Id" is "svc@nvcf-api.local"
    When I send a "DELETE" request to "http://api-keys.localhost:8080/v1/keys/${BDD_EL_READ_KEY_ID}"
    Then the HTTP status should be 204
