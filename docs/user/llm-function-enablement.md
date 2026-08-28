# LLM Function Enablement

Enable the LLM addon before creating or invoking functions with
`functionType: "LLM"` through the LLM invocation route. The addon deploys the
LLM API Gateway and LLM request router, creates the external LLM invocation
route, and configures worker pods to use the `pylon` sidecar for model-aware
routing.

For LLM function payload shape and invocation examples, see
[Function Creation](./function-creation.md) and
[LLM Gateway](./llm-gateway.md).
For request-router deployment, trusted headers, and rollout validation, see
[LLM Request Router Load Balancing](./llm-request-router-load-balancing.md).

## When to Enable

Enable the LLM addon when NVCF should route OpenAI-compatible requests by
function and model through `llm.invocation.<domain>`. The gateway extracts the
function ID from the OpenAI `model` field, applies LLM-specific validation and
rate limits, and sends the request through the LLM request router.

Standard HTTP, gRPC, and LLS functions do not require this addon, even when a
container exposes paths such as `/v1/chat/completions`, `/v1/responses`, or
`/v1/embeddings`.

When enabled, the stack creates:

- `llm-api-gateway` in the `nvcf` namespace.
- `llm-request-router` in the `nvcf` namespace.
- The `llm.invocation.<domain>` HTTPRoute when Gateway API ingress is enabled.
- LLM worker pods with a `pylon` sidecar that forwards requests to the function
  container on the configured `inferencePort`.

## Production TLS Configuration

Production deployments secure two independent worker-to-router paths:

| Path | TLS identity | Pylon trust input |
| --- | --- | --- |
| gRPC registration and watches | The external HTTPS dial hostname, normally an NLB DNS name | Enabled system and public roots, augmented by the gRPC override or the reverse-mode QUIC trust bundle |
| QUIC reverse tunnel | The advertised request-router pod hostname | `--tls-cert-path` for the existing QUIC identity or trust input |

The current production gRPC design uses a public ACM certificate on the NLB
TLS listener. The NLB certificate covers the public gRPC dial hostname. An
unmanaged Pylon process, or one using NVCA system trust mode, can leave both
trust paths unset so Tonic uses its enabled system and public roots. In reverse
mode, Pylon reuses `--tls-cert-path` as gRPC trust when the gRPC-specific
override is unset. NVCA bundle mode points both options at its merged CA file.

`--grpc-tls-ca-cert-path` or `STARGATE_GRPC_TLS_CA_CERT_PATH` is the optional
gRPC-specific override. A configured custom bundle augments the enabled system
and public roots. It does not replace them. In direct mode,
`--tls-cert-path` is the Pylon QUIC server identity and is never treated as a
gRPC CA bundle.

A private-CA certificate on the NLB listener is a supported alternative. Give
Pylon the CA bundle with `--grpc-tls-ca-cert-path <path>` or
`STARGATE_GRPC_TLS_CA_CERT_PATH=<path>`. The input is a PEM bundle containing
the CA certificates required by the selected certificate chain, including a
private root when applicable. It does not disable certificate or hostname
verification. Pylon reads the file once during startup. An unreadable path
fails startup with the configured path in the error. Invalid PEM, untrusted
chains, and hostname mismatches prevent the gRPC watch and registration
connections. Replace or rotate the bundle with a rolling restart of the worker
pods. The `pylonGrpcDialAddress` override must be an explicit `https://` URI
when a custom CA is configured. The separate
`global.workerEndpoints.llmRequestRouterAddress` input remains a scheme-less
`host:port` initial address.

For gRPC, TLS SNI and hostname verification always use the external HTTPS dial
hostname. After discovery, Pylon separately sends the concrete request-router
pod hostname as the HTTP/2 `:authority` so `stargate-k8s-router` can select the
pod. The authority override does not become the TLS SNI. A gRPC NLB leaf
certificate therefore needs the public NLB DNS SAN, not Kubernetes-internal
pod SANs.

The QUIC identity remains separate. The request router presents a certificate
issued by cert-manager, or one you issue and supply in a pre-created Secret.
For a single-cluster deployment, workers dial
`llm-request-router.nvcf.svc.cluster.local:50071`. For remote workers, the
backend router preserves the advertised request-router pod hostname as the
QUIC SNI while it sends traffic through an external UDP endpoint. The QUIC
certificate must cover that advertised hostname. The default wildcard SAN is
`*.llm-request-router-headless.nvcf.svc.cluster.local`. It does not need the
external UDP load-balancer hostname. Do not reuse the gRPC NLB leaf certificate
as the QUIC leaf certificate.

