# Stargate TOML Configuration Plan

## Objective

Replace Stargate's large flat set of runtime CLI options with a structured
TOML configuration file.

Add this CLI option:

~~~
--config-file <PATH>
~~~

When the option is present, Stargate uses only the referenced file as its
effective configuration. Legacy CLI options and their environment fallbacks do
not contribute values. When the option is absent, Stargate retains the current
CLI behavior and logs one deprecation warning.

The new format groups related values into component-specific sections. A
section's presence selects an optional behavior or mode when practical. For
example, stargate_discovery.kubernetes_pods enables local pod enumeration and
pylon_transport.reverse selects reverse tunnel mode.

## Scope

This change includes:

- The stargate service binary and its runtime configuration conversion.
- A public typed Rust configuration model shared with internal callers.
- The llm-request-router Helm chart and its render tests.
- The stargate-bench Compose and Kubernetes renderers.
- Existing BDD fixtures that inspect Stargate arguments.
- Cargo and Bazel dependency lock updates.
- Compatibility tests for the legacy CLI.
- Removal of generic DNS-based Stargate peer discovery while retaining
  headless DNS enumeration of local Kubernetes pods.

This change does not include:

- Removing legacy CLI options, including --disable-dns-discovery.
- Layering or merging TOML, CLI, and environment configuration.
- Live configuration reload.
- Changing the stargate-k8s-router binary's CLI.
- Changing runtime behavior unrelated to configuration.

## Behavior contract

### Configuration source selection

1. If --config-file is present, load that file.
2. Do not use legacy CLI values or legacy environment fallbacks in file mode.
3. If the file cannot be read, parsed, resolved, or validated, fail startup.
4. Do not fall back to CLI after a config file was requested.
5. If --config-file is absent, parse the legacy CLI and preserve its supported
   Kubernetes pod discovery and self-only behavior.
6. Log one structured deprecation warning in legacy mode after telemetry is
   initialized.
7. Do not log the deprecation warning in file mode.

Legacy options may remain in a deployment during migration, but their values
must have no effect when --config-file is present. Normal help, version, and
unknown-argument handling should remain available.

The legacy --disable-dns-discovery flag remains accepted only in CLI fallback
mode. It has no TOML field. The generic non-Kubernetes DNS peer-discovery
branch is removed because Stargate no longer discovers other Stargate
clusters through DNS.

### File behavior

- Read the file once during startup.
- Resolve relative file paths against the directory containing the TOML file.
- Reject unknown keys and sections.
- Report the config file path and TOML source location when parsing fails.
- Report the full logical field path when validation fails.
- Never print secret contents in errors or startup logs.
- Use one source of defaults for both TOML and the legacy CLI adapter.

### Runtime environment values

Some Kubernetes values are not known when Helm renders a shared ConfigMap.
These include the pod name, namespace, pod IP, and addresses derived from the
pod IP.

Fields that need a per-process value accept either a literal or an explicit
environment reference:

~~~
id = "stargate-a"
id = { env = "POD_NAME" }
~~~

The Rust representation will use a Serde untagged enum similar to:

~~~
#[derive(Serialize, Deserialize)]
#[serde(untagged)]
enum ValueSource<T> {
    Literal(T),
    Environment { env: String },
}
~~~

Environment references are part of the TOML configuration, so the file remains
the authoritative source. A missing, empty, non-Unicode, or invalid value fails
startup with the field name and environment variable name.

The Helm chart will define derived address variables after POD_IP, for example:

~~~
- name: POD_IP
  valueFrom:
    fieldRef:
      fieldPath: status.podIP
- name: STARGATE_ADVERTISE_ADDR
  value: "$(POD_IP):50071"
~~~

Kubernetes supports ordered dependent environment values using the
$(VAR_NAME) form:
https://kubernetes.io/docs/tasks/inject-data-application/define-interdependent-environment-variables/

This avoids custom string interpolation inside TOML and prevents environment
values from changing TOML syntax.

## Dependency selection

Use direct TOML and Serde integration:

~~~
toml = { version = "=1.1.4", default-features = false, features = ["parse", "serde", "std"] }
serde_with = { version = "=3.22.0", default-features = false, features = ["macros", "std"] }
~~~

Reasons:

- toml provides direct Serde deserialization and source-positioned parse
  errors.
- serde_with provides readable adapters for durations and FromStr-backed
  types without handwritten serializers.
- Serde container attributes provide centralized defaults and strict unknown
  field rejection.
- Stargate needs one selected source, not a merged configuration stack.
- The config and Figment crates focus on layered sources, profiles, and
  merging. Those semantics conflict with the required file-or-CLI behavior.
- Both selected crates use licenses already allowed by the repository.

References:

- https://docs.rs/toml/1.1.4/toml/
- https://docs.rs/serde_with/3.22.0/serde_with/
- https://serde.rs/container-attrs.html
- https://serde.rs/field-attrs.html
- https://docs.rs/config/latest/config/
- https://docs.rs/figment/latest/figment/

The workspace should use exact versions, update Cargo.lock and
MODULE.bazel.lock, and verify license and NOTICE requirements.

## Configuration model

