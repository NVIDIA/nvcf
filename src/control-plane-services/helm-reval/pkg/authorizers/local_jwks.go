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
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/jwtparse"
)

var localInstrumentation = "reval.authorizers.local"

// Local verifies the bearer token as a JWT against a JWKS endpoint (self-hosted path).
// Non-JWT bearer material (e.g. opaque API keys) is rejected by this authorizer.
//
// When ValidateRequiredScopes or RenderRequiredScopes are set, the JWT's "scopes" claim
// is checked against the required scope for the matched endpoint:
//   - /v1/validate → ValidateRequiredScopes
//   - /v1/render   → RenderRequiredScopes
type Local struct {
	Parser                 *jwtparse.Parser
	ValidateRequiredScopes string
	RenderRequiredScopes   string
	Logger                 *zap.Logger
}

func (l Local) Evaluate(ctx context.Context, ac *AuthzContext) (AuthzResult, error) {
	_, span := otel.Tracer(localInstrumentation).Start(ctx, "reval.authz.local")
	defer span.End()

	if l.Parser == nil {
		return AuthzResult{}, errors.New("local authorizer: parser is not configured")
	}
	if ac.Input.BearerToken == "" {
		return AuthzResult{Allow: false}, nil
	}
	token, err := l.Parser.Parse(ctx, ac.Input.BearerToken)
	if err != nil || token == nil || !token.Valid {
		return AuthzResult{Allow: false}, nil
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		err := fmt.Errorf("local authorizer: unexpected claims type %T, expected jwt.MapClaims", token.Claims)
		if l.Logger != nil {
			l.Logger.Error("unexpected JWT claims type", zap.Error(err))
		}
		return AuthzResult{}, err
	}

	// Per-endpoint scope check: if a required scope is configured for this path, enforce it.
	requiredScope := l.requiredScopeForPath(ac.Input.Path)
	if requiredScope != "" {
		scopesRaw, hasClaim := claims["scopes"]
		if !hasClaim || !hasScope(scopesRaw, requiredScope) {
			return AuthzResult{Allow: false}, nil
		}
	}

	return AuthzResult{Allow: true}, nil
}

// requiredScopeForPath returns the configured required scope for the given request path.
func (l Local) requiredScopeForPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/validate"):
		return l.ValidateRequiredScopes
	case strings.HasPrefix(path, "/v1/render"):
		return l.RenderRequiredScopes
	default:
		return ""
	}
}

// hasScope checks whether scopesRaw (from a JWT claim) contains the required scope.
// Supports string (space-separated) and []interface{} claim formats.
func hasScope(scopesRaw interface{}, required string) bool {
	switch v := scopesRaw.(type) {
	case string:
		for _, s := range strings.Fields(v) {
			if s == required {
				return true
			}
		}
	case []interface{}:
		for _, s := range v {
			if str, ok := s.(string); ok && str == required {
				return true
			}
		}
	}
	return false
}