### Managed OpenBao issuer

The default managed configuration installs `ClusterIssuer/nvcf-openbao-pki`
through the `helm-nvcf-pki` chart. It uses the stack's OpenBao service as the
signing backend.

Add the following values to the Helmfile environment:

```yaml
certManager:
  enabled: true

openbao:
  enabled: true

addons:
  llm:
    enabled: true
    pki:
      enabled: true
      allowedDomains: nvcf.svc.cluster.local
      dnsNames:
        - llm-request-router.nvcf.svc.cluster.local
        - "*.llm-request-router-headless.nvcf.svc.cluster.local"
    gateway:
      replicaCount: 2
    requestRouter:
      replicaCount: 2
```

The stable Service SAN covers the in-cluster Service name. The wildcard SAN
covers the pod-specific headless names advertised through the backend-router
path.

To use a custom managed `ClusterIssuer`, set both the issuer identity and the
management override:

```yaml
addons:
  llm:
    pki:
      issuerKind: ClusterIssuer
      issuerName: custom-openbao-pki
      clusterIssuer:
        enabled: true
```

Issuer management supports only `issuerKind: ClusterIssuer`. OpenBao must be
enabled when the stack manages the issuer.

### External issuer

Set `clusterIssuer.enabled: false` when the issuer is managed outside this
stack. For an external `ClusterIssuer`, use:

```yaml
addons:
  llm:
    enabled: true
    pki:
      enabled: true
      issuerKind: ClusterIssuer
      issuerName: external-llm-pki
      clusterIssuer:
        enabled: false
      dnsNames:
        - llm-request-router.nvcf.svc.cluster.local
        - "*.llm-request-router-headless.nvcf.svc.cluster.local"
```

For a namespaced issuer, create the `Issuer` in the `nvcf` namespace and use:

```yaml
addons:
  llm:
    enabled: true
    pki:
      enabled: true
      issuerKind: Issuer
      issuerName: external-llm-pki
      clusterIssuer:
        enabled: false
      dnsNames:
        - llm-request-router.nvcf.svc.cluster.local
        - "*.llm-request-router-headless.nvcf.svc.cluster.local"
```

The external issuer must allow the requested DNS names and issue a server
certificate from the root CA distributed to the compute planes. You can set
`openbao.enabled: false` when no other stack component requires OpenBao.

`addons.llm.pki.allowedDomains` constrains the managed OpenBao signing role
only. The stack ignores it for an external issuer. Apply the equivalent
constraint in the external issuer's own configuration.

When `addons.llm.enabled` is `true`, the stack defaults
`global.workerEndpoints.llmRequestRouterAddress` to
`llm-request-router.nvcf.svc.cluster.local:50071`. Colocated workers require no
additional configuration. For a split control-plane and compute-plane
deployment, override this value with a host and port that worker pods can
reach.

The stack maps the configured or default address to
`api.remoteConfig.configData.nvcf.llm-request-router.worker-address`. The NVCF
API then includes the address in LLM worker configuration. Do not configure
the worker address under `api.env`. When the LLM addon is disabled, the stack
does not pass a staged endpoint to the API chart.

### Remote compute clusters and regions

One self-managed control plane can serve LLM workers in separate GPU clusters
or regions when every worker cluster has routable DNS and network paths to both
backend-router endpoints.

| Path | Worker-facing values | Gateway route and backend | Terminating component |
| --- | --- | --- | --- |
| gRPC registration and watches | `global.workerEndpoints.llmRequestRouterAddress` for the initial connection, then `pylonGrpcDialAddress` | `llmGrpc` HTTPS listener and `GRPCRoute` to `llm-request-router-backend-router:50071` over h2c | The Gateway terminates TLS. The backend router selects a request-router pod from the HTTP/2 authority and proxies the stream. |
| Reverse inference tunnel | `pylonReverseTunnelDialAddress` | `llmQuic` UDP listener and `UDPRoute` to `llm-request-router-backend-router:50072` | The backend router terminates the worker-facing QUIC connection, selects a request-router pod from SNI, and forwards the tunnel. |

Configure distinct HTTPS and UDP endpoints when the infrastructure uses separate
Gateways or load balancers:

