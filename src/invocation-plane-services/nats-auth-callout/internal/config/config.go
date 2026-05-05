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
	"fmt"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
)

// Koanf instance for configuration management
var k *koanf.Koanf

// Embedded default configuration data (set by main package)
var embeddedDefaultConfig []byte

// SetEmbeddedDefaults sets the embedded default configuration
func SetEmbeddedDefaults(configData []byte) {
	embeddedDefaultConfig = configData
}

// ServerConfig holds server-specific configuration
type ServerConfig struct {
	Port string `mapstructure:"port" yaml:"port" json:"port"`
}

// LoggingSamplingConfig holds log sampling configuration
type LoggingSamplingConfig struct {
	Initial    int `mapstructure:"initial" yaml:"initial" json:"initial"`
	Thereafter int `mapstructure:"thereafter" yaml:"thereafter" json:"thereafter"`
}

// LoggingFieldsConfig holds additional fields for logging
type LoggingFieldsConfig struct {
	ServiceName string `mapstructure:"service_name" yaml:"service_name" json:"service_name"`
	Version     string `mapstructure:"version" yaml:"version" json:"version"`
	Environment string `mapstructure:"environment" yaml:"environment" json:"environment"`
}

// LoggingConfig holds logging-specific configuration
type LoggingConfig struct {
	Level            string                `mapstructure:"level" yaml:"level" json:"level"`
	Format           string                `mapstructure:"format" yaml:"format" json:"format"`
	Output           string                `mapstructure:"output" yaml:"output" json:"output"`
	Caller           bool                  `mapstructure:"caller" yaml:"caller" json:"caller"`
	StacktraceLevel  string                `mapstructure:"stacktrace_level" yaml:"stacktrace_level" json:"stacktrace_level"`
	Development      bool                  `mapstructure:"development" yaml:"development" json:"development"`
	DisableTimestamp bool                  `mapstructure:"disable_timestamp" yaml:"disable_timestamp" json:"disable_timestamp"`
	Sampling         LoggingSamplingConfig `mapstructure:"sampling" yaml:"sampling" json:"sampling"`
	Fields           LoggingFieldsConfig   `mapstructure:"fields" yaml:"fields" json:"fields"`
}

// TracingHeadersConfig holds tracing headers configuration
type TracingHeadersConfig struct {
	Authorization string `mapstructure:"authorization" yaml:"authorization" json:"authorization"`
	XAPIKey       string `mapstructure:"x-api-key" yaml:"x-api-key" json:"x-api-key"`
}

// OtelTracingConfig holds OpenTelemetry-specific configuration
type OtelTracingConfig struct {
	Endpoint           string               `mapstructure:"endpoint" yaml:"endpoint" json:"endpoint"`
	HTTPEndpoint       string               `mapstructure:"http_endpoint" yaml:"http_endpoint" json:"http_endpoint"`
	Insecure           bool                 `mapstructure:"insecure" yaml:"insecure" json:"insecure"`
	Environment        string               `mapstructure:"environment" yaml:"environment" json:"environment"`
	SamplingRatio      string               `mapstructure:"sampling_ratio" yaml:"sampling_ratio" json:"sampling_ratio"`
	Headers            TracingHeadersConfig `mapstructure:"headers" yaml:"headers" json:"headers"`
	TimeoutMs          int                  `mapstructure:"timeout_ms" yaml:"timeout_ms" json:"timeout_ms"`
	RetryDelayMs       int                  `mapstructure:"retry_delay_ms" yaml:"retry_delay_ms" json:"retry_delay_ms"`
	MaxExportBatchSize int                  `mapstructure:"max_export_batch_size" yaml:"max_export_batch_size" json:"max_export_batch_size"`
	ExportTimeoutMs    int                  `mapstructure:"export_timeout_ms" yaml:"export_timeout_ms" json:"export_timeout_ms"`
	MaxQueueSize       int                  `mapstructure:"max_queue_size" yaml:"max_queue_size" json:"max_queue_size"`
	ScheduleDelayMs    int                  `mapstructure:"schedule_delay_ms" yaml:"schedule_delay_ms" json:"schedule_delay_ms"`
	Compression        string               `mapstructure:"compression" yaml:"compression" json:"compression"`
}

