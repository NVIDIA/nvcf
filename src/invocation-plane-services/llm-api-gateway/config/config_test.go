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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
)

func TestDefaultSetsExpectedDefaults(t *testing.T) {
	cfg := Default()

	if cfg.Telemetry.ServiceName != "llm-api-gateway" {
		t.Fatalf("service name = %q, want llm-api-gateway", cfg.Telemetry.ServiceName)
	}
	if cfg.Telemetry.MetricsPort != 9464 {
		t.Fatalf("metrics port = %d, want 9464", cfg.Telemetry.MetricsPort)
	}
	if cfg.DefaultServiceTier != servicetier.Auto {
		t.Fatalf("default service tier = %q, want %q", cfg.DefaultServiceTier, servicetier.Auto)
	}
	if cfg.DefaultTPM != 0 {
		t.Fatalf("default tpm = %d, want 0", cfg.DefaultTPM)
	}
	if cfg.DefaultRPM != 0 {
		t.Fatalf("default rpm = %d, want 0", cfg.DefaultRPM)
	}
	if cfg.Server.InferenceWriteTimeout <= 0 {
		t.Fatalf("inference write timeout = %s, want a positive stall bound", cfg.Server.InferenceWriteTimeout)
	}
}

func TestLoadFromEnvReadsInferenceWriteTimeout(t *testing.T) {
	t.Setenv("NVCF_GATEWAY_INFERENCE_WRITE_TIMEOUT", "0s")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.Server.InferenceWriteTimeout != 0 {
		t.Fatalf("inference write timeout = %s, want 0s", cfg.Server.InferenceWriteTimeout)
	}
}

func TestLoadFromEnvReadsMetricsPort(t *testing.T) {
	t.Setenv("METRICS_PORT", "0")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.Telemetry.MetricsPort != 0 {
		t.Fatalf("metrics port = %d, want 0", cfg.Telemetry.MetricsPort)
	}
}

func TestLoadFromEnvReadsTracingAccessToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"id":"x","secret":"y","tracingAccessToken":"tok-123"}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	t.Setenv("SECRETS_PATH", path)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.Telemetry.TracingAccessToken != "tok-123" {
		t.Fatalf("tracing access token = %q, want tok-123", cfg.Telemetry.TracingAccessToken)
	}
}

func TestLoadFromEnvTracingAccessTokenBestEffort(t *testing.T) {
	// Missing secrets file must leave the token empty, not fail startup.
	t.Setenv("SECRETS_PATH", filepath.Join(t.TempDir(), "does-not-exist.json"))

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.Telemetry.TracingAccessToken != "" {
		t.Fatalf("tracing access token = %q, want empty", cfg.Telemetry.TracingAccessToken)
	}
}

func TestLoadFromEnvReadsOAuth2ProviderHost(t *testing.T) {
	t.Setenv("OAUTH2_PROVIDER_HOST", "https://oauth.example.test")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.NVCF.OAuth2ProviderHost != "https://oauth.example.test" {
		t.Fatalf("oauth2 provider host = %q, want https://oauth.example.test", cfg.NVCF.OAuth2ProviderHost)
	}
}

func TestLoadFromEnvRejectsInvalidMetricsPort(t *testing.T) {
	t.Setenv("METRICS_PORT", "disabled")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "METRICS_PORT") {
		t.Fatalf("error %q does not mention METRICS_PORT", err.Error())
	}
}

func TestLoadFromEnvReadsDefaultTPMAndRPM(t *testing.T) {
	t.Setenv("NVCF_DEFAULT_TPM", "240000")
	t.Setenv("NVCF_DEFAULT_RPM", "120")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.DefaultTPM != 240000 {
		t.Fatalf("default tpm = %d, want 240000", cfg.DefaultTPM)
	}
	if cfg.DefaultRPM != 120 {
		t.Fatalf("default rpm = %d, want 120", cfg.DefaultRPM)
	}
}

func TestLoadFromEnvReadsModelCapabilities(t *testing.T) {
	t.Setenv("NVCF_MODEL_CAPABILITIES", `{"embed-model":{"embeddings":true,"reranking":false}}`)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.ModelCapabilities == nil {
		t.Fatal("model capabilities is nil")
	}
	caps, ok := cfg.ModelCapabilities["embed-model"]
	if !ok {
		t.Fatal("embed-model not found in model capabilities")
	}
	if !caps.SupportsEmbeddings() {
		t.Fatal("embed-model should support embeddings")
	}
	if caps.SupportsReranking() {
		t.Fatal("embed-model should not support reranking")
	}
}