```yaml
global:
  workerEndpoints:
    llmRequestRouterAddress: https://llm-grpc.example.com:50071

addons:
  llm:
    enabled: true
    pki:
      # Include the external gRPC suffix when using the stack-managed issuer.
      allowedDomains: cluster.local,example.com
    requestRouter:
      grpcTls:
        enabled: true
        mode: certManager
        secretName: llm-grpc-tls
        dnsNames:
          - llm-grpc.example.com
        issuerRef:
          kind: ClusterIssuer
          name: nvcf-openbao-pki
      backendRouter:
        pylonGrpcDialAddress: https://llm-grpc.example.com:50071
        pylonReverseTunnelDialAddress: llm-quic.example.com:50072

ingress:
  gatewayApi:
    routes:
      llmWorker:
        enabled: true
        backend:
          namespace: nvcf
    gateways:
      llmGrpc:
        name: llm-grpc-gateway
        namespace: envoy-gateway
        listenerName: llm-grpc
      llmQuic:
        name: llm-quic-gateway
        namespace: envoy-gateway
        listenerName: llm-quic
```

Set both `pylonGrpcDialAddress` and `pylonReverseTunnelDialAddress`, or omit
both to use the in-cluster backend-router Service. Helmfile rendering rejects a
partial override. The gRPC worker address normally uses the same HTTPS endpoint
as `pylonGrpcDialAddress`. A secure external route requires the same explicit
`https://` URI in both values and `grpcTls.enabled: true`. Development-only
plaintext requires `http://` in both values and
`grpcTls.allowInsecureHttp: true`; a scheme-less external route is rejected.
Port 443 alone does not make a connection secure.

To recursively discover request routers in another region, set explicit remote
Watch dial URIs on the self-managed operator surface:

```yaml
addons:
  llm:
    requestRouter:
      discovery:
        remoteWatchUrls:
          - https://region-b-watch.example.com:50071
```

Each URI must use `https://`. Development-only plaintext endpoints require an
explicit `http://` URI and
`addons.llm.requestRouter.discovery.allowInsecureRemoteWatchHttp: true`.
Scheme-less and unsupported endpoints are rejected rather than defaulting to
plaintext. For an HTTPS URI, the dial hostname selects TLS SNI. Identities
advertised by the remote Watch response remain the HTTP/2 authorities used for
registration.

With `grpcTls.mode: certManager`, the gateway-routes chart creates a dedicated
`Certificate` in the gRPC Gateway namespace. With `mode: existingSecret`, the
operator must create the named TLS Secret in that namespace. In both modes the
Gateway HTTPS listener must reference the same Secret. This identity is
separate from the QUIC certificate on port 50072. The operator must ensure an
existing certificate covers the external dial hostname; `dnsNames` is required
only when the chart requests the certificate.

