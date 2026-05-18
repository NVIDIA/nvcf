<!-- markdownlint-disable MD013 -->

# NCP Local Cluster with Generic Credential Provider

This project provides a local Kubernetes (k3d) cluster setup (`ncp-local-cluster`) that includes a generic Kubelet credential provider. The credential provider, now implemented in Go, enables Kubelet to pull images from private container registries using credentials stored in a standard Docker `config.json` format. It supports both host-level and path-specific credentials for enhanced flexibility (see ADR001, ADR002 in the `docs` directory).

## What it Does

* Sets up a local k3d cluster.
* Includes a `generic-credential-provider` Go binary (`./bin/generic-credential-provider`) that allows Kubelet to authenticate with container registries.
* Provides sample configurations for deploying workloads and testing the credential provider.

## Required Tools

* [Docker](https://www.docker.com/get-started)
* [k3d](https://k3d.io/#installation) (v5.x or later recommended)
* [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
* [Make](https://www.gnu.org/software/make/)
* Go (version 1.20+ recommended) for building the provider.

## Makefile Commands

The `Makefile` provides several convenient targets. Run `make help` to see a full list of available commands and their descriptions.

Key targets include:

* `make help`: Display all available Makefile targets.
* `make build`: Build the Go credential provider binary.
* `make test`: Run unit tests for the Go provider and show coverage.
* `make test-manual`: Run the manual end-to-end test harness for the provider.
* `make start`: Create or start the local k3d cluster.
* `make build-and-deploy-cluster`: A convenient command to clean, build the provider, start the cluster, and deploy a sample application.
* `make build-and-deploy-multicluster`: Build one control-plane cluster and one or more compute-plane clusters.
* `make build-and-deploy-control-plane-cluster`: Build only the local control-plane cluster.
* `make build-and-deploy-compute-plane-cluster`: Build one local compute-plane cluster.
* `make clean`: Remove built binaries and test coverage files.
* `make build-credential-provider-multiarch`: Build `linux/amd64` and `linux/arm64` binaries locally without publishing.

## Multi-Cluster Mode

The default single-cluster workflow remains available through
`make build-and-deploy-cluster`. Use multi-cluster mode when validating a split
self-hosted NVCF topology with one local control-plane cluster and one or more
local compute-plane clusters.

Default cluster names:

| Role | Default name | kube context |
|---|---|---|
| Control plane | `ncp-local-cp` | `k3d-ncp-local-cp` |
| Compute plane 1 | `ncp-local-compute-1` | `k3d-ncp-local-compute-1` |
| Compute plane N | `ncp-local-compute-N` | `k3d-ncp-local-compute-N` |

Addon placement:

| Addon or validation | Control plane | Compute plane |
|---|---:|---:|
| Generic credential provider | yes | yes |
| Docker credential file mount | yes | yes |
| Gateway API and Envoy Gateway | yes | no |
| nginx route validation | yes | no |
| CSI SMB driver | no | yes |
| fake GPU operator | no | yes |
| private-image sample validation | no | yes |

The compute-plane clusters get local aliases for control-plane SIS, ReVal, and
NATS. Those aliases route through the control-plane k3d load balancer so pods
can use the expected in-cluster service names while the traffic crosses local
k3d clusters.

Create the default split topology, with one control-plane cluster and one
compute-plane cluster:

```sh
make build-and-deploy-multicluster
```

Create one control-plane cluster and three compute-plane clusters:

```sh
make build-and-deploy-multicluster COMPUTE_CLUSTER_COUNT=3
```

Use explicit compute cluster names instead of count-derived names:

```sh
make build-and-deploy-multicluster COMPUTE_CLUSTERS="ncp-east ncp-west"
```

Build the roles separately:

```sh
make build-and-deploy-control-plane-cluster
make build-and-deploy-compute-plane-cluster
make build-and-deploy-compute-plane-cluster COMPUTE_CLUSTER_NAME=ncp-local-compute-2
```

Destroy the default split topology:

```sh
make destroy-multicluster
```

Destroy explicit compute clusters:

```sh
make destroy-multicluster COMPUTE_CLUSTERS="ncp-east ncp-west"
```

Destroy roles separately:

```sh
make destroy-control-plane
make destroy-compute-plane
make destroy-compute-plane COMPUTE_CLUSTER_NAME=ncp-local-compute-2
```

Switch between clusters with the generated k3d kube contexts:

```sh
kubectl config use-context k3d-ncp-local-cp
kubectl config use-context k3d-ncp-local-compute-1
kubectl config use-context k3d-ncp-local
```

Or run commands against a specific cluster without switching:

```sh
kubectl --context k3d-ncp-local-cp get pods -A
kubectl --context k3d-ncp-local-compute-1 get pods -A
```

Multi-cluster mode uses `k3d-config-control-plane.yaml` for the control-plane
cluster and `k3d-config-compute-plane.yaml` for compute-plane clusters. The
compute-plane config intentionally avoids host port mappings so multiple
compute clusters can run at the same time. The control-plane config maps host
ports `8080`, `8443`, and `4222` for HTTP, HTTPS, and NATS respectively; stop
or destroy any existing cluster or local process that already owns those ports
before creating the control-plane cluster.

If another local cluster already owns those ports, override the control-plane
ports together:

```sh
make build-and-deploy-multicluster \
  CONTROL_PLANE_HTTP_PORT=18080 \
  CONTROL_PLANE_HTTPS_PORT=18443 \
  CONTROL_PLANE_NATS_PORT=14222
```

Use a custom local control-plane DNS suffix:

```sh
make build-and-deploy-multicluster \
  CONTROL_PLANE_DOMAIN=control-plane.dev.test
```

The domain is applied to compute-plane CoreDNS aliases and to the local
control-plane Gateway route hostnames for SIS and ReVal. The NATS Gateway route
is owned by the self-managed stack `nvcf-gateway-routes` chart; ncp-local only
provides the Gateway TCP listener and the compute-plane DNS alias.

Run the dry Makefile checks without creating k3d clusters:

```sh
make test-multicluster-make
```

## Generic Kubelet Credential Provider

The Kubelet on each node in a Kubernetes cluster needs to pull container images. For private registries, it requires credentials. This project uses a Kubelet Credential Provider plugin (`./bin/generic-credential-provider`) to achieve this.

See [Kubernetes Documentation](https://kubernetes.io/docs/tasks/administer-cluster/kubelet-credential-provider/) for more information on credential providers.

The provider has been preconfigured in the sample `config/credential-provider-config.yaml` to match on `nvcr.io` registry hosts, but can be configured for others.

### How it Works

1. The Kubelet is configured (via a `CredentialProviderConfig` file) to use this plugin for specified image patterns.
2. When Kubelet needs to pull an image matching these patterns, it calls the `generic-credential-provider` program.
3. The program receives a JSON request from Kubelet containing the image name.
4. The program reads a Docker `config.json` file (mounted as a static file into the Kubelet's plugin directory, e.g., `/etc/kubernetes/secrets/docker-config.json`) to find authentication details for the requested image. It first looks for path-specific credentials (e.g., `nvcr.io/ngc-org/ngc-team/repository`) and falls back to host-level credentials (e.g., `nvcr.io` or `https://nvcr.io`).
5. It then returns a JSON response to Kubelet with the username and password.

### Setting up the Credential Secret

The `generic-credential-provider` expects the Docker credentials to be available in a JSON file that follows the standard Docker `config.json` format. The `k3d-config.yaml` in this project is set up to mount a host file from `./secrets/docker-config.json` to `/etc/kubernetes/secrets/docker-config.json` on the server node. You must create `./docker-config.json` on your host machine.

The `docker-config.json` file should look like this:

```json
{
  "auths": {
    "nvcr.io/ngc-org/ngc-team/repository": { // Path-specific example
      "auth": "base64_encoded_user:pass_for_repository"
    },
    "nvcr.io": { // Host-level fallback for nvcr.io
      "auth": "base64_encoded_user:pass_for_nvcr_default"
    },
    "your.private-registry.com": {
      "auth": "dXNlcm5hbWU6cGFzc3dvcmQ="
    }
  }
}
```

* Replace registry names and `auth` values with your actual details.
* For NGC paths, replace `ngc-org` and `ngc-team` with the NGC org and team path segments.
* The `auth` value is a base64 encoding of `username:password`. For example, `echo -n "myusername:mypassword" | base64`.

## Building the Provider

The credential provider is written in Go and located in the `credential-provider-go` directory.

* To build the binary for `linux/arm64`: `make build`
  * This will place the `generic-credential-provider` binary in the `./bin` directory.
* To build for Linux AMD64: `make build-linux-amd64`
  * This creates `./bin/generic-credential-provider-linux-amd64`.
  * Important Note: the credential provider config should be updated to use this new binary name

## Testing the Provider

### Unit Tests

Unit tests for the Go provider verify individual functions and logic.

* Run tests: `make test`
  * This will also display a test coverage summary in the console.
* Generate HTML coverage report: `make test-coverage-html`
  * Open `credential-provider-go/coverage.html` in your browser to view detailed coverage.

### Manual End-to-End Tests

A manual test harness script (`credential-provider-go/hack/manual-test.sh`) is provided to test the compiled binary with various scenarios, mock Docker configurations, and image requests.

* Run manual tests: `make test-manual`
  * This target first ensures the binary is built.
  * The script will output the STDOUT, STDERR, and EXIT_CODE for each test case, making it easy to verify behavior.

For a quick, direct invocation (less comprehensive than `make test-manual`):

```sh
# Ensure ./docker-config.json or another test config exists
echo '{"image": "nvcr.io/ngc-org/ngc-team/repository:latest"}' | ./bin/generic-credential-provider get-credentials --config-file ./docker-config.json
```

### Expected Output Structure (on successful credential lookup)

The provider outputs a JSON response to standard output:

```json
{
  "kind": "CredentialProviderResponse",
  "apiVersion": "credentialprovider.kubelet.k8s.io/v1",
  "cacheKeyType": "Registry",
  "cacheDuration": "5m",
  "auth": {
    "nvcr.io": { // This key is the registry host
      "username": "found_username",
      "password": "found_password"
    }
  }
}
```

If no credentials are found, the `auth` map will be empty (`{}`). Errors are logged to STDERR, and for Kubelet compatibility, an empty JSON object (`{}`) is often printed to STDOUT with an exit code of 0 for recoverable errors.

## Building Multi-Arch Provider Binaries

To build both architectures locally without tagging or publishing:

```bash
make build-credential-provider-multiarch
# Outputs to bin/
```

## Node Labels

The cluster is configured with 2 `agent` nodes (i.e. worker nodes - the control plane components run on a single `server` node).
The agents are labeled, so a `nodeSelector` can be used.

* `usage: workload`

## Accessing Routes

The cluster uses Envoy Gateway with hostname-based routing. Routes use the `.localhost` TLD ([RFC 6761](https://www.rfc-editor.org/rfc/rfc6761)), which should resolve to `127.0.0.1` automatically on most systems.

Use the [self-hosted gateway route manifests](../../deploy/helm/gateway-routes) as the source of truth for route names and subdomains.

### Troubleshooting Hostname Resolution

If `.localhost` domains don't resolve automatically, add entries to `/etc/hosts`:

```text
127.0.0.1 api.localhost
127.0.0.1 api-keys.localhost
127.0.0.1 invocation.localhost
127.0.0.1 grpc.localhost
```

> Note: Wildcard subdomains (e.g., `*.invocation.localhost`) cannot be added to `/etc/hosts`. For local testing with dynamic function IDs, you'll need to add specific entries or use a local DNS resolver like `dnsmasq`.

## Architecture Decisions

Key design decisions for the Go credential provider are documented in:

* `docs/go-conversion-proposal.md` (Initial conversion from Bash to Go)
* `docs/go-multiple-credentials.md` (Support for path-specific credentials)
