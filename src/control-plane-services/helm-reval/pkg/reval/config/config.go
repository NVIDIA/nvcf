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

import "time"

// RevalConfig holds all service configuration.
type RevalConfig struct {
	HTTP      HTTPConfig
	Auth      AuthnConfig
	Logging   LoggingConfig
	Telemetry TelemetryConfig
	Tracing   TracingConfig

	// For dry run until object and image validation are enforced by default.
	// Skip validation of objects.
	SkipValidateObjects bool `mapstructure:"skip-validate-objects"`
	// Skip validation of images.
	SkipValidateImages bool `mapstructure:"skip-validate-images"`
	// Skip sanitization of object metadata (labels and annotations).
	SkipSanitizeObjectMetadata bool `mapstructure:"skip-sanitize-object-metadata"`
	// Configured labels to preserve
	PreserveLabels []string `mapstructure:"preserve-labels"`
	// Configured annotations to preserve
	PreserveAnnotations []string `mapstructure:"preserve-annotations"`
}

type TracingConfig struct {
	Enabled              bool   `usage:"Enable tracing"`
	Endpoint             string `default:"otel-collector.example.com:4317" usage:"Where to send traces to"`
	LightstepAccessToken string `mapstructure:"lightstep-access-token" usage:"Lightstep access token, also enables lightstep auth header"`
	Insecure             bool   `usage:"Enable connecting to http instead of https to send traces"`
}

type TelemetryConfig struct {
	ServiceName               string `mapstructure:"service-name" default:"nvcf-reval" usage:"Service name"`
	ServiceVersion            string `mapstructure:"-"`
	GitCommit                 string `mapstructure:"-"`
	DeploymentEnvironmentName string `mapstructure:"deployment-environment-name" default:"production" usage:"Deployment environment name"`
}

type HTTPConfig struct {
	ApiPort        uint16 `mapstructure:"api-port" default:"8080" usage:"API port"`
	MetricsPort    uint16 `mapstructure:"metrics-port" default:"8081" usage:"Metrics port"`
	Local          bool
	ManagementPort uint16 `mapstructure:"management-port" default:"8082" usage:"Management port"`
}

// JWTAuthConfig configures local JWKS JWT authentication (auth.jwt.*).
type JWTAuthConfig struct {
	Enabled                bool   `usage:"Enable JWT authentication (self-hosted JWKS)"`
	JWKSetURL              string `mapstructure:"jwk-set-url" usage:"JWKS endpoint URL for JWT verification"`
	ValidateRequiredScopes string `mapstructure:"validate-required-scopes" usage:"Required scope in JWT for /v1/validate (e.g. helmreval:validate)"`
	RenderRequiredScopes   string `mapstructure:"render-required-scopes" usage:"Required scope in JWT for /v1/render (e.g. helmreval:render)"`
}

// AuthnConfig configures authentication.
//
// Two modes may be enabled individually or together (OR semantics):
//   - auth.jwt.enabled=true with a JWKS URL → local JWT verification against a JWKS endpoint
//   - auth.oidc.enabled=true with an introspect URL → remote RFC 7662 verification via ICMS
//
// At least one mode must be enabled; the server refuses to start otherwise.
type AuthnConfig struct {
	JWT  JWTAuthConfig `mapstructure:"jwt"`
	OIDC OIDCConfig    `mapstructure:"oidc"`
}

// OIDCConfig configures JWT verification via an external introspection endpoint
// (RFC 7662). Used in deployments where signature verification is delegated to
// an identity service (e.g. ICMS) rather than performed locally against a JWKS.
//
// When enabled the introspection endpoint must validate the token's signature,
// audience, and freshness; the server only enforces:
//   - active=true in the response
//   - the resolved subject identifies an NVCA workload (PSAT
//     system:serviceaccount:<ns>:nvca, or a SPIFFE SVID ending in /nvca)
type OIDCConfig struct {
	Enabled       bool   `default:"false" usage:"Enable JWT verification via external introspection endpoint (RFC 7662)"`
	IntrospectURL string `mapstructure:"introspect-url" usage:"Token introspection endpoint URL (POST {token})"`
	// Registered manually as a duration flag because the autobinder treats time.Duration as int64.
	CacheTTL time.Duration `mapstructure:"cache-ttl" flag:"-" default:"5m" usage:"TTL for caching successful introspection results, bounded by the JWT exp claim. 0 disables caching."`
}

type LoggingConfig struct {
	ZapConfiguration string `mapstructure:"zap-configuration" default:"production" usage:"Zap configuration production/development"`
	Level            string `default:"info" usage:"Logging level"`
}