For an HTTPS dial URI, the gRPC dial hostname is the TLS SNI and must be a SAN
on the Gateway listener certificate. It does not replace the advertised
request-router pod hostname used as the HTTP/2 authority or as the QUIC SNI.
The dedicated HTTPS listener and LLM `GRPCRoute` therefore do not set
`hostname` or `hostnames`. The listener serves the dedicated certificate for
normal public-SNI verification, while the backend router receives the selected
Stargate identity in `:authority`. Setting the listener hostname to the public
dial name would also constrain `:authority` and reject these streams. Envoy
Gateway's route-scoped traffic policy disables the request and maximum stream
durations for long-lived Watch and Register streams.
Keep the default wildcard SAN on the separate QUIC leaf, or issue a certificate
that covers a customized advertised hostname. Every compute cluster must trust
the required issuing CAs as described in
[Compute-plane trust](#compute-plane-trust). Server certificate and key updates
follow [Certificate Renewal](#certificate-renewal). A CA or trust-bundle change
requires a worker rollout.

The stack does not provision cross-region networking, firewall rules, load
balancers, DNS, or trust distribution. Supply those dependencies and register
the trust bundle in every compute cluster. A successful Helm render and
accepted Gateway routes do not prove remote TCP or UDP reachability. Test both
paths and an LLM invocation from each region before production use.

The request router uses `power-of-two` when no load-balancer configuration is
set, and accepts any supported `routingMethod` from a function. When a
load-balancer configuration is set, a function can only select an algorithm
that the configuration enables.

### Pre-created request-router Secret

Set `mode: existingSecret` when you already issue the request-router server
certificate yourself and want the stack to mount it without managing issuance:

```yaml
addons:
  llm:
    enabled: true
    pki:
      enabled: true
      mode: existingSecret
      secretName: stargate-quic-tls
```

The stack renders no `Certificate`, installs no `ClusterIssuer`, and adds no
cert-manager or OpenBao dependency for the request router. You can set
`certManager.enabled: false` and `openbao.enabled: false` when no other stack
component needs them.

Create the Secret in the `nvcf` namespace before installing the stack. It must
carry the `tls.crt` and `tls.key` entries, as a `kubernetes.io/tls` Secret
does:

```bash
kubectl create secret tls stargate-quic-tls \
  --namespace nvcf \
  --cert path/to/tls.crt \
  --key path/to/tls.key
```

`clusterIssuer.enabled`, `dnsNames`, and `allowedDomains` only steer
stack-managed issuance. Rendering fails if any of them is set in this mode, so
a configuration that expects the stack to issue a certificate cannot be
mistaken for one that expects you to.

The certificate must carry a SAN covering the router's advertised hostname.
The stack enables backend routing and advertises per-pod headless names, so
include `*.llm-request-router-headless.nvcf.svc.cluster.local` along with the
stable `llm-request-router.nvcf.svc.cluster.local` name. An external TCP or UDP
dial hostname does not need to be a SAN unless the advertised identity is also
customized to use that hostname. The stack cannot read your Secret at render
time, so it validates neither the SANs nor the expiry. A certificate that does
not cover the advertised hostname fails at worker connection time, not at
install time.

You own issuance, renewal, rotation, and recovery in this mode:

- Renewal and rotation: hot reload requires `helm-nvcf-llm-request-router`
  1.10.0 or later and Stargate 0.11.1 or later. With those versions, update the
  Secret and follow the
  [transport TLS rotation runbook](./runbooks/transport-tls-rotation.md). Older
  chart releases and Stargate image overrides older than 0.11.1 require a
  request-router restart after the Secret update.
- Expiry: track it yourself. Nothing in the stack renews the certificate or
  alerts on an approaching expiry.
- Recovery: a running router keeps its last-known-good identity after rejecting
  a malformed replacement. A new pod cannot start from a missing or malformed
  Secret. Restore a valid Secret and follow the rotation runbook to verify the
  active identity.

Compute-plane trust works exactly as described in
[Compute-plane trust](#compute-plane-trust). Author the `transportTls` block
in the control-plane profile by hand with the public root CA that signed your
certificate, as you would for an external issuer. The profile exporter sources
a bundle only from the managed OpenBao hierarchy, so it leaves the block empty
here. The bundle must contain `CERTIFICATE` blocks only. Profile validation
rejects a private key, so the request-router private key never reaches the
compute plane.

### External cert-manager

Set `certManager.enabled: false` when cert-manager is installed and managed
outside this stack:

```yaml
certManager:
  enabled: false
```

Install the cert-manager CRDs and controller before applying the NVCF stack.
When the stack manages the OpenBao issuer, the external installation must also
provide `ServiceAccount/cert-manager` in the `cert-manager` namespace because
the managed issuer uses Kubernetes authentication with that identity.

### Compute-plane trust

The control-plane profile carries the public transport CA:

```yaml
transportTls:
  trustMode: bundle
  trustBundleFingerprint: sha256:<64-lowercase-hex-digits>
  trustBundlePem: |
    -----BEGIN CERTIFICATE-----
    <public-root-ca-pem-body>
    -----END CERTIFICATE-----
```

For the managed OpenBao issuer, export a refreshed profile after the managed
PKI hierarchy is available:

```bash
nvcf-cli self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --env <environment-name> \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  control-plane profile export \
  --cluster-name <control-plane-cluster-name> \
  --nca-id <nca-id> \
  --region <region>
```

The exporter cannot infer the worker-facing request-router endpoint. Add the
reachable `host:port` before validating or registering the profile:

```yaml
controlPlane:
  addons:
    llm:
      requestRouterAddress: llm-router.example.com:443
```

Use the gRPC endpoint that compute-plane workers can resolve and reach. This is
a dial endpoint. It does not replace the advertised request-router hostname
that the certificate covers.

Registration renders this field as `agent.llm.requestRouterAddress` in the
compute-plane values. That is operator configuration, not a runtime fallback
for workers. At this release the worker sidecar takes its `--stargate-address`
from the `LLM_REQUEST_ROUTER_ADDRESS` variable in its launch environment, with
`STARGATE_ADDRESS` accepted as a legacy alias. The NVCF API injects
`LLM_REQUEST_ROUTER_ADDRESS` on the normal launch path, derived from
`global.workerEndpoints.llmRequestRouterAddress`. If neither variable reaches
the workload, translation rejects the launch instead of falling back to the
registered address.

Keep the default stable and wildcard entries in `addons.llm.pki.dnsNames`.
Add another SAN only when the advertised request-router identity changes, not
when only a load-balancer dial endpoint changes. For the managed issuer, any
new advertised DNS suffix must also be covered by `allowedDomains`. The
OpenBao role allows subdomains and wildcards but not bare domains, so configure
the parent domain rather than the exact leaf name.

The managed export reads the public root CA certificate from
`services/all/pki/root` in the stack's OpenBao service and calculates the
canonical NVCF trust-bundle fingerprint. It does not discover the CA for an
external issuer. For an external issuer, add the issuer owner's public CA
bundle and canonical NVCF fingerprint to `transportTls` in the existing
profile.

Calculate the canonical fingerprint for a public CA bundle with:

```bash
set -euo pipefail

bundle_file=path/to/public-ca-bundle.pem
fingerprint_tmp="$(mktemp -d)"
trap 'rm -rf "$fingerprint_tmp"' EXIT

awk -v output_dir="$fingerprint_tmp" '
  /-----BEGIN CERTIFICATE-----/ { certificate++; in_certificate=1 }
  in_certificate {
    print > (output_dir "/certificate-" certificate ".pem")
  }
  /-----END CERTIFICATE-----/ { in_certificate=0 }
' "$bundle_file"

for certificate_file in "$fingerprint_tmp"/certificate-*.pem; do
  openssl x509 -in "$certificate_file" -outform DER \
    | openssl dgst -sha256 -r \
    | awk '{print $1}'
done | sort -u >"$fingerprint_tmp/certificate-hashes"

{
  printf 'nvcf-trust-bundle-v1\n'
  cat "$fingerprint_tmp/certificate-hashes"
} | openssl dgst -sha256 -r \
  | awk '{print "sha256:" $1}'
```

The procedure hashes each certificate's DER bytes, removes duplicates, sorts
the certificate hashes, and hashes the versioned canonical text. Use the
result for `trustBundleFingerprint`.

Validate the profile after updating it:

```bash
nvcf-cli self-hosted control-plane profile validate \
  --file deploy/stacks/self-managed/out/control-plane-profile.yaml \
  --require compute-reachable
```

Compute-plane registration converts `transportTls` into this exact generated
NVCA fragment:

```yaml
agentConfig:
  mergeConfig: |
    workload:
      transportTLS:
        trustMode: bundle
        trustBundleFingerprint: sha256:<64-lowercase-hex-digits>
        trustBundlePem: |
          -----BEGIN CERTIFICATE-----
          <public-root-ca-pem-body>
          -----END CERTIFICATE-----
```

Use only public CA certificates in `trustBundlePem`. Do not add a private key,
leaf certificate, or OpenBao token.

In bundle mode, NVCA creates one merged system and private CA file in the
`llm-worker` container and explicitly points both Pylon trust inputs at it:

```text
STARGATE_TLS_CERT_PATH=/etc/ssl/certs/ca-certificates.crt
STARGATE_GRPC_TLS_CA_CERT_PATH=/etc/ssl/certs/ca-certificates.crt
```

Sharing this CA bundle does not couple the protocols or reuse a leaf
certificate. `STARGATE_TLS_CERT_PATH` retains its QUIC meaning.
`STARGATE_GRPC_TLS_CA_CERT_PATH` applies only to Stargate gRPC HTTPS. The bundle
can contain roots for both independent leaf-certificate hierarchies. In system
trust mode, NVCA injects neither variable and Pylon keeps Tonic's default HTTPS
root behavior.

For an unmanaged Pylon process, set the equivalent
`--grpc-tls-ca-cert-path <path>` option directly. The file is read once at
startup and is not reloaded.

When replacing a local plaintext configuration, also set this in the
compute-plane Helmfile environment:

```yaml
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: false
```

The compute-plane Helmfile merges the environment fragment over the generated
registration fragment, so the TLS trust configuration is retained and an old
plaintext override is disabled.

If you mirror images to a registry that does not use the stack's default
`global.image.registry` and `global.image.repository`, override the `pylon`
sidecar image passed to generated LLM workers:

```yaml
api:
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: <registry>/<repository>/pylon:0.14.1
```

The LLM API Gateway and request router images are resolved from the same stack
artifact registry settings as the other control-plane services.

## Apply and Verify

Apply the updated control-plane environment:

```bash
cd path/to/nvcf-self-managed-stack
make apply HELMFILE_ENV=<environment-name>
```

For the managed issuer, wait for the `ClusterIssuer`:

```bash
kubectl wait --for=condition=Ready \
  clusterissuer/nvcf-openbao-pki \
  --timeout=2m
kubectl get clusterissuer nvcf-openbao-pki
```

For an external issuer, wait for the configured resource instead:

```bash
kubectl wait --for=condition=Ready \
  clusterissuer/<issuer-name> \
  --timeout=2m
# For a namespaced Issuer:
kubectl -n nvcf wait --for=condition=Ready \
  issuer/<issuer-name> \
  --timeout=2m
```

Set `REQUEST_ROUTER_TLS_NAME` to the value of
`addons.llm.pki.secretName`. Its default is `stargate-quic-tls`. In
`certManager` mode, verify the request-router `Certificate` and its issuer
reference. The `existingSecret` mode does not render a `Certificate`, so skip
the first two commands in that mode. The final command applies to both modes
and reads only the public certificate:

```bash
export REQUEST_ROUTER_TLS_NAME="<configured-request-router-tls-name>"
kubectl -n nvcf wait --for=condition=Ready \
  "certificate/$REQUEST_ROUTER_TLS_NAME" \
  --timeout=2m
kubectl -n nvcf get certificate "$REQUEST_ROUTER_TLS_NAME" \
  -o jsonpath='{.spec.issuerRef.kind}{"/"}{.spec.issuerRef.name}{"\n"}'
kubectl -n nvcf get secret "$REQUEST_ROUTER_TLS_NAME" \
  -o jsonpath='{.data.tls\.crt}' \
  | base64 --decode \
  | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
```

Regenerate and apply the registration values for each compute cluster:

```bash
nvcf-cli self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <environment-name> \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  compute-plane register \
  --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml \
  --cluster-name <compute-plane-cluster-name> \
  --kube-context <compute-plane-context> \
  --region <region> \
  --output deploy/stacks/nvcf-compute-plane/out/<compute-plane-cluster-name>-register-values.yaml

nvcf-cli self-hosted \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <environment-name> \
  compute-plane install \
  --values deploy/stacks/nvcf-compute-plane/out/<compute-plane-cluster-name>-register-values.yaml \
  --kube-context <compute-plane-context> \
  --cluster-name <compute-plane-cluster-name>
```

Existing LLM function pods keep their current sidecar arguments. Recreate or
redeploy those functions after refreshing the compute plane.

After deploying an LLM function, verify the workload trust bundle. Compare
only `.data.fingerprint` with `transportTls.trustBundleFingerprint` in the
profile. `openssl x509 -fingerprint` does not calculate the canonical NVCF
bundle fingerprint.

```bash
kubectl -n nvcf-backend get configmap nvcf-transport-trust-bundle \
  -o jsonpath='{.data.fingerprint}{"\n"}'
kubectl -n nvcf-backend get configmap nvcf-transport-trust-bundle \
  -o jsonpath='{.data.nvcf-ca-bundle\.pem}' \
  | openssl x509 -noout -subject -issuer
```

Verify the worker sidecar:

```bash
kubectl get pods -n nvcf-backend -L FUNCTION_ID
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[*]}{.name}{"\t"}{.image}{"\n"}{end}'
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[?(@.name=="llm-worker")].args[*]}{.}{"\n"}{end}'
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[?(@.name=="llm-worker")].env[*]}{.name}{"="}{.value}{"\n"}{end}' \
  | grep -E '^STARGATE_(GRPC_TLS_CA_CERT_PATH|TLS_CERT_PATH)='
```

The worker args must contain
`--stargate-address=llm-request-router.nvcf.svc.cluster.local:50071`, or the
configured routable DNS name, and must not contain `--quic-insecure`. The
external address is the initial gRPC dial endpoint. The reverse tunnel verifies
the advertised request-router pod hostname instead. The environment must
contain:

```text
STARGATE_TLS_CERT_PATH=/etc/ssl/certs/ca-certificates.crt
STARGATE_GRPC_TLS_CA_CERT_PATH=/etc/ssl/certs/ca-certificates.crt
```

Also verify the control-plane components:

```bash
kubectl get deployment -n nvcf llm-api-gateway
kubectl get deployment -n nvcf llm-request-router-backend-router
kubectl get deployment,statefulset -n nvcf llm-request-router
kubectl get service -n nvcf llm-request-router-backend-router
kubectl get pods -n nvcf | grep -E 'llm-api-gateway|llm-request-router'
kubectl get httproute -A | grep llm
```

For remote workers, also verify the LLM `GRPCRoute`, `UDPRoute`, dedicated
gRPC `Certificate`, stream timeout policy, and
`ReferenceGrant` as described in
[Gateway Routing and DNS](./gateway-routing.md#verify-llm-worker-routes).

## Certificate Renewal

cert-manager renews the request-router certificate and updates the Secret named
by `addons.llm.pki.secretName`. The default is `Secret/stargate-quic-tls`.
With `mode: existingSecret` there is no renewal loop and you update the
configured Secret yourself.

Hot reload requires `helm-nvcf-llm-request-router` 1.10.0 or later and Stargate
0.11.1 or later. The request router polls the mounted server certificate and
private key every 30 seconds. A valid replacement becomes active for new
handshakes without restarting the request router. Established connections
remain open. If a replacement is invalid, the router rejects it and keeps the
last-known-good identity. Older chart releases and Stargate image overrides
older than 0.11.1 require a request-router restart after the Secret update.

This reload contract covers the server certificate and private key only. Pylon
loads both its gRPC CA bundle and QUIC trust bundle at startup. A trust-bundle
change, including a root CA rotation, requires a rolling restart of the LLM
worker pods. Renewing an intermediate under an already-trusted root does not
change the trust bundle and does not require a worker-pod restart. Follow
the [transport TLS rotation runbook](./runbooks/transport-tls-rotation.md) for
the atomic Secret update, reload checks, trust-bundle rollout order, and
recovery procedure.

## Upgrade and Rollback

Use this order for an upgrade from plaintext transport:

1. Make the `helm-nvcf-pki` chart available from the configured chart source.
2. Remove the old plaintext setting or set
   `workload.stargateQUICInsecure: false`.
3. Apply the dependency Helmfile stage to install cert-manager, OpenBao, and
   the managed issuer, or prepare the external cert-manager and issuer:

   ```bash
   HELMFILE_ENV=<environment-name> helmfile \
     --file deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl \
     --environment default \
     apply
   ```

4. Schedule a maintenance window and undeploy the existing LLM functions.
   The router does not support a mixed plaintext and TLS transition.
5. Apply the remaining control-plane stack. The managed router hook prepares
   the OpenBao signing path before cert-manager reconciles the request-router
   `Certificate`.
6. Wait for the issuer and the `Certificate` named by
   `addons.llm.pki.secretName` to become ready. The default name is
   `stargate-quic-tls`.
7. Export or update the control-plane profile, register each compute plane
   again, and install the refreshed registration values.
8. Recreate the LLM functions and verify the certificate SAN, trust-bundle
   fingerprint, worker address, and worker arguments.

Use this order for a safe rollback:

1. Create and verify the replacement `ClusterIssuer` or namespaced `Issuer`.
2. Add both the current and replacement public roots to the compute-plane
   profile, calculate the canonical bundle fingerprint, register each compute
   plane again, and recreate the LLM workers with the combined trust bundle.
3. Set `addons.llm.pki.issuerKind` and `issuerName` to the replacement, set
   `clusterIssuer.enabled: false`, and apply the control-plane stack.
4. Wait for the `Certificate` named by `addons.llm.pki.secretName` to become
   ready, confirm a successful `server_identity` reload, and verify the
   replacement TLS data path with a new connection.
5. Remove the old root from the compute-plane profile, register each compute
   plane again, and recreate the workers.
6. Confirm that no `Certificate` references the old issuer:

   ```bash
   kubectl --context <control-plane-context> get certificate -A \
     -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,KIND:.spec.issuerRef.kind,ISSUER:.spec.issuerRef.name
   ```

7. Remove the old managed release after it is no longer part of the Helmfile
   state. Run `helm status` first and confirm the release is the superseded
   one, that the previous step listed no `Certificate` still referencing its
   issuer, and that the replacement data path is verified. Uninstalling is
   not reversible from the cluster; only proceed once all three hold.

   ```bash
   helm status nvcf-pki \
     --namespace cert-manager \
     --kube-context <control-plane-context>
   ```

   After confirming, remove the release:

   ```bash
   helm uninstall nvcf-pki \
     --namespace cert-manager \
     --kube-context <control-plane-context>
   ```

8. Delete the retained old `ClusterIssuer` only after all references are gone:

   ```bash
   kubectl --context <control-plane-context> \
     delete clusterissuer <old-issuer-name>
   ```

If no replacement issuer is available, undeploy the LLM functions and disable
the LLM addon before removing the issuer. Keep LLM traffic stopped until a
secure issuer and trust path are available.

The managed `ClusterIssuer` is retained when its Helm release is removed. Do
not delete it before its certificates and consumers have moved to the
replacement trust path. Do not use `stargateQUICInsecure` as a production
rollback path.

## Local Plaintext Transport

Use plaintext transport only in local or isolated test clusters. Set both
plaintext controls:

```yaml
addons:
  llm:
    enabled: true
    gateway:
      replicaCount: 1
      auth:
        grpcInsecure: true
    requestRouter:
      replicaCount: 1

agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: true
```

`addons.llm.gateway.auth.grpcInsecure: true` configures the LLM API Gateway to
talk to the NVCF API over plaintext gRPC.

`workload.stargateQUICInsecure: true` configures generated LLM workers to pass
the plaintext QUIC setting to the `pylon` sidecar.

<Warning>
Use these settings only for local or isolated test clusters. Do not enable
plaintext worker transport in production.

</Warning>

## Troubleshooting

`404 no_eligible_candidates` from `llm.invocation.<domain>` means the request
reached the LLM Gateway, but the requested function or model was unknown or was
not registered on the selected request router. Similar `503` candidate errors
mean the router knows the target but has no active eligible backend. Check:

- The LLM function is deployed and its pod is `Running`.
- The request `model` value uses `<function-id>/<model-name>`.
- The function's `models[].name` matches the model suffix in the request.
- `models[].llmConfig.uris` includes the invoked path.
- When `addons.llm.requestRouter.loadBalancer.config` is set, it includes the
  algorithm selected by the function's `models[].llmConfig.routingMethod`.
- The `llm-worker` sidecar connected to `llm-request-router`.
- The effective LLM request-router worker address is reachable from the worker
  cluster.
- Local clusters using plaintext transport include both `grpcInsecure` and
  `stargateQUICInsecure`.

For transport TLS failures, check:

- gRPC unknown issuer: for a public ACM NLB certificate, verify the system and
  public root store. For a private-CA NLB certificate, verify
  `STARGATE_GRPC_TLS_CA_CERT_PATH` and the CA chain in that PEM bundle.
- QUIC unknown issuer: inspect the `Certificate` Ready condition and verify
  `issuerRef.kind`, `issuerRef.name`, and the issuer namespace. A namespaced
  `Issuer` must be in `nvcf`.
- gRPC SAN mismatch: compare the external HTTPS dial hostname with the public
  DNS SANs on the NLB listener certificate. The internal pod authority is not
  the gRPC TLS identity.
- QUIC SAN mismatch: compare the request router's
  `--advertised-hostname-template` with the SANs in the Secret named by
  `addons.llm.pki.secretName`. The default is `Secret/stargate-quic-tls`. The
  external UDP dial endpoint does not replace the advertised QUIC identity.
- Expired or not-yet-valid certificate: inspect the certificate dates and the
  cluster clock. Renew the certificate and use the
  [transport TLS rotation runbook](./runbooks/transport-tls-rotation.md) to
  verify that the replacement becomes active.
- System trust mode: confirm `STARGATE_TLS_CERT_PATH` and
  `STARGATE_GRPC_TLS_CA_CERT_PATH` are both unset so Pylon uses its enabled
  system and public roots.
- Bundle trust mode: verify `ConfigMap/nvcf-transport-trust-bundle`, compare its
  fingerprint with the compute-plane profile, and confirm
  `STARGATE_TLS_CERT_PATH` in the `llm-worker` container. The
  `STARGATE_GRPC_TLS_CA_CERT_PATH` override is optional; when unset, the gRPC
  registration and watch paths reuse the existing Stargate bundle. Restart the
  worker after changing either bundle.

Useful logs:

```bash
kubectl logs -n nvcf deploy/llm-api-gateway --tail=100
kubectl logs -n nvcf statefulset/llm-request-router \
  --all-pods=true --tail=100
kubectl logs -n nvcf-backend <function-pod> -c llm-worker --tail=100
```

In healthy routing, the request router logs show a reverse tunnel connection
from the worker and at least one routing candidate for the requested function.
