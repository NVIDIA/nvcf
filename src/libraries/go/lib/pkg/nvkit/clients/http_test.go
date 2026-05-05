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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
)

const (
	httpTestAddress = "https://www.nvkit.testbbcNMEslekwer.ev"
)

type HttpRequstTestObject struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

func TestDefaultHTTPClient(t *testing.T) {
	cfg := &BaseClientConfig{
		Type: "http",
		Addr: "test-addr",
		TLS:  auth.TLSConfigOptions{},
		AuthnCfg: &auth.AuthnConfig{
			OIDCConfig: &auth.ProviderConfig{
				Host:         "test-host",
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				Scopes:       []string{"test-scope"},
			},
			RefreshConfig: nil,
		},
	}

	spanFormatter := func(_ string, r *http.Request) string {
		return "test"
	}

	c, err := DefaultHTTPClient(&HTTPClientConfig{BaseClientConfig: cfg, NumRetries: 3}, spanFormatter)
	assert.Nil(t, err)
	assert.NotNil(t, c)
	assert.NotNil(t, c.client.Load())
}

func TestHTTPClient_Client(t *testing.T) {
	defaultHttpClient := createAuthnContext()
	c := defaultHttpClient.Client(context.Background())
	assert.NotNil(t, c)

	defaultHttpClient = createNonAuthnContext()
	c = defaultHttpClient.Client(context.Background())
	assert.NotNil(t, c)
}

func TestHTTPClientConfig_isAuthNEnabled(t *testing.T) {
	defaultHttpClient := createAuthnContext()
	assert.Equal(t, true, defaultHttpClient.Config.isAuthNEnabled())

	defaultHttpClient = createNonAuthnContext()
	assert.Equal(t, false, defaultHttpClient.Config.isAuthNEnabled())
}

func TestNewHTTPClient(t *testing.T) {

	c := NewHTTPClient(&http.Client{}, &HTTPClientConfig{BaseClientConfig: &BaseClientConfig{}})
	assert.NotNil(t, c)
}

func TestHTTPClientRetry(t *testing.T) {

	httpClientConfig := &HTTPClientConfig{
		BaseClientConfig: &BaseClientConfig{
			Type: "http",
			Addr: httpTestAddress,
		},
		NumRetries: 2,
	}

	defaultHttpClient, _ := DefaultHTTPClient(httpClientConfig, func(_ string, r *http.Request) string {
		return "test"
	})

	internalClient := defaultHttpClient.Client(context.Background())
	testResponse := "response!"
	testResponseBytes, err := json.Marshal(testResponse)
	require.Nil(t, err)

	obj := HttpRequstTestObject{
		Name:  "test-retry",
		Value: 1,
	}

	var testCases = []struct {
		desc                   string
		context                context.Context
		req                    HttpRequstTestObject
		expectedResponseStatus int
		expectedResponseBody   []byte
		expectedNumCalls       int
	}{
		{
			desc:                   "Case: 4xx failed",
			context:                context.Background(),
			req:                    obj,
			expectedResponseStatus: 400,
			expectedResponseBody:   testResponseBytes,
			expectedNumCalls:       3,
		},
		{
			desc:                   "Case: 5xx failed",
			context:                context.Background(),
			req:                    obj,
			expectedResponseStatus: 500,
			expectedResponseBody:   testResponseBytes,
			expectedNumCalls:       3,
		},
	}

	//For the failed cases, the client should retry and return an error with 'giving up after xx attempts(s)'
	for _, tc := range testCases {
		err := sendHelper(internalClient, httpTestAddress, tc.req)
		expectedResult := "giving up after " + strconv.Itoa(tc.expectedNumCalls) + " attempt(s)"
		res := strings.Contains(err.Error(), expectedResult)
		assert.True(t, res)
	}

}

