# Dynamic OIDC Authentication Setup

## 1. Background and Justification

The OpenBao JWT authentication method requires configuration with the OIDC issuer URL specific to the Kubernetes cluster it runs on. This issuer URL is not constant; it varies significantly between standard distributions (like k3d or KinD) and managed cloud provider services (like Amazon EKS, GKE, or AKS).

A static configuration that works for a local k3d cluster (`https://kubernetes.default.svc.cluster.local`) would fail in an EKS environment, which uses an external public URL for its OIDC issuer to support features like IAM Roles for Service Accounts (IRSA). A portable and reliable solution must be able to adapt to its environment at runtime.

To solve this, we implemented a dynamic discovery mechanism that runs during the Helm deployment as part of the `openbao-server-migrations` job. This process queries the cluster's OIDC discovery endpoint, determines the correct issuer for the current environment, and uses it to configure the OpenBao JWT auth method.

## 2. Implementation Details

The core of the implementation resides in two utility scripts and the Helm chart templates.

### `issuer_discovery.sh`

This script is the primary driver of the discovery process. It is sourced at the beginning of the migrations job and is responsible for:

1. Securely connecting to the cluster's OIDC discovery endpoint (`/.well-known/openid-configuration`). It uses the pod's automatically mounted Service Account token and CA certificate to authenticate and establish a trusted TLS connection.
2. Parsing the JSON response to extract the `issuer` and `jwks_uri` values.
3. Handling the EKS-specific networking architecture. The EKS discovery endpoint returns a `jwks_uri` with an internal, unreachable VPC hostname. The script detects an EKS issuer and reconstructs the correct, publicly-accessible JWKS URL by appending `/keys` to the public issuer URL, as per official AWS documentation.
4. Exporting the final `OPENBAO_JWT_ISSUER` and `OPENBAO_JWT_JWKS_URL` as environment variables for use by other migration scripts.

### `functions.sh` (`configure_auth_jwt`)

This script contains the `configure_auth_jwt` function, which consumes the environment variables and executes the `bao write` command. A key design decision was to implement two distinct configuration paths to maximize robustness:

* **Default Issuer Path**: If the discovered issuer is the standard in-cluster one (`https://kubernetes.default.svc.cluster.local`), the function configures OpenBao using the `jwt_validation_pubkeys` parameter. It reads the public key directly from a secret that is pre-mounted into the pod by the Helm chart. This approach is more resilient for standard clusters as it removes any dependency on the OpenBao server pod having network access to the JWKS endpoint.
* **Custom Issuer Path**: For any other issuer (e.g., EKS, GKE), the function configures OpenBao using the discovered `jwks_url`. This is the correct approach for public OIDC providers as it delegates the responsibility of fetching, caching, and rotating the signing keys to the OpenBao server, ensuring long-term operational stability.

### Helm Chart (`values.yaml` and Templates)

The feature is controlled via the `openbao.migrations.issuerDiscovery` section in `values.yaml`.

* `enabled`: A boolean to enable or disable the discovery feature. If `false`, the system gracefully falls back to the robust "Default Issuer Path".
* `urlOverride`: Allows operators to point the discovery script at a non-standard or private OIDC provider.
* `caBundleSecretName`: Provides a mechanism for the discovery script to trust private CAs, which is a critical feature for enterprise environments that use TLS-intercepting proxies or internal identity providers.
* `insecure`: A flag to disable TLS verification for development and testing purposes only.