Add a public configuration module to the stargate library. The binary, legacy
CLI adapter, benchmark renderer, and tests must all use the same model.

Each struct should use deny_unknown_fields. Defaultable sections should use
Serde defaults backed by the same Default implementation used by the legacy
adapter. Do not use flatten because it weakens section boundaries and unknown
field diagnostics.

Durations are represented as integer milliseconds in TOML and as
std::time::Duration after deserialization. Addresses, URI values, header names,
nonzero counts, paths, and tunnel protocol values should become typed before
runtime construction.

### Sections

| Section | Contents and selection behavior |
| --- | --- |
| stargate_identity | Stable Stargate identity and advertised hostname rendering |
| stargate_identity.kubernetes | Kubernetes pod name and namespace |
| stargate_network | Stargate gRPC, model discovery, HTTP, and advertised addresses |
| process_lifecycle | Process readiness warm-up and shutdown drain timeout |
| pylon_registration | Pylon registration stream idle limits |
| stargate_discovery | WatchStargates remote endpoints and snapshot heartbeat behavior |
| stargate_discovery.kubernetes_pods | Presence enumerates local Stargate pods through a Kubernetes headless Service |
| stargate_discovery.kubernetes_pods.development_peer_forwarding | Presence enables development-only forwarding to another local Stargate pod |
| pylon_transport | Common Pylon gRPC and QUIC tunnel configuration |
| pylon_transport.direct | Direct-mode Pylon connection count and optional trust bundle |
| pylon_transport.reverse | Presence selects reverse Pylon tunnels and contains the listener and server identity |
| pylon_transport.tls | Common outbound Pylon QUIC verification policy |
| request_proxy | Namespace for policies applied while proxying inference requests |
| request_proxy.retry | Proxied request retry counts, replay limit, retry signal, and budget header |
| request_proxy.load_balancer | Presence selects a proxied request load-balancer config file |
| observability | OpenTelemetry service identity |
| observability.metrics | Prometheus listener and metric prefix |
| observability.tracing | Presence enables OTLP trace export |
| observability.tracing.access_token | Optional trace exporter token source |
| worker_authentication | Presence enables worker authentication |
| worker_authentication.bearer_token | Static bearer token source |
| worker_authentication.oauth2 | OAuth2 client credential source |

Section names identify the component or relationship they configure:

- stargate_discovery owns the Stargate entries and remote watch URLs published
  through WatchStargates, not the model discovery API.
- pylon_registration and pylon_transport identify the Pylon-facing settings.
- request_proxy identifies settings that affect proxied inference requests.
- process_lifecycle identifies process startup and shutdown behavior.
- Generic names such as identity, network, discovery, backend, proxy, and
  registration are not used as top-level sections.

### Section ownership

The ownership boundaries below are part of the schema contract. A setting
should move to another section only when its runtime owner changes.

#### Document root

The document root owns schema_version. It identifies the configuration schema
understood by the binary and gives future versions an explicit compatibility
boundary. It does not own ordinary runtime settings. All runtime settings
belong to a named component section.

#### stargate_identity

This section owns the identity Stargate presents to the rest of the system:

- id is the stable process or pod identity used in registration, discovery,
  logs, and self-only discovery.
- advertised_hostname_template defines the per-Stargate hostname used as the
  Pylon gRPC authority and reverse QUIC server name. It supports the existing
  pod_name and namespace placeholders.

This section does not own IP addresses or listening sockets. Those belong to
stargate_network. It also does not own the headless Service used to enumerate
local Stargate pods. That belongs to
stargate_discovery.kubernetes_pods.

#### stargate_identity.kubernetes

This section owns Kubernetes identity metadata:

- pod_name identifies the current pod.
- namespace identifies the pod's Kubernetes namespace.

Both values are required when the section is present. They may be literals or
explicit environment references. This section is required when
stargate_discovery.kubernetes_pods is present because pod enumeration needs a
stable pod identity and namespace.

This section does not own the Downward API declaration in a Pod manifest. The
Helm chart owns that declaration and the configuration only names the
environment values it consumes.

#### stargate_network

This section owns the primary Stargate service addresses:

- grpc_listen_addr is the backend-facing gRPC listener for Stargate watch and
  Pylon registration traffic.
- model_discovery_listen_addr is the frontend-facing gRPC listener for the
  model discovery API.
- http_listen_addr is the HTTP listener for inference proxy traffic and
  health probes.
- advertise_addr is the gRPC address published for this Stargate.

Listen addresses describe sockets bound by this process. advertise_addr
describes the address other processes should use and can differ from the bound
address.

This section does not own the reverse QUIC listener, which belongs to
pylon_transport.reverse. It does not own the metrics listener, which belongs
to observability.metrics.

#### process_lifecycle

This section owns process-level startup and shutdown timing:

- readiness_warmup_ms is the minimum process age before the readiness endpoint
  accepts request traffic. Zero disables the warm-up.
- shutdown_drain_timeout_ms is the grace period allowed for draining and
  shutdown tasks after termination begins.

This section does not own network request timeouts, registration idle
timeouts, or discovery polling intervals. Those belong to the component that
performs the operation.

#### pylon_registration

