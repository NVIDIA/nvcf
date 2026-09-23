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

package nvcaintrospect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidNVCASubject(t *testing.T) {
	tests := []struct {
		name string
		sub  string
		want bool
	}{
		{"psat with nvca service account", "system:serviceaccount:customer-ns:nvca", true},
		{"psat with other service account", "system:serviceaccount:customer-ns:default", false},
		{"psat missing service account segment", "system:serviceaccount:customer-ns", false},
		{"spiffe nvca svid", "spiffe://cluster.local/ns/customer-ns/sa/nvca", true},
		{"spiffe non-nvca svid", "spiffe://cluster.local/ns/customer-ns/sa/nvca-imposter", false},
		{"unrelated subject", "some-other-subject", false},
		{"empty subject", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsValidNVCASubject(tc.sub))
		})
	}
}

func signedTestToken(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64URLEncode(t, map[string]any{"alg": "none"})
	payload := base64URLEncode(t, map[string]any{"exp": exp.Unix()})
	return header + "." + payload + ".sig"
}

func base64URLEncode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestClientIntrospectRejectsOversizedToken(t *testing.T) {
	client, err := NewClient("http://example.invalid/introspect", time.Second, 0)
	require.NoError(t, err)

	oversized := strings.Repeat("a", MaxTokenSize+1)
	_, err = client.Introspect(context.Background(), oversized)
	assert.ErrorIs(t, err, ErrTokenTooLarge)
}

func TestClientIntrospectRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", maxResponseSize+1)))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, 0)
	require.NoError(t, err)

	_, err = client.Introspect(context.Background(), signedTestToken(t, time.Now().Add(time.Hour)))
	assert.ErrorIs(t, err, errResponseTooLarge)
}

func TestClientIntrospectDoesNotFollowRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{Active: true, Sub: "system:serviceaccount:customer-ns:nvca", ClusterID: "cluster-a"})
	}))
	defer other.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, 0)
	require.NoError(t, err)

	_, err = client.Introspect(context.Background(), signedTestToken(t, time.Now().Add(time.Hour)))
	require.Error(t, err, "a redirect response must not be silently followed and treated as success")
}

func TestClientIntrospectCachesActiveValidSubject(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{
			Active:    true,
			Sub:       "system:serviceaccount:customer-ns:nvca",
			ClusterID: "cluster-a",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	token := signedTestToken(t, time.Now().Add(time.Hour))

	result, err := client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.True(t, result.Active)
	assert.Equal(t, "cluster-a", result.ClusterID)
	assert.Equal(t, 1, calls)

	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "expected the second introspection to be served from cache")
}

func TestClientIntrospectCachesActiveInvalidSubject(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{
			Active: true,
			Sub:    "system:serviceaccount:customer-ns:some-other-workload",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	token := signedTestToken(t, time.Now().Add(time.Hour))

	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "an active token's subject is immutable, so an invalid-subject result must be cached")
}

func TestClientIntrospectDoesNotCacheValidSubjectMissingClusterID(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{
			Active: true,
			Sub:    "system:serviceaccount:customer-ns:nvca",
			// ClusterID intentionally omitted.
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	token := signedTestToken(t, time.Now().Add(time.Hour))

	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "a valid-subject result missing ClusterID is incomplete and must not be cached, or a later complete response stays hidden behind the stale entry")
}

func TestClientIntrospectDoesNotCacheInactiveResult(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{Active: false})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	token := signedTestToken(t, time.Now().Add(time.Hour))

	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	_, err = client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "an inactive result must never be cached, since clock skew can make the same token valid moments later")
}

func TestClientIntrospectCacheEvictsAtCapacity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req IntrospectRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{
			Active:    true,
			Sub:       "system:serviceaccount:customer-ns:nvca",
			ClusterID: "cluster-" + req.Token,
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	for i := 0; i <= maxCacheEntries; i++ {
		token := signedTestToken(t, time.Now().Add(time.Hour)) + fmt.Sprintf(".%d", i)
		_, err := client.Introspect(context.Background(), token)
		require.NoError(t, err)
	}

	client.cacheMu.RLock()
	size := len(client.cache)
	client.cacheMu.RUnlock()
	assert.LessOrEqual(t, size, maxCacheEntries, "cache must never grow past maxCacheEntries")
}

func TestClientIntrospectCachedResultIsIsolatedFromCallerMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(IntrospectResult{
			Active:    true,
			Sub:       "system:serviceaccount:customer-ns:nvca",
			ClusterID: "cluster-a",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, time.Minute)
	require.NoError(t, err)

	token := signedTestToken(t, time.Now().Add(time.Hour))

	result, err := client.Introspect(context.Background(), token)
	require.NoError(t, err)
	result.ClusterID = "tampered"

	again, err := client.Introspect(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "cluster-a", again.ClusterID, "mutating a returned result must not corrupt the cached value")
}
