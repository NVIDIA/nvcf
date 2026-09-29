/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
)

type Config struct {
	Telemetry          TelemetryConfig
	Server             ServerConfig
	Stargate           StargateConfig
	NVCF               NVCFConfig
	Olric              OlricConfig
	RateLimiter        RateLimiterConfig
	RateLimitSync      RateLimitSynchronizationConfig
	DefaultServiceTier servicetier.Tier
	DefaultTPM         int64
	DefaultRPM         int64
	ModelCapabilities  map[string]ModelCapabilities
	// ModelURIAllowlistEnabled refuses requests to endpoints a model does
	// not declare in its uris allowlist. When false, undeclared endpoints
	// are only counted and logged so enforcement can roll out per
	// environment.
	ModelURIAllowlistEnabled bool
	Auth                     AuthConfig
}

type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	// InferenceWriteTimeout replaces WriteTimeout on inference routes. It
	// bounds each write rather than the whole response and does not run
	// between writes, so long generations, streams, and upstream pauses are
	// not cut off; it only stops a write that stalls on a client that
	// stopped reading. Zero or negative disables the deadline.
	InferenceWriteTimeout time.Duration
	IdleTimeout           time.Duration
	Region                string
	// MaxRequestBodyBytes rejects larger request bodies with 413. Zero disables
	// the limit.
	MaxRequestBodyBytes int64
	// TLSCertFile and TLSKeyFile enable TLS on the listener. Both or neither
	// must be set.
	TLSCertFile string
	TLSKeyFile  string
	// TLSReloadInterval is how often the TLS files are checked for a renewed
	// pair while serving. A changed pair is served to new connections
	// without a restart.
	TLSReloadInterval time.Duration
}

// TLSEnabled reports whether the listener is configured for TLS. A
// half-configured pair also counts, so startup fails instead of silently
// serving plaintext.
func (c ServerConfig) TLSEnabled() bool {
	return c.TLSCertFile != "" || c.TLSKeyFile != ""
}

// AuthConfig selects how callers are authenticated. NVCF gRPC auth
// (NVCFConfig.GRPCAddr) and static API keys (APIKeysPath) are mutually
// exclusive; with neither, the gateway starts only when AllowAnonymous is set.
type AuthConfig struct {
	// APIKeysPath is the static API key file. Setting it selects static key
	// mode: bare model names, no routing-key prefix and no NVCF API.
	APIKeysPath string
	// AllowAnonymous lets the gateway start without any authenticator. Every
	// request is then accepted unauthenticated.
	AllowAnonymous bool
	// StaticAllowedPaths are the request paths served in static key mode.
	// Other paths are refused with 403 after authentication. The read-only
	// discovery endpoints (GET /v1/models and GET /v1/registry) are always
	// allowed and need not be listed.
	StaticAllowedPaths []string
	// PublicReadEndpoints is the PUBLIC_READ_ENDPOINTS override. Nil derives
	// the value from the auth mode; see Config.PublicReadEndpointsEnabled.
	PublicReadEndpoints *bool
}

// AuthMode names how the gateway authenticates callers.
type AuthMode string

const (
	AuthModeNVCF       AuthMode = "nvcf"
	AuthModeStaticKeys AuthMode = "static-keys"
	AuthModeAnonymous  AuthMode = "anonymous"
)

// ErrNoAuthConfigured is returned by AuthMode when no authenticator is
// configured and anonymous access was not explicitly allowed.
var ErrNoAuthConfigured = errors.New(
	"no request authentication configured: set NVCF_GRPC_ADDR for NVCF auth, " +
		"API_KEYS_PATH for static API keys, or ALLOW_ANONYMOUS=true to accept " +
		"unauthenticated requests",
)

// StaticAuthMode reports whether the gateway authenticates callers with
// static API keys. In this mode the whole request model string is the model
// id and the routing key is always empty.
func (c *Config) StaticAuthMode() bool {
	return c != nil && c.Auth.APIKeysPath != ""
}

