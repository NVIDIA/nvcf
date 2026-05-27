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
}

type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	Region            string
}

type TelemetryConfig struct {
	ServiceName string
	MetricsPort int
}

type StargateConfig struct {
	URL            string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

type NVCFConfig struct {
	GRPCAddr     string
	SecretsPath  string
	GRPCInsecure bool
	GRPCTimeout  time.Duration
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
			Addr:              ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       60 * time.Second,
			Region:            "global",
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

	if err := errs.err(); err != nil {
		return nil, err
	}
	return cfg, nil
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

	if serviceName := os.Getenv("OTEL_SERVICE_NAME"); serviceName != "" {
		cfg.Telemetry.ServiceName = serviceName
	}

	if port, ok := errs.integer("METRICS_PORT"); ok {
		cfg.Telemetry.MetricsPort = port
	}

	if region := os.Getenv("NVCF_REGION"); region != "" {
		cfg.Server.Region = region
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