This section owns idle enforcement for Pylon registration update streams:

- update_idle_timeout_ms is the minimum heartbeat-aware idle timeout.
- update_max_idle_timeout_ms is the maximum heartbeat-aware idle timeout.

Zero retains the existing meaning of disabling idle enforcement. These values
govern Pylon registration streams, not Stargate peer watch heartbeats. Peer
watch heartbeat timing belongs to stargate_discovery.

#### stargate_discovery

This section owns the Stargate membership information published to Pylons
through WatchStargates:

- remote_watch_urls lists remote or cross-region WatchStargates seed
  endpoints that Pylons should watch explicitly.
- allow_insecure_remote_watch_http permits explicit plaintext HTTP seeds for
  development.
- watch_heartbeat_ms controls the maximum interval between unchanged
  WatchStargates snapshots.

Stargate does not resolve remote clusters through DNS. Remote clusters enter
the discovery graph only through explicit remote_watch_urls. Stargate includes
those URLs in WatchStargates responses and Pylons follow them.

The section remains useful without its kubernetes_pods child because remote
watch URLs and snapshot heartbeat behavior also apply when the local
WatchStargates entry contains only this Stargate.

This section does not own model discovery for frontend clients. The model
discovery API listener belongs to stargate_network. It does not own generic
DNS-based discovery because that behavior is removed.

#### stargate_discovery.kubernetes_pods

This section owns enumeration of local Stargate pods through a Kubernetes
headless Service:

- headless_service_dns_name is the headless Service DNS name queried for local
  pod SRV records.
- poll_interval_ms controls how often Stargate refreshes the local pod set.
- resolver_ttl_ms caps the resolver's positive cache lifetime.

Presence makes this Stargate resolve local pod membership and publish concrete
StargateInfo entries through WatchStargates. Pylons never query this DNS name.
Absence publishes only this Stargate as the local membership entry.

This section requires stargate_identity.kubernetes. There is no generic
non-Kubernetes DNS fallback. Other Stargate clusters are represented only by
the explicit remote_watch_urls in the parent section.

This section does not enable development forwarding by itself. That behavior
requires its development_peer_forwarding child section.

#### stargate_discovery.kubernetes_pods.development_peer_forwarding

This empty marker section owns the development-only peer forwarding mode.
Presence enables the forwarding resolver; absence disables it. It has no
fields because there are no independent settings for the mode.

It is valid only when stargate_identity.kubernetes and
stargate_discovery.kubernetes_pods are present. Production routing remains
owned by stargate-k8s-router or another supported load balancer.

#### pylon_transport

This section owns settings shared by direct and reverse communication with
Pylons:

- pylon_grpc_dial_uri is an optional load-balanced gRPC dial URI for Pylon
  registration traffic.
- tunnel_protocol selects the request stream protocol used by both ends.
- quic_connect_timeout_ms bounds outbound QUIC connection establishment and
  development peer relay connection attempts.
- quic_request_timeout_ms bounds each proxied request over an established
  tunnel.

The section does not select direct or reverse mode with a boolean. Its direct
and reverse child sections own mode-specific settings.

#### pylon_transport.direct

This section owns settings used only when Stargate directly opens QUIC
connections to Pylons:

- connections is the nonzero number of QUIC connections opened per Pylon.
- trust_bundle_path is an optional certificate bundle used to verify Pylon
  certificates.

If neither direct nor reverse is present, direct mode uses its defaults.
Explicit direct settings and pylon_transport.reverse are mutually exclusive.

This section does not own the common QUIC timeouts or verification bypass.
Those belong to pylon_transport and pylon_transport.tls.

#### pylon_transport.reverse

This section owns settings used only when Pylons connect back to Stargate:

- listen_addr is the local UDP address for incoming reverse QUIC tunnels.
- pylon_dial_addr is the optional address advertised to Pylons when it differs
  from the per-pod target.
- connect_timeout_ms bounds the wait for a reverse tunnel after registration.
- certificate_path and private_key_path provide the reverse listener's server
  identity.

Presence selects reverse mode. listen_addr is required. The certificate and
private key are optional as a pair; when both are absent, Stargate preserves
the current generated identity behavior. Supplying only one is invalid.

This section does not own the Pylon gRPC registration endpoint or common QUIC
request timeout. Those remain in pylon_transport.

#### pylon_transport.tls

This section owns TLS policy shared by outbound QUIC connections and
development peer relays:

- insecure_skip_verify controls certificate verification for outbound
  connections.

This boolean is a security policy, not a transport mode selector. Direct-mode
trust material belongs to pylon_transport.direct. Reverse listener identity
belongs to pylon_transport.reverse.

#### request_proxy

This parent section defines the ownership boundary for settings applied while
Stargate proxies inference requests. It currently has no direct fields. Retry
and load-balancing behavior live in focused child sections so transport,
discovery, and observability settings do not become mixed with request policy.

#### request_proxy.retry

This section owns retry and replay policy for proxied inference requests:

- max_connect_retries limits reconnect attempts on the direct proxy path.
- max_request_retries limits retries for explicitly retryable upstream
  responses.