// AuthMode resolves the authentication mode. It fails closed: with no
// authenticator configured and anonymous access not allowed it returns
// ErrNoAuthConfigured.
func (c *Config) AuthMode() (AuthMode, error) {
	if c == nil {
		return "", ErrNoAuthConfigured
	}
	switch {
	case c.NVCF.GRPCAddr != "" && c.Auth.APIKeysPath != "":
		return "", errAuthModesExclusive
	case c.NVCF.GRPCAddr != "":
		return AuthModeNVCF, nil
	case c.Auth.APIKeysPath != "":
		return AuthModeStaticKeys, nil
	case c.Auth.AllowAnonymous:
		return AuthModeAnonymous, nil
	default:
		return "", ErrNoAuthConfigured
	}
}

var errAuthModesExclusive = errors.New("NVCF_GRPC_ADDR and API_KEYS_PATH are mutually exclusive")

// PublicReadEndpointsEnabled reports whether the read-only discovery
// endpoints, GET /v1/models and GET /v1/registry, are served without caller
// authentication. An explicit PUBLIC_READ_ENDPOINTS wins. Otherwise they are
// public in static key and anonymous mode and authenticated in NVCF mode,
// where the router's model list spans every tenant. A config whose auth mode
// does not resolve fails closed.
func (c *Config) PublicReadEndpointsEnabled() bool {
	if c == nil {
		return false
	}
	if c.Auth.PublicReadEndpoints != nil {
		return *c.Auth.PublicReadEndpoints
	}
	mode, err := c.AuthMode()
	if err != nil {
		return false
	}
	return mode != AuthModeNVCF
}

// DefaultTLSReloadInterval is the TLS_RELOAD_INTERVAL default.
const DefaultTLSReloadInterval = 30 * time.Second

// DefaultStaticAllowedPaths is the STATIC_ALLOWED_PATHS default.
var DefaultStaticAllowedPaths = []string{"/v1/chat/completions"}

type TelemetryConfig struct {
	ServiceName        string
	MetricsPort        int
	TracingAccessToken string
}

type StargateConfig struct {
	URL            string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	// ServiceToken, when set, is sent to the router as the Authorization
	// bearer. The caller's own credentials are never forwarded.
	ServiceToken string
}

type NVCFConfig struct {
	GRPCAddr           string
	SecretsPath        string
	OAuth2ProviderHost string
	GRPCInsecure       bool
	GRPCTimeout        time.Duration
}

// OlricConfig controls the embedded Olric node used as the rate-limit state
// store. Enabled gates whether a node is started at all; if false, the rate
// limiter falls back to AllowAll or RejectAll depending on RateLimiter.FailOpen.
type OlricConfig struct {
	Enabled            bool
	Environment        string // "local", "lan", or "wan"; controls Olric's memberlist defaults
	BindAddr           string
	BindPort           int
	MemberlistBindAddr string
	MemberlistBindPort int
	// Peers is the static seed list. When non-empty it takes precedence over
	// in-cluster discovery: Olric will join exactly these addresses.
	Peers []string
	// K8sLabelSelector is used by the in-cluster Kubernetes service discovery
	// plugin to find peer pods (see util.NewK8sDiscovery). Discovery is chosen
	// automatically when Peers is empty and POD_NAMESPACE is set; leave it at
	// the default for typical deployments.
	K8sLabelSelector string
	ReplicaCount     int
	PartitionCount   uint64
	DMapName         string
	StartupTimeout   time.Duration
	// ShutdownTimeout bounds the total time a graceful Olric shutdown is
	// allowed to take. Used by util.ShutdownOlricNode on process exit and on
	// the error-handling paths inside NewOlricNode. Zero => default (5s).
	ShutdownTimeout time.Duration
	LogLevel        string // "DEBUG", "INFO", "WARN", "ERROR"
	LogVerbosity    int32
	LogOutput       io.Writer
}

