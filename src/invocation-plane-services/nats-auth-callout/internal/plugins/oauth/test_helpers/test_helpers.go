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

package test_helpers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// CreateMockJWKSServer creates a mock JWKS server for testing OAuth JWT validation
func CreateMockJWKSServer(t *testing.T) *httptest.Server {
	t.Helper()
	// Use the test RSA key for JWKS
	testRSAKey := getTestRSAKey()
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/jwks.json" {

			jwks := map[string]any{
				"keys": []map[string]any{
					{
						"kty": "RSA",
						"use": "sig",
						"kid": "test-key-1",
						"n":   testRSAKey.N,
						"e":   testRSAKey.E,
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(jwks); err != nil {
				t.Logf("Failed to encode JWKS response: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(jwksServer.Close)
	return jwksServer
}

// TestRSAKey structure for JWKS
// Exported for use in helpers
type TestRSAKey struct {
	N string `json:"n"`
	E string `json:"e"`
}

var testPrivateKey = Must(rsa.GenerateKey(rand.Reader, 2048))

func Must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

// getTestRSAKey returns a test RSA key for JWKS
func getTestRSAKey() *TestRSAKey {
	// Convert to base64url for JWKS
	n := base64.RawURLEncoding.EncodeToString(testPrivateKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(testPrivateKey.E)).Bytes())

	return &TestRSAKey{
		N: n,
		E: e,
	}
}

// OAuthTestClaims represents the JWT claims for testing
// Exported for use in helpers
type OAuthTestClaims struct {
	jwt.RegisteredClaims
	Scopes []string `json:"scopes"`
}

// CreateValidJWT creates a valid JWT token for testing
func CreateValidJWT(t *testing.T, userID string, scopes []string) string {
	t.Helper()
	claims := OAuthTestClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://test-issuer.example.com",
			Subject:   userID,
			Audience:  []string{"test-account"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(time.Now()),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Scopes: scopes,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key-1"

	tokenString, err := token.SignedString(testPrivateKey)
	if err != nil {
		t.Fatalf("Failed to sign JWT: %v", err)
	}

	return tokenString
}

// CreateExpiredJWT creates an expired JWT token for testing
func CreateExpiredJWT(t *testing.T, userID string, scopes []string) string {
	t.Helper()
	claims := OAuthTestClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://test-issuer.example.com",
			Subject:   userID,
			Audience:  []string{"test-account"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)), // Expired
			NotBefore: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
		Scopes: scopes,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key-1"
	tokenString, err := token.SignedString(testPrivateKey)
	if err != nil {
		t.Fatalf("Failed to sign JWT: %v", err)
	}
	return tokenString
}

// CreateJWTWithAudience creates a JWT token with specific audience
func CreateJWTWithAudience(t *testing.T, userID string, scopes []string, audience string) string {
	t.Helper()
	claims := OAuthTestClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://test-issuer.example.com",
			Subject:   userID,
			Audience:  []string{audience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(time.Now()),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Scopes: scopes,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key-1"

	tokenString, err := token.SignedString(testPrivateKey)
	if err != nil {
		t.Fatalf("Failed to sign JWT: %v", err)
	}

	return tokenString
}
