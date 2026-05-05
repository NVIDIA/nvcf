/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/auth"
)

// TestProbe_Success verifies that ProbeWithClient correctly fetches OIDC +
// JWKS metadata and constructs a Fingerprint with the expected fields.
func TestProbe_Success(t *testing.T) {
	var srvURL string // filled after server starts

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   "https://cp.example/",
			"jwks_uri": srvURL + "/.well-known/jwks.json",
		})
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{map[string]string{"kid": "abc-2026"}},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	fp, err := auth.ProbeWithClient(context.Background(), srv.URL, srv.Client())
	require.NoError(t, err)
	require.NotNil(t, fp)

	assert.Equal(t, "https://cp.example/", fp.IssuerURL)
	assert.Equal(t, "abc-2026", fp.JWKSKid)
	assert.Equal(t, "https://cp.example/api-keys", fp.APIKeysEndpoint)
}

// TestProbe_RelativeJWKSURI verifies that a relative jwks_uri (path-only) is
// resolved against the server base URL, as some embedded OIDC providers emit
// path-only values.
func TestProbe_RelativeJWKSURI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Intentionally path-only jwks_uri
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   "https://cp.local/",
			"jwks_uri": "/.well-known/jwks.json",
		})
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{map[string]string{"kid": "key-001"}},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	fp, err := auth.ProbeWithClient(context.Background(), srv.URL, srv.Client())
	require.NoError(t, err)
	require.NotNil(t, fp)

	assert.Equal(t, "https://cp.local/", fp.IssuerURL)
	assert.Equal(t, "key-001", fp.JWKSKid)
	assert.Equal(t, "https://cp.local/api-keys", fp.APIKeysEndpoint)
}

// TestProbe_503CacheCorruption verifies that a 5xx from the control plane
// produces a *ProbeError with Category="cache_corruption".
func TestProbe_503CacheCorruption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := auth.ProbeWithClient(context.Background(), srv.URL, srv.Client())
	require.Error(t, err)

	var pe *auth.ProbeError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "cache_corruption", pe.Category)
}

// TestProbe_MissingIssuer verifies that an OIDC document without an issuer
// field produces a cache_corruption ProbeError.
func TestProbe_MissingIssuer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Missing issuer field
		_ = json.NewEncoder(w).Encode(map[string]string{
			"jwks_uri": "/.well-known/jwks.json",
		})
	}))
	defer srv.Close()

	_, err := auth.ProbeWithClient(context.Background(), srv.URL, srv.Client())
	require.Error(t, err)

	var pe *auth.ProbeError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "cache_corruption", pe.Category)
}

// TestProbe_EmptyJWKSKeys verifies that a JWKS with an empty keys array
// produces a cache_corruption ProbeError.
func TestProbe_EmptyJWKSKeys(t *testing.T) {
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   "https://cp.example/",
			"jwks_uri": srvURL + "/.well-known/jwks.json",
		})
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	_, err := auth.ProbeWithClient(context.Background(), srv.URL, srv.Client())
	require.Error(t, err)

	var pe *auth.ProbeError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "cache_corruption", pe.Category)
}

// TestFingerprint_Equal verifies the Equal method's field-by-field comparison
// and nil-safety semantics.
func TestFingerprint_Equal(t *testing.T) {
	a := &auth.Fingerprint{IssuerURL: "u", JWKSKid: "k", APIKeysEndpoint: "e"}
	b := &auth.Fingerprint{IssuerURL: "u", JWKSKid: "k", APIKeysEndpoint: "e"}

	assert.True(t, a.Equal(b), "identical fields should be equal")

	b.JWKSKid = "different"
	assert.False(t, a.Equal(b), "differing JWKSKid should not be equal")

	b.JWKSKid = "k"
	b.IssuerURL = "other"
	assert.False(t, a.Equal(b), "differing IssuerURL should not be equal")

	b.IssuerURL = "u"
	b.APIKeysEndpoint = "other"
	assert.False(t, a.Equal(b), "differing APIKeysEndpoint should not be equal")

	// nil safety
	var n *auth.Fingerprint
	assert.False(t, a.Equal(n), "non-nil.Equal(nil) should be false")
	assert.False(t, n.Equal(a), "nil.Equal(non-nil) should be false")
	assert.False(t, n.Equal(n), "nil.Equal(nil) should be false")
}

// TestCache_Valid exercises all branches of Cache.Valid.
func TestCache_Valid(t *testing.T) {
	fp := &auth.Fingerprint{IssuerURL: "u", JWKSKid: "k", APIKeysEndpoint: "e"}
	now := time.Now()

	c := &auth.Cache{
		Token:       "tok",
		ExpiresAt:   now.Add(1 * time.Hour),
		Fingerprint: fp,
	}

	assert.True(t, c.Valid(now, fp), "unexpired token + matching fingerprint should be valid")

	// Expired token
	c.ExpiresAt = now.Add(-1 * time.Second)
	assert.False(t, c.Valid(now, fp), "expired token should not be valid")

	// Restore expiry, mismatched fingerprint
	c.ExpiresAt = now.Add(1 * time.Hour)
	other := &auth.Fingerprint{IssuerURL: "different"}
	assert.False(t, c.Valid(now, other), "mismatched fingerprint should not be valid")

	// No token
	empty := &auth.Cache{ExpiresAt: now.Add(1 * time.Hour), Fingerprint: fp}
	assert.False(t, empty.Valid(now, fp), "empty token should not be valid")

	// Zero expiry means no-expiry (backwards compat)
	noExpiry := &auth.Cache{Token: "tok", Fingerprint: fp}
	assert.True(t, noExpiry.Valid(now, fp), "zero ExpiresAt should be treated as no expiry")

	// Nil cache
	var nilC *auth.Cache
	assert.False(t, nilC.Valid(now, fp), "nil cache should not be valid")
}