type RateLimiterConfig struct {
	Enabled  bool
	FailOpen bool
}

type RateLimitSynchronizationConfig struct {
	Transport   string
	ClusterName string
	ApplyRemote bool
	NATS        RateLimitNATSConfig
	PubSub      RateLimitPubSubConfig
}

type RateLimitNATSConfig struct {
	URL            string
	Subject        string
	ConnectTimeout time.Duration
}

type RateLimitPubSubConfig struct {
	Create       bool
	ProjectID    string
	Topic        string
	Subscription string
	Endpoint     string
	EmulatorHost string
}

type ModelCapabilities struct {
	Embeddings     *bool `json:"embeddings,omitempty"`
	Reranking      *bool `json:"reranking,omitempty"`
	TextToSpeech   *bool `json:"textToSpeech,omitempty"`
	Transcription  *bool `json:"transcription,omitempty"`
	Translation    *bool `json:"translation,omitempty"`
	SpeechToSpeech *bool `json:"speechToSpeech,omitempty"`
}

type TextToSpeechCapabilities struct {
	Voices                   []string
	SampleRates              []uint32
	ResponseFormats          []string
	MinSpeed                 float32
	MaxSpeed                 float32
	UnsupportedFormatsByRate map[uint32][]string
	DefaultSampleRate        *uint32
	DefaultTemperature       *float32
	MaxInputLength           int
}

func (c ModelCapabilities) SupportsEmbeddings() bool {
	return c.Embeddings == nil || *c.Embeddings
}

func (c ModelCapabilities) SupportsReranking() bool {
	return c.Reranking == nil || *c.Reranking
}

func (c ModelCapabilities) SupportsTextToSpeech() bool {
	return c.TextToSpeech == nil || *c.TextToSpeech
}

func (c ModelCapabilities) SupportsTranscription() bool {
	return c.Transcription == nil || *c.Transcription
}

func (c ModelCapabilities) SupportsTranslation() bool {
	return c.Translation == nil || *c.Translation
}

func (c ModelCapabilities) SupportsSpeechToSpeech() bool {
	return c.SpeechToSpeech == nil || *c.SpeechToSpeech
}

func Default() *Config {
	return &Config{
		Telemetry: TelemetryConfig{
			ServiceName: "llm-api-gateway",
			MetricsPort: 9464,
		},
		Server: ServerConfig{
			Addr:                  ":8080",
			ReadHeaderTimeout:     5 * time.Second,
			ReadTimeout:           15 * time.Second,
			WriteTimeout:          60 * time.Second,
			InferenceWriteTimeout: 60 * time.Second,
			IdleTimeout:           60 * time.Second,
			Region:                "global",
			TLSReloadInterval:     DefaultTLSReloadInterval,
		},
		Stargate: StargateConfig{
			URL:            "http://127.0.0.1:8000",
			ConnectTimeout: 2 * time.Second,
			RequestTimeout: 0,
		},
		NVCF: NVCFConfig{
			GRPCAddr:    "",
			GRPCTimeout: 2 * time.Second,
		},
		Olric: OlricConfig{
			Enabled:          false,
			Environment:      "local",
			DMapName:         "rate-limit",
			StartupTimeout:   15 * time.Second,
			ShutdownTimeout:  5 * time.Second,
			K8sLabelSelector: "app.kubernetes.io/part-of=llm-api-gateway",
		},
		RateLimiter: RateLimiterConfig{
			Enabled:  true,
			FailOpen: true,
		},
		RateLimitSync: RateLimitSynchronizationConfig{
			ApplyRemote: true,
			NATS: RateLimitNATSConfig{
				Subject:        "rate-limit-events",
				ConnectTimeout: 5 * time.Second,
			},
		},
		DefaultServiceTier: servicetier.Auto,
		Auth: AuthConfig{
			StaticAllowedPaths: slices.Clone(DefaultStaticAllowedPaths),
		},
	}
}

