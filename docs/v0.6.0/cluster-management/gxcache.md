# Distributed Shader Cache (GXCache)

This guide provides detailed information on the Distributed Shader Cache (GXCache) component and its use in NVCF GPU clusters.

GXCache is a cloud-native shader cache for NVIDIA that provides distributed shader caching capabilities. It consists of the GXCache server and mutating webhook to optimize shader compilation and caching across your cluster.

GXCache improves rendering performance by caching compiled shaders, reducing compilation time for frequently used shaders and enabling faster scene loading and rendering.

## Installation

### Prerequisites

- A running Kubernetes cluster with `kubectl` access
- **Helm** >= 3.12
- Credentials for the registry where your NVCF charts and images are stored

### Step 1. Authenticate Helm to your chart registry

Authenticate Helm to your OCI registry where the NVCF charts are stored:

```bash
echo "${REGISTRY_PASSWORD}" | helm registry login ${REGISTRY} \
  --username '${REGISTRY_USERNAME}' --password-stdin
```

### Step 2. Create the namespace and image pull secret

```bash
kubectl create namespace gxcache
```

Create an image pull secret so that pods can pull container images from your registry.

<Tabs>
<Tab title="BYOC / NGC">

```bash
kubectl create secret docker-registry nvcr-creds \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="${NGC_API_KEY}" \
  --namespace=gxcache
```

</Tab>

<Tab title="Amazon ECR">

```bash
kubectl create secret docker-registry nvcr-creds \
  --docker-server=<account-id>.dkr.ecr.<region>.amazonaws.com \
  --docker-username=AWS \
  --docker-password="$(aws ecr get-login-password --region <region>)" \
  --namespace=gxcache
```

</Tab>

<Tab title="Other Registry">

```bash
kubectl create secret docker-registry nvcr-creds \
  --docker-server=<your-registry> \
  --docker-username=<your-username> \
  --docker-password=<your-password> \
  --namespace=gxcache
```

</Tab>

</Tabs>

<Note>
The secret name `nvcr-creds` is referenced in the values file under
`webhook.deployment.secret` and `service.deployment.imageSecret`.
If you use a different secret name, update the values file to match.

</Note>

### Step 3. Create a values file

Create a `values.yaml` using the complete example in [Base Configuration] below.

- **BYOC users** pulling directly from NGC can use the `nvcr.io/nvidia/nvcf-byoc/` image
  paths without mirroring.
- **Self-hosted users** should replace the `<your-registry>/<your-repo>` placeholders with
  their mirrored registry path (see the Artifact Manifest in the self-hosted installation guide).

At minimum, configure the storage class for your environment (e.g., `gp3` for AWS EKS)
and the node selector for your GPU nodes.

### Step 4. Install the chart

<Tabs>
<Tab title="BYOC (NGC Helm Repo)">

```bash
helm upgrade --install gxcache \
  nvcf-byoc/gxcache \
  --namespace gxcache \
  --values values.yaml
```

</Tab>

<Tab title="Self-Hosted (OCI)">

Replace `<your-registry>/<your-repo>` with your mirrored registry path.

```bash
helm upgrade --install gxcache \
  oci://<your-registry>/<your-repo>/gxcache \
  --version 0.8.2 \
  --namespace gxcache \
  --values values.yaml
```

</Tab>

</Tabs>

### Step 5. Verify the installation

```bash
# All pods should reach Running status
kubectl get pods -n gxcache

# Check the service and webhook are available
kubectl get svc -n gxcache

# Check persistent volume claims are bound
kubectl get pvc -n gxcache
```

## Base Configuration

The following is a complete example `values.yaml` for deploying GXCache. Copy this file and
adjust values for your environment. Each section is explained in detail below.

- **BYOC users** can use the `nvcr.io/nvidia/nvcf-byoc/` paths directly.
- **Self-hosted users** should replace `<your-registry>/<your-repo>` with their mirrored
  registry path (see the Artifact Manifest in the self-hosted installation guide for source image paths).

```yaml
webhook:
  nodeSelector:
    node-type: compute  # Adjust based on your node labels, or set to null
  deployment:
    image: <your-registry>/<your-repo>/gxcache-webhook
    version: 59bd8ec5
    secret: nvcr-creds  # Image pull secret created in Step 2
  client:
    version:
      image: <your-registry>/<your-repo>/gxcache-init
      tag: 1e47f722
    config:
      tls:
        enabled: false
  metrics:
    enabled: false  # Requires monitoring.coreos.com/v1 CRD

service:
  nodeSelector:
    node-type: compute  # Adjust based on your node labels, or set to null
  deployment:
    image: <your-registry>/<your-repo>/gxcache-service:b206ce39  # Tag is part of the image field
    imageSecret: nvcr-creds  # Image pull secret created in Step 2
  vault:
    enabled: false
  kns:
    enabled: true
    keyset:
      api: /.well-known/jwks.json
      endpoint: http://notary.nvcf.svc.cluster.local:8080
  persistence:
    enabled: true
    storageClass: gp3  # Use gp3 for AWS EKS, adjust for other platforms
    accessMode: ReadWriteOnce
    size: 20Gi
  metrics:
    enabled: false  # Requires monitoring.coreos.com/v1 CRD
```

Replace `<your-registry>/<your-repo>` with your registry path. BYOC users pulling
directly from NGC can use `nvcr.io/nvidia/nvcf-byoc` as the registry/repo path.

<Tip>
If your cluster does not use node labels, set `nodeSelector` to `null`.

</Tip>

## Configuration Sections

### Webhook Configuration

The GXCache webhook is responsible for intercepting and modifying pod specifications to enable shader caching:

