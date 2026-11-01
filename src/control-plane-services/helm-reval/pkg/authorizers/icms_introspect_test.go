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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// makeJWTWithExp builds a syntactically-valid (but unsigned) JWT whose payload
// contains the given exp. Only the payload's exp is meaningful for these tests
// — the introspection authorizer never verifies signatures locally.
func makeJWTWithExp(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": exp.Unix()})
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return fmt.Sprintf("%s.%s.%s", header, body, sig)
}

// newMockIntrospectServer returns an httptest.Server that simulates an RFC 7662
// introspection endpoint. The handler receives the token from the request body
// and returns an IntrospectResult plus HTTP status code.
func newMockIntrospectServer(t *testing.T, handler func(token string) (IntrospectResult, int)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req IntrospectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, code := handler(req.Token)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(result)
	}))
}

func newAC(token string) *AuthzContext {
	return &AuthzContext{
		Input: AuthzInput{Method: http.MethodPost, Path: "/v1/validate", BearerToken: token},
		Extra: map[string]any{},
	}
}

func TestICMSIntrospect_NewRequiresURL(t *testing.T) {
	a, err := NewICMSIntrospect("", 0, zap.NewNop())
	assert.Nil(t, a)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "introspect URL is required")
}

func TestICMSIntrospect_AllowsValidPSAT(t *testing.T) {
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		return IntrospectResult{
			Active:    true,
			Sub:       "system:serviceaccount:nvca-system:nvca",
			Aud:       "nvcf-icms",
			Iss:       "https://kubernetes.default.svc",
			ClusterID: "cluster-abc-123",
			TokenType: "psat",
		}, http.StatusOK
	})
	defer srv.Close()

	a, err := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	require.NoError(t, err)

	ac := newAC("any-token")
	res, err := a.Evaluate(context.Background(), ac)
	require.NoError(t, err)
	assert.True(t, res.Allow)
	assert.Equal(t, "system:serviceaccount:nvca-system:nvca", ac.Extra["subject"])
	assert.Equal(t, "cluster-abc-123", ac.Extra["clusterID"])
	assert.Equal(t, "psat", ac.Extra["tokenType"])
}

func TestICMSIntrospect_AllowsValidSPIFFE(t *testing.T) {
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		return IntrospectResult{
			Active:    true,
			Sub:       "spiffe://nvcf.nvidia.com/cluster/xyz/nvca",
			ClusterID: "cluster-xyz",
			TokenType: "spiffe",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.True(t, res.Allow)
}

func TestICMSIntrospect_DeniesInactive(t *testing.T) {
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		return IntrospectResult{Active: false, Error: "expired"}, http.StatusOK
	})
	defer srv.Close()
	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

// RFC 7662 §2.2: a payload missing `active` MUST be treated as inactive.
// The IntrospectResult.Active field is bool (not *bool) so absence unmarshals
// to false. This test guards that invariant.
func TestICMSIntrospect_DeniesMissingActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"system:serviceaccount:ns:nvca"}`))
	}))
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_DeniesNon200(t *testing.T) {
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		return IntrospectResult{}, http.StatusInternalServerError
	})
	defer srv.Close()
	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_DeniesUnreachableEndpoint(t *testing.T) {
	a, _ := NewICMSIntrospect("http://127.0.0.1:1", 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_DeniesMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid`))
	}))
	defer srv.Close()
	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC("tok"))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_RejectsOversizedToken(t *testing.T) {
	a, _ := NewICMSIntrospect("http://127.0.0.1:1", 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC(strings.Repeat("a", MaxJWTSize+1)))
	assert.ErrorIs(t, err, ErrJWTTooLarge)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_DeniesEmptyToken(t *testing.T) {
	a, _ := NewICMSIntrospect("http://127.0.0.1:1", 0, zap.NewNop())
	res, err := a.Evaluate(context.Background(), newAC(""))
	require.NoError(t, err)
	assert.False(t, res.Allow)
}

func TestICMSIntrospect_RejectsNonNvcaSubjects(t *testing.T) {
	cases := []struct {
		name string
		sub  string
	}{
		{"sibling operator SA", "system:serviceaccount:nvca-system:nvca-operator"},
		{"prefix-attack SA", "system:serviceaccount:nvca-system:nvca-attacker"},
		{"worker SA", "system:serviceaccount:nvcf:worker"},
		{"arbitrary default SA", "system:serviceaccount:default:default"},
		{"malformed missing ns", "system:serviceaccount:nvca"},
		{"malformed trailing segment", "system:serviceaccount:ns:nvca:extra"},
		{"spiffe trailing-path attack", "spiffe://nvcf.nvidia.com/cluster/xyz/nvca/malicious"},
		{"spiffe prefix-attack", "spiffe://nvcf.nvidia.com/cluster/xyz/nvca-attacker"},
		{"non-psat non-spiffe", "user:mallory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
				return IntrospectResult{Active: true, Sub: tc.sub, TokenType: "psat"}, http.StatusOK
			})
			defer srv.Close()
			a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
			res, err := a.Evaluate(context.Background(), newAC("tok"))
			require.NoError(t, err)
			assert.False(t, res.Allow, "expected deny for sub=%s", tc.sub)
		})
	}
}