// LoadFromEnv builds a Config from environment variables, layering over
// Default(). It returns an aggregated error covering every malformed env var
// and JSON blob it encountered so callers get the full picture in one shot.
//
// Fail-loud is deliberate: silent fallbacks here routinely mask rollout-time
// typos (e.g. OLRIC_STARTUP_TIMEOUT="30" instead of "30s") that then manifest
// as mysterious production behaviour hours later.
func LoadFromEnv() (*Config, error) {
	cfg := Default()
	var errs envErrs

	applyServerTelemetryEnv(cfg, &errs)
	applyStargateNVCFEnv(cfg, &errs)
	applyOlricNetworkEnv(cfg, &errs)
	applyOlricRuntimeEnv(cfg, &errs)
	applyRateLimitEnv(cfg, &errs)
	applyDefaultModelEnv(cfg, &errs)
	applyAuthTLSEnv(cfg, &errs)

	if v, ok := errs.boolean("MODEL_URI_ALLOWLIST_ENABLED"); ok {
		cfg.ModelURIAllowlistEnabled = v
	}

	validateAuthTLS(cfg, &errs)

	// SecretsPath is populated by applyStargateNVCFEnv above.
	cfg.Telemetry.TracingAccessToken = loadTracingAccessToken(cfg.NVCF.SecretsPath)

	if err := errs.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadTracingAccessToken reads the Lightstep access token for the OTLP
// exporters. Best-effort: a missing file or key leaves the token empty rather
// than failing startup.
func loadTracingAccessToken(secretsPath string) string {
	if secretsPath == "" {
		return ""
	}
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return ""
	}
	var secrets struct {
		TracingAccessToken string `json:"tracingAccessToken"`
	}
	if err := json.Unmarshal(data, &secrets); err != nil {
		return ""
	}
	return secrets.TracingAccessToken
}

func applyServerTelemetryEnv(cfg *Config, errs *envErrs) {
	if addr := os.Getenv("PORT"); addr != "" {
		if strings.HasPrefix(addr, ":") {
			cfg.Server.Addr = addr
		} else {
			cfg.Server.Addr = ":" + addr
		}
	}

	if addr := os.Getenv("NVCF_GATEWAY_ADDR"); addr != "" {
		cfg.Server.Addr = addr
	}

	if limit, ok := errs.integer64("NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES"); ok {
		if limit < 0 {
			errs.add("NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES", strconv.FormatInt(limit, 10), errors.New("must be >= 0"))
		} else {
			cfg.Server.MaxRequestBodyBytes = limit
		}
	}

	if serviceName := os.Getenv("OTEL_SERVICE_NAME"); serviceName != "" {
		cfg.Telemetry.ServiceName = serviceName
	}

	if port, ok := errs.integer("METRICS_PORT"); ok {
		cfg.Telemetry.MetricsPort = port
	}

	if region := os.Getenv("NVCF_REGION"); region != "" {
		cfg.Server.Region = region
	}

	if timeout, ok := errs.duration("NVCF_GATEWAY_INFERENCE_WRITE_TIMEOUT"); ok {
		cfg.Server.InferenceWriteTimeout = timeout
	}
}

