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
	"errors"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/tracing"
)

var (
	ErrMissingAuthProvider               = errors.New("auth: provider is required when auth is enabled")
	ErrInvalidAuthProvider               = errors.New("auth: invalid provider specified")
	ErrMissingJWKSetURL                  = errors.New("auth: jwk-set-url is required")
	ErrMissingJWTIssuer                  = errors.New("auth: issuer is required for the jwt provider")
	ErrMissingJWTAudience                = errors.New("auth: audience is required for the jwt provider")
	ErrMissingJWTTenantClaim             = errors.New("auth: tenant-claim is required for the jwt provider")
	ErrJWTLegacyEndpointsUnsupported     = errors.New("auth: jwt provider requires deprecate-endpoints to disable legacy routes without tenant partition keys")
	ErrMissingPolicyCredsFile            = errors.New("policy: creds-file is required for hot-reload support")
	ErrMissingPolicyTokenIssuerAddr      = errors.New("policy: token-issuer-addr is required")
	ErrMissingPolicyEvaluatorAddr        = errors.New("policy: policy-evaluator-addr is required")
	ErrMissingPolicyNamespace            = errors.New("policy: namespace is required")
	ErrMissingPolicyFQDN                 = errors.New("policy: policy-fqdn is required")
	ErrInvalidPolicyCredsRefreshInterval = errors.New("policy: creds-refresh-interval must be greater than 0")
)

// Top-level config
type Config struct {
	Database           DBConfig               `mapstructure:"database"`
	Telemetry          common.TelemetryConfig `mapstructure:"telemetry"`
	Tracing            tracing.TracingConfig  `mapstructure:"tracing"`
	Logging            logging.LoggingConfig  `mapstructure:"logging"`
	Auth               AuthConfig             `mapstructure:"auth"`
	Service            ServiceConfig          `mapstructure:"service"`
	Indexer            IndexerConfig          `mapstructure:"indexer"`
	Profiling          ProfilingConfig        `mapstructure:"profiling"`
	Publisher          PublisherConfig        `mapstructure:"publisher"`
	HTTP               HTTPClientConfig       `mapstructure:"http"`
	Pagination         PaginationConfig       `mapstructure:"pagination"`
	Stats              StatsConfig            `mapstructure:"stats"`
	ID                 string                 `mapstructure:"id"`
	Secret             string                 `mapstructure:"secret"`
	DeprecateEndpoints bool                   `mapstructure:"deprecate-endpoints"`
	SecretsPath        string                 `mapstructure:"secrets-path"`
	SelfManaged        bool                   `mapstructure:"self-managed"`
}

type PublisherConfig struct {
	BatchedPublisher BatchedPublisherConfig `mapstructure:"batched"`
	Cloudevents      CloudEventsConfig      `mapstructure:"cloudevents"`
}

type AuthConfig struct {
	Enabled              bool
	Provider             string       `mapstructure:"provider"`
	JWKSetUrl            string       `mapstructure:"jwk-set-url"`
	Issuer               string       `mapstructure:"issuer"`
	Audience             string       `mapstructure:"audience"`
	TenantClaim          string       `mapstructure:"tenant-claim"`
	CacheRefreshInterval int          `mapstructure:"cache-refresh-interval"`
	Policy               PolicyConfig `mapstructure:"policy"`
}

type PolicyConfig struct {
	Namespace                  string `mapstructure:"namespace"`
	PolicyFQDN                 string `mapstructure:"policy-fqdn"`
	PolicyEvaluatorAddr        string `mapstructure:"policy-evaluator-addr"`
	TokenIssuerAddr            string `mapstructure:"token-issuer-addr"`
	ClientID                   string `mapstructure:"client-id"`
	ClientSecret               string `mapstructure:"client-secret"`
	CredsFile                  string `mapstructure:"creds-file"`
	CredentialsRefreshInterval int64  `mapstructure:"creds-refresh-interval"`
	SubjectField               string `mapstructure:"subject-field"`
	APIKeyField                string `mapstructure:"api-key-field"`
}

func (p PolicyConfig) WithDefaults() PolicyConfig {
	if p.SubjectField == "" {
		p.SubjectField = "subject"
	}
	if p.APIKeyField == "" {
		p.APIKeyField = "apiKey"
	}
	return p
}