- max_replay_body_bytes limits request-body buffering for retry replay.
- require_pylon_retry_signal controls whether an upstream response must carry
  Pylon's explicit retry signal.
- request_budget_header names the header that carries the remaining request
  budget in milliseconds. An empty value retains the existing disabled
  behavior.

This section does not own QUIC connection or per-request timeouts. Those
belong to pylon_transport because they govern the tunnel operation itself.

#### request_proxy.load_balancer

This section owns selection of an external load-balancer policy file:

- config_path points to the load-balancer JSON configuration.

Presence tells Stargate to load the file. Absence preserves the built-in
power-of-n behavior with the built-in algorithms available per request.
Relative paths are resolved from the TOML file's directory.

This section owns the reference, not creation or mounting of the JSON file.
Helm and benchmark deployment code own those artifacts.

#### observability

This section owns the identity shared by Stargate telemetry:

- service_name supplies the OpenTelemetry service.name resource and tracer
  name.

Metrics and tracing have separate child sections because their listeners,
export destinations, and authentication are independent.

#### observability.metrics

This section owns Prometheus metric serving:

- listen_addr is the HTTP socket used by the metrics endpoint.
- prefix is prepended to Stargate metric names.

Omitting explicit values uses the existing listener and prefix defaults.
Metrics remain enabled to preserve current behavior. This section does not own
the main HTTP proxy listener, which belongs to stargate_network.

#### observability.tracing

This section owns OTLP trace export:

- endpoint is the OTLP gRPC collector endpoint.

Presence enables trace export. Absence disables it. The OpenTelemetry service
identity remains in the parent observability section so it is not coupled to a
specific exporter.

#### observability.tracing.access_token

This section owns the optional source for the trace export access token:

- secrets_path identifies the JSON secrets file.
- json_path identifies the nested JSON key containing the token.

The token value is read only when tracing is enabled and is never stored in the
deserialized configuration or logged. Absence preserves unauthenticated trace
export and its existing warning.

This source is intentionally separate from worker authentication credentials.
The two consumers may use the same file, but neither owns the other's secret
configuration.

#### worker_authentication

This section owns authentication of proxied requests against the worker
authentication gRPC service:

- endpoint is the worker authentication service URI.

Presence enables the gRPC authenticator. Absence preserves the open
authenticator. The section may omit a credential child to preserve the current
unauthenticated client behavior.

This section does not own authentication for the OTLP exporter. That belongs
to observability.tracing.access_token.

#### worker_authentication.bearer_token

This section owns a static bearer token source for worker authentication:

- secrets_path identifies the JSON secrets file.
- json_path is an array of JSON object keys leading to the token.

Using an array keeps key boundaries explicit and permits keys that contain
dots. The legacy dot-separated CLI path is split into this representation by
the compatibility adapter.

This section is mutually exclusive with worker_authentication.oauth2.

#### worker_authentication.oauth2

This section owns OAuth2 client-credential token acquisition for worker
authentication:

- provider_host is the OAuth2 service host used for its token endpoint.
- secrets_path identifies the JSON file containing the client ID and secret.

The existing llm:check_worker scope remains a runtime constant rather than a
configuration option. This section is mutually exclusive with
worker_authentication.bearer_token.

### Presence-based selections

- If stargate_discovery.kubernetes_pods is absent, publish only this Stargate
  as the local WatchStargates membership entry.
- If stargate_discovery.kubernetes_pods is present, enumerate local Stargate
  pods through the configured Kubernetes headless Service.
- stargate_discovery.kubernetes_pods requires
  stargate_identity.kubernetes.
- If stargate_discovery.kubernetes_pods.development_peer_forwarding is present,
  enable the development-only forwarding resolver.
- Generic non-Kubernetes DNS discovery is not supported.
- If pylon_transport.reverse is present, use reverse Pylon connectivity.
- If pylon_transport.reverse is absent, use direct Pylon connectivity.
- pylon_transport.direct and pylon_transport.reverse cannot both be present.
- If request_proxy.load_balancer is present, load its referenced JSON file.
- If observability.tracing is present, enable OTLP export.
- If worker_authentication is present, enable worker authentication.
- worker_authentication.bearer_token and worker_authentication.oauth2 cannot
  both be present.

Boolean values remain appropriate for actual policy toggles, such as allowing
insecure remote HTTP, skipping TLS verification, and requiring Pylon's retry
signal. They must not be used to select a configuration mode that can be
represented by section presence.

## Full TOML example

This is a complete valid example for a Kubernetes deployment using local pod
enumeration, reverse tunnels, TLS, a load-balancer file, remote watch seeds,
authenticated tracing, metrics, retries, and OAuth2 worker authentication.

The Helm chart supplies the referenced environment variables. Static
deployments can replace each environment reference with a literal string.

~~~toml
schema_version = 1

[stargate_identity]
id = { env = "POD_NAME" }
advertised_hostname_template = "{pod_name}.llm-request-router-headless.{namespace}.svc.cluster.local"

[stargate_identity.kubernetes]
pod_name = { env = "POD_NAME" }
namespace = { env = "POD_NAMESPACE" }