func applyStargateNVCFEnv(cfg *Config, errs *envErrs) {
	if stargateURL := os.Getenv("STARGATE_URL"); stargateURL != "" {
		cfg.Stargate.URL = stargateURL
	}

	if timeout, ok := errs.duration("STARGATE_CONNECT_TIMEOUT"); ok {
		cfg.Stargate.ConnectTimeout = timeout
	}

	if timeout, ok := errs.duration("STARGATE_REQUEST_TIMEOUT"); ok {
		cfg.Stargate.RequestTimeout = timeout
	}

	if grpcAddr := os.Getenv("NVCF_GRPC_ADDR"); grpcAddr != "" {
		cfg.NVCF.GRPCAddr = grpcAddr
	}

	if secretsPath := os.Getenv("SECRETS_PATH"); secretsPath != "" {
		cfg.NVCF.SecretsPath = secretsPath
	}

	if oauth2ProviderHost := os.Getenv("OAUTH2_PROVIDER_HOST"); oauth2ProviderHost != "" {
		cfg.NVCF.OAuth2ProviderHost = oauth2ProviderHost
	}

	if insecure, ok := errs.boolean("NVCF_GRPC_INSECURE"); ok {
		cfg.NVCF.GRPCInsecure = insecure
	}

	if timeout, ok := errs.duration("NVCF_GRPC_TIMEOUT"); ok {
		cfg.NVCF.GRPCTimeout = timeout
	}

}

func applyOlricNetworkEnv(cfg *Config, errs *envErrs) {
	if v, ok := errs.boolean("OLRIC_ENABLED"); ok {
		cfg.Olric.Enabled = v
	}

	if env := os.Getenv("OLRIC_ENV"); env != "" {
		cfg.Olric.Environment = env
	}

	if addr := os.Getenv("OLRIC_BIND_ADDR"); addr != "" {
		cfg.Olric.BindAddr = addr
	}

	if port, ok := errs.integer("OLRIC_BIND_PORT"); ok {
		cfg.Olric.BindPort = port
	}

	if addr := os.Getenv("OLRIC_MEMBERLIST_BIND_ADDR"); addr != "" {
		cfg.Olric.MemberlistBindAddr = addr
	}

	if port, ok := errs.integer("OLRIC_MEMBERLIST_BIND_PORT"); ok {
		cfg.Olric.MemberlistBindPort = port
	}

	if peers := os.Getenv("OLRIC_PEERS"); peers != "" {
		cfg.Olric.Peers = splitAndTrim(peers, ",")
	}

	if selector := os.Getenv("OLRIC_K8S_LABEL_SELECTOR"); selector != "" {
		cfg.Olric.K8sLabelSelector = selector
	}
}

func applyOlricRuntimeEnv(cfg *Config, errs *envErrs) {
	if replicas, ok := errs.integer("OLRIC_REPLICA_COUNT"); ok && replicas > 0 {
		cfg.Olric.ReplicaCount = replicas
	}

	if partitions, ok := errs.integer64("OLRIC_PARTITION_COUNT"); ok && partitions > 0 {
		cfg.Olric.PartitionCount = uint64(partitions)
	}

	if dmapName := os.Getenv("OLRIC_DMAP_NAME"); dmapName != "" {
		cfg.Olric.DMapName = dmapName
	}

	if timeout, ok := errs.duration("OLRIC_STARTUP_TIMEOUT"); ok {
		cfg.Olric.StartupTimeout = timeout
	}

	if timeout, ok := errs.duration("OLRIC_SHUTDOWN_TIMEOUT"); ok {
		cfg.Olric.ShutdownTimeout = timeout
	}

	if level := os.Getenv("OLRIC_LOG_LEVEL"); level != "" {
		cfg.Olric.LogLevel = level
	}
}