func TestICMSIntrospect_CachesSuccessfulResults(t *testing.T) {
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{
			Active: true, Sub: "system:serviceaccount:ns:nvca", ClusterID: "c1", TokenType: "psat",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Minute, zap.NewNop())
	for i := 0; i < 3; i++ {
		res, err := a.Evaluate(context.Background(), newAC("same-token"))
		require.NoError(t, err)
		assert.True(t, res.Allow)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "subsequent calls should hit cache")
}

func TestICMSIntrospect_DoesNotCacheWhenTTLZero(t *testing.T) {
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{
			Active: true, Sub: "system:serviceaccount:ns:nvca", TokenType: "psat",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, 0, zap.NewNop())
	for i := 0; i < 3; i++ {
		_, _ = a.Evaluate(context.Background(), newAC("same-token"))
	}
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "TTL=0 must disable caching")
}

func TestICMSIntrospect_CacheBoundedByJWTExp(t *testing.T) {
	// JWT exp is 50ms in the future; configured TTL is 1h. Cache must honor
	// the shorter of the two — second call after exp should hit the server again.
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{
			Active: true, Sub: "system:serviceaccount:ns:nvca", TokenType: "psat",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Hour, zap.NewNop())
	tok := makeJWTWithExp(t, time.Now().Add(50*time.Millisecond))

	_, _ = a.Evaluate(context.Background(), newAC(tok))
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
	time.Sleep(120 * time.Millisecond)
	_, _ = a.Evaluate(context.Background(), newAC(tok))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "expired cache entry must trigger a fresh call")
}

func TestICMSIntrospect_DistinctTokensCachedSeparately(t *testing.T) {
	var calls int32
	srv := newMockIntrospectServer(t, func(token string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{
			Active: true, Sub: "system:serviceaccount:ns:nvca", ClusterID: token, TokenType: "psat",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Minute, zap.NewNop())
	_, _ = a.Evaluate(context.Background(), newAC("token-a"))
	_, _ = a.Evaluate(context.Background(), newAC("token-b"))
	_, _ = a.Evaluate(context.Background(), newAC("token-a")) // cached
	_, _ = a.Evaluate(context.Background(), newAC("token-b")) // cached
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "each distinct token should produce one introspection call")
}

func TestICMSIntrospect_OpaqueTokenUsesWallClockTTL(t *testing.T) {
	// Opaque (non-JWT) tokens have no exp claim; cache TTL must fall back to
	// the configured CacheTTL only. Two calls within TTL should hit the cache.
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{
			Active: true, Sub: "system:serviceaccount:ns:nvca", TokenType: "opaque",
		}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Minute, zap.NewNop())
	opaque := "not-a-jwt-just-some-bytes" // no dots → extractJWTExpiry returns ok=false
	_, _ = a.Evaluate(context.Background(), newAC(opaque))
	_, _ = a.Evaluate(context.Background(), newAC(opaque))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "opaque tokens should still be cached using configured TTL")
}

func TestICMSIntrospect_DoesNotCacheInactiveResults(t *testing.T) {
	// active=false is not cached: the same token may become valid shortly after
	// due to clock skew or an nbf window. Each request must hit ICMS.
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{Active: false, Error: "not yet valid"}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Minute, zap.NewNop())
	for i := 0; i < 3; i++ {
		res, err := a.Evaluate(context.Background(), newAC("same-token"))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	}
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "inactive results must not be cached (clock-skew / nbf risk)")
}

func TestICMSIntrospect_CachesNonNvcaSubjectResults(t *testing.T) {
	var calls int32
	srv := newMockIntrospectServer(t, func(string) (IntrospectResult, int) {
		atomic.AddInt32(&calls, 1)
		return IntrospectResult{Active: true, Sub: "system:serviceaccount:ns:not-nvca", TokenType: "psat"}, http.StatusOK
	})
	defer srv.Close()

	a, _ := NewICMSIntrospect(srv.URL, time.Minute, zap.NewNop())
	for i := 0; i < 3; i++ {
		res, err := a.Evaluate(context.Background(), newAC("same-token"))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "non-NVCA subject denial should be cached and not re-introspected")
}

func TestICMSIntrospect_DoesNotCacheNetworkErrors(t *testing.T) {
	a, _ := NewICMSIntrospect("http://127.0.0.1:1", time.Minute, zap.NewNop())
	for i := 0; i < 3; i++ {
		res, err := a.Evaluate(context.Background(), newAC("same-token"))
		require.NoError(t, err)
		assert.False(t, res.Allow)
	}
	// No call count to assert — we're just verifying it doesn't panic and
	// keeps retrying (each attempt hits the unreachable endpoint).
}

func TestIsValidNvcaSubject(t *testing.T) {
	assert.True(t, IsValidNvcaSubject("system:serviceaccount:nvca-system:nvca"))
	assert.True(t, IsValidNvcaSubject("system:serviceaccount:any-ns:nvca"))
	assert.True(t, IsValidNvcaSubject("spiffe://example/foo/nvca"))
	assert.False(t, IsValidNvcaSubject(""))
	assert.False(t, IsValidNvcaSubject("system:serviceaccount:ns:nvca-attacker"))
	assert.False(t, IsValidNvcaSubject("spiffe://example/nvca-bad"))
}
