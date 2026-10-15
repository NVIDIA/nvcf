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

package oauth

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"go.uber.org/zap"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Config contains OAuth2 JWT plugin configuration.
type Config struct {
	JWKSEndpointURL  string                        `mapstructure:"jwks_endpoint_url"           yaml:"jwks_endpoint_url"`
	Issuer           string                        `mapstructure:"issuer"                      yaml:"issuer"`
	Audience         string                        `mapstructure:"audience"                    yaml:"audience"`
	ScopePermissions map[string]*types.Permissions `mapstructure:"scope_permissions,omitempty" yaml:"scope_permissions"`
}

// OAuthPlugin implements OAuth2 JWT Bearer token authentication.
type OAuthPlugin struct {
	config  *Config
	keyfunc keyfunc.Keyfunc
	parser  *jwt.Parser
	logger  *zap.Logger
}

// Claims represents the expected JWT claims structure.
type Claims struct {
	jwt.RegisteredClaims
	Scopes []string `json:"scopes"`
}

// NewPlugin creates a new OAuth JWT plugin instance.
func NewPlugin(configData any, logger *zap.Logger) (*OAuthPlugin, error) {
	oauthConfig, err := parseOAuthConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OAuth config: %w", err)
	}

	// Create keyfunc for JWKS using v3 API.
	jwks, err := keyfunc.NewDefault([]string{oauthConfig.JWKSEndpointURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS keyfunc: %w", err)
	}

	parserOptions := []jwt.ParserOption{
		jwt.WithIssuer(oauthConfig.Issuer),
		jwt.WithAudience(oauthConfig.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(10 * time.Second),
	}

	parser := jwt.NewParser(parserOptions...)

	plugin := &OAuthPlugin{
		config:  oauthConfig,
		keyfunc: jwks,
		parser:  parser,
		logger:  logger,
	}

	return plugin, nil
}

// Authenticate validates the JWT and returns auth result.
func (p *OAuthPlugin) Authenticate(_ context.Context, request *types.Request) (*types.Result, error) {
	if request.Payload == "" {
		return nil, types.NewAuthError(types.ErrTypeInvalidToken, "missing JWT", 401)
	}

	claims, err := p.validateJWT(request.Payload)
	if err != nil {
		return nil, err
	}

	permissions, err := p.buildPermissions(claims)
	if err != nil {
		return nil, types.NewAuthError(types.ErrTypeUnauthorized, "invalid claims", 403)
	}

	return &types.Result{
		UserID:      claims.Subject,
		Account:     request.Account, // Use account from request
		Permissions: permissions,
	}, nil
}

// validateJWT validates the JWT token using keyfunc and parser options.
func (p *OAuthPlugin) validateJWT(tokenString string) (*Claims, error) {
	token, err := p.parser.ParseWithClaims(tokenString, &Claims{}, p.keyfunc.Keyfunc)
	if err != nil {
		p.logger.Error("JWT validation failed", zap.Error(err))
		return nil, types.NewAuthError(types.ErrTypeUnauthorized, "invalid JWT token", 401)
	}

	if !token.Valid {
		return nil, types.NewAuthError(types.ErrTypeUnauthorized, "invalid JWT token", 401)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, types.NewAuthError(types.ErrTypeUnauthorized, "invalid JWT claims", 401)
	}

	p.logger.Info("JWT token validated successfully", zap.String("user_id", claims.Subject), zap.Strings("scopes", claims.Scopes))

	return claims, nil
}

// buildPermissions constructs permissions based on OAuth2 scopes from JWT.
func (p *OAuthPlugin) buildPermissions(claims *Claims) (*types.Permissions, error) {
	matchedScope := false
	// Start with minimal base permissions
	permissions := &types.Permissions{
		Publish: &types.PubPermissions{
			Allow: []string{},
		},
		Subscribe: &types.SubPermissions{
			Allow: []string{"_INBOX.>"}, // Always allow inbox for responses
		},
	}

	// Check each scope the user has against the configured permissions
	for _, scope := range claims.Scopes {
		if scopePerms, exists := p.config.ScopePermissions[scope]; exists {
			// Merge permissions from this scope
			if scopePerms.Publish != nil && scopePerms.Publish.Allow != nil {
				permissions.Publish.Allow = append(permissions.Publish.Allow, scopePerms.Publish.Allow...)
			}
			if scopePerms.Subscribe != nil && scopePerms.Subscribe.Allow != nil {
				permissions.Subscribe.Allow = append(permissions.Subscribe.Allow, scopePerms.Subscribe.Allow...)
			}
			p.logger.Debug("Applied scope permissions", zap.String("scope", scope))
			matchedScope = true
		} else {
			p.logger.Debug("Scope not found in configuration", zap.String("scope", scope))
		}
	}

	if !matchedScope {
		p.logger.Warn("No scope permissions matched", zap.Strings("scopes", claims.Scopes))
		return nil, fmt.Errorf("no scope permissions matched")
	}

	return permissions, nil
}

// parseOAuthConfig parses OAuth plugin configuration.
func parseOAuthConfig(configData any) (*Config, error) {
	oauthConfig := &Config{}
	if err := config.DecodeConfig(configData, oauthConfig); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	if oauthConfig.JWKSEndpointURL == "" {
		return nil, fmt.Errorf("jwks_endpoint_url is required")
	}
	if oauthConfig.Issuer == "" {
		return nil, fmt.Errorf("issuer is required")
	}
	if oauthConfig.Audience == "" {
		return nil, fmt.Errorf("audience is required")
	}

	return oauthConfig, nil
}
