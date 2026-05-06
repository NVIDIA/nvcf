# Configuration

The application supports multiple configuration methods with a clear precedence order. Configuration is managed using [Viper](https://github.com/spf13/viper) and supports various formats and sources.

## Configuration Methods

Configuration can be provided through the following methods (in order of precedence, highest to lowest):

1. **Command Line Flags** (highest priority)
2. **Environment Variables** with `NVCF_NATS_AUTH_CALLOUT_SERVICE` prefix
3. **Custom Configuration File** (via `--config` flag or `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH` environment variable)
4. **Secrets File** (via `--secrets-file` flag or `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH` environment variable)
5. **Embedded Default Configuration** (lowest priority)

The application uses embedded default configuration, which means default values are compiled into the binary from the config.yaml file. This eliminates hardcoded defaults and ensures consistency between development and production environments.

## Available CLI Commands

```bash
# Server commands
./nvcf-nats-auth-callout-service server --help              # Show server options
./nvcf-nats-auth-callout-service server --config FILE       # Use custom config file
./nvcf-nats-auth-callout-service server --secrets-file FILE # Use secrets file
./nvcf-nats-auth-callout-service server --port 8080         # Set server port
./nvcf-nats-auth-callout-service server --service-name NAME # Set service name
./nvcf-nats-auth-callout-service server --nats-url URL      # Set NATS server URL
./nvcf-nats-auth-callout-service server --nkey-seed SEED    # Set NKey seed for authentication
./nvcf-nats-auth-callout-service server --nkey-signature SIG # Set NKey signature for JWT signing

# Version commands  
./nvcf-nats-auth-callout-service version                     # Show version info
./nvcf-nats-auth-callout-service version --json             # Show version as JSON

# Configuration management
./nvcf-nats-auth-callout-service config show                # Show current configuration
./nvcf-nats-auth-callout-service config debug               # Show configuration debug info
./nvcf-nats-auth-callout-service config validate            # Validate configuration
```

## Quick Start

```bash
# Run with default configuration
make run

# Run with hot reloading (recommended for development)
make dev

# Show current configuration
make build && ./nvcf-nats-auth-callout-service config show

# Or using go run for one-off configuration checks
go run ./cmd/nvcf-nats-auth-callout-service config show
```

## Configuration Options

The service supports configuration through multiple sources (in order of precedence):
1. **CLI Flags**: `--port 8080 --service-name my-service --config config.yaml --secrets-file secrets.json`
2. **Environment Variables**: `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=8080 NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME=my-service NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=secrets.json`
3. **Config File**: `--config /path/to/config.yaml`
4. **Secrets File**: `--secrets-file /path/to/secrets.json`
5. **Embedded Defaults**: Built-in configuration

### Core Configuration

| Setting | CLI Flag | Environment Variable | Config File Path | Default Value | Description |
|---------|----------|---------------------|------------------|---------------|-------------|
| Server Port | `--port`, `-p` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT` | `server.port` | `8080` | HTTP server port |
| Service Name | `--service-name` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME` | `service.name` | `nvcf-nats-auth-callout-service` | Service identifier |
| Config File | `--config`, `-c` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH` | - | embedded defaults | Path to configuration file |
| Secrets File | `--secrets-file`, `-s` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH` | - | none | Path to secrets file |

### NATS Configuration

| Setting | CLI Flag | Environment Variable | Config File Path | Default Value | Description |
|---------|----------|---------------------|------------------|---------------|-------------|
| NATS URL | `--nats-url` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL` | `service.nats_url` | `nats://localhost:4222` | NATS server URL |
| NKey Seed | `--nkey-seed` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED` | `service.nkey_seed` | - | NKey seed for authentication |
| NKey Signature | `--nkey-signature` | `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE` | `service.nkey_signature` | - | Signing key seed for JWT signing |

### Essential Configuration

```yaml
# Basic service configuration
server:
  port: "8080"
service:
  name: "nvcf-nats-auth-callout-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: "SUA..." # NKey seed for authentication
  nkey_signature: "SAA..." # Signing key seed for JWT signing

# Enable metrics collection
metrics:
  enabled: true
  port: "9090"

# Enable distributed tracing
tracing:
  enabled: true
  provider: "otel"  # or "lightstep"
  otel:
    endpoint: "http://jaeger:4317"
    insecure: true
```

### NKey Configuration

The service requires two NKey seeds for operation:

1. **NKey Seed** (`nkey_seed`): Used for authenticating with the NATS server
   - Must be a user key (starts with `SU`)
   - Generated using `nsc generate --user`

2. **NKey Signature** (`nkey_signature`): Used for signing JWT tokens
   - Must be an account or operator key (starts with `SA` or `SO`)
   - Generated using `nsc generate --account` or `nsc generate --operator`

**Security Note**: NKey seeds are sensitive credentials and should be:
- Stored securely (e.g., in Kubernetes secrets, Vault, or environment variables)
- Never committed to version control
- Rotated regularly

**Example Configuration:**
```bash
# Using environment variables (recommended for production)
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED="SUA..."
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE="SAA..."

# Using CLI flags
./nvcf-nats-auth-callout-service server \
  --nkey-seed "SUA..." \
  --nkey-signature "SAA..." \
  --nats-url "nats://localhost:4222"

# Using config file
service:
  nkey_seed: "SUA..."
  nkey_signature: "SAA..."
  nats_url: "nats://localhost:4222"
```

## Logging Configuration

The application supports comprehensive logging configuration:

| Setting | Environment Variable | Config File Path | Default Value | Description |
|---------|---------------------|------------------|---------------|-------------|
| Log Level | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL` | `logging.level` | `info` | Log level (debug, info, warn, error, panic, fatal) |
| Log Format | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FORMAT` | `logging.format` | `json` | Log format (json, console, text) |
| Log Output | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_OUTPUT` | `logging.output` | `stdout` | Log output (stdout, stderr) |
| Show Caller | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_CALLER` | `logging.caller` | `true` | Include caller information |
| Stacktrace Level | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_STACKTRACE_LEVEL` | `logging.stacktrace_level` | `error` | Stacktrace level |
| Development Mode | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_DEVELOPMENT` | `logging.development` | `false` | Development mode logging |
| Service Name Field | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FIELDS_SERVICE_NAME` | `logging.fields.service_name` | - | Service name in logs |
| Environment Field | `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FIELDS_ENVIRONMENT` | `logging.fields.environment` | - | Environment name in logs |

**Note**: The version field in logs is automatically populated from build-time version injection and cannot be configured.

## Metrics Configuration

Metrics are **disabled by default** and must be explicitly enabled. The service uses Prometheus metrics format.

### Metrics Settings

| Setting | Environment Variable | Config File Path | Default Value | Description |
|---------|---------------------|------------------|---------------|-------------|
| Metrics Enabled | `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED` | `metrics.enabled` | `false` | Enable/disable metrics collection |
| Metrics Port | `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_PORT` | `metrics.port` | `9090` | Port for metrics endpoint (when enabled) |

### Enabling Metrics

**Via Configuration File:**
```yaml
# config.yaml
metrics:
  enabled: true
  port: "9090"
```

**Via Environment Variable:**
```bash
NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true make dev
```

**Via Command Line (with environment variable):**
```bash
NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true ./nvcf-nats-auth-callout-service server --port 8080
```

### Accessing Metrics

When metrics are enabled, they are available at the `/metrics` endpoint:

```bash
# Local development
curl http://localhost:8080/metrics

# With DevSpace port forwarding
curl http://localhost:8083/metrics

# Production deployment
curl http://your-service-host:8080/metrics
```

### Available Metrics

The service exposes standard Go runtime metrics plus custom application metrics:
- **HTTP Request Duration**: Request processing time by endpoint
- **HTTP Request Count**: Number of requests by endpoint and status
- **Go Runtime Metrics**: Memory, GC, goroutines, etc.

### Health Endpoints

These endpoints are always available regardless of metrics configuration:

```bash
# Health check endpoint
curl http://localhost:8080/healthz

# API ping endpoint
curl http://localhost:8080/v1/ping
```

## Using Command Line Flags

```bash
# Basic usage
./nvcf-nats-auth-callout-service server --port 9090 --service-name my-service

# Using short flags
./nvcf-nats-auth-callout-service server -p 9090

# With NATS and NKey configuration
./nvcf-nats-auth-callout-service server \
  --port 9090 \
  --service-name my-service \
  --nats-url nats://localhost:4222 \
  --nkey-seed "SUA..." \
  --nkey-signature "SAA..."

# With custom config file
./nvcf-nats-auth-callout-service server --config /path/to/my-config.yaml --port 9090

# With secrets file
./nvcf-nats-auth-callout-service server --secrets-file /path/to/secrets.json

# With both config and secrets files
./nvcf-nats-auth-callout-service server --config /path/to/my-config.yaml --secrets-file /path/to/secrets.json

# Get help
./nvcf-nats-auth-callout-service server --help
```

## Using Environment Variables

All environment variables use the `NVCF_NATS_AUTH_CALLOUT_SERVICE` prefix to avoid conflicts with other services:

```bash
# Set environment variables
export NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=/path/to/config.yaml
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=/path/to/secrets.json
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME=my-service
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL=nats://localhost:4222
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED="SUA..."
export NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE="SAA..."
export NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL=debug
export NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FORMAT=console
export NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true

# Run the server
./nvcf-nats-auth-callout-service server

# Or run with inline environment variables
NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME=my-service NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL=nats://localhost:4222 NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=/path/to/secrets.json ./nvcf-nats-auth-callout-service server
```

## Using Configuration Files

You can create custom configuration files and specify them with the `--config` flag or `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH` environment variable. You can also use separate secrets files with the `--secrets-file` flag or `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH` environment variable for sensitive configuration data:

**my-config.yaml**:
```yaml
server:
  port: "9090"
service:
  name: "my-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: "SUA..." # NKey seed for authentication
  nkey_signature: "SAA..." # Signing key seed for JWT signing
logging:
  level: "debug"
  format: "console"
  fields:
    service_name: "my-service"
    environment: "development"
metrics:
  enabled: true
  port: "9090"
tracing:
  enabled: true
  provider: "otel"
  otel:
    endpoint: "http://jaeger:4317"
    insecure: true
```

**my-secrets.json**:
```json
{
  "service": {
    "nkey_seed": "SUA...",
    "nkey_signature": "SAA..."
  },
  "tracing": {
    "lightstep": {
      "access_token": "sensitive-lightstep-token"
    }
  }
}
```

Then run:
```bash
# Using CLI flags
./nvcf-nats-auth-callout-service server --config my-config.yaml
./nvcf-nats-auth-callout-service server --secrets-file my-secrets.json
./nvcf-nats-auth-callout-service server --config my-config.yaml --secrets-file my-secrets.json

# Using environment variables
NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=my-config.yaml ./nvcf-nats-auth-callout-service server
NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=my-secrets.json ./nvcf-nats-auth-callout-service server
NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=my-config.yaml NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=my-secrets.json ./nvcf-nats-auth-callout-service server

# CLI flags take precedence over environment variables
NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH=other-config.yaml ./nvcf-nats-auth-callout-service server --config my-config.yaml
NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=other-secrets.json ./nvcf-nats-auth-callout-service server --secrets-file my-secrets.json
```

## Configuration Precedence Examples

### Example 1: Command line overrides everything
```bash
# Config file has port: "8080"
# Environment variable: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090
# Command line flag: --port 3000
# Result: Server runs on port 3000 (command line wins)

NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 ./nvcf-nats-auth-callout-service server --config my-config.yaml --port 3000
```

### Example 2: Environment variables override config file
```bash
# Config file has port: "8080"
# Environment variable: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090
# Result: Server runs on port 9090 (environment variable wins)

NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 ./nvcf-nats-auth-callout-service server --config my-config.yaml
```

### Example 3: Custom config file overrides embedded defaults
```bash
# Embedded defaults: port "8080"
# Custom config file: port "9090"
# Result: Server runs on port 9090 (custom config wins)

./nvcf-nats-auth-callout-service server --config my-config.yaml
```

## Configuration Management Commands

The application provides helpful commands for configuration management:

```bash
# Show current configuration
./nvcf-nats-auth-callout-service config show

# Show configuration in different formats
./nvcf-nats-auth-callout-service config show --format json
./nvcf-nats-auth-callout-service config show --format yaml

# Debug configuration (shows all sources and values)
./nvcf-nats-auth-callout-service config debug

# Validate configuration
./nvcf-nats-auth-callout-service config validate
```

## Advanced Configuration Examples

```bash
# Run with custom port
make build && ./nvcf-nats-auth-callout-service server --port 9090

# Run with environment variables
NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 make run

# Run with custom config file
make build && ./nvcf-nats-auth-callout-service server --config my-config.yaml

# Run with secrets file
make build && ./nvcf-nats-auth-callout-service server --secrets-file my-secrets.json

# Run with both config and secrets files
make build && ./nvcf-nats-auth-callout-service server --config my-config.yaml --secrets-file my-secrets.json

# Show configuration with specific format
make build && ./nvcf-nats-auth-callout-service config show --format json
make build && ./nvcf-nats-auth-callout-service config show --format yaml
```

## Logging Examples

```bash
# Run with console logging for development
NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FORMAT=console NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL=debug ./nvcf-nats-auth-callout-service server

# Run with JSON logging for production
NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FORMAT=json NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL=info ./nvcf-nats-auth-callout-service server

# Add custom fields to logs
NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FIELDS_ENVIRONMENT=production NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FIELDS_SERVICE_NAME=my-service ./nvcf-nats-auth-callout-service server
```

## DevSpace Integration

When using DevSpace for development, you can:

1. **Override via environment variables in devspace.yaml:**
   ```yaml
   dev:
     nvcf-nats-auth-callout-service:
       env:
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT
           value: "9090"
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL
           value: "nats://localhost:4222"
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED
           value: "SUA..." # NKey seed for authentication
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE
           value: "SAA..." # Signing key seed for JWT signing
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL
           value: "debug"
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_FORMAT
           value: "console"
         - name: NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED
           value: "true"
   ```

2. **Use port forwarding:**
   ```yaml
   dev:
     ports:
       - imageSelector: nvcf-nats-auth-callout-service
         forward:
           - port: 8080
             remotePort: 9090  # if you changed the port via config
   ```

## Troubleshooting Configuration

1. **Check what configuration is being used:**
   ```bash
   ./nvcf-nats-auth-callout-service config debug
   ```

2. **Validate your configuration:**
   ```bash
   ./nvcf-nats-auth-callout-service config validate
   ```

3. **Check version information:**
   ```bash
   ./nvcf-nats-auth-callout-service version
   ```

4. **Check if metrics are enabled:**
   ```bash
   ./nvcf-nats-auth-callout-service config show | grep metrics
   ```

5. **Common environment variable naming:**
   - Config file path → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH`
   - Secrets file path → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH`
   - Config key `server.port` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT`
   - Config key `service.name` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME`
   - Config key `service.nats_url` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL`
   - Config key `service.nkey_seed` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED`
   - Config key `service.nkey_signature` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE`
   - Config key `logging.level` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL`
   - Config key `metrics.enabled` → Environment variable `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED`
   - Kebab-case in config becomes SNAKE_CASE in environment variables
   - Dots (.) become underscores (_) in environment variables

6. **Check if config file is being loaded:**
   The debug command shows which config file (if any) is being used.

## Embedded Configuration System

The application uses Go's `embed` directive to include the default configuration file (`cmd/nvcf-nats-auth-callout-service/config.yaml`) directly in the compiled binary. This provides several benefits:

1. **No hardcoded defaults** - All defaults come from the same config file used in development
2. **Consistency** - Development and production use identical default values  
3. **Self-contained binaries** - The application works without external config files
4. **Override flexibility** - Environment variables and CLI flags still take precedence

The embedded configuration is loaded automatically when the application starts and serves as the baseline configuration that can be overridden by higher-priority sources.

## Production Configuration

For production deployments:

1. **Use environment variables for sensitive or environment-specific values**
2. **Use custom config files for static configuration overrides**
3. **Enable metrics for monitoring:**
   ```bash
   NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true ./nvcf-nats-auth-callout-service server
   ```
4. **Validate configuration before deployment:**
   ```bash
   ./nvcf-nats-auth-callout-service config validate
   ```
5. **The binary includes embedded defaults** - No need to distribute config files with the binary
6. **Version information is automatically included in logs** - No need to configure version fields

## Multi-Provider Tracing

The application includes comprehensive tracing support with multiple provider options and automatic runtime attribute detection for Kubernetes environments.

### Tracing Configuration

Tracing is **disabled by default** and configured through the standard configuration system, supporting all configuration methods (CLI flags, environment variables, config files, embedded defaults).

### Supported Providers

- **OpenTelemetry (otel)**: Direct OTLP integration with HTTP and gRPC endpoints
- **Lightstep**: Dedicated Lightstep integration with access token authentication  
- **Noop**: No-operation provider when tracing is disabled (default)

### Enabling Tracing

**Via Configuration File:**
```yaml
# config.yaml
tracing:
  enabled: true
  provider: "otel"  # OpenTelemetry
  otel:
    endpoint: "http://jaeger:4317"
    insecure: true
```

**Via Environment Variables:**
```bash
# Enable tracing manually
NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=otel make dev
```

**In Development with DevSpace:**
```bash
# Start with tracing profile in DevSpace
devspace dev -p tracing
```

### Provider Configuration

**Common Settings:**

| Setting | Environment Variable | Config File Path | Default Value | Description |
|---------|---------------------|------------------|---------------|-------------|
| Enabled | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED` | `tracing.enabled` | `false` | Enable/disable tracing |
| Provider | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER` | `tracing.provider` | `otel` | Tracing provider: otel, lightstep |

**OpenTelemetry Provider Settings:**

| Setting | Environment Variable | Config File Path | Default Value | Description |
|---------|---------------------|------------------|---------------|-------------|
| gRPC Endpoint | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_ENDPOINT` | `tracing.otel.endpoint` | - | OTLP gRPC endpoint |
| HTTP Endpoint | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HTTP_ENDPOINT` | `tracing.otel.http_endpoint` | - | OTLP HTTP endpoint (preferred) |
| Insecure | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_INSECURE` | `tracing.otel.insecure` | `false` | Use insecure connection |
| Environment | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_ENVIRONMENT` | `tracing.otel.environment` | - | Deployment environment |
| Sampling Ratio | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_SAMPLING_RATIO` | `tracing.otel.sampling_ratio` | `1.0` | Trace sampling ratio (0.0-1.0) |
| Timeout | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_TIMEOUT_MS` | `tracing.otel.timeout_ms` | `10000` | Export timeout (ms) |
| Compression | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_COMPRESSION` | `tracing.otel.compression` | `gzip` | Compression (gzip, none) |
| Retry Delay | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_RETRY_DELAY_MS` | `tracing.otel.retry_delay_ms` | `5000` | Retry delay (ms) |
| Authorization | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HEADERS_AUTHORIZATION` | `tracing.otel.headers.authorization` | - | Authorization header |
| API Key | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HEADERS_X_API_KEY` | `tracing.otel.headers.x_api_key` | - | X-API-Key header |

**Lightstep Provider Settings:**

| Setting | Environment Variable | Config File Path | Default Value | Description |
|---------|---------------------|------------------|---------------|-------------|
| Endpoint | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ENDPOINT` | `tracing.lightstep.endpoint` | `https://ingest.lightstep.com:443/traces/otel/v1` | Lightstep OTLP endpoint |
| Access Token | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ACCESS_TOKEN` | `tracing.lightstep.access_token` | - | Lightstep access token |
| Environment | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ENVIRONMENT` | `tracing.lightstep.environment` | - | Deployment environment |
| Sampling Ratio | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_SAMPLING_RATIO` | `tracing.lightstep.sampling_ratio` | `1.0` | Trace sampling ratio (0.0-1.0) |
| Timeout | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_TIMEOUT_MS` | `tracing.lightstep.timeout_ms` | `10000` | Export timeout (ms) |
| Compression | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_COMPRESSION` | `tracing.lightstep.compression` | `gzip` | Compression (gzip, none) |
| Insecure | `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_INSECURE` | `tracing.lightstep.insecure` | `false` | Use insecure connection |

### Automatic Runtime Detection

The tracing system automatically detects and includes Kubernetes runtime attributes:

- **Pod Name**: Auto-detected from `POD_NAME` environment variable (with `HOSTNAME` fallback)
- **Namespace**: Auto-detected from `POD_NAMESPACE` environment variable  
- **Node Name**: Auto-detected from `NODE_NAME` environment variable
- **Cluster Name**: Auto-detected from `CLUSTER_NAME` environment variable
- **Service Instance ID**: Generated from pod name or hostname

These are automatically injected in Kubernetes deployments via the Downward API.

### Configuration Examples

#### Environment Variables

**OpenTelemetry Provider:**
```bash
# Enable OpenTelemetry tracing with Jaeger
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=otel
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HTTP_ENDPOINT="http://jaeger:14268/api/traces"
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_INSECURE=true
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_SAMPLING_RATIO=0.1
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_ENVIRONMENT=production

./nvcf-nats-auth-callout-service server
```

**Lightstep Provider:**
```bash
# Enable Lightstep tracing
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=lightstep
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ACCESS_TOKEN="your-lightstep-token"
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ENVIRONMENT=production
export NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_SAMPLING_RATIO=0.1

./nvcf-nats-auth-callout-service server
```

#### Configuration File

**OpenTelemetry Provider:**
```yaml
tracing:
  enabled: true
  provider: "otel"
  otel:
    http_endpoint: "http://jaeger:14268/api/traces"
    insecure: true
    environment: "production"
    sampling_ratio: 0.1
    timeout_ms: 5000
    compression: "gzip"
    retry_delay_ms: 2000
    max_export_batch_size: 100
    export_timeout_ms: 10000
    max_queue_size: 1000
    schedule_delay_ms: 2000
    headers:
      authorization: "Bearer your-token"
      x_api_key: "your-api-key"
```

**Lightstep Provider:**
```yaml
tracing:
  enabled: true
  provider: "lightstep"
  lightstep:
    endpoint: "https://ingest.lightstep.com:443/traces/otel/v1"
    access_token: "your-lightstep-token"
    environment: "production"
    sampling_ratio: 0.1
    timeout_ms: 5000
    compression: "gzip"
    max_export_batch_size: 100
    export_timeout_ms: 10000
    max_queue_size: 1000
    schedule_delay_ms: 2000
```

#### Command Line Flags
Tracing follows OpenTelemetry standards and should only be configured via environment variables and config files. CLI flags are not provided for tracing configuration.

### Supported Backends

**OpenTelemetry Provider** supports any OpenTelemetry-compatible backend:
- **Jaeger**: Use HTTP endpoint `http://jaeger:14268/api/traces`
- **Tempo**: Use HTTP endpoint `http://tempo:4318/v1/traces`  
- **OTLP Collectors**: Use appropriate HTTP or gRPC endpoints
- **Cloud Providers**: Use provider-specific OTLP endpoints with authentication

**Lightstep Provider** provides native integration:
- **Lightstep SaaS**: Production-ready cloud observability platform
- **Lightstep Microsatellites**: On-premises deployments
- **Custom Lightstep Endpoints**: Self-hosted Lightstep instances

### Docker Examples

```bash
# OpenTelemetry with Jaeger
docker run -p 8080:8080 \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=otel \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HTTP_ENDPOINT="http://jaeger:14268/api/traces" \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_INSECURE=true \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_SAMPLING_RATIO=0.1 \
  nvcf-nats-auth-callout-service server

# OpenTelemetry with Tempo and authentication
docker run -p 8080:8080 \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=otel \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HTTP_ENDPOINT="http://tempo:4318/v1/traces" \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HEADERS_AUTHORIZATION="Bearer your-token" \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_COMPRESSION=gzip \
  nvcf-nats-auth-callout-service server

# Lightstep tracing
docker run -p 8080:8080 \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_PROVIDER=lightstep \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ACCESS_TOKEN="your-lightstep-token" \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ENVIRONMENT=production \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_SAMPLING_RATIO=0.1 \
  nvcf-nats-auth-callout-service server
```

### Kubernetes Deployment

The Kubernetes deployment automatically configures runtime attribute detection:

```yaml
# Already included in deploy/templates/deployment.yaml
env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: NODE_NAME
    valueFrom:
      fieldRef:
        fieldPath: spec.nodeName
```

Enable tracing in your Helm values:

```yaml
# values.yaml for OpenTelemetry
tracing:
  enabled: true
  provider: "otel"
  otel:
    http_endpoint: "http://jaeger:14268/api/traces"
    insecure: true
    sampling_ratio: 0.1
    headers:
      authorization: "Bearer your-token"

# values.yaml for Lightstep
tracing:
  enabled: true
  provider: "lightstep"
  lightstep:
    access_token: "your-lightstep-token"
    environment: "production"
    sampling_ratio: 0.1
```

### Provider-Specific Secrets Management

The Helm chart supports automatic creation and management of provider-specific secrets for secure credential handling in Kubernetes deployments.

#### Secret Configuration Options

**OpenTelemetry Secrets:**

```yaml
# values.yaml - OpenTelemetry with created secret
tracing:
  enabled: true
  otel:
    # Use existing Kubernetes secret
    existingSecret: ""
    # Name for created secret (auto-generated if empty)
    secretName: "my-app-tracing-otel"
    # Secret data - triggers secret creation
    secretHeaders:
      authorization: "Bearer your-otel-token"
      x_api_key: "your-api-key"

serviceConfig:
  tracing:
    provider: "otel"
    otel:
      http_endpoint: "http://tempo:4318/v1/traces"
```

**Lightstep Secrets:**

```yaml
# values.yaml - Lightstep with created secret
tracing:
  enabled: true
  lightstep:
    # Use existing Kubernetes secret
    existingSecret: ""
    # Name for created secret (auto-generated if empty)
    secretName: "my-app-tracing-lightstep"
    # Secret data - triggers secret creation
    secretAccessToken: "your-lightstep-access-token"

serviceConfig:
  tracing:
    provider: "lightstep"
    lightstep:
      endpoint: "https://ingest.lightstep.com:443/traces/otel/v1"
```

#### Secret Creation Logic

- **Automatic Creation**: Secrets are created when secret data is provided and `existingSecret` is empty
- **Existing Secrets**: Reference existing secrets by setting `existingSecret` to the secret name
- **Environment Variables**: Secret values are automatically injected as environment variables:
  - OpenTelemetry: `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HEADERS_AUTHORIZATION`, `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_OTEL_HEADERS_X_API_KEY`
  - Lightstep: `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_LIGHTSTEP_ACCESS_TOKEN`

#### Examples

**Using Created Secrets:**

```yaml
# Helm will create secrets automatically
tracing:
  enabled: true
  otel:
    secretName: "nvcf-nats-auth-callout-service-otel-creds"
    secretHeaders:
      authorization: "Bearer production-token"
  lightstep:
    secretName: "nvcf-nats-auth-callout-service-lightstep-creds"
    secretAccessToken: "lightstep-production-token"
```

**Using Existing Secrets:**

```yaml
# Reference pre-existing secrets
tracing:
  enabled: true
  otel:
    existingSecret: "company-otel-credentials"
  lightstep:
    existingSecret: "company-lightstep-credentials"
```

**Mixed Configuration:**

```yaml
# Use existing secret for one provider, create for another
tracing:
  enabled: true
  otel:
    existingSecret: "shared-otel-secret"
  lightstep:
    secretName: "app-specific-lightstep"
    secretAccessToken: "lightstep-token"
```

#### Secret Structure

**OpenTelemetry Secret Keys:**
- `authorization`: Authorization header value
- `x-api-key`: X-API-Key header value

**Lightstep Secret Keys:**
- `access-token`: Lightstep access token

#### Validation Rules

- Cannot specify both `existingSecret` and secret creation values
- If `secretName` is provided, corresponding secret data must also be provided
- Secret names must be valid Kubernetes secret names
- At least one secret field must be provided when creating secrets 