// ValidateAuthConfig validates the authentication configuration.
// selfManaged indicates a self-managed deployment where OAuth2 credential fields are not required.
func ValidateAuthConfig(cfg AuthConfig, selfManaged bool) error {
	if !cfg.Enabled {
		return nil
	}

	switch cfg.Provider {
	case "jwt":
		if cfg.JWKSetUrl == "" {
			return ErrMissingJWKSetURL
		}
		if cfg.Issuer == "" {
			return ErrMissingJWTIssuer
		}
		if cfg.Audience == "" {
			return ErrMissingJWTAudience
		}
		if cfg.TenantClaim == "" {
			return ErrMissingJWTTenantClaim
		}
	case "policy":
		if cfg.JWKSetUrl == "" {
			return ErrMissingJWKSetURL
		}
		if cfg.Policy.PolicyEvaluatorAddr == "" {
			return ErrMissingPolicyEvaluatorAddr
		}
		if cfg.Policy.Namespace == "" {
			return ErrMissingPolicyNamespace
		}
		if cfg.Policy.PolicyFQDN == "" {
			return ErrMissingPolicyFQDN
		}
		if !selfManaged {
			if cfg.Policy.CredsFile == "" {
				return ErrMissingPolicyCredsFile
			}
			if cfg.Policy.TokenIssuerAddr == "" {
				return ErrMissingPolicyTokenIssuerAddr
			}
			if cfg.Policy.CredentialsRefreshInterval <= 0 {
				return ErrInvalidPolicyCredsRefreshInterval
			}
		}
	case "":
		return ErrMissingAuthProvider
	default:
		return ErrInvalidAuthProvider
	}

	return nil
}

// ValidateEndpointAuthConfig prevents scope-only JWT authorization from being
// used with legacy routes that do not carry a tenant partition key.
func ValidateEndpointAuthConfig(cfg Config) error {
	if cfg.Auth.Enabled && cfg.Auth.Provider == "jwt" && !cfg.DeprecateEndpoints {
		return ErrJWTLegacyEndpointsUnsupported
	}
	return nil
}

// Database config
type DBConfig struct {
	Provider        string          `mapstructure:"provider"`
	CassandraConfig CassandraConfig `mapstructure:"cassandra"`
}

ok type CassandraConfig struct {
	Hosts              []string `mapstructure:"hosts"`
	Port               int      `mapstructure:"port"`
	Keyspace           string   `mapstructure:"keyspace"`
	Username           string   `mapstructure:"username"`
	Password           string   `mapstructure:"password"`
	Consistency        string   `mapstructure:"consistency"`
	NumConns           int      `mapstructure:"num-conns"`
	PubKeyB64          string   `mapstructure:"pub-key-b64"`
	PrivKeyB64         string   `mapstructure:"priv-key-b64"`
	CACertB64          string   `mapstructure:"ca-cert-b64"`
	PubKeyPath         string   `mapstructure:"pub-key-path"`
	PrivKeyPath        string   `mapstructure:"priv-key-path"`
	CACertPath         string   `mapstructure:"ca-cert-path"`
	InsecureSkipVerify bool     `mapstructure:"insecure-skip-verify"`
}

type ServiceConfig struct {
	ApiPort      int `mapstructure:"api-port"`
	InternalPort int `mapstructure:"internal-port"`
}

type ProfilingConfig struct {
	Enabled bool
}

type IndexerConfig struct {
	Enabled           bool `mapstructure:"enabled"`
	ChannelBufferSize int  `mapstructure:"channel-buffer-size"`
	WorkerCount       int  `mapstructure:"worker-count"`
}

type HTTPClientConfig struct {
	MaxIdleConns             int `mapstructure:"max-idle-conns"`
	MaxIdleConnsPerHost      int `mapstructure:"max-idle-conns-per-host"`
	IdleConnTimeoutSec       int `mapstructure:"idle-conn-timeout-sec"`
	TLSHandshakeTimeoutSec   int `mapstructure:"tls-handshake-timeout-sec"`
	ExpectContinueTimeoutSec int `mapstructure:"expect-continue-timeout-sec"`
}

// PaginationConfig defines configurable pagination behavior
type PaginationConfig struct {
	DefaultPageSize int `mapstructure:"default-page-size"`
	MaxPageSize     int `mapstructure:"max-page-size"`
	MinPageSize     int `mapstructure:"min-page-size"`
}

// GetDefaultPaginationConfig returns default pagination configuration values
func GetDefaultPaginationConfig() PaginationConfig {
	return PaginationConfig{
		DefaultPageSize: 50,  // Default page size when no limit specified
		MaxPageSize:     100, // Maximum allowed page size
		MinPageSize:     1,   // Minimum allowed page size
	}
}

// StatsConfig defines configuration for stats table behavior
type StatsConfig struct {
	// StatsEnabledEventNames lists which event names should trigger stats_v3 updates
	// If empty, all events will update stats (default behavior)
	StatsEnabledEventNames []string `mapstructure:"stats-enabled-event-names"`
	// FilteredStatsEnabledEventNames lists which event names should update the filtered stats view.
	// The mapstructure key is retained for compatibility. If empty, the view is disabled (no writes).
	FilteredStatsEnabledEventNames []string `mapstructure:"ngc-stats-enabled-event-names"`
}

// WithDefaults returns a PaginationConfig with default values for unset fields
func (p PaginationConfig) WithDefaults() PaginationConfig {
	defaults := GetDefaultPaginationConfig()

	if p.DefaultPageSize <= 0 {
		p.DefaultPageSize = defaults.DefaultPageSize
	}
	if p.MaxPageSize <= 0 {
		p.MaxPageSize = defaults.MaxPageSize
	}
	if p.MinPageSize <= 0 {
		p.MinPageSize = defaults.MinPageSize
	}

	return p
}
