# Container Cache

This guide provides detailed information on the Container Cache component and its use in NVCF GPU clusters.

Container Cache provides a container caching solution specifically optimized for NGC. It is designed to enhance the efficiency of Docker image pulls from NGC by caching the images locally, reducing network bandwidth and improving pull times for frequently accessed images.

The Container Cache acts as a proxy between your Kubernetes cluster and the NGC registry, caching frequently accessed container images locally to reduce network bandwidth usage and improve deployment times.

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
kubectl create namespace container-caching
```

Create an image pull secret so that pods can pull container images from your registry.

<Tabs>
<Tab title="Amazon ECR">

```bash
kubectl create secret docker-registry nvcr-creds \
  --docker-server=<account-id>.dkr.ecr.<region>.amazonaws.com \
  --docker-username=AWS \
  --docker-password="$(aws ecr get-login-password --region <region>)" \
  --namespace=container-caching
```

</Tab>

<Tab title="Other Registry">

```bash
kubectl create secret docker-registry nvcr-creds \
  --docker-server=<your-registry> \
  --docker-username=<your-username> \
  --docker-password=<your-password> \
  --namespace=container-caching
```

</Tab>

</Tabs>

<Note>
The secret name `nvcr-creds` is referenced in the values file under `images.secrets`.
If you use a different secret name, update the values file to match.

</Note>

### Step 3. Create a values file

Create a `values.yaml` using the complete example in [Base Configuration] below.

- **BYOC users** pulling directly from NGC can use the `nvcr.io/nvidia/nvcf-byoc/` image
  paths without mirroring.
- **Self-hosted users** should replace the `<your-registry>/<your-repo>` placeholders with
  their mirrored registry path (see the Artifact Manifest in the self-hosted installation guide).

Adjust `storageClassName` for your environment (e.g., `gp3` for AWS EKS).

### Step 4. Install the chart

<Tabs>
<Tab title="Self-Hosted (OCI)">

Replace `<your-registry>/<your-repo>` with your mirrored registry path.

```bash
helm upgrade --install container-cache \
  oci://<your-registry>/<your-repo>/nvcf-container-cache \
  --version 0.25.6 \
  --namespace container-caching \
  --values values.yaml
```

</Tab>

</Tabs>

### Step 5. Verify the installation

Container Cache deploys two workloads:

- A **StatefulSet** (`container-cache`) with the number of replicas set by `replicaCount`.
  Each replica runs two containers (nginx proxy + prometheus exporter) and provisions two PVCs
  (`cache` and `proxy-cache`).
- A **DaemonSet** (`container-cache-cc`) that runs on every node and configures the container
  runtime (containerd) to route image pulls through the cache.

```bash
# StatefulSet replicas and DaemonSet pods should all be Running
kubectl get pods -n container-caching

# Verify the StatefulSet is fully ready (READY should match replicaCount)
kubectl get statefulset -n container-caching

# Verify the DaemonSet is running on all nodes
kubectl get daemonset -n container-caching

# Check services are created
kubectl get svc -n container-caching

# Check persistent volume claims are Bound
kubectl get pvc -n container-caching
```

## Base Configuration

The following is a complete example `values.yaml` for deploying Container Cache.
Copy this file and adjust values for your environment. Each section is explained in detail
below.

- **BYOC users** can use the `nvcr.io/nvidia/nvcf-byoc/` paths directly.
- **Self-hosted users** should replace `<your-registry>/<your-repo>` with their mirrored
  registry path (see the Artifact Manifest in the self-hosted installation guide for source image paths).

```yaml
replicaCount: 3

targetHost: nvcr.io,docker.io

images:
  server: <your-registry>/<your-repo>/nvcf-container-cache:v1.1.31
  exporter: nginx/nginx-prometheus-exporter:1.0
  certificates: <your-registry>/<your-repo>/nvcf-proxy-tls-certs:1.2.0
  secrets:
    - nvcr-creds

cache:
  keyStorageSize: 50m
  maxSize: 180g
  inactive: 1d
  valid: 1h

persistentVolumeClaim:
  sizeGB: 100
  storageClassName: gp3  # Use gp3 for AWS EKS, adjust for other platforms
  sizeProxyGB: 100

service:
  type: ClusterIP
  port: 30345

metrics:
  cacheMetricsStorageSize: 300m
  throughputHistogramBuckets: 25000000, 30000000, 35000000, 40000000, 50000000, 60000000, 80000000, 100000000

resources:
  requests:
    memory: 2Gi
    cpu: "1"
  limits:
    memory: 4Gi
    cpu: "2"

traces:
  enabled: false

nucleus:
  enabled: false

vault:
  enabled: false

monitoring:
  enabled: false
```

## Configuration Sections

### Replicas

The number of Container Cache pods is controlled through the `replicaCount` value. Container Cache replicas operate independently and distribute requests from worker nodes.

```yaml
# values.yaml

# Min Value: 1
# Recommended Value: 3
replicaCount: 3
```

<Info>
Container Cache is designed to scale horizontally to handle increased load.

</Info>

### Node Selection

Container Cache pods are scheduled on nodes with appropriate labels to ensure they run on compute nodes. Adjust the node selector based on your cluster's node labeling scheme.

```yaml
# values.yaml

nodeSelector:
  nvcf.nvidia.com/workload: gpu  # Adjust based on your node labels, or remove if not using node labels
