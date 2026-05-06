/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// setupTestConfig sets up embedded config for testing
func setupTestConfig() {
	testConfigYAML := `
# Test Configuration file for the nvcf-nats-auth-callout-service service
service:
  name: "nvcf-nats-auth-callout-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: ""
  nkey_signature: ""
server:
  port: "8080"
logging:
  level: "info"
  format: "json"
  output: "stdout"
  caller: true
  stacktrace_level: "error"
  development: false
  disable_timestamp: false
  sampling:
    initial: 100
    thereafter: 100
  fields:
    service_name: "nvcf-nats-auth-callout-service"
    version: "1.0.0"
    environment: "development"
metrics:
  enabled: true
  port: "9090"
# Database configuration (optional)
# database:
#   postgres:
#     host: "localhost"
#     port: 5432
#     database: "nvcf-nats-auth-callout-service"
#     username: "postgres"
#     password: ""
#     sslmode: "disable"
#     max_connections: 25
#     max_idle_connections: 5
# Tracing configuration (optional)
# tracing:
#   provider: "otel"
#   enabled: true
#   endpoint: "http://localhost:4317"
#   http_endpoint: "http://localhost:4318/v1/traces"
#   insecure: true
#   service_name: "nvcf-nats-auth-callout-service"
#   service_version: "1.0.0"
#   environment: "development"
#   sampling_ratio: 1.0
#   timeout_ms: 10000
#   max_export_batch_size: 512
#   compression: "gzip"
`
	SetEmbeddedDefaults([]byte(testConfigYAML))
}

func TestInitConfig(t *testing.T) {
	// Reset config before test
	defer ResetConfig()
	setupTestConfig()

	cfg, err := InitConfig("test", "")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Test that we can access the basic config fields
	assert.Equal(t, "nvcf-nats-auth-callout-service", cfg.Service.Name)
	assert.Equal(t, "8080", cfg.Server.Port)
}

func TestEnvironmentVariables(t *testing.T) {
	defer ResetConfig()
	setupTestConfig()

	// Set environment variables
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT", "9999")
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME", "test-env-service")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME")

	config, err := InitConfig("test", "")
	if err != nil {
		t.Fatalf("Failed to init config: %v", err)
	}

	if config.Server.Port != "9999" {
		t.Errorf("Expected port 9999, got %s", config.Server.Port)
	}
	if config.Service.Name != "test-env-service" {
		t.Errorf("Expected service name 'test-env-service', got %s", config.Service.Name)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
	}{
		{
			name: "valid_config",
			config: &Config{
				Server: ServerConfig{
					Port: "8080",
				},
				Service: ServiceConfig{
					Name:          "test-service",
					NatsURL:       "nats://localhost:4222",
					NkeySeed:      "SUAOTESTSEEDKEYFORAUTHENTICATION",
					NkeySignature: "SAATESTSIGNATUREKEYFORJWTSIGNING",
				},
			},
			expectError: false,
		},
		{
			name: "empty_port",
			config: &Config{
				Server: ServerConfig{
					Port: "",
				},
				Service: ServiceConfig{
					Name: "test-service",
				},
			},
			expectError: true,
		},
		{
			name: "empty_service_name",
			config: &Config{
				Server: ServerConfig{
					Port: "8080",
				},
				Service: ServiceConfig{
					Name: "",
				},
			},
			expectError: true,
		},
		{
			name: "empty_nats_url",
			config: &Config{
				Server: ServerConfig{
					Port: "8080",
				},
				Service: ServiceConfig{
					Name:          "test-service",
					NatsURL:       "",
					NkeySeed:      "SUAOTESTSEEDKEYFORAUTHENTICATION",
					NkeySignature: "SAATESTSIGNATUREKEYFORJWTSIGNING",
				},
			},
			expectError: true,
		},
		{
			name: "empty_nkey_seed",
			config: &Config{
				Server: ServerConfig{
					Port: "8080",
				},
				Service: ServiceConfig{
					Name:          "test-service",
					NatsURL:       "nats://localhost:4222",
					NkeySeed:      "",
					NkeySignature: "SAATESTSIGNATUREKEYFORJWTSIGNING",
				},
			},
			expectError: true,
		},
		{
			name: "empty_nkey_signature",
			config: &Config{
				Server: ServerConfig{
					Port: "8080",
				},
				Service: ServiceConfig{
					Name:          "test-service",
					NatsURL:       "nats://localhost:4222",
					NkeySeed:      "SUAOTESTSEEDKEYFORAUTHENTICATION",
					NkeySignature: "",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.config)
			if tt.expectError && err == nil {
				t.Errorf("Expected validation error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no validation error but got: %v", err)
			}
		})
	}
}