```yaml
# values.yaml

webhook:
  nodeSelector:
    node-type: compute  # Adjust based on your node labels
  deployment:
    image: <your-registry>/<your-repo>/gxcache-webhook
    version: 59bd8ec5
    secret: nvcr-creds  # Registry credentials secret
  client:
    version:
      image: <your-registry>/<your-repo>/gxcache-init
      tag: 1e47f722
    config:
      tls:
        enabled: false
  metrics:
    enabled: false  # Requires monitoring.coreos.com/v1 CRD
```

### Service Configuration

The GXCache service provides the shader cache functionality:

```yaml
# values.yaml

service:
  nodeSelector:
    node-type: compute  # Adjust based on your node labels
  deployment:
    image: <your-registry>/<your-repo>/gxcache-service:b206ce39
    imageSecret: nvcr-creds  # Registry credentials secret
  vault:
    enabled: false
```

### Node Selection

GXCache components are scheduled on nodes with appropriate labels to ensure they run on GPU compute nodes. Adjust the node selector based on your cluster's node labeling scheme.

```yaml
# values.yaml

webhook:
  nodeSelector:
    node-type: compute
service:
  nodeSelector:
    node-type: compute
```

### Key Management

GXCache uses NVIDIA's Key Notary Service (KNS) for secure key management.
For self-hosted deployments use the in-cluster notary service:

```yaml
# values.yaml

service:
  kns:
    enabled: true
    keyset:
      api: /.well-known/jwks.json
      endpoint: http://notary.nvcf.svc.cluster.local:8080
```

### Storage Configuration

GXCache requires persistent storage for caching shader data:

```yaml
# values.yaml

service:
  persistence:
    enabled: true
    storageClass: gp3  # Use gp3 for AWS EKS, adjust for other platforms
    accessMode: ReadWriteOnce
    size: 20Gi
```

### Metrics Configuration

GXCache supports Prometheus metrics for monitoring:

```yaml
# values.yaml

webhook:
  metrics:
    enabled: false  # Requires monitoring.coreos.com/v1 CRD
service:
  metrics:
    enabled: false  # Requires monitoring.coreos.com/v1 CRD
```

## Architecture

### GXCache Architecture

GXCache consists of two main components:

1. **GXCache Service**: Provides the shader cache storage and retrieval functionality
2. **GXCache Webhook**: Intercepts pod creation to inject shader cache configuration

### Data Flow

1. **Pod Creation**: Application pod is created with GPU requirements
2. **Webhook Interception**: GXCache webhook intercepts pod creation
3. **Configuration Injection**: Webhook injects shader cache configuration
4. **Shader Compilation**: Application compiles shaders during runtime
5. **Cache Storage**: Compiled shaders are stored in GXCache
6. **Cache Retrieval**: Subsequent requests retrieve cached shaders

## Performance Considerations

### Storage Performance

For optimal shader cache performance:

- **Storage Type**: Use high-performance SSD storage (gp3, io1, io2)
- **Storage Size**: Size based on expected shader cache usage (20GB minimum)
- **Access Mode**: ReadWriteOnce is sufficient for most deployments

### Network Configuration

GXCache should be deployed to minimize network latency:

- Deploy in the same availability zone as GPU nodes
- Use high-bandwidth network connections
- Consider dedicated network interfaces for cache traffic

### Cache Size Planning

Plan cache size based on your workload:

- **Small workloads**: 20-50GB
- **Medium workloads**: 50-200GB
- **Large workloads**: 200GB+

## Monitoring and Observability

### Metrics

GXCache provides several key metrics:

- **Cache hit ratio**: Percentage of shader requests served from cache
- **Cache size**: Current size of cached shader data
- **Request latency**: Time to serve cached shaders
- **Storage utilization**: Disk space usage

### Prometheus Integration

To enable Prometheus metrics:

1. **Install Prometheus Operator** (if not already installed):

   ```bash
   helm install --wait --timeout 15m \
     --namespace monitoring --create-namespace \
     --repo https://prometheus-community.github.io/helm-charts \
     prometheus-agent kube-prometheus-stack
   ```

2. **Enable Metrics in GXCache**:

   ```bash
   helm upgrade gxcache -n gxcache . \
     --values values.yaml \
     --set webhook.metrics.enabled=true \
     --set service.metrics.enabled=true
   ```

3. **Access Prometheus**:

   ```bash
   kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
   ```

### Logging

GXCache logs include:

- Cache hit/miss events
- Shader compilation requests
- Error conditions and troubleshooting information
- Performance metrics

## Troubleshooting

### Common Issues

**Webhook Not Working**
- Verify webhook is running and healthy
- Check webhook configuration and secrets
- Ensure proper RBAC permissions

**Cache Not Storing Shaders**
- Verify GXCache service is running
- Check storage configuration and persistent volumes
- Review application logs for shader compilation errors

**Low Cache Hit Ratio**
- Review cache size configuration
- Check cache eviction policies
- Monitor storage performance

**Storage Issues**
- Verify storage class availability
- Check persistent volume claims
- Monitor disk space usage

## Best Practices

### Deployment

- Deploy GXCache before deploying GPU workloads
- Use high-performance storage for better cache performance
- Monitor cache performance and adjust configuration as needed

### Configuration

- Size cache appropriately for your shader workload
- Use high-performance storage for better performance
- Enable metrics for monitoring and optimization

### Security

- Use proper RBAC permissions for webhook and service
- Secure communication between components
- Regularly update GXCache images for security patches

### Maintenance

- Monitor cache hit ratios and adjust cache size as needed
- Regularly review and clean up unused cached shaders
- Update GXCache images regularly for security patches
- Monitor storage usage and plan for growth
