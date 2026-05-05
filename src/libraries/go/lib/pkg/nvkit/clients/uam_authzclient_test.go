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

package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"
	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

// ---------------------------------------------------------------------------
// Helpers / mocks
// ---------------------------------------------------------------------------

// mockUAMAuthorizer is an in-process UAMAuthorizer used to test UAMAuthZClient
// without touching the network.
type mockUAMAuthorizer struct {
	resp *pdp_types.RuleResponse
	err  error
	// callCount tracks how many times Evaluate was called (cache-hit tests).
	callCount int
}

func (m *mockUAMAuthorizer) Evaluate(_ context.Context, _ *pdp_types.RuleRequest) (*pdp_types.RuleResponse, error) {
	m.callCount++
	return m.resp, m.err
}

// newAuthzClientWithMock creates a UAMAuthZClient that delegates to mock, making
// it easy to test all business-logic paths without an HTTP server.
func newAuthzClientWithMock(mock UAMAuthorizer, policyCfg *auth.UAMPolicyConfig, opts ...AuthZClientOption) (*UAMAuthZClient, error) {
	c := &UAMAuthZClient{
		cfg:       &BaseClientConfig{},
		policyCfg: policyCfg,
		client:    mock,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// NewUAMAuthzClient – constructor / routing
// ---------------------------------------------------------------------------

func TestNewUAMAuthzClient_NilConfig(t *testing.T) {
	client, err := NewUAMAuthzClient(nil, nil)
	assert.Nil(t, client)
	assert.ErrorIs(t, err, nverrors.ErrBadConfig)
}

func TestNewUAMAuthzClient_GRPCTypeReturnsError(t *testing.T) {
	cfg := &BaseClientConfig{Type: string(ClientTypeGRPC), Addr: "localhost:9090"}
	client, err := NewUAMAuthzClient(cfg, nil)
	assert.Nil(t, client)
	assert.ErrorIs(t, err, nverrors.ErrBadConfig)
}

func TestNewUAMAuthzClient_UnknownTypeReturnsError(t *testing.T) {
	cfg := &BaseClientConfig{Type: "websocket", Addr: "localhost:9090"}
	client, err := NewUAMAuthzClient(cfg, nil)
	assert.Nil(t, client)
	assert.ErrorIs(t, err, nverrors.ErrInvalidConfig)
}

func TestNewUAMAuthzClient_HTTPType(t *testing.T) {
	cfg := &BaseClientConfig{Type: string(ClientTypeHTTP), Addr: "http://localhost:8080"}
	policyCfg := &auth.UAMPolicyConfig{Namespace: "ns", PolicyFQDN: "pkg.policy"}
	client, err := NewUAMAuthzClient(cfg, policyCfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, policyCfg, client.PolicyConfig())
}

func TestNewUAMAuthzClient_HTTPType_WithBadCacheOption(t *testing.T) {
	cfg := &BaseClientConfig{Type: string(ClientTypeHTTP), Addr: "http://localhost:8080"}
	badCache := WithCachingEnabled(AuthzCacheConfig{CacheDuration: "not-a-duration", CacheSize: 10})
	client, err := NewUAMAuthzClient(cfg, nil, badCache)
	assert.Nil(t, client)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// UAMAuthZClient.PolicyConfig
// ---------------------------------------------------------------------------

func TestUAMAuthZClient_PolicyConfig(t *testing.T) {
	policyCfg := &auth.UAMPolicyConfig{Namespace: "test-ns", PolicyFQDN: "test.policy"}
	c, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, policyCfg)
	require.NoError(t, err)
	assert.Equal(t, policyCfg, c.PolicyConfig())
}

func TestUAMAuthZClient_PolicyConfig_Nil(t *testing.T) {
	c, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil)
	require.NoError(t, err)
	assert.Nil(t, c.PolicyConfig())
}

// ---------------------------------------------------------------------------
// initializeCache / WithCachingEnabled
// ---------------------------------------------------------------------------

func TestInitializeCache_InvalidDuration(t *testing.T) {
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "bad", CacheSize: 10}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-duration", cfgErr.FieldName)
}

func TestInitializeCache_ZeroDuration(t *testing.T) {
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "0s", CacheSize: 10}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-duration", cfgErr.FieldName)
}

func TestInitializeCache_NegativeDuration(t *testing.T) {
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "-1s", CacheSize: 10}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-duration", cfgErr.FieldName)
}

func TestInitializeCache_ExceedMaxDuration(t *testing.T) {
	// maxCacheDurationSec = 86400 s; 86401s exceeds it
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "86401s", CacheSize: 10}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-duration", cfgErr.FieldName)
}