func applyRateLimitEnv(cfg *Config, errs *envErrs) {
	if v, ok := errs.boolean("RATE_LIMIT_FAIL_OPEN"); ok {
		cfg.RateLimiter.FailOpen = v
	}

	if v, ok := errs.boolean("RATE_LIMIT_ENABLED"); ok {
		cfg.RateLimiter.Enabled = v
	}

	if transport := os.Getenv("RATE_LIMIT_SYNC_TRANSPORT"); transport != "" {
		cfg.RateLimitSync.Transport = transport
	}

	if clusterName := os.Getenv("RATE_LIMIT_SYNC_CLUSTER_NAME"); clusterName != "" {
		cfg.RateLimitSync.ClusterName = clusterName
	}

	if v, ok := errs.boolean("RATE_LIMIT_SYNC_APPLY_REMOTE"); ok {
		cfg.RateLimitSync.ApplyRemote = v
	}

	if url := os.Getenv("RATE_LIMIT_SYNC_NATS_URL"); url != "" {
		cfg.RateLimitSync.NATS.URL = url
	}

	if subject := os.Getenv("RATE_LIMIT_SYNC_NATS_SUBJECT"); subject != "" {
		cfg.RateLimitSync.NATS.Subject = subject
	}

	if timeout, ok := errs.duration("RATE_LIMIT_SYNC_NATS_CONNECT_TIMEOUT"); ok {
		cfg.RateLimitSync.NATS.ConnectTimeout = timeout
	}

	if v, ok := errs.boolean("RATE_LIMIT_SYNC_PUBSUB_CREATE"); ok {
		cfg.RateLimitSync.PubSub.Create = v
	}

	if projectID := os.Getenv("RATE_LIMIT_SYNC_PUBSUB_PROJECT_ID"); projectID != "" {
		cfg.RateLimitSync.PubSub.ProjectID = projectID
	}

	if topic := os.Getenv("RATE_LIMIT_SYNC_PUBSUB_TOPIC"); topic != "" {
		cfg.RateLimitSync.PubSub.Topic = topic
	}

	if subscription := os.Getenv("RATE_LIMIT_SYNC_PUBSUB_SUBSCRIPTION"); subscription != "" {
		cfg.RateLimitSync.PubSub.Subscription = subscription
	}

	if endpoint := os.Getenv("RATE_LIMIT_SYNC_PUBSUB_ENDPOINT"); endpoint != "" {
		cfg.RateLimitSync.PubSub.Endpoint = endpoint
	}

	if emulatorHost := os.Getenv("RATE_LIMIT_SYNC_PUBSUB_EMULATOR_HOST"); emulatorHost != "" {
		cfg.RateLimitSync.PubSub.EmulatorHost = emulatorHost
	}
}

func applyDefaultModelEnv(cfg *Config, errs *envErrs) {
	if tier := os.Getenv("NVCF_DEFAULT_SERVICE_TIER"); tier != "" {
		var parsed servicetier.Tier
		if err := parsed.UnmarshalText([]byte(tier)); err != nil {
			errs.add("NVCF_DEFAULT_SERVICE_TIER", tier, err)
		} else {
			cfg.DefaultServiceTier = parsed
		}
	}

	if tpm, ok := errs.integer64("NVCF_DEFAULT_TPM"); ok {
		cfg.DefaultTPM = tpm
	}

	if rpm, ok := errs.integer64("NVCF_DEFAULT_RPM"); ok {
		cfg.DefaultRPM = rpm
	}

	if raw := os.Getenv("NVCF_MODEL_CAPABILITIES"); raw != "" {
		var caps map[string]ModelCapabilities
		if err := json.Unmarshal([]byte(raw), &caps); err != nil {
			errs.add("NVCF_MODEL_CAPABILITIES", raw, err)
		} else {
			cfg.ModelCapabilities = caps
		}
	}
}