// LightstepTracingConfig holds Lightstep-specific configuration
type LightstepTracingConfig struct {
	Endpoint           string `mapstructure:"endpoint" yaml:"endpoint" json:"endpoint"`
	AccessToken        string `mapstructure:"access_token" yaml:"access_token" json:"access_token"`
	Environment        string `mapstructure:"environment" yaml:"environment" json:"environment"`
	SamplingRatio      string `mapstructure:"sampling_ratio" yaml:"sampling_ratio" json:"sampling_ratio"`
	TimeoutMs          int    `mapstructure:"timeout_ms" yaml:"timeout_ms" json:"timeout_ms"`
	MaxExportBatchSize int    `mapstructure:"max_export_batch_size" yaml:"max_export_batch_size" json:"max_export_batch_size"`
	ExportTimeoutMs    int    `mapstructure:"export_timeout_ms" yaml:"export_timeout_ms" json:"export_timeout_ms"`
	MaxQueueSize       int    `mapstructure:"max_queue_size" yaml:"max_queue_size" json:"max_queue_size"`
	ScheduleDelayMs    int    `mapstructure:"schedule_delay_ms" yaml:"schedule_delay_ms" json:"schedule_delay_ms"`
	Compression        string `mapstructure:"compression" yaml:"compression" json:"compression"`
	Insecure           bool   `mapstructure:"insecure" yaml:"insecure" json:"insecure"`
}