func TestInitializeCache_ZeroCacheSize(t *testing.T) {
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: 0}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-size", cfgErr.FieldName)
}

func TestInitializeCache_NegativeCacheSize(t *testing.T) {
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: -1}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-size", cfgErr.FieldName)
}

func TestInitializeCache_ExceedMaxCacheSize(t *testing.T) {
	// maxCacheSize = 1000
	_, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: maxCacheSize + 1}),
	)
	require.Error(t, err)
	var cfgErr *nverrors.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache-size", cfgErr.FieldName)
}

func TestInitializeCache_Valid(t *testing.T) {
	c, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: 50}),
	)
	require.NoError(t, err)
	assert.NotNil(t, c.cache)
}

func TestInitializeCache_Valid_ExtendTTLOnHit(t *testing.T) {
	c, err := newAuthzClientWithMock(&mockUAMAuthorizer{}, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "30s", CacheSize: 100, extendTTLOnHit: true}),
	)
	require.NoError(t, err)
	assert.NotNil(t, c.cache)
}

// ---------------------------------------------------------------------------
// UAMAuthZClient.Evaluate – no cache
// ---------------------------------------------------------------------------

func TestUAMAuthZClient_Evaluate_NoCache_Success(t *testing.T) {
	expected := &pdp_types.RuleResponse{Namespace: "ns", RuleName: "rule"}
	mock := &mockUAMAuthorizer{resp: expected}
	c, err := newAuthzClientWithMock(mock, nil)
	require.NoError(t, err)

	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule"}
	resp, err := c.Evaluate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expected, resp)
	assert.Equal(t, 1, mock.callCount)
}

func TestUAMAuthZClient_Evaluate_NoCache_Error(t *testing.T) {
	expectedErr := errors.New("evaluate failed")
	mock := &mockUAMAuthorizer{err: expectedErr}
	c, err := newAuthzClientWithMock(mock, nil)
	require.NoError(t, err)

	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, expectedErr)
}

// ---------------------------------------------------------------------------
// UAMAuthZClient.Evaluate – with cache
// ---------------------------------------------------------------------------

func TestUAMAuthZClient_Evaluate_Cache_Miss_Then_Hit(t *testing.T) {
	expected := &pdp_types.RuleResponse{Namespace: "ns", RuleName: "rule"}
	mock := &mockUAMAuthorizer{resp: expected}
	c, err := newAuthzClientWithMock(mock, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: 10}),
	)
	require.NoError(t, err)

	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule"}

	// First call: cache miss → delegates to mock
	resp, err := c.Evaluate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expected.Namespace, resp.Namespace)
	assert.Equal(t, 1, mock.callCount)

	// Second call: cache hit → mock NOT called again
	resp2, err := c.Evaluate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expected.Namespace, resp2.Namespace)
	assert.Equal(t, 1, mock.callCount, "mock should not be called on cache hit")
}

func TestUAMAuthZClient_Evaluate_Cache_BackendError(t *testing.T) {
	expectedErr := errors.New("backend down")
	mock := &mockUAMAuthorizer{err: expectedErr}
	c, err := newAuthzClientWithMock(mock, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: 10}),
	)
	require.NoError(t, err)

	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, expectedErr)
}

func TestUAMAuthZClient_Evaluate_Cache_DifferentRequests_BothMiss(t *testing.T) {
	mock := &mockUAMAuthorizer{resp: &pdp_types.RuleResponse{Namespace: "ns"}}
	c, err := newAuthzClientWithMock(mock, nil,
		WithCachingEnabled(AuthzCacheConfig{CacheDuration: "10s", CacheSize: 10}),
	)
	require.NoError(t, err)

	req1 := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule1"}
	req2 := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule2"}

	_, err = c.Evaluate(context.Background(), req1)
	require.NoError(t, err)
	_, err = c.Evaluate(context.Background(), req2)
	require.NoError(t, err)

	assert.Equal(t, 2, mock.callCount, "two distinct requests should each miss the cache")
}

// ---------------------------------------------------------------------------
// getRuleRequestHash
// ---------------------------------------------------------------------------

func TestGetRuleRequestHash_Deterministic(t *testing.T) {
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule"}
	h1 := getRuleRequestHash(req)
	h2 := getRuleRequestHash(req)
	assert.Equal(t, h1, h2)
}