```

### Target Hosts

The Container Cache can proxy requests to multiple container registries. The default configuration includes both NGC and Docker Hub.

```yaml
# values.yaml

# Domain of target Host where the proxy passes the request to
targetHost: nvcr.io,docker.io
```

### Image Configuration

Container Cache uses specific images for the server, exporter, and certificates:

```yaml
# values.yaml

images:
  # Container Cache Nginx Proxy
  server: <your-registry>/<your-repo>/nvcf-container-cache:v1.1.31

  # Nginx Prometheus Exporter (public image, no mirroring required)
  exporter: nginx/nginx-prometheus-exporter:1.0

  # TLS Certificates
  certificates: <your-registry>/<your-repo>/nvcf-proxy-tls-certs:1.2.0

  # Image pull secret created in Step 2
  secrets:
    - nvcr-creds
```

Replace `<your-registry>/<your-repo>` with your registry path. BYOC users pulling
directly from NGC can use `nvcr.io/nvidia/nvcf-byoc` as the registry/repo path.

### Cache Configuration

The cache behavior is controlled through several parameters:

```yaml
# values.yaml

cache:
  # Size for storing cache keys
  keyStorageSize: 50m

  # Maximum size of the cache
  maxSize: 180g

  # Period a resource can remain in cache without being accessed
  inactive: 1d

  # Period a cache is valid if resource doesn't become inactive first
  valid: 1h
```

### Storage Configuration

Container Cache requires persistent storage for caching container images:

```yaml
# values.yaml

persistentVolumeClaim:
  # Size of persistent volume
  sizeGB: 100

  # Storage class for persistent volume claim
  storageClassName: gp3  # Use gp3 for Amazon EKS, adjust for other platforms

  # Size of persistent volume for proxy cache
  sizeProxyGB: 100
```

### Service Configuration

The service type and port can be configured based on your access requirements:

```yaml
# values.yaml

service:
  # Service type: ClusterIP, NodePort, or LoadBalancer
  type: ClusterIP

  # Port for the Container Cache service
  port: 30345
```

### Metrics Configuration

Container Cache includes Prometheus metrics for monitoring cache performance:

```yaml
# values.yaml

metrics:
  # Size for storing cache metrics
  cacheMetricsStorageSize: 300m

  # Bucket configuration for throughput histogram
  throughputHistogramBuckets: 25000000, 30000000, 35000000, 40000000, 50000000, 60000000, 80000000, 100000000
```

### Resource Requests and Limits

Resource requests and limits for the Container Cache StatefulSet pods are **required**. The
chart will fail to install without them.

```yaml
# values.yaml

resources:
  requests:
    memory: 2Gi
    cpu: "1"
  limits:
    memory: 4Gi
    cpu: "2"
```

<Info>
Adjust these values based on your cluster size and expected cache throughput. Larger
deployments with high pull rates may need more memory and CPU.

</Info>

## Architecture

### Container Cache Architecture

Container Cache consists of several components:

1. **Nginx Proxy Server**: Handles incoming requests and serves cached content
2. **Prometheus Exporter**: Provides metrics for monitoring
3. **Persistent Storage**: Stores cached container images
4. **DaemonSet**: Configures containerd on worker nodes to use the cache

### Data Flow

1. **Initial Request**: Worker node requests a container image
2. **Cache Check**: Container Cache checks if image is cached locally
3. **Cache Hit**: If cached, serve image directly from local storage
4. **Cache Miss**: If not cached, fetch from upstream registry and cache locally
5. **Response**: Return image to requesting worker node

## Performance Considerations

### Cache Size

The cache size should be sized based on your workload requirements:

- **Small deployments**: 50-100GB
- **Medium deployments**: 100-500GB
- **Large deployments**: 500GB+

### Storage Performance

For optimal performance, use high-performance storage:

- **AWS**: Use gp3 or io1/io2 EBS volumes
- **Azure**: Use Premium SSD storage
- **GCP**: Use SSD persistent disks

### Network Configuration

Container Cache should be deployed close to worker nodes to minimize network latency:

- Deploy in the same availability zone as worker nodes
- Use high-bandwidth network connections
- Consider using dedicated network interfaces for cache traffic

## Monitoring and Observability

### Metrics

Container Cache provides several key metrics:

- **Cache hit ratio**: Percentage of requests served from cache
- **Cache size**: Current size of cached data
- **Request throughput**: Number of requests per second
- **Response times**: Time to serve cached vs. uncached content

### Logging

Container Cache logs include:

- Cache hit/miss events
- Upstream registry communication
- Error conditions and troubleshooting information
- Performance metrics

## Troubleshooting

### Common Issues

**Cache Not Working**
: - Verify containerd configuration on worker nodes
  - Check network connectivity to Container Cache service
  - Ensure proper DNS resolution

**Low Cache Hit Ratio**
: - Review cache size configuration
  - Check cache eviction policies
  - Monitor storage performance

**Storage Issues**
: - Verify storage class availability
  - Check persistent volume claims
  - Monitor disk space usage

## Best Practices

### Deployment

- Deploy Container Cache before deploying workloads
- Use multiple replicas for high availability
- Monitor cache performance and adjust configuration as needed

### Configuration

- Size cache appropriately for your workload
- Use high-performance storage for better performance
- Configure appropriate cache eviction policies

### Maintenance

- Monitor cache hit ratios and adjust cache size as needed
- Regularly review and clean up unused cached images
- Update Container Cache images regularly for security patches
