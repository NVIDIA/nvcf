# NVCF Image Credential Helper

NVCF Image Credential Helper is a Kubernetes utility that automates the management of container registry credentials for NVIDIA Cloud Functions (NVCF) Bring-Your-Own-Compute (BYOC) clusters.

This tool monitors and refreshes image pull secrets across Kubernetes namespaces, ensuring that pods can consistently pull container images from private registries such as AWS ECR, Volcengine, and others. It eliminates the need for manual secret management and credential rotation.

## Features

- **Automatic Credential Refresh**: Periodically updates image pull secrets before they expire
- **Multi-Cloud Support**: Supports AWS ECR, Volcengine, and other container registries
- **Flexible Targeting**: Run for specific namespaces, all namespaces, or filtered by label selectors
- **Kubernetes-Native**: Integrates seamlessly with Kubernetes RBAC and secret management
- **CronJob or One-Shot**: Can be deployed as a recurring CronJob or executed as a one-time Job

## How It Works

The credential helper runs as a Kubernetes Job or CronJob and performs the following operations:

1. Discovers target namespaces based on provided flags or label selectors
2. Fetches fresh credentials from cloud provider APIs (e.g., ECR `GetAuthorizationToken`)
3. Updates or creates Kubernetes image pull secrets in target namespaces
4. Patches service accounts to reference the updated secrets

This ensures that pods can always authenticate to private container registries without manual intervention.

## Installation

### Prerequisites

- A Kubernetes cluster with appropriate RBAC permissions
- Cloud provider credentials configured (e.g., AWS credentials for ECR)
- `kubectl` access to the cluster

### Deploy as a CronJob

The recommended deployment method is as a CronJob that runs periodically:

```bash
kubectl apply -f examples/helper.yaml
```

This creates:

- A ServiceAccount with necessary permissions
- A CronJob that runs every 6 hours to refresh credentials
- Required RBAC roles and bindings

### Deploy as a One-Shot Job

For immediate credential refresh:

```bash
kubectl create job --from=cronjob/image-credential-helper image-credential-helper-manual -n image-credential-helper
```

## Configuration

The helper is configured via command-line flags:

| Flag                        | Description                                 | Example                                                      |
| --------------------------- | ------------------------------------------- | ------------------------------------------------------------ |
| `-global`                   | Run for all namespaces matched by selectors | `-global`                                                    |
| `-target-namespace`         | Run for a specific namespace                | `-target-namespace=my-namespace`                             |
| `-namespace-label-selector` | Filter namespaces by labels                 | `-namespace-label-selector=nvca.nvcf.nvidia.io/backend=true` |
| `-secret-label-selector`    | Filter secrets by labels                    | `-secret-label-selector=credential-type=ecr`                 |

### Example Configurations

**Refresh credentials in a single namespace:**

```yaml
args:
  - -target-namespace=my-app
```

**Refresh credentials in all namespaces with a specific label:**

```yaml
args:
  - -global
  - -namespace-label-selector=nvca.nvcf.nvidia.io/backend=true
```

**Refresh only specific secrets:**

```yaml
args:
  - -global
  - -secret-label-selector=credential-type=ecr
```

## Supported Registry Providers

### AWS Elastic Container Registry (ECR)

The helper uses AWS SDK credentials from the environment or IAM role to fetch ECR authorization tokens.

**Required Environment Variables:**

- `AWS_REGION` (or use IAM instance profile)
- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (if not using IAM)

### Volcengine Container Registry

Support for Volcengine container registry using Volcengine API credentials.

**Required Environment Variables:**

- `VOLCENGINE_ACCESS_KEY_ID`
- `VOLCENGINE_SECRET_ACCESS_KEY`
- `VOLCENGINE_REGION`

## Examples

Complete examples are available in the [`examples/`](./examples/) directory:

- [`helper.yaml`](./examples/helper.yaml) - CronJob deployment example
- [`init.yaml`](./examples/init.yaml) - InitContainer pattern example
- [`pod.yaml`](./examples/pod.yaml) - Test pod to verify credentials
- [`run.sh`](./examples/run.sh) - Convenience script for deployment

See the [examples README](./examples/README.md) for detailed usage instructions.

## Local Development

### Prerequisites

- Go 1.23 or newer
- Docker
- A Kubernetes cluster for testing (e.g., kind, minikube)

### Building

```bash
# Build the binary locally
make build

# Build the container image
make image

# Run tests
make test

# Run linter
make lint
```

### Testing

```bash
# Run unit tests
make test

# Run tests with coverage
make test-coverage

# Apply example manifests for manual testing
./examples/run.sh
kubectl apply -f examples/pod.yaml
```

## Release Process

For detailed release instructions, see [RELEASING.md](RELEASING.md).

To create a release:

1. Ensure all changes are merged to the main branch
2. Create and push a version tag:
   ```bash
   git tag -a v1.0.0 -m "Release v1.0.0"
   git push origin v1.0.0
   ```
3. The CI pipeline will automatically build, test, and publish the release

## Troubleshooting

### Credentials Not Refreshing

Check the CronJob logs:

```bash
kubectl logs -n image-credential-helper -l job-name --tail=100
```

Verify the ServiceAccount has proper RBAC permissions:

```bash
kubectl auth can-i update secrets --as=system:serviceaccount:image-credential-helper:image-credential-helper
```

### Pods Cannot Pull Images

Verify the secret exists and is properly formatted:

```bash
kubectl get secret -n <namespace> <secret-name> -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d | jq
```

Check that the secret is referenced in the ServiceAccount:

```bash
kubectl get serviceaccount default -n <namespace> -o yaml
```

### Cloud Provider Authentication Issues

**For ECR:**

- Ensure AWS credentials are properly configured
- Verify IAM permissions include `ecr:GetAuthorizationToken`
- Check the AWS region is correctly set

**For Volcengine:**

- Verify Volcengine API credentials are valid
- Check the region configuration

## Contributing

Contributions are welcome! Please see our [Contributing Guidelines](CONTRIBUTING.md) for details on:

- How to report bugs and request features
- Code contribution workflow
- Commit message conventions
- Development setup

Please also review our [Code of Conduct](CODE_OF_CONDUCT.md) before contributing.

## Support

For questions and support:

- **GitHub Issues**: [Create an issue](https://github.com/NVIDIA/nvcf-image-credential-helper/issues)

## Security

For security concerns, please see our [Security Policy](SECURITY.md). Do not report security vulnerabilities through public GitHub issues.

## Governance & Maintainers

This project is maintained by the [nvidia/nvca-maintainers](https://github.com/orgs/nvidia/teams/nvca-maintainers) team.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

Copyright © 2025 NVIDIA Corporation. All rights reserved.