func createAuthnContext() *HTTPClient {
	cfg := &BaseClientConfig{
		Type: "http",
		Addr: "test-addr",
		TLS:  auth.TLSConfigOptions{},
		AuthnCfg: &auth.AuthnConfig{
			OIDCConfig: &auth.ProviderConfig{
				Host:         "test-host",
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				Scopes:       []string{"test-scope"},
			},
			RefreshConfig: nil,
		},
	}

	spanFormatter := func(_ string, r *http.Request) string {
		return "test"
	}

	c, _ := DefaultHTTPClient(&HTTPClientConfig{BaseClientConfig: cfg}, spanFormatter)
	return c
}

func createNonAuthnContext() *HTTPClient {
	cfg := &BaseClientConfig{
		Type:     "http",
		Addr:     "test-addr",
		TLS:      auth.TLSConfigOptions{},
		AuthnCfg: nil,
	}

	spanFormatter := func(_ string, r *http.Request) string {
		return "test"
	}

	c, _ := DefaultHTTPClient(&HTTPClientConfig{BaseClientConfig: cfg}, spanFormatter)
	return c
}

func sendHelper(client *http.Client, endpoint string, reqObject HttpRequstTestObject) error {
	reqBytes, _ := json.Marshal(reqObject)
	req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(reqBytes))
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	defer closeResponse(resp)
	if err != nil {
		zap.L().Error("Error sending data", zap.Error(err))
		return err
	}
	return nil
}

func closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
}

func TestDefaultHTTPClient_ClientAssignment(t *testing.T) {
	tests := []struct {
		name            string
		cfg             *HTTPClientConfig
		checkOrigClient func(t *testing.T, c *HTTPClient)
		checkClient     func(t *testing.T, c *HTTPClient)
	}{
		{
			name: "retries enabled and auth disabled",
			cfg: &HTTPClientConfig{
				BaseClientConfig: &BaseClientConfig{
					Type: "http",
					Addr: "test-addr",
					TLS:  auth.TLSConfigOptions{},
				},
				NumRetries: 2,
			},
			checkOrigClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.origClient)
				assert.NotNil(t, c.origClient.Transport)
				// Verify it's otelhttp client
				_, ok := c.origClient.Transport.(*otelhttp.Transport)
				assert.True(t, ok)
			},
			checkClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.client.Load())
				assert.NotNil(t, c.client.Load().Transport)
				// Verify it's retry client
				_, ok := c.client.Load().Transport.(*retryablehttp.RoundTripper)
				assert.True(t, ok)
			},
		},
		{
			name: "retries disabled and auth disabled",
			cfg: &HTTPClientConfig{
				BaseClientConfig: &BaseClientConfig{
					Type: "http",
					Addr: "test-addr",
					TLS:  auth.TLSConfigOptions{},
				},
				NumRetries: 0,
			},
			checkOrigClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.origClient)
				assert.NotNil(t, c.origClient.Transport)
				// Verify it's otelhttp client
				_, ok := c.origClient.Transport.(*otelhttp.Transport)
				assert.True(t, ok)
			},
			checkClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.client.Load())
				assert.NotNil(t, c.client.Load().Transport)
				// Verify it's otelhttp client
				_, ok := c.client.Load().Transport.(*otelhttp.Transport)
				assert.True(t, ok)
			},
		},
		{
			name: "retries enabled and auth enabled",
			cfg: &HTTPClientConfig{
				NumRetries: 2,
				BaseClientConfig: &BaseClientConfig{
					Type: "http",
					Addr: "test-addr",
					TLS:  auth.TLSConfigOptions{},
					AuthnCfg: &auth.AuthnConfig{
						OIDCConfig: &auth.ProviderConfig{
							Host:         "test-host",
							ClientID:     "test-client",
							ClientSecret: "test-secret",
							Scopes:       []string{"test-scope"},
						},
					},
				},
			},
			checkOrigClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.origClient)
				assert.NotNil(t, c.origClient.Transport)
				// Verify it's a retry client
				_, ok := c.origClient.Transport.(*retryablehttp.RoundTripper)
				assert.True(t, ok)
			},
			checkClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.client.Load())
				assert.NotNil(t, c.client.Load().Transport)
				// Verify it's oauth2 client
				_, ok := c.client.Load().Transport.(*oauth2.Transport)
				assert.True(t, ok)
			},
		},
		{
			name: "retries disabled and auth enabled",
			cfg: &HTTPClientConfig{
				NumRetries: 0,
				BaseClientConfig: &BaseClientConfig{
					Type: "http",
					Addr: "test-addr",
					TLS:  auth.TLSConfigOptions{},
					AuthnCfg: &auth.AuthnConfig{
						OIDCConfig: &auth.ProviderConfig{
							Host:         "test-host",
							ClientID:     "test-client",
							ClientSecret: "test-secret",
							Scopes:       []string{"test-scope"},
						},
					},
				},
			},
			checkOrigClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.origClient)
				assert.NotNil(t, c.origClient.Transport)
				// Verify it's otelhttp client
				_, ok := c.origClient.Transport.(*otelhttp.Transport)
				assert.True(t, ok)
			},
			checkClient: func(t *testing.T, c *HTTPClient) {
				assert.NotNil(t, c.client.Load())
				assert.NotNil(t, c.client.Load().Transport)
				// Verify it's oauth2 client
				_, ok := c.client.Load().Transport.(*oauth2.Transport)
				assert.True(t, ok)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanFormatter := func(_ string, r *http.Request) string {
				return "test"
			}

			c, err := DefaultHTTPClient(tt.cfg, spanFormatter)
			require.NoError(t, err)
			require.NotNil(t, c)

			tt.checkOrigClient(t, c)
			tt.checkClient(t, c)
		})
	}
}