[stargate_network]
grpc_listen_addr = "0.0.0.0:50071"
model_discovery_listen_addr = "0.0.0.0:50073"
http_listen_addr = "0.0.0.0:8000"
advertise_addr = { env = "STARGATE_ADVERTISE_ADDR" }

[process_lifecycle]
readiness_warmup_ms = 60000
shutdown_drain_timeout_ms = 30000

[pylon_registration]
update_idle_timeout_ms = 60000
update_max_idle_timeout_ms = 300000

[stargate_discovery]
remote_watch_urls = [
  "https://stargate-watch.us-west.example.com",
  "https://stargate-watch.us-east.example.com",
]
allow_insecure_remote_watch_http = false
watch_heartbeat_ms = 5000

[stargate_discovery.kubernetes_pods]
headless_service_dns_name = "llm-request-router-headless.nvcf.svc.cluster.local"
poll_interval_ms = 1000
resolver_ttl_ms = 1000

[pylon_transport]
pylon_grpc_dial_uri = "https://stargate-k8s-router.nvcf.svc.cluster.local:50071"
tunnel_protocol = "raw-quic"
quic_connect_timeout_ms = 2000
quic_request_timeout_ms = 30000

[pylon_transport.reverse]
listen_addr = "0.0.0.0:50072"
pylon_dial_addr = { env = "STARGATE_REVERSE_PYLON_DIAL_ADDR" }
connect_timeout_ms = 10000
certificate_path = "/var/run/stargate-tls/tls.crt"
private_key_path = "/var/run/stargate-tls/tls.key"

[pylon_transport.tls]
insecure_skip_verify = false

[request_proxy.retry]
max_connect_retries = 2
max_request_retries = 2
max_replay_body_bytes = 67108864
require_pylon_retry_signal = true
request_budget_header = "x-stargate-max-wait-ms"

[request_proxy.load_balancer]
config_path = "/etc/llm-request-router/lb-config.json"

[observability]
service_name = "stargate"

[observability.metrics]
listen_addr = "0.0.0.0:9090"
prefix = "stargate_"

[observability.tracing]
endpoint = "https://otel-collector.nvcf.svc.cluster.local:4317"

[observability.tracing.access_token]
secrets_path = "/var/run/secrets/stargate/secrets.json"
json_path = ["tracingAccessToken"]

[worker_authentication]
endpoint = "http://llm-gateway.nvcf.svc.cluster.local:50051"

[worker_authentication.oauth2]
provider_host = "https://oauth.example.com"
secrets_path = "/var/run/secrets/stargate/secrets.json"
~~~

### Direct tunnel alternative

For direct mode, remove pylon_transport.reverse and replace it with:

~~~toml
[pylon_transport.direct]
connections = 1
trust_bundle_path = "/var/run/stargate-tls/ca.crt"
~~~

The legacy adapter maps --tls-cert-path to trust_bundle_path in direct mode.

### Self-only discovery alternative

For self-only local membership, remove
stargate_discovery.kubernetes_pods and its child sections. Keep the top-level
stargate_discovery section if remote watch URLs or snapshot heartbeat settings
are needed.

### Development-only peer forwarding

To enable development-only peer forwarding, add this empty section:

~~~toml
[stargate_discovery.kubernetes_pods.development_peer_forwarding]
~~~

It is valid only when stargate_identity.kubernetes and
stargate_discovery.kubernetes_pods are both present. Production deployments
must continue to use stargate-k8s-router or another supported load balancer.

### Static bearer token alternative

To use a token from a JSON file, remove worker_authentication.oauth2 and
replace it with:

~~~toml
[worker_authentication.bearer_token]
secrets_path = "/var/run/secrets/stargate/secrets.json"
json_path = ["authToken"]
~~~

An absent credential subsection preserves the current unauthenticated
worker-auth client behavior.

### Unauthenticated tracing alternative

Remove observability.tracing.access_token to export traces without an access
token. Stargate should preserve the current warning for unauthenticated trace
export.

## Legacy mapping

The legacy adapter must account for every current option. All retained runtime
settings map into the typed model; --disable-dns-discovery remains a
legacy-only source selector.

