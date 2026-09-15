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

package jwtparse_test

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

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/jwtparse"
)

// testKID is the key ID embedded in both the JWKS and the signed test JWTs.
const testKID = "test-key-1"

// generateRSAKey creates a fresh 2048-bit RSA key for each test.
func generateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

// jwksServer starts an httptest.Server that serves a JWKS containing the given RSA public key.
func jwksServer(t *testing.T, priv *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	jwkKey, err := jwk.FromRaw(priv.Public())
	require.NoError(t, err)
	require.NoError(t, jwkKey.Set(jwk.KeyIDKey, testKID))
	require.NoError(t, jwkKey.Set(jwk.AlgorithmKey, "RS256"))

	set := jwk.NewSet()
	require.NoError(t, set.AddKey(jwkKey))

	raw, err := json.Marshal(set)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signJWT creates a signed RS256 JWT with the given claims and RSA key.
func signJWT(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKID
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
}

// ── NewCachedParser ───────────────────────────────────────────────────────────

func TestNewCachedParser_EmptyURL(t *testing.T) {
	_, err := jwtparse.NewCachedParser(context.Background(), "")
	require.ErrorIs(t, err, jwtparse.ErrMissingJWKSURL)
}

func TestNewCachedParser_UnreachableURL(t *testing.T) {
	// Use a local address no server is listening on.
	_, err := jwtparse.NewCachedParser(context.Background(), "http://127.0.0.1:1")
	require.Error(t, err)
}

func TestNewCachedParser_ValidServer(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)
	require.NotNil(t, p)
}

// ── Parser.Parse ──────────────────────────────────────────────────────────────

func TestParse_ValidToken(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	signed := signJWT(t, priv, validClaims())
	tok, err := p.Parse(context.Background(), signed)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.True(t, tok.Valid)
}

func TestParse_ExpiredToken(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	claims := jwt.MapClaims{
		"sub": "user-123",
		"exp": time.Now().Add(-time.Hour).Unix(), // expired
	}
	signed := signJWT(t, priv, claims)

	tok, err := p.Parse(context.Background(), signed)
	// jwt.Parse returns the token even on expiry; we verify it is invalid.
	require.Error(t, err)
	if tok != nil {
		assert.False(t, tok.Valid)
	}
}

func TestParse_WrongSigningKey(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	// Sign with a different key that the server doesn't know about.
	otherKey := generateRSAKey(t)
	signed := signJWT(t, otherKey, validClaims())

	_, err = p.Parse(context.Background(), signed)
	require.Error(t, err)
}

func TestParse_MissingKID(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	// Build a token without the kid header.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims())
	// kid is deliberately omitted from tok.Header
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = p.Parse(context.Background(), signed)
	require.ErrorIs(t, err, jwtparse.ErrJwtMissingKID)
}

func TestParse_UnknownKID(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims())
	tok.Header["kid"] = "nonexistent-key-id"
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = p.Parse(context.Background(), signed)
	require.ErrorIs(t, err, jwtparse.ErrJwtWrongKey)
}

func TestParse_MalformedToken(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	_, err = p.Parse(context.Background(), "not.a.jwt")
	require.Error(t, err)
}

func TestParse_TokenWithSubjectAndScopes(t *testing.T) {
	priv := generateRSAKey(t)
	srv := jwksServer(t, priv)

	p, err := jwtparse.NewCachedParser(context.Background(), srv.URL)
	require.NoError(t, err)

	claims := jwt.MapClaims{
		"sub":    "alice",
		"scopes": []string{"helmreval:validate", "helmreval:render"},
		"exp":    time.Now().Add(time.Hour).Unix(),
	}
	signed := signJWT(t, priv, claims)

	tok, err := p.Parse(context.Background(), signed)
	require.NoError(t, err)
	assert.True(t, tok.Valid)

	mc, ok := tok.Claims.(jwt.MapClaims)
	require.True(t, ok)
	sub, _ := mc.GetSubject()
	assert.Equal(t, "alice", sub)
}