func TestGetConfigFields(t *testing.T) {
	// Reset koanf instance
	defer ResetConfig()
	setupTestConfig()

	// Initialize config
	_, err := InitConfig("nvcf-nats-auth-callout-service", "")
	if err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	fields, err := GetConfigFields()
	if err != nil {
		t.Fatalf("Failed to get config fields: %v", err)
	}

	if len(fields) == 0 {
		t.Error("Expected at least one config field")
	}

	// Check that we have expected fields
	expectedFields := map[string]bool{
		"Server Port":    false,
		"Service Name":   false,
		"NATS URL":       false,
		"NKey Seed":      false,
		"NKey Signature": false,
	}

	for _, field := range fields {
		if _, exists := expectedFields[field.Name]; exists {
			expectedFields[field.Name] = true
		}

		// Validate field structure
		if field.Name == "" {
			t.Error("Config field name should not be empty")
		}
		// NKey fields can be empty in test config, so don't validate their values
		if field.Value == "" && field.Name != "NKey Seed" && field.Name != "NKey Signature" {
			t.Error("Config field value should not be empty")
		}
	}

	// Check all expected fields were found
	for fieldName, found := range expectedFields {
		if !found {
			t.Errorf("Expected field '%s' not found in config fields", fieldName)
		}
	}
}

func TestGetConfigFieldsWithEnvironmentVariables(t *testing.T) {
	// Set environment variables
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT", "7777")
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME", "env-test-service")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME")

	// Reset koanf instance
	defer ResetConfig()
	setupTestConfig()

	// Initialize config
	_, err := InitConfig("nvcf-nats-auth-callout-service", "")
	if err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	fields, err := GetConfigFields()
	if err != nil {
		t.Fatalf("Failed to get config fields: %v", err)
	}

	// Check that environment variables are reflected
	fieldValues := make(map[string]string)
	for _, field := range fields {
		fieldValues[field.Name] = field.Value
	}

	if fieldValues["Server Port"] != "7777" {
		t.Errorf("Expected Server Port to be '7777' from env var, got '%s'", fieldValues["Server Port"])
	}

	if fieldValues["Service Name"] != "env-test-service" {
		t.Errorf("Expected Service Name to be 'env-test-service' from env var, got '%s'", fieldValues["Service Name"])
	}
}

func TestDisplayConfigFields(t *testing.T) {
	fields := []ConfigField{
		{Name: "Test Port", Value: "8080", Source: "config", Description: "Test port"},
		{Name: "Test Service", Value: "test-service", Source: "env", Description: "Test service"},
	}

	output := DisplayConfigFields(fields)

	if output == "" {
		t.Error("Expected non-empty output from DisplayConfigFields")
	}

	// Check that both fields are in the output
	if !strings.Contains(output, "Test Port:") {
		t.Error("Expected 'Test Port:' in output")
	}
	if !strings.Contains(output, "Test Service:") {
		t.Error("Expected 'Test Service:' in output")
	}
	if !strings.Contains(output, "8080") {
		t.Error("Expected port value '8080' in output")
	}
	if !strings.Contains(output, "test-service") {
		t.Error("Expected service name 'test-service' in output")
	}

	// Check formatting - should be aligned
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Errorf("Expected 2 lines of output, got %d", len(lines))
	}
}