// TestHTTPClient_AuthRefreshThreadSafety verifies that the authentication refresh mechanism
// is thread-safe and handles errors properly.
func TestHTTPClient_AuthRefreshThreadSafety(t *testing.T) {
	// Create HTTP client with authentication
	cfg := &HTTPClientConfig{
		BaseClientConfig: &BaseClientConfig{
			Type: "http",
			Addr: "test-addr",
			TLS:  auth.TLSConfigOptions{},
			AuthnCfg: &auth.AuthnConfig{
				OIDCConfig: &auth.ProviderConfig{
					Host:         "test-host",
					ClientID:     "test-client",
					ClientSecret: "test-secret",
					Scopes:       []string{"test-scope"},
				},
			},
		},
		NumRetries: 2,
	}

	spanFormatter := func(_ string, r *http.Request) string {
		return "test"
	}

	httpClient, err := DefaultHTTPClient(cfg, spanFormatter)
	require.NoError(t, err)
	require.NotNil(t, httpClient)

	// Verify thread-safe concurrent access during HTTP requests and credential refresh
	t.Run("Thread-safe concurrent access during HTTP requests", func(t *testing.T) {
		// Capture original client reference for verification
		originalClient := httpClient.Client(context.Background())

		// Create a mock request
		req, err := http.NewRequest("GET", "https://example.com", nil)
		require.NoError(t, err)

		// Channel to coordinate goroutines
		startSignal := make(chan struct{})
		doneSignal := make(chan struct{}, 2)

		// Goroutine 1: Make HTTP requests
		go func() {
			defer func() { doneSignal <- struct{}{} }()
			<-startSignal

			for i := 0; i < 50; i++ {
				// This accesses the client concurrently with credential updates
				// We ignore errors since we're testing thread safety, not HTTP functionality
				_, _ = httpClient.Do(context.Background(), req)
			}
		}()

		// Goroutine 2: Update credentials during requests
		go func() {
			defer func() { doneSignal <- struct{}{} }()
			<-startSignal

			for i := 0; i < 25; i++ {
				newCreds := &auth.ClientCredentials{
					ClientID:     fmt.Sprintf("concurrent-client-id-%d", i),
					ClientSecret: fmt.Sprintf("concurrent-client-secret-%d", i),
				}
				httpClient.Update(newCreds)
			}
		}()

		// Start both goroutines
		close(startSignal)

		// Wait for completion
		<-doneSignal
		<-doneSignal

		// Verify that the client was updated during concurrent operations
		currentClient := httpClient.Client(context.Background())
		assert.NotEqual(t, originalClient, currentClient, "Client should have been refreshed")
	})
}

