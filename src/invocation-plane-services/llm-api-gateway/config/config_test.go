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
	"strings"
	"testing"

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
