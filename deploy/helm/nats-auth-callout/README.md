# nvcf-nats-auth-callout-service Service Helm Chart

This Helm chart deploys the nvcf-nats-auth-callout-service service with comprehensive configuration validation.

## Configuration Validation

The chart includes built-in validation to ensure proper configuration before deployment. The validation checks are automatically executed during `helm install` or `helm template` operations.

### Database Configuration Validation

When using PostgreSQL as the database provider, you must provide authentication credentials:

```yaml
# Option 1: Use existing secret
serviceConfig:
  database:
    provider: postgres
postgres:
  existingSecret: my-postgres-secret

# Option 2: Provide username and password (will create secret)
serviceConfig:
  database:
    provider: postgres
postgres:
  username: myuser
  password: mypass
```

**Validation Rules:**
- If `database.provider` is `postgres` or `postgresql`, either:
  - `postgres.existingSecret` must be set, OR
  - Both `postgres.username` AND `postgres.password` must be provided
- Supported database providers: `postgres`, `postgresql`, `sqlite`, `sqlite3`

### Metrics Configuration Validation

When metrics are enabled, a port must be specified:

```yaml
metrics:
  enabled: true
  port: "9090"  # Required when enabled=true
```

**ServiceMonitor Validation:**
ServiceMonitor can only be enabled when metrics are enabled:

```yaml
# Valid: Both metrics and ServiceMonitor enabled
metrics:
  enabled: true
  port: "9090"
  serviceMonitor:
    enabled: true

# Valid: Both metrics and ServiceMonitor disabled
metrics:
  enabled: false
  serviceMonitor:
    enabled: false

# Invalid: ServiceMonitor enabled but metrics disabled
metrics:
  enabled: false
  serviceMonitor:
    enabled: true  # This will fail validation
```

**Dashboard Validation:**
Grafana dashboard can only be enabled when metrics are enabled:

```yaml
# Valid: Both metrics and dashboard enabled
metrics:
  enabled: true
  port: "9090"
dashboard:
  enabled: true

# Valid: Both metrics and dashboard disabled
metrics:
  enabled: false
dashboard:
  enabled: false

# Invalid: Dashboard enabled but metrics disabled
metrics:
  enabled: false
dashboard:
  enabled: true  # This will fail validation
```

The dashboard configuration supports additional options:

```yaml
dashboard:
  enabled: true
  grafana:
    # Labels for dashboard discovery by Grafana operator
    labels:
      dashboards: "grafana"
    # Additional labels for the dashboard
    additionalLabels: {}
    # Allow cross-namespace import of the dashboard
    allowCrossNamespaceImport: true
    # Resync period for the dashboard
    resyncPeriod: "30s"
```

### Health Checks Configuration

The chart includes comprehensive health check probe configuration:

```yaml
healthChecks:
  # Liveness probe - determines when to restart container
  livenessProbe:
    enabled: true
    httpGet:
      path: "/healthz"
      port: "http" 
      scheme: "HTTP"
    initialDelaySeconds: 10
    periodSeconds: 10
    timeoutSeconds: 5
    failureThreshold: 3
    successThreshold: 1

  # Readiness probe - determines when container is ready to serve traffic
  readinessProbe:
    enabled: true
    httpGet:
      path: "/healthz"
      port: "http"
      scheme: "HTTP"
    initialDelaySeconds: 5
    periodSeconds: 5
    timeoutSeconds: 3
    failureThreshold: 3
    successThreshold: 1

  # Startup probe - for slow-starting containers
  startupProbe:
    enabled: false  # Disabled by default
    httpGet:
      path: "/healthz"
      port: "http"
      scheme: "HTTP"
    initialDelaySeconds: 0
    periodSeconds: 10
    timeoutSeconds: 5
    failureThreshold: 30  # Allow up to 5 minutes for startup
    successThreshold: 1
```

**Available Health Endpoints:**
- `/healthz` - Primary health check endpoint (recommended)
- `/health` - Alternative health check endpoint
- `/v1/ping` - Basic connectivity test

### Tracing Configuration Validation

When tracing is enabled, authentication credentials must be provided:

```yaml
# Option 1: Use existing secret
tracing:
  enabled: true
  existingSecret: my-tracing-secret

# Option 2: Provide token or API key
tracing:
  enabled: true
  token: my-token
  # OR
  api_key: my-api-key
```

## Testing Validation

You can test the validation rules using the provided test script:

```bash
./scripts/test-helm-validation.sh
```

Or manually test specific configurations:

```bash
# Test valid configuration
helm template test-release ./deploy --set serviceConfig.database.provider=sqlite

# Test invalid configuration (should fail)
helm template test-release ./deploy \
  --set serviceConfig.database.provider=postgres \
  --set postgres.username="" \
  --set postgres.password="" \
  --set postgres.existingSecret=""
```

## Installation Examples

### SQLite (Default)
```bash
helm install my-release ./deploy
```

### PostgreSQL with credentials
```bash
helm install my-release ./deploy \
  --set serviceConfig.database.provider=postgres \
  --set postgres.username=myuser \
  --set postgres.password=mypass
```

### PostgreSQL with existing secret
```bash
# Create secret first
kubectl create secret generic my-postgres-secret \
  --from-literal=postgres_username=myuser \
  --from-literal=postgres_password=mypass

# Install with existing secret
helm install my-release ./deploy \
  --set serviceConfig.database.provider=postgres \
  --set postgres.existingSecret=my-postgres-secret
```

### With metrics enabled
```bash
helm install my-release ./deploy \
  --set metrics.enabled=true \
  --set metrics.port=9090
```

### With metrics and Grafana dashboard enabled
```bash
helm install my-release ./deploy \
  --set metrics.enabled=true \
  --set metrics.port=9090 \
  --set dashboard.enabled=true
```

### With custom health check configuration
```bash
helm install my-release ./deploy \
  --set healthChecks.livenessProbe.initialDelaySeconds=15 \
  --set healthChecks.readinessProbe.periodSeconds=10 \
  --set healthChecks.startupProbe.enabled=true
```

### With health checks disabled
```bash
helm install my-release ./deploy \
  --set healthChecks.livenessProbe.enabled=false \
  --set healthChecks.readinessProbe.enabled=false
```

## Validation Error Examples

The chart will fail with helpful error messages for invalid configurations:

```
Error: When database.provider is 'postgres', either postgres.existingSecret must be set OR both postgres.username and postgres.password must be provided

Error: Unsupported database provider 'mysql'. Supported providers are: postgres, postgresql, sqlite, sqlite3

Error: When metrics.enabled is true, metrics.port must be specified

Error: When metrics.enabled is false, metrics.serviceMonitor.enabled cannot be true. ServiceMonitor requires metrics to be enabled.

Error: When metrics.enabled is false, dashboard.enabled cannot be true. Dashboard requires metrics to be enabled.
```

## Values File Structure

See `values.yaml` for the complete configuration structure and all available options. 