func TestModelCapabilitiesDefaultsToAllEnabled(t *testing.T) {
	var caps ModelCapabilities

	if !caps.SupportsEmbeddings() {
		t.Fatal("zero-value capabilities should support embeddings")
	}
	if !caps.SupportsReranking() {
		t.Fatal("zero-value capabilities should support reranking")
	}
	if !caps.SupportsTextToSpeech() {
		t.Fatal("zero-value capabilities should support text to speech")
	}
	if !caps.SupportsTranscription() {
		t.Fatal("zero-value capabilities should support transcription")
	}
	if !caps.SupportsTranslation() {
		t.Fatal("zero-value capabilities should support translation")
	}
	if !caps.SupportsSpeechToSpeech() {
		t.Fatal("zero-value capabilities should support speech to speech")
	}
}

func TestLoadFromEnvRateLimitSyncNATS(t *testing.T) {
	t.Setenv("RATE_LIMIT_SYNC_TRANSPORT", "nats")
	t.Setenv("RATE_LIMIT_SYNC_CLUSTER_NAME", "cluster-a")
	t.Setenv("RATE_LIMIT_SYNC_APPLY_REMOTE", "false")
	t.Setenv("RATE_LIMIT_SYNC_NATS_URL", "nats://127.0.0.1:4222")
	t.Setenv("RATE_LIMIT_SYNC_NATS_SUBJECT", "rate-limit.test")
	t.Setenv("RATE_LIMIT_SYNC_NATS_CONNECT_TIMEOUT", "7s")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.RateLimitSync.Transport != "nats" {
		t.Fatalf("transport = %q, want nats", cfg.RateLimitSync.Transport)
	}

	if cfg.RateLimitSync.ClusterName != "cluster-a" {
		t.Fatalf("cluster name = %q, want cluster-a", cfg.RateLimitSync.ClusterName)
	}

	if cfg.RateLimitSync.ApplyRemote {
		t.Fatal("apply remote = true, want false")
	}

	if cfg.RateLimitSync.NATS.URL != "nats://127.0.0.1:4222" {
		t.Fatalf("nats url = %q, want nats://127.0.0.1:4222", cfg.RateLimitSync.NATS.URL)
	}

	if cfg.RateLimitSync.NATS.Subject != "rate-limit.test" {
		t.Fatalf("nats subject = %q, want rate-limit.test", cfg.RateLimitSync.NATS.Subject)
	}

	if cfg.RateLimitSync.NATS.ConnectTimeout.Seconds() != 7 {
		t.Fatalf("connect timeout = %s, want 7s", cfg.RateLimitSync.NATS.ConnectTimeout)
	}
}

// TestLoadFromEnvFailsOnInvalidDuration is the fail-loud guard the previous
// silent fallback was missing: a typo like "30" (missing unit) should stop
// the process at startup, not quietly fall back to the default.
func TestLoadFromEnvFailsOnInvalidDuration(t *testing.T) {
	t.Setenv("OLRIC_STARTUP_TIMEOUT", "30")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OLRIC_STARTUP_TIMEOUT") {
		t.Fatalf("error %q does not mention the offending key", err.Error())
	}
}

// TestLoadFromEnvFailsOnInvalidBool sanity-checks the same fail-loud behaviour
// for bool parsing.
func TestLoadFromEnvFailsOnInvalidBool(t *testing.T) {
	t.Setenv("OLRIC_ENABLED", "yeah")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OLRIC_ENABLED") {
		t.Fatalf("error %q does not mention the offending key", err.Error())
	}
}

// TestLoadFromEnvFailsOnInvalidJSON covers the same fail-loud contract for
// NVCF_MODEL_CAPABILITIES.
func TestLoadFromEnvFailsOnInvalidJSON(t *testing.T) {
	t.Setenv("NVCF_MODEL_CAPABILITIES", "not-json")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "NVCF_MODEL_CAPABILITIES") {
		t.Fatalf("error %q does not mention the offending key", err.Error())
	}
}

func TestLoadFromEnvReadsMaxRequestBodyBytes(t *testing.T) {
	t.Setenv("NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES", "1048576")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.Server.MaxRequestBodyBytes != 1048576 {
		t.Fatalf("max request body bytes = %d, want 1048576", cfg.Server.MaxRequestBodyBytes)
	}
}

