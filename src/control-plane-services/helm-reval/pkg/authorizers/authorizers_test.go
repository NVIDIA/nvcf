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

package authorizers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// allowAuthorizer always allows.
type allowAuthorizer struct{}

func (allowAuthorizer) Evaluate(_ context.Context, _ *authorizers.AuthzContext) (authorizers.AuthzResult, error) {
	return authorizers.AuthzResult{Allow: true}, nil
}

// denyAuthorizer always denies.
type denyAuthorizer struct{}

func (denyAuthorizer) Evaluate(_ context.Context, _ *authorizers.AuthzContext) (authorizers.AuthzResult, error) {
	return authorizers.AuthzResult{Allow: false}, nil
}

// errorAuthorizer always returns an error.
type errorAuthorizer struct{ err error }

func (e errorAuthorizer) Evaluate(_ context.Context, _ *authorizers.AuthzContext) (authorizers.AuthzResult, error) {
	return authorizers.AuthzResult{}, e.err
}

func newAC(method, path, token string) *authorizers.AuthzContext {
	return &authorizers.AuthzContext{
		Request: httptest.NewRequest(method, path, nil),
		Input: authorizers.AuthzInput{
			Method:      method,
			Path:        path,
			BearerToken: token,
		},
		Extra: map[string]any{},
	}
}

// ── ExtractBearerToken ────────────────────────────────────────────────────────

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantToken string
		wantErr   error
	}{
		{"happy path", "Bearer mytoken", "mytoken", nil},
		{"case insensitive scheme", "bearer mytoken", "mytoken", nil},
		{"BEARER uppercase", "BEARER mytoken", "mytoken", nil},
		{"token with spaces preserved", "Bearer a b", "a b", nil},
		{"empty header", "", "", authorizers.ErrNoTokenInRequest},
		{"whitespace only (too short)", "   ", "", authorizers.ErrNoTokenInRequest},
		{"leading whitespace not trimmed", "  Bearer mytoken", "", authorizers.ErrNoTokenInRequest},
		{"wrong scheme", "Basic user:pass", "", authorizers.ErrNoTokenInRequest},
		{"no space after scheme", "Bearertoken", "", authorizers.ErrNoTokenInRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := authorizers.ExtractBearerToken(tt.header)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantToken, got)
			}
		})
	}
}

func TestBearerTokenFromRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer abc123")
	tok, err := authorizers.BearerTokenFromRequest(r)
	require.NoError(t, err)
	assert.Equal(t, "abc123", tok)

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err = authorizers.BearerTokenFromRequest(r2)
	require.ErrorIs(t, err, authorizers.ErrNoTokenInRequest)
}

// ── Disabled ──────────────────────────────────────────────────────────────────