// TestHTTPClient_TracingIntegration verifies that OpenTelemetry tracing works correctly
// for both retry operations and authentication refresh operations.
func TestHTTPClient_TracingIntegration(t *testing.T) {
	// Test case 1: Verify retry operations are traced
	t.Run("Retry operations are traced", func(t *testing.T) {
		// Create client with retries enabled but no auth
		cfg := &HTTPClientConfig{
			BaseClientConfig: &BaseClientConfig{
				Type: "http",
				Addr: "test-addr",
				TLS:  auth.TLSConfigOptions{},
			},
			NumRetries: 2,
		}

		spanFormatter := func(_ string, r *http.Request) string {
			return "test-retry-trace"
		}

		httpClient, err := DefaultHTTPClient(cfg, spanFormatter)
		require.NoError(t, err)
		require.NotNil(t, httpClient)

		// Verify the client has the expected transport stack
		client := httpClient.Client(context.Background())
		assert.NotNil(t, client.Transport)

		// The final transport should be retryablehttp.RoundTripper
		_, isRetryTransport := client.Transport.(*retryablehttp.RoundTripper)
		assert.True(t, isRetryTransport, "Expected retry transport as outermost layer")

		// The original client should have otelhttp.Transport
		assert.NotNil(t, httpClient.origClient.Transport)
		_, isOtelTransport := httpClient.origClient.Transport.(*otelhttp.Transport)
		assert.True(t, isOtelTransport, "Expected otelhttp transport in original client")
	})

	// Test case 2: Verify auth + retry operations preserve tracing
	t.Run("Auth and retry operations preserve tracing", func(t *testing.T) {
		// Create client with both retries and auth enabled
		cfg := &HTTPClientConfig{
			BaseClientConfig: &BaseClientConfig{
				Type: "http",
				Addr: "test-addr",
				TLS:  auth.TLSConfigOptions{},
				AuthnCfg: &auth.AuthnConfig{
					OIDCConfig: &auth.ProviderConfig{
						Host:         "test-host",
						ClientID:     "test-client",
						ClientSecret: "test-secret",
						Scopes:       []string{"test-scope"},
					},
				},
			},
			NumRetries: 2,
		}

		spanFormatter := func(_ string, r *http.Request) string {
			return "test-auth-retry-trace"
		}

		httpClient, err := DefaultHTTPClient(cfg, spanFormatter)
		require.NoError(t, err)
		require.NotNil(t, httpClient)

		// Verify the client has the expected transport stack
		client := httpClient.Client(context.Background())
		assert.NotNil(t, client.Transport)

		// The final transport should be oauth2.Transport
		_, isOAuth2Transport := client.Transport.(*oauth2.Transport)
		assert.True(t, isOAuth2Transport, "Expected OAuth2 transport as outermost layer")

		// The origClient should be the retry client (for proper auth refresh)
		assert.NotNil(t, httpClient.origClient.Transport)
		_, isRetryTransport := httpClient.origClient.Transport.(*retryablehttp.RoundTripper)
		assert.True(t, isRetryTransport, "Expected retry transport in origClient for auth refresh")
	})

	// Test case 3: Verify authentication refresh preserves tracing stack
	t.Run("Authentication refresh preserves tracing stack", func(t *testing.T) {
		// Create client with auth enabled
		cfg := &HTTPClientConfig{
			BaseClientConfig: &BaseClientConfig{
				Type: "http",
				Addr: "test-addr",
				TLS:  auth.TLSConfigOptions{},
				AuthnCfg: &auth.AuthnConfig{
					OIDCConfig: &auth.ProviderConfig{
						Host:         "test-host",
						ClientID:     "test-client",
						ClientSecret: "test-secret",
						Scopes:       []string{"test-scope"},
					},
				},
			},
			NumRetries: 2,
		}

		spanFormatter := func(_ string, r *http.Request) string {
			return "test-auth-refresh-trace"
		}

		httpClient, err := DefaultHTTPClient(cfg, spanFormatter)
		require.NoError(t, err)
		require.NotNil(t, httpClient)

		// Capture original client stack
		originalClient := httpClient.Client(context.Background())
		originalTransport := originalClient.Transport

		// Perform authentication refresh
		newCreds := &auth.ClientCredentials{
			ClientID:     "new-client-id",
			ClientSecret: "new-client-secret",
		}
		httpClient.Update(newCreds)

		// Verify the client was updated
		updatedClient := httpClient.Client(context.Background())
		assert.NotEqual(t, originalClient, updatedClient, "Client should have been refreshed")

		// Verify the transport stack is preserved
		updatedTransport := updatedClient.Transport
		assert.NotNil(t, updatedTransport)

		// Should still be OAuth2 transport at the top
		_, isOAuth2Transport := updatedTransport.(*oauth2.Transport)
		assert.True(t, isOAuth2Transport, "Expected OAuth2 transport after refresh")

		// The transport instance should be different (new auth client)
		assert.NotEqual(t, originalTransport, updatedTransport, "Transport should be new instance after refresh")

		// Verify origClient is still the retry client (maintains proper refresh base)
		assert.NotNil(t, httpClient.origClient.Transport)
		_, isRetryTransport := httpClient.origClient.Transport.(*retryablehttp.RoundTripper)
		assert.True(t, isRetryTransport, "origClient should maintain retry transport for future refreshes")
	})
}