func TestLoadFromEnvDefaultsMaxRequestBodyBytesToUnlimited(t *testing.T) {
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.Server.MaxRequestBodyBytes != 0 {
		t.Fatalf("max request body bytes = %d, want 0", cfg.Server.MaxRequestBodyBytes)
	}
}

func TestLoadFromEnvRejectsInvalidMaxRequestBodyBytes(t *testing.T) {
	for _, value := range []string{"-1", "10MB"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES", value)

			_, err := LoadFromEnv()
			if err == nil {
				t.Fatal("LoadFromEnv() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES") {
				t.Fatalf("error %q does not mention NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES", err.Error())
			}
		})
	}
}

func TestDefaultAuthConfig(t *testing.T) {
	cfg := Default()

	if cfg.StaticAuthMode() {
		t.Fatal("static auth mode = true, want false by default")
	}
	if cfg.Auth.AllowAnonymous {
		t.Fatal("allow anonymous = true, want false by default")
	}
	if !slices.Equal(cfg.Auth.StaticAllowedPaths, []string{"/v1/chat/completions"}) {
		t.Fatalf("static allowed paths = %q, want [/v1/chat/completions]", cfg.Auth.StaticAllowedPaths)
	}
	if cfg.Server.TLSEnabled() {
		t.Fatal("tls enabled = true, want false by default")
	}
	if cfg.Server.TLSReloadInterval != DefaultTLSReloadInterval || DefaultTLSReloadInterval != 30*time.Second {
		t.Fatalf("tls reload interval = %s, want 30s", cfg.Server.TLSReloadInterval)
	}
	if cfg.Stargate.ServiceToken != "" {
		t.Fatal("stargate service token is set by default")
	}
}

func TestLoadFromEnvReadsAuthTLSAndServiceToken(t *testing.T) {
	t.Setenv("API_KEYS_PATH", "/etc/llm-api-gateway/api-keys.json")
	t.Setenv("ALLOW_ANONYMOUS", "true")
	t.Setenv("STARGATE_SERVICE_TOKEN", "router-token")
	t.Setenv("TLS_CERT_FILE", "/etc/tls/tls.crt")
	t.Setenv("TLS_KEY_FILE", "/etc/tls/tls.key")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.Auth.APIKeysPath != "/etc/llm-api-gateway/api-keys.json" {
		t.Fatalf("api keys path = %q", cfg.Auth.APIKeysPath)
	}
	if !cfg.StaticAuthMode() {
		t.Fatal("static auth mode = false, want true when API_KEYS_PATH is set")
	}
	if !cfg.Auth.AllowAnonymous {
		t.Fatal("allow anonymous = false, want true")
	}
	if cfg.Stargate.ServiceToken != "router-token" {
		t.Fatalf("stargate service token = %q, want router-token", cfg.Stargate.ServiceToken)
	}
	if cfg.Server.TLSCertFile != "/etc/tls/tls.crt" || cfg.Server.TLSKeyFile != "/etc/tls/tls.key" {
		t.Fatalf("tls files = %q, %q", cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
	}
	if !cfg.Server.TLSEnabled() {
		t.Fatal("tls enabled = false, want true")
	}
}

func TestLoadFromEnvStaticAllowedPaths(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "unset keeps default", raw: "", want: []string{"/v1/chat/completions"}},
		{name: "single path", raw: "/v1/embeddings", want: []string{"/v1/embeddings"}},
		{
			name: "trims and drops empty entries",
			raw:  " /v1/chat/completions , ,/v1/responses ",
			want: []string{"/v1/chat/completions", "/v1/responses"},
		},
		{name: "only separators", raw: " , ", wantErr: true},
		{name: "relative path", raw: "/v1/chat/completions,v1/embeddings", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STATIC_ALLOWED_PATHS", tc.raw)

			cfg, err := LoadFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatal("LoadFromEnv() error = nil, want error")
				}
				if !strings.Contains(err.Error(), "STATIC_ALLOWED_PATHS") {
					t.Fatalf("error %q does not mention STATIC_ALLOWED_PATHS", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFromEnv() error = %v", err)
			}
			if !slices.Equal(cfg.Auth.StaticAllowedPaths, tc.want) {
				t.Fatalf("static allowed paths = %q, want %q", cfg.Auth.StaticAllowedPaths, tc.want)
			}
		})
	}
}

