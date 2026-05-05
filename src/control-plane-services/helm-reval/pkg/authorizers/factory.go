// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authorizers

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/jwtparse"
)

// NewLocalAuthorizer constructs a Local (JWKS) authorizer from the JWT auth config.
func NewLocalAuthorizer(ctx context.Context, jwt *config.JWTAuthConfig, logger *zap.Logger) (Authorizer, error) {
	if strings.TrimSpace(jwt.JWKSetURL) == "" {
		return nil, fmt.Errorf("auth.jwt: enabled but auth.jwt.jwk-set-url is not configured")
	}
	parser, err := jwtparse.NewCachedParser(ctx, jwt.JWKSetURL)
	if err != nil {
		return nil, fmt.Errorf("auth.jwt: failed to initialize JWKS parser: %w", err)
	}
	return Local{
		Parser:                 parser,
		ValidateRequiredScopes: jwt.ValidateRequiredScopes,
		RenderRequiredScopes:   jwt.RenderRequiredScopes,
		Logger:                 logger,
	}, nil
}

// NewOIDCAuthorizer constructs an ICMSIntrospect authorizer from the OIDC config.
func NewOIDCAuthorizer(oidc *config.OIDCConfig, logger *zap.Logger) (Authorizer, error) {
	a, err := NewICMSIntrospect(oidc.IntrospectURL, oidc.CacheTTL, logger)
	if err != nil {
		return nil, fmt.Errorf("auth.oidc: %w", err)
	}
	return a, nil
}

// BuildChain assembles the enabled authorizers into a chain (OR semantics).
//
// Two modes may be enabled individually or together:
//   - auth.jwt.enabled=true with a JWKS URL → Local authorizer
//   - auth.oidc.enabled=true with an introspect URL → ICMSIntrospect authorizer
//
// If neither is enabled, auth is disabled and a warning is logged.
func BuildChain(ctx context.Context, authn *config.AuthnConfig, logger *zap.Logger) ([]Authorizer, error) {
	var steps []Authorizer

	if authn != nil && authn.JWT.Enabled {
		a, err := NewLocalAuthorizer(ctx, &authn.JWT, logger)
		if err != nil {
			return nil, err
		}
		steps = append(steps, a)
	}

	if authn != nil && authn.OIDC.Enabled {
		a, err := NewOIDCAuthorizer(&authn.OIDC, logger)
		if err != nil {
			return nil, err
		}
		steps = append(steps, a)
	}

	if len(steps) == 0 && logger != nil {
		logger.Warn("auth is disabled: no authentication mode enabled")
	}
	return steps, nil
}
