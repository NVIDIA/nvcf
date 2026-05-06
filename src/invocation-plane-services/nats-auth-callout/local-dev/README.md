# DevSpace Development Guide

This document provides guidance on using DevSpace for local development and deployment of the nvcf-nats-auth-callout-service service.

## Prerequisites

- [DevSpace CLI](https://devspace.sh/docs/getting-started/installation) installed
- Docker installed and running
- kubectl configured to access your Kubernetes cluster
- Helm installed

## Required DevSpace Plugin

Before using DevSpace with this project, you need to install the required plugin:

```bash
devspace add plugin ssh://git@github.com/NVIDIA:12051/cds/devspaces/devspace-plugin-nv-core.git
```

This plugin provides additional functionality required for the nvcf-nats-auth-callout-service service development workflow.

## Quick Start

### Create Kind Cluster

First, create a local Kubernetes cluster using the nv-core plugin:

```bash
devspace nv-core kind create
```

This will give you an interactive prompt - select the **basic cluster no gpu** option. These configurations are pre-configured for:
- Ingress to work correctly with kind
- Proper node configuration for development

**Custom Configurations**: You can create custom kind configurations in `~/.nv-devspace/plugins/nv-core/clusters` and the plugin will make them available in the interactive prompt.

### Set DevSpace Namespace

After creating the cluster, set the namespace that DevSpace will use for the nvcf-nats-auth-callout-service service:

```bash
devspace use namespace nvcf-nats-auth-callout-service
```

This tells DevSpace to deploy the nvcf-nats-auth-callout-service service and its resources to the `nvcf-nats-auth-callout-service` namespace, keeping it organized and separate from other services.

## Development and Deployment Options

The nvcf-nats-auth-callout-service service supports multiple deployment profiles for different observability and monitoring requirements:

### Development Mode Commands

**Basic Development**:
```bash
devspace dev
```
Starts development environment with basic functionality (no observability).

**Development with Tracing**:
```bash
devspace dev -p tracing
```
Enables OpenTelemetry tracing with OTLP collector integration.

**Development with Metrics**:
```bash
devspace dev -p metrics
```
Enables Prometheus metrics collection and ServiceMonitor resources.

**Development with Full Observability**:
```bash
devspace dev -p obs
```
Enables both tracing and metrics for complete observability stack.

**Development with Debian Base Image**:
```bash
devspace dev -p debian
```
Uses Debian-based development container instead of Alpine (can be combined with other profiles).

### Production Deployment Commands

**Basic Production Deployment**:
```bash
devspace deploy
```
Deploys using production images (no observability).

**Production Images with Tracing**:
```bash
devspace deploy -p tracing
```
Production image deployment with OpenTelemetry tracing enabled.

**Production Images with Metrics**:
```bash
devspace deploy -p metrics
```
Production image deployment with Prometheus metrics collection enabled.

**Production Images with Full Observability**:
```bash
devspace deploy -p obs
```
Production image deployment with both tracing and metrics enabled.

## Profile Details

### Tracing Profile (`-p tracing`)
- Enables OpenTelemetry tracing in the nvcf-nats-auth-callout-service service
- Configures OTLP HTTP endpoint: `opentelemetry-collector.observability:4318`
- Uses insecure connection for development environments
- Requires observability infrastructure (OpenTelemetry Collector and Tempo) to be deployed

🔍 **[View Tracing Profile Architecture Diagram](docs/tracing-profile.md)**

### Metrics Profile (`-p metrics`) 
- Enables Prometheus metrics endpoint on the nvcf-nats-auth-callout-service service
- Creates ServiceMonitor resources for Prometheus scraping
- Enables metrics collection for the nvcf-nats-auth-callout-service service
- Requires Prometheus operator for ServiceMonitor functionality

📈 **[View Metrics Profile Architecture Diagram](docs/metrics-profile.md)**

### Observability Profile (`-p obs`)
- Combines both tracing and metrics profiles
- Provides complete observability stack integration
- Recommended for monitoring and development debugging
- Requires both OpenTelemetry Collector, Tempo, and Prometheus infrastructure

📊 **[View Observability Profile Architecture Diagram](docs/obs-profile.md)**

### Debian Profile (`-p debian`)
- Changes the development image from Alpine Linux to Debian
- **Size**: Debian images are larger (~300MB+) compared to Alpine (~50MB), but provide better compatibility
- **C Library**: Uses glibc instead of musl, offering better compatibility with certain Go packages and C dependencies
- **Package Management**: Uses `apt` package manager with access to extensive Debian repositories
- **Use Cases**: Choose Debian when you need:
  - Better compatibility with CGO-enabled packages
  - Specific Debian packages not available in Alpine
  - Troubleshooting compatibility issues with musl
- Only affects the development container, not the production image
- Can be combined with other profiles (e.g., `devspace dev -p debian,obs`)

## Development Workflow

All development commands follow the same workflow pattern:

1. **Start Development**: Run `devspace dev [profile]` to start the development environment
2. **Container Access**: You'll be dropped into the DevSpace development container
3. **Start Service**: Choose your development mode:
   - `make dev` - Start with hot reloading (recommended for active development)
   - `make dev-debug` - Start with debugger enabled (continues immediately)
   - `make dev-debug-suspend` - Start with debugger enabled (waits for IDE connection)
   - `make run` - Run normally without hot reloading
4. **Development Tools**: Additional commands available:
   - `make dev-test` - Run tests in development mode
   - `make dev-lint` - Run code analysis and linting
5. **Code Changes**: Edit code locally - changes are automatically synced to the container
6. **Hot Reloading**: When using `make dev`, the service automatically restarts on code changes using [Air](https://github.com/air-verse/air)
7. **Debugging**: Connect your IDE to port 5005 for debugging (when using debug modes)
8. **Testing**: Access the service at:
   - `http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080` (via ingress)
   - `http://localhost:8083` (via port forwarding)

### Available Development Commands

The development container provides several make targets optimized for development:

- **`make dev`** - Hot reloading development (recommended)
- **`make dev-debug`** - Development with debugger (non-blocking)
- **`make dev-debug-suspend`** - Development with debugger (waits for connection)
- **`make run`** - Standard run without hot reloading
- **`make dev-test`** - Run tests in development environment
- **`make dev-lint`** - Run linting and code analysis

### Proxied Host Commands

The following commands are proxied to your host machine when run from the container:
- `devspace` - DevSpace CLI commands
- `kubectl` - Kubernetes CLI commands
- `helm` - Helm CLI commands
- `git` - Git commands with host credentials

For detailed configuration options, see the [Configuration Guide](../docs/CONFIGURATION.md).

## Configuration Overview

### Images

The DevSpace configuration builds two images:

- **nvcf-nats-auth-callout-service**: Production-ready image
- **nvcf-nats-auth-callout-service-dev**: Development image with debugging tools

### Dependencies

The service is designed as a stateless microservice with minimal external dependencies:

- **NGINX Ingress Controller**: Provides HTTP ingress routing to the service
- **Optional Observability Stack**: OpenTelemetry Collector, Tempo, and Prometheus (profile-dependent)

#### Profile-Specific Dependencies

Each profile deploys different combinations of infrastructure:

**Basic Profile** (no flags):
- NGINX ingress controller in `managed` namespace
- nvcf-nats-auth-callout-service service deployment with basic HTTP API

🏗️ **[View Basic Profile Architecture Diagram](docs/basic-profile.md)**

**Tracing Profile** (`-p tracing`):
- All basic profile components, plus:
- OpenTelemetry Collector in `observability` namespace
- Tempo distributed tracing in `observability` namespace

**Metrics Profile** (`-p metrics`):
- All basic profile components, plus:
- Prometheus server (StatefulSet) in `observability` namespace
- Grafana dashboard in `observability` namespace
- Prometheus Node Exporter (DaemonSet) in `observability` namespace
- Kube State Metrics in `observability` namespace
- ServiceMonitors for automatic metrics collection

**Observability Profile** (`-p obs`):
- All components from basic, tracing, and metrics profiles
- Complete observability stack with both metrics and tracing integration

### Environment Variables

The service supports configuration through environment variables with the `NVCF_NATS_AUTH_CALLOUT_SERVICE` prefix:

- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT`: HTTP server port (default: 8080)
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME`: Service name for identification and tracing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED`: Enable Prometheus metrics endpoint
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED`: Enable distributed tracing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL`: Log level (debug, info, warn, error)

For a complete list of configuration options, see the [Configuration Guide](../docs/CONFIGURATION.md).

### Port Forwarding

Development mode forwards these ports:

- `5005`: Debug port for Go debugging
- `8083`: Service HTTP port forwarded to container's port 8080

## Available Commands

### Built-in Pipelines

```bash
# Start development environment (basic)
devspace dev

# Start development with observability profiles
devspace dev -p tracing
devspace dev -p metrics  
devspace dev -p obs

# Deploy with production images (basic)
devspace deploy

# Deploy production images with observability profiles
devspace deploy -p tracing
devspace deploy -p metrics
devspace deploy -p obs

# Deploy only dependencies
devspace run-pipeline deps
```

### Build Commands

```bash
# Build all defined images and push them into the kind cluster nodes
devspace build

# Force rebuild all images and push them into the kind cluster nodes
devspace build -b

# Build specific image and push it into the kind cluster nodes
devspace build nvcf-nats-auth-callout-service

# Force rebuild specific image
devspace build nvcf-nats-auth-callout-service -b
```

### Reset Commands

```bash
# Reset pods (replaces development image with production image)
devspace reset pods

# Reset variables cache
devspace reset vars
```

## Service Endpoints

Once the service is running, you can access the following endpoints:

### Health and Status Endpoints

```bash
# Health check endpoint
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/healthz
curl http://localhost:8083/healthz

# API ping endpoint  
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/v1/ping
curl http://localhost:8083/v1/ping
```

### Metrics Endpoint (when enabled)

```bash
# Prometheus metrics (only available when metrics are enabled)
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/metrics
curl http://localhost:8083/metrics
```

### API Documentation

```bash
# Swagger/OpenAPI documentation
http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/swagger/index.html
http://localhost:8083/swagger/index.html
```

## File Synchronization

DevSpace automatically syncs your local project directory with the container, enabling:
- Real-time code changes
- Local development experience
- Automatic hot reloading with Air

## SSH Access

Development containers include SSH access for IDE integration:
- SSH is enabled by default in dev mode
- Allows remote debugging and development

## Proxy Commands

The following commands are available in the development container:
- `devspace`
- `kubectl`
- `helm`
- Git credentials are automatically forwarded

## Troubleshooting

### Common Issues

1. **Port conflicts**: Change port forwarding in `devspace.yaml` if ports are already in use
2. **Image build failures**: Ensure Docker is running and has sufficient resources
3. **Service not starting**: Check configuration and environment variables

### Debugging

1. **Check pod status**: `kubectl get pods -n nvcf-nats-auth-callout-service`
2. **View logs**: `kubectl logs -f <pod-name> -n nvcf-nats-auth-callout-service`
3. **Check service configuration**: Use `./nvcf-nats-auth-callout-service config show` inside the container
4. **Verify service endpoints**: Test health endpoints to confirm service is running
5. **Reset pods**: `devspace reset pods` to replace development image with production image
6. **Clear variable cache**: `devspace reset vars` if environment variables seem stale
7. **Rebuild images**: `devspace build` to rebuild and push images

### Configuration Debugging

```bash
# Inside the development container, check current configuration
./nvcf-nats-auth-callout-service config show

# Show configuration in different formats
./nvcf-nats-auth-callout-service config show --format json
./nvcf-nats-auth-callout-service config show --format yaml

# Debug configuration loading
./nvcf-nats-auth-callout-service config debug

# Validate configuration
./nvcf-nats-auth-callout-service config validate
```

### Profile-Specific Troubleshooting

**Observability Issues**:
- For tracing issues, verify OpenTelemetry Collector and Tempo are running in the `observability` namespace
- For metrics issues, check Prometheus operator and ServiceMonitor resources
- Use `kubectl get servicemonitor -n nvcf-nats-auth-callout-service` to verify ServiceMonitor creation
- Check observability infrastructure status: `kubectl get pods -n observability`

**Service Access Issues**:
- Verify ingress is working: `kubectl get ingress -n nvcf-nats-auth-callout-service`
- Check service status: `kubectl get svc -n nvcf-nats-auth-callout-service`
- Test direct pod access: `kubectl port-forward <pod-name> 8080:8080 -n nvcf-nats-auth-callout-service`

### Cleanup

To clean up the development environment, use `devspace purge` with the same profile that was used for development or deployment:

```bash
# Clean up basic deployment (no profiles used)
devspace purge

# Clean up deployment with tracing profile
devspace purge -p tracing

# Clean up deployment with metrics profile  
devspace purge -p metrics

# Clean up deployment with full observability
devspace purge -p obs

# Clean up deployment with combined profiles (e.g., debian + obs)
devspace purge -p debian,obs
```

**Important**: Always use the same profile(s) with `devspace purge` that you used with `devspace dev` or `devspace deploy` to ensure all resources created by that profile are properly removed from the cluster.

This will remove all deployed resources from the cluster.

## Advanced Configuration

### Custom Values

You can customize the deployment by modifying the `values` section in `devspace.yaml`:

```yaml
values:
  containers:
    - image: ${runtime.images.nvcf-nats-auth-callout-service.image}:${runtime.images.nvcf-nats-auth-callout-service.tag}
      env:
        - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME
          value: "custom-nvcf-nats-auth-callout-service"
        - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL
          value: "debug"
```

### Build Arguments

The Docker build process supports custom build arguments:

```yaml
buildArgs:
  TARGETARCH: amd64
  TARGETPLATFORM: linux/amd64
```

## Usage Examples

### Common Development Scenarios

**Basic Development (No Observability)**:
```bash
devspace dev
# Then run: make dev
```
Use this for general development with hot reloading when you don't need tracing or metrics.

**Debugging Performance Issues**:
```bash
devspace dev -p obs
```
Use full observability to trace requests and monitor performance metrics.

**Testing Tracing Integration**:
```bash
devspace dev -p tracing
```
Use when developing or testing OpenTelemetry tracing features.

**Metrics Development**:
```bash
devspace dev -p metrics
```
Use when working on Prometheus metrics or monitoring dashboards.

**Development with Custom Environment**:
```bash
devspace dev -p debian,obs
```
Combine profiles for Debian-based development with full observability.

### Production Deployment Scenarios

**Staging Environment with Full Observability**:
```bash
devspace deploy -p obs
```
Deploy production images to staging with complete monitoring stack.

**Production Images with Selective Monitoring**:
```bash
devspace deploy -p metrics
```
Deploy production images with metrics only (if tracing is handled separately).

## Testing Your Service

### Manual Testing

```bash
# Test basic functionality
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/v1/ping

# Check health status
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/healthz

# View metrics (when enabled)
curl http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/metrics

# Access API documentation
# Open in browser: http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/swagger/index.html
```

### Automated Testing

```bash
# Inside the development container
make dev-test  # Run tests in development mode
make test      # Run standard test suite
make test-coverage  # Run tests with coverage
```

## Best Practices

1. **Use appropriate profiles** for your development needs - avoid unnecessary overhead
2. **Use `obs` profile** for comprehensive debugging and performance analysis
3. **Use `debian` profile** when you need specific Debian packages or tooling during development
4. **Combine profiles** as needed (e.g., `devspace dev -p debian,tracing`)
5. **Test with production images** using `devspace deploy` commands to validate production builds
6. **Monitor resources** to ensure adequate cluster capacity, especially with observability
7. **Regular cleanup** of unused deployments and images
8. **Use configuration validation** to ensure proper service setup

## Additional Resources

- [DevSpace Documentation](https://devspace.sh/docs)
- [Kubernetes Documentation](https://kubernetes.io/docs/)
- [Helm Documentation](https://helm.sh/docs/) 
- [Configuration Guide](../docs/CONFIGURATION.md)
- [OpenTelemetry Documentation](https://opentelemetry.io/docs/)
- [Prometheus Documentation](https://prometheus.io/docs/) 