func TestLoadFromEnvRejectsNVCFAndStaticKeysTogether(t *testing.T) {
	t.Setenv("NVCF_GRPC_ADDR", "api.nvcf.example:9090")
	t.Setenv("API_KEYS_PATH", "/etc/llm-api-gateway/api-keys.json")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	for _, want := range []string{"API_KEYS_PATH", "NVCF_GRPC_ADDR", "mutually exclusive"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestLoadFromEnvRejectsInvalidAllowAnonymous(t *testing.T) {
	t.Setenv("ALLOW_ANONYMOUS", "sure")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "ALLOW_ANONYMOUS") {
		t.Fatalf("error %q does not mention ALLOW_ANONYMOUS", err.Error())
	}
}

func TestLoadFromEnvTLSPairValidation(t *testing.T) {
	tests := []struct {
		name        string
		certFile    string
		keyFile     string
		wantErrKey  string
		wantEnabled bool
	}{
		{name: "neither", wantEnabled: false},
		{name: "both", certFile: "/tls/tls.crt", keyFile: "/tls/tls.key", wantEnabled: true},
		{name: "cert only", certFile: "/tls/tls.crt", wantErrKey: "TLS_KEY_FILE"},
		{name: "key only", keyFile: "/tls/tls.key", wantErrKey: "TLS_CERT_FILE"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TLS_CERT_FILE", tc.certFile)
			t.Setenv("TLS_KEY_FILE", tc.keyFile)

			cfg, err := LoadFromEnv()
			if tc.wantErrKey != "" {
				if err == nil {
					t.Fatal("LoadFromEnv() error = nil, want error")
				}
				if !strings.Contains(err.Error(), tc.wantErrKey) {
					t.Fatalf("error %q does not mention %s", err.Error(), tc.wantErrKey)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFromEnv() error = %v", err)
			}
			if cfg.Server.TLSEnabled() != tc.wantEnabled {
				t.Fatalf("tls enabled = %v, want %v", cfg.Server.TLSEnabled(), tc.wantEnabled)
			}
		})
	}
}

func TestLoadFromEnvTLSReloadInterval(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: 30 * time.Second},
		{name: "override", raw: "5s", want: 5 * time.Second},
		{name: "minutes", raw: "2m", want: 2 * time.Minute},
		{name: "missing unit", raw: "30", wantErr: true},
		{name: "zero", raw: "0s", wantErr: true},
		{name: "negative", raw: "-1s", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TLS_RELOAD_INTERVAL", tc.raw)

			cfg, err := LoadFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatal("LoadFromEnv() error = nil, want error")
				}
				if !strings.Contains(err.Error(), "TLS_RELOAD_INTERVAL") {
					t.Fatalf("error %q does not mention TLS_RELOAD_INTERVAL", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFromEnv() error = %v", err)
			}
			if cfg.Server.TLSReloadInterval != tc.want {
				t.Fatalf("tls reload interval = %s, want %s", cfg.Server.TLSReloadInterval, tc.want)
			}
		})
	}
}

func TestServerConfigTLSEnabledFailsClosedOnHalfPair(t *testing.T) {
	// A half pair can only come from a Config built outside LoadFromEnv; it
	// must still select TLS so startup fails instead of serving plaintext.
	if !(ServerConfig{TLSCertFile: "/tls/tls.crt"}).TLSEnabled() {
		t.Fatal("tls enabled = false for cert-only pair, want true")
	}
	if !(ServerConfig{TLSKeyFile: "/tls/tls.key"}).TLSEnabled() {
		t.Fatal("tls enabled = false for key-only pair, want true")
	}
}