// TracingConfig holds tracing-specific configuration
type TracingConfig struct {
	Provider  string                 `mapstructure:"provider" yaml:"provider" json:"provider"`
	Enabled   bool                   `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Otel      OtelTracingConfig      `mapstructure:"otel" yaml:"otel" json:"otel"`
	Lightstep LightstepTracingConfig `mapstructure:"lightstep" yaml:"lightstep" json:"lightstep"`
}

// MetricsConfig holds metrics-specific configuration
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Port    string `mapstructure:"port" yaml:"port" json:"port"`
}

// Config represents the complete application configuration
type Config struct {
	Server  ServerConfig  `mapstructure:"server" yaml:"server" json:"server"`
	Service ServiceConfig `mapstructure:"service" yaml:"service" json:"service"`
	Logging LoggingConfig `mapstructure:"logging" yaml:"logging" json:"logging"`
	Tracing TracingConfig `mapstructure:"tracing" yaml:"tracing" json:"tracing"`
	Metrics MetricsConfig `mapstructure:"metrics" yaml:"metrics" json:"metrics"`
}

// InitConfig initializes the configuration management with Koanf
func InitConfig(serviceName string, customConfigPaths ...string) (*Config, error) {
	if k != nil {
		return nil, fmt.Errorf("koanf already initialized")
	}

	k = koanf.New(".")

	// Load embedded defaults first (if available)
	if embeddedDefaultConfig != nil {
		if err := k.Load(rawbytes.Provider(embeddedDefaultConfig), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("error reading embedded default config: %w", err)
		}
	} else {
		// No embedded config available (e.g., during tests) - rely on environment variables and config files
		return nil, fmt.Errorf("no embedded default configuration available")
	}

	// If a custom config file is specified, load it (merges with existing config)
	for _, customConfigPath := range customConfigPaths {
		if customConfigPath != "" {
			fileType := filepath.Ext(customConfigPath)
			var parser koanf.Parser
			switch fileType {
			case ".json":
				parser = json.Parser()
			case ".yaml", ".yml":
				parser = yaml.Parser()
			default:
				return nil, fmt.Errorf("unsupported config file type: %s", fileType)
			}
			provider := file.Provider(customConfigPath)
			if err := k.Load(provider, parser); err != nil {
				return nil, fmt.Errorf("error reading custom config file '%s': %w", customConfigPath, err)
			}
		}
	}

	// Always load environment variables (so they can override defaults or previous values)
	if err := k.Load(env.Provider("NVCF_NATS_AUTH_CALLOUT_SERVICE_", ".", func(s string) string {
		// Convert environment variable to config key
		// Remove prefix and convert to lowercase
		key := strings.TrimPrefix(s, "NVCF_NATS_AUTH_CALLOUT_SERVICE_")
		key = strings.ToLower(key)
		// Replace underscores with dots for nested keys
		key = strings.ReplaceAll(key, "_", ".")
		return key
	}), nil); err != nil {
		return nil, fmt.Errorf("error loading environment variables: %w", err)
	}

	// Unmarshal config into struct using mapstructure
	var config Config
	if err := k.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "mapstructure",
	}); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	return &config, nil
}

// BindFlag binds a CLI flag to a configuration key
func BindFlag(key string, cmd *cobra.Command, flagName string) error {
	if k == nil {
		return fmt.Errorf("koanf not initialized")
	}

	// Load all flags from the command
	return k.Load(posflag.Provider(cmd.Flags(), ".", k), nil)
}

// ValidateConfig validates the configuration
func ValidateConfig(cfg *Config) error {
	if cfg.Server.Port == "" {
		return fmt.Errorf("server port cannot be empty")
	}
	if cfg.Service.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if cfg.Service.NatsURL == "" {
		return fmt.Errorf("NATS URL cannot be empty")
	}
	if cfg.Service.NkeySeed == "" {
		return fmt.Errorf("NKey seed cannot be empty")
	}
	if cfg.Service.NkeySignature == "" {
		return fmt.Errorf("NKey signature cannot be empty")
	}
	return nil
}

// Debug returns debug information about the configuration
func Debug() map[string]any {
	if k == nil {
		return map[string]any{
			"error": "koanf not initialized",
		}
	}

	return map[string]any{
		"all_settings": k.All(),
		"env_prefix":   "NVCF_NATS_AUTH_CALLOUT_SERVICE",
	}
}

// GetCurrentConfig returns the current configuration values (including bound flags)
func GetCurrentConfig() (*Config, error) {
	if k == nil {
		return nil, fmt.Errorf("koanf not initialized")
	}

	var config Config
	if err := k.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "mapstructure",
	}); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	return &config, nil
}

// ConfigField represents a configuration field for display
type ConfigField struct {
	Name        string
	Value       string
	Source      string
	Description string
}

// GetConfigFields returns all configuration fields dynamically
func GetConfigFields() ([]ConfigField, error) {
	if k == nil {
		return nil, fmt.Errorf("koanf not initialized")
	}

	cfg, err := GetCurrentConfig()
	if err != nil {
		return nil, err
	}

	fields := []ConfigField{
		{
			Name:        "Server Port",
			Value:       cfg.Server.Port,
			Source:      getConfigSource("server.port"),
			Description: "HTTP server port",
		},
		{
			Name:        "Service Name",
			Value:       cfg.Service.Name,
			Source:      getConfigSource("service.name"),
			Description: "Service identifier",
		},
		{
			Name:        "NATS URL",
			Value:       cfg.Service.NatsURL,
			Source:      getConfigSource("service.nats_url"),
			Description: "NATS server URL",
		},
		{
			Name:        "NKey Seed",
			Value:       maskSensitiveValue(cfg.Service.NkeySeed),
			Source:      getConfigSource("service.nkey_seed"),
			Description: "NKey seed for authentication",
		},
		{
			Name:        "NKey Signature",
			Value:       maskSensitiveValue(cfg.Service.NkeySignature),
			Source:      getConfigSource("service.nkey_signature"),
			Description: "Signing key seed for JWT signing",
		},
	}

	return fields, nil
}

// getConfigSource determines where a configuration value came from
func getConfigSource(key string) string {
	if k == nil {
		return "unknown"
	}

	// Koanf doesn't track sources like Viper does
	// We indicate it came from the configuration system
	if k.Exists(key) {
		return "config"
	}
	return "default"
}

// maskSensitiveValue masks sensitive configuration values
func maskSensitiveValue(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

// DisplayConfigFields formats configuration fields for display
func DisplayConfigFields(fields []ConfigField) string {
	var result strings.Builder

	maxNameLen := 0
	for _, field := range fields {
		nameWithColon := field.Name + ":"
		if len(nameWithColon) > maxNameLen {
			maxNameLen = len(nameWithColon)
		}
	}

	for _, field := range fields {
		nameWithColon := field.Name + ":"
		result.WriteString(fmt.Sprintf("%-*s %s\n", maxNameLen+1, nameWithColon, field.Value))
	}

	return result.String()
}

// GetConfigSummary returns a summary of current configuration
func GetConfigSummary() (map[string]any, error) {
	if k == nil {
		return nil, fmt.Errorf("koanf not initialized")
	}

	cfg, err := GetCurrentConfig()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"server": map[string]string{
			"port": cfg.Server.Port,
		},
		"service": map[string]string{
			"name": cfg.Service.Name,
		},
		"env_prefix": "NVCF_NATS_AUTH_CALLOUT_SERVICE",
	}, nil
}

// ResetConfig resets the configuration state (useful for testing)
func ResetConfig() {
	k = nil
}