func TestGetRuleRequestHash_DiffForDiffRequests(t *testing.T) {
	r1 := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule1"}
	r2 := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "rule2"}
	assert.NotEqual(t, getRuleRequestHash(r1), getRuleRequestHash(r2))
}

// ---------------------------------------------------------------------------
// authzHTTPClient.Evaluate via httptest server
// ---------------------------------------------------------------------------

func newTestAuthzHTTPClient(t *testing.T, serverURL string) *authzHTTPClient {
	t.Helper()
	httpCfg := &HTTPClientConfig{
		BaseClientConfig: &BaseClientConfig{
			Type: string(ClientTypeHTTP),
			Addr: serverURL,
		},
	}
	return &authzHTTPClient{
		client: NewHTTPClient(&http.Client{}, httpCfg),
	}
}

func TestAuthzHTTPClient_Evaluate_Success(t *testing.T) {
	expected := &pdp_types.RuleResponse{Namespace: "ns", RuleName: "policy"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	c := newTestAuthzHTTPClient(t, server.URL)
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, expected.Namespace, resp.Namespace)
	assert.Equal(t, expected.RuleName, resp.RuleName)
}

func TestAuthzHTTPClient_Evaluate_Non200Status(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer server.Close()

	c := newTestAuthzHTTPClient(t, server.URL)
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("%d", http.StatusForbidden))
}

func TestAuthzHTTPClient_Evaluate_InvalidJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	c := newTestAuthzHTTPClient(t, server.URL)
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot unmarshal")
}

func TestAuthzHTTPClient_Evaluate_NetworkError(t *testing.T) {
	// Point at a server that is already closed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Close() // immediately close so requests fail

	c := newTestAuthzHTTPClient(t, server.URL)
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	require.Error(t, err)
}

func TestAuthzHTTPClient_Evaluate_InvalidAddr(t *testing.T) {
	// "://bad" is not a valid URL, so http.NewRequestWithContext will fail
	c := newTestAuthzHTTPClient(t, "://bad")
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	assert.Nil(t, resp)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// newHTTPAuthzClient error path – DefaultHTTPClient fails for bad TLS config
// ---------------------------------------------------------------------------

func TestNewUAMAuthzClient_HTTPType_BadTLS(t *testing.T) {
	cfg := &BaseClientConfig{
		Type: string(ClientTypeHTTP),
		Addr: "http://localhost:8080",
		TLS: auth.TLSConfigOptions{
			Enabled:    true,
			CertFile:   "/nonexistent/cert.pem",
			KeyFile:    "/nonexistent/key.pem",
			RootCAFile: "/nonexistent/ca.pem",
		},
	}
	client, err := NewUAMAuthzClient(cfg, nil)
	assert.Nil(t, client)
	require.Error(t, err)
}

// TestNewUAMAuthzClient_HTTPType_EndToEnd exercises the full stack created by
// newHTTPAuthzClient (including the OTel span-formatter closure) by actually
// performing an Evaluate request through the constructed client.
func TestNewUAMAuthzClient_HTTPType_EndToEnd(t *testing.T) {
	expected := &pdp_types.RuleResponse{Namespace: "ns", RuleName: "policy"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	cfg := &BaseClientConfig{Type: string(ClientTypeHTTP), Addr: server.URL}
	policyCfg := &auth.UAMPolicyConfig{Namespace: "ns", PolicyFQDN: "policy"}
	client, err := NewUAMAuthzClient(cfg, policyCfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := client.Evaluate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, expected.Namespace, resp.Namespace)
}

// TestAuthzHTTPClient_Evaluate_BodyReadError triggers the io.ReadAll error path
// by hijacking the connection and closing it after writing partial headers.
func TestAuthzHTTPClient_Evaluate_BodyReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write status 200 and declare a Content-Length that we will never fulfil,
		// then hijack and close – causing io.ReadAll to receive an unexpected EOF.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Skip("server does not support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		// Write a valid HTTP/1.1 200 header with a body size that won't arrive.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close() // close before sending the body
	}))
	defer server.Close()

	c := newTestAuthzHTTPClient(t, server.URL)
	req := &pdp_types.RuleRequest{Namespace: "ns", RuleName: "policy"}
	resp, err := c.Evaluate(context.Background(), req)
	// The read may error OR the retry logic may swallow it – either way we
	// should not get a valid response.
	if err == nil {
		// Some HTTP stacks return a partial empty body without erroring –
		// in that case the JSON unmarshal will fail, which is also acceptable.
		assert.Nil(t, resp)
	} else {
		assert.Nil(t, resp)
	}
}