func TestConfigAuthMode(t *testing.T) {
	tests := []struct {
		name           string
		grpcAddr       string
		apiKeysPath    string
		allowAnonymous bool
		wantMode       AuthMode
		wantErr        error
	}{
		{name: "nvcf", grpcAddr: "api.nvcf.example:9090", wantMode: AuthModeNVCF},
		{name: "static keys", apiKeysPath: "/keys.json", wantMode: AuthModeStaticKeys},
		{name: "nvcf ignores allow anonymous", grpcAddr: "api.nvcf.example:9090", allowAnonymous: true, wantMode: AuthModeNVCF},
		{name: "static ignores allow anonymous", apiKeysPath: "/keys.json", allowAnonymous: true, wantMode: AuthModeStaticKeys},
		{name: "anonymous only when allowed", allowAnonymous: true, wantMode: AuthModeAnonymous},
		{name: "fails closed without authenticator", wantErr: ErrNoAuthConfigured},
		{name: "mutually exclusive", grpcAddr: "api.nvcf.example:9090", apiKeysPath: "/keys.json", wantErr: errAuthModesExclusive},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.NVCF.GRPCAddr = tc.grpcAddr
			cfg.Auth.APIKeysPath = tc.apiKeysPath
			cfg.Auth.AllowAnonymous = tc.allowAnonymous

			mode, err := cfg.AuthMode()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("AuthMode() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("AuthMode() error = %v", err)
			}
			if mode != tc.wantMode {
				t.Fatalf("AuthMode() = %q, want %q", mode, tc.wantMode)
			}
			if cfg.StaticAuthMode() != (tc.wantMode == AuthModeStaticKeys) {
				t.Fatalf("StaticAuthMode() = %v for mode %q", cfg.StaticAuthMode(), tc.wantMode)
			}
		})
	}
}

func TestErrNoAuthConfiguredNamesEveryOption(t *testing.T) {
	for _, want := range []string{"NVCF_GRPC_ADDR", "API_KEYS_PATH", "ALLOW_ANONYMOUS"} {
		if !strings.Contains(ErrNoAuthConfigured.Error(), want) {
			t.Fatalf("error %q does not name %s", ErrNoAuthConfigured.Error(), want)
		}
	}
}

func TestNilConfigAuthModeFailsClosed(t *testing.T) {
	var cfg *Config
	if cfg.StaticAuthMode() {
		t.Fatal("nil config reports static auth mode")
	}
	if _, err := cfg.AuthMode(); !errors.Is(err, ErrNoAuthConfigured) {
		t.Fatalf("AuthMode() error = %v, want ErrNoAuthConfigured", err)
	}
}

func TestPublicReadEndpointsEnabled(t *testing.T) {
	tests := []struct {
		name           string
		grpcAddr       string
		apiKeysPath    string
		allowAnonymous bool
		raw            string
		want           bool
	}{
		{name: "static keys default public", apiKeysPath: "/keys.json", want: true},
		{name: "anonymous default public", allowAnonymous: true, want: true},
		{name: "nvcf default authenticated", grpcAddr: "api.nvcf.example:9090", want: false},
		{name: "no authenticator fails closed", want: false},
		{name: "static keys override false", apiKeysPath: "/keys.json", raw: "false", want: false},
		{name: "anonymous override false", allowAnonymous: true, raw: "false", want: false},
		{name: "nvcf override true", grpcAddr: "api.nvcf.example:9090", raw: "true", want: true},
		{name: "nvcf override false", grpcAddr: "api.nvcf.example:9090", raw: "0", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NVCF_GRPC_ADDR", tc.grpcAddr)
			t.Setenv("API_KEYS_PATH", tc.apiKeysPath)
			t.Setenv("ALLOW_ANONYMOUS", strconv.FormatBool(tc.allowAnonymous))
			t.Setenv("PUBLIC_READ_ENDPOINTS", tc.raw)

			cfg, err := LoadFromEnv()
			if err != nil {
				t.Fatalf("LoadFromEnv() error = %v", err)
			}
			if tc.raw == "" && cfg.Auth.PublicReadEndpoints != nil {
				t.Fatalf("public read endpoints override = %v, want nil when unset", *cfg.Auth.PublicReadEndpoints)
			}
			if got := cfg.PublicReadEndpointsEnabled(); got != tc.want {
				t.Fatalf("PublicReadEndpointsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadFromEnvRejectsInvalidPublicReadEndpoints(t *testing.T) {
	t.Setenv("PUBLIC_READ_ENDPOINTS", "sometimes")
	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("LoadFromEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "PUBLIC_READ_ENDPOINTS") {
		t.Fatalf("error %q does not mention PUBLIC_READ_ENDPOINTS", err.Error())
	}
}

func TestNilConfigPublicReadEndpointsFailsClosed(t *testing.T) {
	var cfg *Config
	if cfg.PublicReadEndpointsEnabled() {
		t.Fatal("nil config reports public read endpoints")
	}
}