| Legacy option | TOML destination |
| --- | --- |
| --stargate-id | stargate_identity.id |
| --listen-addr | stargate_network.grpc_listen_addr |
| --model-discovery-listen-addr | stargate_network.model_discovery_listen_addr |
| --http-listen-addr | stargate_network.http_listen_addr |
| --advertise-addr | stargate_network.advertise_addr |
| --stargate-discovery-dns-name | stargate_discovery.kubernetes_pods.headless_service_dns_name when local pod enumeration is active |
| --remote-stargate-url | stargate_discovery.remote_watch_urls |
| --allow-insecure-remote-watch-http | stargate_discovery.allow_insecure_remote_watch_http |
| --grpc-pylon-dial-addr | pylon_transport.pylon_grpc_dial_uri |
| --advertised-hostname-template | stargate_identity.advertised_hostname_template |
| --pod-name | stargate_identity.kubernetes.pod_name |
| --pod-namespace | stargate_identity.kubernetes.namespace |
| --disable-dns-discovery | Legacy-only selector for self-only local membership; no TOML field |
| --enable-dev-peer-forwarding | Presence of stargate_discovery.kubernetes_pods.development_peer_forwarding |
| --dns-poll-ms | stargate_discovery.kubernetes_pods.poll_interval_ms |
| --dns-resolver-ttl-ms | stargate_discovery.kubernetes_pods.resolver_ttl_ms |
| --watch-heartbeat-ms | stargate_discovery.watch_heartbeat_ms |
| --registration-update-idle-timeout-ms | pylon_registration.update_idle_timeout_ms |
| --registration-update-max-idle-timeout-ms | pylon_registration.update_max_idle_timeout_ms |
| --shutdown-drain-timeout-ms | process_lifecycle.shutdown_drain_timeout_ms |
| --readiness-warmup-ms | process_lifecycle.readiness_warmup_ms |
| --quic-connect-timeout-ms | pylon_transport.quic_connect_timeout_ms |
| --quic-request-timeout-ms | pylon_transport.quic_request_timeout_ms |
| --direct-quic-connections | pylon_transport.direct.connections |
| --proxy-max-connect-retries | request_proxy.retry.max_connect_retries |
| --proxy-max-request-retries | request_proxy.retry.max_request_retries |
| --proxy-max-replay-body-bytes | request_proxy.retry.max_replay_body_bytes |
| --proxy-require-pylon-retry-signal | request_proxy.retry.require_pylon_retry_signal |
| --proxy-retry-budget-header | request_proxy.retry.request_budget_header |
| --tls-cert-path | pylon_transport.direct.trust_bundle_path in direct mode or pylon_transport.reverse.certificate_path in reverse mode |
| --tls-key-path | pylon_transport.reverse.private_key_path in reverse mode |
| --quic-insecure | pylon_transport.tls.insecure_skip_verify |
| --lb-config-path | request_proxy.load_balancer.config_path |
| --otel-endpoint | observability.tracing.endpoint |
| --otel-service-name | observability.service_name |
| --metrics-port | observability.metrics.listen_addr using 0.0.0.0 and the supplied port |
| --metrics-prefix | observability.metrics.prefix |
| --backend-connectivity=direct | Absence of pylon_transport.reverse |
| --backend-connectivity=reverse | Presence of pylon_transport.reverse |
| --reverse-tunnel-listen-addr | pylon_transport.reverse.listen_addr |
| --reverse-tunnel-pylon-dial-addr | pylon_transport.reverse.pylon_dial_addr |
| --reverse-tunnel-connect-timeout-ms | pylon_transport.reverse.connect_timeout_ms |
| --tunnel-protocol | pylon_transport.tunnel_protocol |
| --worker-auth-endpoint | worker_authentication.endpoint |
| --secrets-path | Worker auth credential source and tracing access token source when applicable |
| --secrets-json-path | worker_authentication.bearer_token.json_path |
| --oauth2-provider-host | worker_authentication.oauth2.provider_host |

The adapter must preserve the legacy shared secrets-path behavior. In the TOML
model, tracing and worker authentication use independent, properly grouped
secret sources.

The adapter also retains --disable-dns-discovery for compatibility:

- If the flag is present, select self-only local membership.
- If the flag is absent and both pod name and namespace are available, build
  stargate_discovery.kubernetes_pods from the legacy DNS options.
- If the flag is absent and Kubernetes pod identity is unavailable, select
  self-only local membership instead of invoking generic DNS discovery.
- If only one Kubernetes identity value is present, fail validation rather
  than silently selecting a different discovery implementation.

The flag is omitted from the TOML schema and remains covered by the single
legacy CLI deprecation warning.

## Validation rules

Validation occurs after deserialization and environment resolution, before any
listener is bound or external connection is opened.

Required rules include:

- schema_version must equal 1.
- stargate_identity.id must not be empty.
- Required listen and advertised addresses must parse as socket addresses.
- Remote watch URLs must use explicit http or https schemes.
- Plain HTTP remote watch URLs require
  stargate_discovery.allow_insecure_remote_watch_http.
- Kubernetes pod poll intervals and direct connection counts must be nonzero.
- Registration timeout value 0 retains its existing disabled meaning.
- stargate_identity.kubernetes requires both pod_name and namespace.
- stargate_discovery.kubernetes_pods requires
  stargate_identity.kubernetes.
- headless_service_dns_name must be nonempty when Kubernetes pod enumeration
  is configured.
- Development peer forwarding requires Kubernetes identity and local pod
  enumeration.
- Remote Stargate clusters must use explicit remote_watch_urls; no generic DNS
  peer-discovery configuration is accepted.
- pylon_transport.direct and pylon_transport.reverse are mutually exclusive.
- pylon_transport.reverse requires listen_addr.
- Reverse TLS certificate and private key paths must be supplied together.
- A direct trust bundle is valid only in pylon_transport.direct.
- Pylon gRPC dial values must use explicit http or https schemes.
- Proxy retry budget headers must be empty or valid HTTP header names.
- worker_authentication.bearer_token and worker_authentication.oauth2 are
  mutually exclusive.
- OAuth2 worker authentication requires a secrets file.
- JSON key paths must contain at least one nonempty component.
- All referenced files should fail with contextual path errors when read.