func applyAuthTLSEnv(cfg *Config, errs *envErrs) {
	if path := os.Getenv("API_KEYS_PATH"); path != "" {
		cfg.Auth.APIKeysPath = path
	}

	if v, ok := errs.boolean("ALLOW_ANONYMOUS"); ok {
		cfg.Auth.AllowAnonymous = v
	}

	if raw := os.Getenv("STATIC_ALLOWED_PATHS"); raw != "" {
		paths := splitAndTrim(raw, ",")
		switch {
		case len(paths) == 0:
			errs.add("STATIC_ALLOWED_PATHS", raw, errors.New("must list at least one path"))
		case slices.ContainsFunc(paths, func(p string) bool { return !strings.HasPrefix(p, "/") }):
			errs.add("STATIC_ALLOWED_PATHS", raw, errors.New("every path must start with /"))
		default:
			cfg.Auth.StaticAllowedPaths = paths
		}
	}

	if v, ok := errs.boolean("PUBLIC_READ_ENDPOINTS"); ok {
		cfg.Auth.PublicReadEndpoints = &v
	}

	if token := os.Getenv("STARGATE_SERVICE_TOKEN"); token != "" {
		cfg.Stargate.ServiceToken = token
	}

	if certFile := os.Getenv("TLS_CERT_FILE"); certFile != "" {
		cfg.Server.TLSCertFile = certFile
	}

	if keyFile := os.Getenv("TLS_KEY_FILE"); keyFile != "" {
		cfg.Server.TLSKeyFile = keyFile
	}

	if interval, ok := errs.duration("TLS_RELOAD_INTERVAL"); ok {
		if interval <= 0 {
			errs.add("TLS_RELOAD_INTERVAL", os.Getenv("TLS_RELOAD_INTERVAL"), errors.New("must be > 0"))
		} else {
			cfg.Server.TLSReloadInterval = interval
		}
	}
}

// validateAuthTLS rejects combinations that are individually well-formed but
// contradictory. The fail-closed "no authenticator" decision is not made here
// because the rate-limit sync worker loads the same config without serving
// requests; the gateway enforces it at startup through Config.AuthMode.
func validateAuthTLS(cfg *Config, errs *envErrs) {
	if cfg.NVCF.GRPCAddr != "" && cfg.Auth.APIKeysPath != "" {
		errs.add("API_KEYS_PATH", cfg.Auth.APIKeysPath, errAuthModesExclusive)
	}

	switch {
	case cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile == "":
		errs.add("TLS_KEY_FILE", "", errors.New("must be set together with TLS_CERT_FILE"))
	case cfg.Server.TLSCertFile == "" && cfg.Server.TLSKeyFile != "":
		errs.add("TLS_CERT_FILE", "", errors.New("must be set together with TLS_KEY_FILE"))
	}
}

// envErrs accumulates per-env-var parse failures so LoadFromEnv can surface
// every malformed value at once instead of bailing on the first one. Callers
// invoke the typed helpers (boolean, integer, integer64, duration); each
// returns (zero, false) on a parse failure and records the error on the
// accumulator.
type envErrs struct {
	items []envErrItem
}

type envErrItem struct {
	key string
	raw string
	err error
}

func (e *envErrs) add(key, raw string, err error) {
	e.items = append(e.items, envErrItem{key: key, raw: raw, err: err})
}

func (e *envErrs) err() error {
	if len(e.items) == 0 {
		return nil
	}
	joined := make([]error, 0, len(e.items))
	for _, it := range e.items {
		joined = append(joined, fmt.Errorf("invalid %s=%q: %w", it.key, it.raw, it.err))
	}
	return errors.Join(joined...)
}

func (e *envErrs) boolean(key string) (bool, bool) {
	value := os.Getenv(key)
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		e.add(key, value, err)
		return false, false
	}
	return parsed, true
}

func (e *envErrs) integer64(key string) (int64, bool) {
	value := os.Getenv(key)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		e.add(key, value, err)
		return 0, false
	}
	return parsed, true
}

func (e *envErrs) integer(key string) (int, bool) {
	parsed, ok := e.integer64(key)
	if !ok {
		return 0, false
	}
	return int(parsed), true
}

func (e *envErrs) duration(key string) (time.Duration, bool) {
	value := os.Getenv(key)
	if value == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		e.add(key, value, err)
		return 0, false
	}
	return parsed, true
}

func splitAndTrim(raw, sep string) []string {
	return nonEmptyTrimmed(strings.Split(raw, sep))
}

// nonEmptyTrimmed trims whitespace from every element and filters empties. It
// is the shape every comma-/whitespace-split env var needs in practice.
func nonEmptyTrimmed(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