func TestDisabled_AlwaysAllow(t *testing.T) {
	d := authorizers.Disabled{}
	res, err := d.Evaluate(context.Background(), newAC("GET", "/", ""))
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

// ── Chain ─────────────────────────────────────────────────────────────────────

func TestChain(t *testing.T) {
	ctx := context.Background()

	t.Run("empty chain denies", func(t *testing.T) {
		chain := authorizers.Chain(nil)
		res, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	})

	t.Run("any allow → allow without calling rest", func(t *testing.T) {
		chain := authorizers.Chain([]authorizers.Authorizer{allowAuthorizer{}, denyAuthorizer{}})
		res, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.NoError(t, err)
		assert.True(t, res.Allow)
	})

	t.Run("all deny → deny", func(t *testing.T) {
		chain := authorizers.Chain([]authorizers.Authorizer{denyAuthorizer{}, denyAuthorizer{}})
		res, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	})

	t.Run("error in step is wrapped", func(t *testing.T) {
		sentinel := errors.New("pdp down")
		chain := authorizers.Chain([]authorizers.Authorizer{errorAuthorizer{err: sentinel}})
		_, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("nil step is skipped", func(t *testing.T) {
		chain := authorizers.Chain([]authorizers.Authorizer{nil, allowAuthorizer{}})
		res, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.NoError(t, err)
		assert.True(t, res.Allow)
	})

	t.Run("all nil steps deny", func(t *testing.T) {
		chain := authorizers.Chain([]authorizers.Authorizer{nil, nil})
		res, err := chain.Evaluate(ctx, newAC("GET", "/", ""))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	})
}

// ── EvaluateMiddleware ────────────────────────────────────────────────────────

func TestEvaluateMiddleware(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	var failureErr error
	onFailure := func(r *http.Request, w http.ResponseWriter, err error) {
		failureErr = err
		w.WriteHeader(http.StatusUnauthorized)
	}

	t.Run("missing auth header calls onFailure", func(t *testing.T) {
		reached = false
		failureErr = nil
		mw := authorizers.EvaluateMiddleware(allowAuthorizer{}, nil, onFailure)
		r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, r)
		assert.False(t, reached)
		require.ErrorIs(t, failureErr, authorizers.ErrNoTokenInRequest)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("deny calls onFailure with ErrDenied", func(t *testing.T) {
		reached = false
		failureErr = nil
		mw := authorizers.EvaluateMiddleware(denyAuthorizer{}, nil, onFailure)
		r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, r)
		assert.False(t, reached)
		require.ErrorIs(t, failureErr, authorizers.ErrDenied)
	})

	t.Run("authorizer error calls onFailure", func(t *testing.T) {
		reached = false
		failureErr = nil
		sentinel := errors.New("boom")
		mw := authorizers.EvaluateMiddleware(errorAuthorizer{err: sentinel}, nil, onFailure)
		r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, r)
		assert.False(t, reached)
		require.ErrorIs(t, failureErr, sentinel)
	})

	t.Run("allow passes to next handler", func(t *testing.T) {
		reached = false
		failureErr = nil
		mw := authorizers.EvaluateMiddleware(allowAuthorizer{}, nil, onFailure)
		r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, r)
		assert.True(t, reached)
		assert.Nil(t, failureErr)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ── Local (hasScope + requiredScopeForPath) ───────────────────────────────────

func TestLocal_NilParser(t *testing.T) {
	l := authorizers.Local{Parser: nil}
	_, err := l.Evaluate(context.Background(), newAC("POST", "/v1/validate", "tok"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parser is not configured")
}

func TestLocal_EmptyToken(t *testing.T) {
	// Parser is nil — the nil-parser error fires before the empty-token check.
	l := authorizers.Local{Parser: nil}
	_, err := l.Evaluate(context.Background(), newAC("POST", "/v1/validate", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parser is not configured")
}

// ── BuildChain ────────────────────────────────────────────────────────────────

func TestBuildChain(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing enabled → empty chain, no error", func(t *testing.T) {
		steps, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{JWT: config.JWTAuthConfig{Enabled: false}}, nil,
		)
		require.NoError(t, err)
		assert.Empty(t, steps)
	})

	t.Run("nil configs → empty chain, no error", func(t *testing.T) {
		steps, err := authorizers.BuildChain(ctx, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, steps)
	})

	t.Run("jwt enabled but no jwk-set-url → error", func(t *testing.T) {
		_, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{JWT: config.JWTAuthConfig{Enabled: true, JWKSetURL: ""}}, nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwk-set-url")
	})

	t.Run("oidc enabled but no introspect-url → error", func(t *testing.T) {
		_, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{OIDC: config.OIDCConfig{Enabled: true, IntrospectURL: ""}}, nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "introspect URL is required")
	})

	t.Run("jwt enabled with valid jwks → Local authorizer", func(t *testing.T) {
		jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
		}))
		defer jwksSrv.Close()

		auths, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{JWT: config.JWTAuthConfig{Enabled: true, JWKSetURL: jwksSrv.URL}}, nil,
		)
		require.NoError(t, err)
		require.Len(t, auths, 1)
		_, isLocal := auths[0].(authorizers.Local)
		assert.True(t, isLocal)
	})

	t.Run("oidc only → ICMSIntrospect authorizer", func(t *testing.T) {
		auths, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{OIDC: config.OIDCConfig{Enabled: true, IntrospectURL: "http://example/introspect"}}, nil,
		)
		require.NoError(t, err)
		require.Len(t, auths, 1)
		_, isICMS := auths[0].(*authorizers.ICMSIntrospect)
		assert.True(t, isICMS)
	})

	t.Run("both enabled → two-step chain (Local + ICMSIntrospect)", func(t *testing.T) {
		jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
		}))
		defer jwksSrv.Close()
		auths, err := authorizers.BuildChain(ctx,
			&config.AuthnConfig{
				JWT:  config.JWTAuthConfig{Enabled: true, JWKSetURL: jwksSrv.URL},
				OIDC: config.OIDCConfig{Enabled: true, IntrospectURL: "http://example/introspect"},
			}, nil,
		)
		require.NoError(t, err)
		require.Len(t, auths, 2)
		_, isLocal := auths[0].(authorizers.Local)
		_, isICMS := auths[1].(*authorizers.ICMSIntrospect)
		assert.True(t, isLocal)
		assert.True(t, isICMS)
	})
}