Configuration parsing and validation must not bind sockets, read credentials,
initialize clients, or otherwise perform runtime side effects.

## Implementation steps

### 1. Add dependencies and the typed model

- Add exact toml and serde_with dependencies to the Stargate Cargo workspace.
- Add src/config.rs to the stargate library and export its public types.
- Derive Serialize, Deserialize, Debug, Clone, and PartialEq where useful.
- Redact or provide custom Debug behavior for secret-bearing configuration.
- Implement defaults once in the typed model.
- Add typed environment value resolution.
- Add config validation and path resolution.
- Update Cargo.lock and MODULE.bazel.lock.

### 2. Add config source selection

- Add --config-file <PATH> to the binary CLI.
- Make legacy required options conditional on config-file being absent.
- Resolve the source before constructing runtime settings.
- In file mode, load only TOML-derived values.
- In legacy mode, convert the existing Args values into StargateConfig.
- Retain --disable-dns-discovery only in LegacyArgs and map it to self-only
  local membership.
- Require --stargate-discovery-dns-name only when the legacy inputs select
  Kubernetes pod enumeration.
- Keep --help and --version behavior.
- Add the one-time legacy deprecation warning after telemetry initialization.

### 3. Refactor startup conversion

- Change startup helpers to accept StargateConfig or focused nested config
  structures instead of the flat Args type.
- Delete the generic DnsDiscovery implementation, its startup branch, and its
  tests.
- Retain SelfOnlyDiscovery for configurations without local pod enumeration.
- Rename HeadlessDnsDiscovery and HeadlessDnsDiscoveryConfig to names that
  state their actual responsibility, such as KubernetesPodDiscovery and
  KubernetesPodDiscoveryConfig.
- Make the runtime carry optional Kubernetes pod discovery settings instead of
  an always-required generic stargate_discovery_dns_name.
- Keep remote_watch_urls independent from local pod enumeration. Stargate
  publishes those explicit URLs for Pylons and never resolves remote clusters
  through DNS.
- Preserve existing listener binding, pod enumeration, TLS reload, retry,
  authentication, and shutdown behavior.
- Preserve legacy direct-mode handling of --tls-key-path while mapping the
  meaningful certificate value into pylon_transport.direct.trust_bundle_path.
- Keep file parsing and pure validation separate from runtime side effects.
- Log the selected source and safe effective settings.

Primary source files:

- src/libraries/rust/stargate/crates/stargate/src/main.rs
- src/libraries/rust/stargate/crates/stargate/src/main/startup.rs
- src/libraries/rust/stargate/crates/stargate/src/lib.rs
- src/libraries/rust/stargate/crates/stargate/src/config.rs

### 4. Migrate the Helm chart

- Render stargate.toml in a ConfigMap.
- Mount it read-only under /etc/llm-request-router.
- Invoke the main Stargate container with only:

~~~
--config-file=/etc/llm-request-router/stargate.toml
~~~

- Define POD_NAME, POD_NAMESPACE, POD_IP, and derived address variables in
  dependency order.
- Remove llmRequestRouter.discovery.disableDnsDiscovery from chart values. It
  does not have a TOML equivalent.
- Render stargate_discovery.kubernetes_pods for StatefulSets and for
  Deployments that use the EndpointSlice backend router.
- Omit stargate_discovery.kubernetes_pods for the supported single-replica
  Deployment topology that publishes only itself.
- Continue mounting the load-balancer JSON and reference it from TOML.
- Keep stargate-k8s-router arguments unchanged.
- Update checked-in generated manifests.
- Update render tests to extract and validate TOML instead of grepping
  Stargate CLI flags.
- Continue asserting backend-router CLI flags separately.

Primary deployment files:

- deploy/helm/llm-request-router/llm-request-router/templates/deployment.yaml
- deploy/helm/llm-request-router/llm-request-router/values.yaml
- deploy/helm/llm-request-router/bin/manifest.yaml
- deploy/helm/llm-request-router/scripts/check-multi-replica-render.sh
- deploy/helm/llm-request-router/scripts/check-pki-render.sh
- deploy/helm/llm-request-router/scripts/check-backend-router-render.sh

### 5. Migrate benchmark and integration callers

- Make stargate-bench construct and serialize StargateConfig.
- Mount the serialized file for Compose runs.
- Add the serialized file to a ConfigMap for Kubernetes runs.
- Use self-only local membership for the single-Stargate Compose topology.
- Use stargate_discovery.kubernetes_pods for the Kubernetes StatefulSet
  topology.
- Replace generated Stargate flags with --config-file.
- Keep the benchmark's load-balancer JSON as a separate mounted file.
- Update benchmark unit tests to assert the parsed config and mount.
- Update multiregion BDD fixtures that currently expect
  --remote-stargate-url.

Primary benchmark files:

- src/libraries/rust/stargate/crates/stargate-bench/src/orchestrator.rs
- src/libraries/rust/stargate/crates/stargate-bench/src/k8s/render.rs
- src/libraries/rust/stargate/crates/stargate-bench/src/k8s/tests.rs

### 6. Update user-facing material

- Add a complete configuration reference and example.
- Mark legacy flags as deprecated in CLI help.
- Explain file precedence, relative paths, environment references, and
  presence-based mode selection.