func TestHTTPClient_Get(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	client := NewHTTPClient(&http.Client{}, &HTTPClientConfig{BaseClientConfig: &BaseClientConfig{}})
	resp, err := client.Get(context.Background(), ts.URL)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestHTTPClient_Put(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewHTTPClient(&http.Client{}, &HTTPClientConfig{BaseClientConfig: &BaseClientConfig{}})
	resp, err := client.Put(context.Background(), ts.URL, strings.NewReader("body"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestHTTPClient_Post(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	client := NewHTTPClient(&http.Client{}, &HTTPClientConfig{BaseClientConfig: &BaseClientConfig{}})
	resp, err := client.Post(context.Background(), ts.URL, strings.NewReader("body"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

func TestHTTPClientConfig_AddClientFlags(t *testing.T) {
	// nil cmd should return false
	cfg := &HTTPClientConfig{}
	ok := cfg.AddClientFlags(nil, "myservice")
	assert.False(t, ok)

	// nil config should return false
	var nilCfg *HTTPClientConfig
	ok = nilCfg.AddClientFlags(&cobra.Command{}, "myservice")
	assert.False(t, ok)

	// empty clientName should return false
	ok = cfg.AddClientFlags(&cobra.Command{}, "")
	assert.False(t, ok)

	// valid inputs should add flags
	cfg2 := &HTTPClientConfig{}
	cmd := &cobra.Command{}
	ok = cfg2.AddClientFlags(cmd, "myservice")
	assert.True(t, ok)
	assert.True(t, cmd.Flags().HasFlags())
}

func TestHTTPClient_Update_NoAuthn(t *testing.T) {
	// When authn is not enabled, Update should be a no-op
	client := NewHTTPClient(&http.Client{}, &HTTPClientConfig{
		BaseClientConfig: &BaseClientConfig{},
	})
	originalClient := client.Client(context.Background())
	client.Update(&auth.ClientCredentials{ClientID: "new-id", ClientSecret: "new-secret"})
	// Client should remain the same since authn is not enabled
	assert.Equal(t, originalClient, client.Client(context.Background()))
}