func TestGetConfigSummary(t *testing.T) {
	defer ResetConfig()
	setupTestConfig()

	_, err := InitConfig("test", "")
	if err != nil {
		t.Fatalf("Failed to init config: %v", err)
	}

	summary, err := GetConfigSummary()
	if err != nil {
		t.Fatalf("Failed to get config summary: %v", err)
	}

	// Check that summary contains expected keys
	if _, exists := summary["server"]; !exists {
		t.Error("Summary should contain server section")
	}
	if _, exists := summary["service"]; !exists {
		t.Error("Summary should contain service section")
	}

	// Verify specific values
	serverSection := summary["server"].(map[string]string)
	if serverSection["port"] == "" {
		t.Error("Server port should be set in summary")
	}

	serviceSection := summary["service"].(map[string]string)
	if serviceSection["name"] == "" {
		t.Error("Service name should be set in summary")
	}
}

func TestGetCurrentConfig(t *testing.T) {
	defer ResetConfig()
	setupTestConfig()

	// Initialize config first
	_, err := InitConfig("test", "")
	if err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	// Test GetCurrentConfig
	cfg, err := GetCurrentConfig()
	if err != nil {
		t.Fatalf("Failed to get current config: %v", err)
	}

	if cfg.Server.Port == "" {
		t.Error("Expected server port to be set")
	}

	if cfg.Service.Name == "" {
		t.Error("Expected service name to be set")
	}
}

func TestGetCurrentConfigWithoutInit(t *testing.T) {
	defer ResetConfig()

	// Test GetCurrentConfig without initialization
	_, err := GetCurrentConfig()
	if err == nil {
		t.Error("Expected error when getting config without initialization")
	}
}

func TestConfigFieldStructure(t *testing.T) {
	field := ConfigField{
		Name:        "Test Field",
		Value:       "test-value",
		Source:      "config",
		Description: "Test description",
	}

	if field.Name != "Test Field" {
		t.Error("ConfigField Name not set correctly")
	}
	if field.Value != "test-value" {
		t.Error("ConfigField Value not set correctly")
	}
	if field.Source != "config" {
		t.Error("ConfigField Source not set correctly")
	}
	if field.Description != "Test description" {
		t.Error("ConfigField Description not set correctly")
	}
}

func TestEnvironmentVariablePrecedence(t *testing.T) {
	defer ResetConfig()
	setupTestConfig()

	// Set environment variables
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT", "9999")
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME", "env-service")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME")

	cfg, err := InitConfig("test", "")
	if err != nil {
		t.Fatalf("Failed to init config: %v", err)
	}

	// Environment variables should override config file values
	if cfg.Server.Port != "9999" {
		t.Errorf("Expected port '9999' from env var, got '%s'", cfg.Server.Port)
	}
	if cfg.Service.Name != "env-service" {
		t.Errorf("Expected service name 'env-service' from env var, got '%s'", cfg.Service.Name)
	}
}

func TestDebugFunction(t *testing.T) {
	// Reset koanf instance
	defer ResetConfig()
	setupTestConfig()

	// Test Debug without initialization
	debugInfo := Debug()
	if debugInfo["error"] == nil {
		t.Error("Expected error in debug info when koanf not initialized")
	}

	// Initialize config
	_, err := InitConfig("nvcf-nats-auth-callout-service", "")
	if err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	// Test Debug after initialization
	debugInfo = Debug()
	if debugInfo["error"] != nil {
		t.Error("Expected no error in debug info after initialization")
	}

	if debugInfo["env_prefix"] != "NVCF_NATS_AUTH_CALLOUT_SERVICE" {
		t.Errorf("Expected env_prefix to be 'NVCF_NATS_AUTH_CALLOUT_SERVICE', got '%v'", debugInfo["env_prefix"])
	}

	if _, exists := debugInfo["all_settings"]; !exists {
		t.Error("Expected 'all_settings' in debug info")
	}
}