- Update existing architecture or sequence diagrams if they describe the
  startup configuration flow.

Repository guidance requires explicit approval before reading existing
Markdown documentation. Obtain that approval before inspecting or editing
those documentation files.

## Test plan

### Configuration unit tests

- Parse a minimal direct-mode file.
- Parse the complete reverse-mode example in this plan.
- Parse self-only local membership when
  stargate_discovery.kubernetes_pods is absent.
- Serialize and deserialize a constructed StargateConfig.
- Verify every retained setting default matches legacy CLI behavior.
- Reject unknown top-level, section, and nested keys.
- Report useful file, line, column, and logical field context.
- Reject unsupported schema versions.
- Resolve literal and environment-backed values.
- Reject missing, empty, non-Unicode, and invalid environment values.
- Resolve relative paths from the config file directory.
- Validate every section-presence rule.
- Reject obsolete generic DNS peer-discovery fields.
- Validate remote URLs, TLS combinations, retry headers, auth sources, and
  nonzero values.

### CLI compatibility tests

- --config-file works without legacy required options.
- Valid contradictory legacy values do not affect file mode.
- Legacy environment variables do not affect file mode.
- A missing or invalid config file never falls back to CLI.
- The no-file path retains current parsing, defaults, and environment support.
- --disable-dns-discovery remains accepted in legacy mode and selects
  self-only local membership.
- Legacy Kubernetes identity plus a headless Service DNS name selects local
  pod enumeration.
- Legacy inputs without Kubernetes identity select self-only membership and
  never invoke generic DNS discovery.
- A partial legacy Kubernetes identity is rejected.
- Legacy mode emits exactly one deprecation warning.
- File mode emits no legacy deprecation warning.
- Help and version output remain usable.
- Unknown CLI options still fail.

### Runtime startup tests

- Move runtime conversion coverage from flat Args to StargateConfig.
- Keep adapter tests proving legacy values produce the same effective config.
- Verify direct, reverse, self-only, Kubernetes pod enumeration, forwarding,
  TLS, retry, auth, tracing, and load-balancer construction.
- Remove generic DnsDiscovery unit tests and retain focused tests for
  KubernetesPodDiscovery and SelfOnlyDiscovery.
- Verify parsing and validation happen before listener binding.

### Deployment and benchmark tests

- Rendered Helm resources contain valid TOML.
- The main container has one --config-file argument and the expected mount.
- The rendered TOML reflects values overrides.
- Dynamic pod values use explicit environment references.
- StatefulSet and backend-router Deployment renders include
  stargate_discovery.kubernetes_pods.
- Single-replica Deployment renders omit
  stargate_discovery.kubernetes_pods.
- The removed discovery.disableDnsDiscovery Helm value is no longer accepted
  or rendered.
- Backend-router flags remain unchanged.
- Compose and Kubernetes benchmark output mounts a valid generated file.
- Multiregion remote watch configuration appears in TOML.

## Verification

Run from the repository root as applicable:

~~~
cargo fmt --all -- --check
cargo test -p stargate
cargo test -p stargate-bench
cargo clippy -p stargate -p stargate-bench --all-targets --all-features -- -D warnings
bazel test //src/libraries/rust/stargate/crates/stargate:stargate_test
bazel build //src/libraries/rust/stargate/crates/stargate:stargate
bazel build //src/libraries/rust/stargate/crates/stargate-bench:stargate-bench
make -C deploy/helm/llm-request-router lint
git diff --check HEAD
~~~

Also run the chart render scripts and the relevant multiregion BDD tests.
Regenerate any checked-in Cargo, Bazel, Helm, or generated manifest artifacts
using their repository-native generators.

## Rollout

1. Land the typed model, config-file path, compatibility adapter, and tests in
   one change.
2. Migrate all first-party Stargate invocations in the same change so the new
   path receives production and test coverage immediately.
3. Remove generic DnsDiscovery in the same change so no unsupported
   cross-cluster DNS behavior survives behind the new model.
4. Retain the legacy CLI, --disable-dns-discovery, and their tests during the
   deprecation period.
5. Track removal of legacy options as separate follow-up work after downstream
   users have migrated.

## Completion criteria

- A config-file-only Stargate invocation starts successfully.
- File mode has no effective dependency on legacy CLI or legacy environment
  values.
- Legacy mode preserves supported Kubernetes pod and self-only discovery,
  retains --disable-dns-discovery, and warns once.
- Generic non-Kubernetes DNS peer discovery and its tests are removed.
- Every retained runtime setting has a typed TOML destination;
  --disable-dns-discovery remains legacy-only.
- Related values are nested together.
- Mode booleans are replaced by section presence where practical.
- stargate_discovery.kubernetes_pods is the only DNS-backed local membership
  source, and remote clusters require explicit remote_watch_urls.
- Unknown keys and invalid cross-section combinations fail clearly.
- Helm, benchmark, and BDD callers use the TOML path.
- Direct and reverse mode behavior remains covered.
- Cargo, Bazel, Helm, and integration verification passes.
- No generated artifacts, documentation, or compatibility tests are left
  stale.
