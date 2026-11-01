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
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/jwtparse"
)

const localTestKID = "local-test-key"

func localRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k
}

func localJWKSServer(t *testing.T, priv *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	jwkKey, err := jwk.FromRaw(priv.Public())
	require.NoError(t, err)
	require.NoError(t, jwkKey.Set(jwk.KeyIDKey, localTestKID))
	require.NoError(t, jwkKey.Set(jwk.AlgorithmKey, "RS256"))
	set := jwk.NewSet()
	require.NoError(t, set.AddKey(jwkKey))
	raw, err := json.Marshal(set)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func localSignJWT(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = localTestKID
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func buildLocalAuthorizer(t *testing.T, priv *rsa.PrivateKey, srv *httptest.Server, validateScope, renderScope string) authorizers.Local {
	t.Helper()
	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)
	return authorizers.Local{
		Parser:                 p,
		ValidateRequiredScopes: validateScope,
		RenderRequiredScopes:   renderScope,
	}
}

// ── Local.Evaluate — basic token validation ───────────────────────────────────

func TestLocal_ValidToken_NoScopeRequired(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "")

	claims := jwt.MapClaims{"sub": "alice", "exp": time.Now().Add(time.Hour).Unix()}
	ac := newAC("POST", "/v1/validate", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

func TestLocal_ExpiredToken_Denied(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "")

	claims := jwt.MapClaims{"sub": "alice", "exp": time.Now().Add(-time.Hour).Unix()}
	ac := newAC("POST", "/v1/validate", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestLocal_InvalidToken_Denied(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "")

	ac := newAC("POST", "/v1/validate", "not.a.real.jwt")
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

// ── Local.Evaluate — scope enforcement (string claim) ────────────────────────

func TestLocal_StringScope_MatchingScope_Allowed(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "helmreval:validate", "helmreval:render")

	claims := jwt.MapClaims{
		"sub":    "alice",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": "helmreval:validate helmreval:render",
	}
	ac := newAC("POST", "/v1/validate", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

func TestLocal_StringScope_MissingScope_Denied(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "helmreval:validate", "")

	claims := jwt.MapClaims{
		"sub":    "alice",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": "helmreval:render", // has render scope but not validate
	}
	ac := newAC("POST", "/v1/validate", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestLocal_StringScope_NoScopesClaim_Denied(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "helmreval:validate", "")

	claims := jwt.MapClaims{"sub": "alice", "exp": time.Now().Add(time.Hour).Unix()}
	// no scopes claim at all
	ac := newAC("POST", "/v1/validate", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

// ── Local.Evaluate — scope enforcement ([]interface{} claim) ─────────────────

func TestLocal_SliceScope_MatchingScope_Allowed(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "helmreval:render")

	claims := jwt.MapClaims{
		"sub":    "carol",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": []interface{}{"helmreval:validate", "helmreval:render"},
	}
	ac := newAC("POST", "/v1/render", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

func TestLocal_SliceScope_MissingScope_Denied(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "helmreval:render")

	claims := jwt.MapClaims{
		"sub":    "carol",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": []interface{}{"helmreval:validate"},
	}
	ac := newAC("POST", "/v1/render", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

// ── requiredScopeForPath — path routing ───────────────────────────────────────

func TestLocal_PathRouting_ValidatePath(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	// Only validate scope required; render scope is empty
	l := buildLocalAuthorizer(t, priv, srv, "helmreval:validate", "")

	claims := jwt.MapClaims{
		"sub":    "alice",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": "helmreval:validate",
	}
	signed := localSignJWT(t, priv, claims)

	// /v1/validate → needs helmreval:validate → should allow
	ac := newAC("POST", "/v1/validate", signed)
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)

	// /v1/render → no scope required → should allow
	ac2 := newAC("POST", "/v1/render", signed)
	res2, err := l.Evaluate(context.Background(), ac2)
	require.NoError(t, err)
	assert.True(t, res2.Allow)
}

func TestLocal_PathRouting_OtherPath_NoScopeCheck(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	// Both scopes required but /healthz is not under /v1/validate or /v1/render
	l := buildLocalAuthorizer(t, priv, srv, "helmreval:validate", "helmreval:render")

	claims := jwt.MapClaims{"sub": "alice", "exp": time.Now().Add(time.Hour).Unix()}
	// No scopes claim, but path is /healthz — no scope check
	ac := newAC("GET", "/healthz", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

func TestLocal_PathRouting_RenderWithSubpath(t *testing.T) {
	priv := localRSAKey(t)
	srv := localJWKSServer(t, priv)
	l := buildLocalAuthorizer(t, priv, srv, "", "helmreval:render")

	claims := jwt.MapClaims{
		"sub":    "alice",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"scopes": "helmreval:render",
	}
	ac := newAC("POST", "/v1/render/something", localSignJWT(t, priv, claims))
	res, err := l.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
}
