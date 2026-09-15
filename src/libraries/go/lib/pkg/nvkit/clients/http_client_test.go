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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTokenRefresher is a simple mock for testing
type mockTokenRefresher struct {
	token string
}

func (m *mockTokenRefresher) Token() string {
	return m.token
}

func TestNewHTTPClientV2(t *testing.T) {
	tests := []struct {
		name               string
		options            []HTTPClientOption
		expectedClientType string
		validateConfig     func(t *testing.T, client *HTTPClientV2)
	}{
		{
			name:               "default configuration without auth",
			options:            []HTTPClientOption{},
			expectedClientType: "*HTTPClientV2",
			validateConfig: func(t *testing.T, client *HTTPClientV2) {
				assert.NotNil(t, client.httpClient)
				assert.Nil(t, client.authTokenProvider)
			},
		},
		{
			name: "with auth token provider",
			options: []HTTPClientOption{
				WithAuthTokenProvider(&TokenRefresher{authToken: "test-token"}),
			},
			expectedClientType: "*HTTPClientV2",
			validateConfig: func(t *testing.T, client *HTTPClientV2) {
				assert.NotNil(t, client.httpClient)
				assert.NotNil(t, client.authTokenProvider)
				assert.Equal(t, "test-token", client.authTokenProvider.authToken)
			},
		},
		{
			name: "with retry configuration",
			options: []HTTPClientOption{
				WithRetryMax(5),
				WithRetryWait(100*time.Millisecond, 2*time.Second),
			},
			expectedClientType: "*HTTPClientV2",
			validateConfig: func(t *testing.T, client *HTTPClientV2) {
				assert.NotNil(t, client.httpClient)
				assert.Nil(t, client.authTokenProvider)
			},
		},
		{
			name: "with connection limits",
			options: []HTTPClientOption{
				WithConnectionLimits(200, 20, 100),
			},
			expectedClientType: "*HTTPClientV2",
			validateConfig: func(t *testing.T, client *HTTPClientV2) {
				assert.NotNil(t, client.httpClient)
				assert.Nil(t, client.authTokenProvider)
			},
		},
		{
			name: "with error handler",
			options: []HTTPClientOption{
				WithErrorHandler(retryablehttp.PassthroughErrorHandler),
			},
			expectedClientType: "*HTTPClientV2",
			validateConfig: func(t *testing.T, client *HTTPClientV2) {
				assert.NotNil(t, client.httpClient)
				assert.Nil(t, client.authTokenProvider)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewHTTPClientV2(tt.options...)
			assert.NotNil(t, client)
			tt.validateConfig(t, client)
		})
	}
}

func TestNewHTTPClientV2WithClient(t *testing.T) {
	customClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	tests := []struct {
		name       string
		httpClient *http.Client
		options    []HTTPClientOption
	}{
		{
			name:       "with custom client and no auth",
			httpClient: customClient,
			options:    []HTTPClientOption{},
		},
		{
			name:       "with custom client and auth",
			httpClient: customClient,
			options:    []HTTPClientOption{WithAuthTokenProvider(&TokenRefresher{})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewHTTPClientV2WithClient(tt.httpClient, tt.options...)
			assert.NotNil(t, client)
			assert.Equal(t, tt.httpClient, client.httpClient)
		})
	}
}

func TestSingletonBehavior(t *testing.T) {
	// Default clients share the same underlying HTTP client
	defaultClient1 := DefaultHTTPClientV2()
	defaultClient2 := DefaultHTTPClientV2()
	authClient := DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: "test"})

	// All default clients should share the same underlying HTTP client
	assert.Same(t, defaultClient1.httpClient, defaultClient2.httpClient)
	assert.Same(t, defaultClient1.httpClient, authClient.httpClient)

	// But custom clients should get their own HTTP client
	customClient := NewHTTPClientV2(WithRetryMax(10))
	assert.NotSame(t, defaultClient1.httpClient, customClient.httpClient)
}

func TestHTTPClientV2_Do(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if Authorization header is present
		auth := r.Header.Get("Authorization")
		if auth != "" {
			w.Header().Set("X-Auth-Header", auth)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	tests := []struct {
		name               string
		authTokenProvider  *TokenRefresher
		expectedAuthHeader string
	}{
		{
			name:               "without authentication",
			authTokenProvider:  nil,
			expectedAuthHeader: "",
		},
		{
			name: "with authentication",
			authTokenProvider: &TokenRefresher{
				authToken: "test-token-123",
			},
			expectedAuthHeader: "Bearer test-token-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *HTTPClientV2
			if tt.authTokenProvider != nil {
				client = DefaultHTTPClientV2WithAuth(tt.authTokenProvider)
			} else {
				client = DefaultHTTPClientV2()
			}

			req, err := http.NewRequest("GET", server.URL, nil)
			require.NoError(t, err)

			ctx := context.Background()
			resp, err := client.Do(ctx, req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			authHeader := resp.Header.Get("X-Auth-Header")
			assert.Equal(t, tt.expectedAuthHeader, authHeader)
		})
	}
}

func TestHTTPClientV2_DoWithContext(t *testing.T) {
	// Create a server that returns the request context timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if context is properly set
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusRequestTimeout)
			return
		case <-time.After(10 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}
	}))
	defer server.Close()

	client := DefaultHTTPClientV2()

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		_, err = client.Do(ctx, req)
		assert.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "canceled"))
	})

	t.Run("successful request with context", func(t *testing.T) {
		ctx := context.Background()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(ctx, req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestWithRetryWait(t *testing.T) {
	minWait := 100 * time.Millisecond
	maxWait := 2 * time.Second

	config := defaultConfig()
	option := WithRetryWait(minWait, maxWait)
	option(config)

	assert.Equal(t, minWait, config.retryWaitMin)
	assert.Equal(t, maxWait, config.retryWaitMax)
}

func TestWithRetryMax(t *testing.T) {
	maxRetries := 5

	config := defaultConfig()
	option := WithRetryMax(maxRetries)
	option(config)

	assert.Equal(t, maxRetries, config.retryMax)
}

func TestWithConnectionLimits(t *testing.T) {
	maxIdle := 200
	maxIdlePerHost := 20
	maxPerHost := 100

	config := defaultConfig()
	option := WithConnectionLimits(maxIdle, maxIdlePerHost, maxPerHost)
	option(config)

	assert.Equal(t, maxIdle, config.maxIdleConns)
	assert.Equal(t, maxIdlePerHost, config.maxIdleConnsPerHost)
	assert.Equal(t, maxPerHost, config.maxConnsPerHost)
}

func TestWithErrorHandler(t *testing.T) {
	errorHandler := retryablehttp.PassthroughErrorHandler

	config := defaultConfig()
	option := WithErrorHandler(errorHandler)
	option(config)

	// Since functions can't be compared directly, we check if it's not nil
	assert.NotNil(t, config.errorHandler)
}

func TestWithAuthTokenProvider(t *testing.T) {
	tokenProvider := &TokenRefresher{authToken: "test-token"}

	config := defaultConfig()
	option := WithAuthTokenProvider(tokenProvider)
	option(config)

	assert.Equal(t, tokenProvider, config.authTokenProvider)
	assert.Equal(t, "test-token", config.authTokenProvider.authToken)
}

func TestDefaultConfig(t *testing.T) {
	config := defaultConfig()

	assert.Equal(t, 100, config.maxIdleConns)
	assert.Equal(t, 5, config.maxIdleConnsPerHost)
	assert.Equal(t, 0, config.maxConnsPerHost)
	assert.Equal(t, 2, config.retryMax)
	assert.Equal(t, 50*time.Millisecond, config.retryWaitMin)
	assert.Equal(t, 300*time.Millisecond, config.retryWaitMax)
	assert.NotNil(t, config.errorHandler)
}

func TestBuildHTTPClient(t *testing.T) {
	tests := []struct {
		name   string
		config *httpClientConfigV2
	}{
		{
			name:   "default configuration",
			config: defaultConfig(),
		},
		{
			name: "custom configuration",
			config: &httpClientConfigV2{
				maxIdleConns:        200,
				maxIdleConnsPerHost: 20,
				maxConnsPerHost:     100,
				retryMax:            5,
				retryWaitMin:        100 * time.Millisecond,
				retryWaitMax:        2 * time.Second,
				errorHandler:        retryablehttp.PassthroughErrorHandler,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := buildHTTPClient(tt.config)
			assert.NotNil(t, client)
			assert.NotNil(t, client.Transport)
		})
	}
}

func TestHTTPClientV2WithRetryBehavior(t *testing.T) {
	// Create a server that fails the first few requests then succeeds
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	// Create client with retry enabled
	client := NewHTTPClientV2(WithRetryMax(3))

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := client.Do(ctx, req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(t, requestCount, 3) // Should have retried
}

func TestHTTPClientV2ConcurrentRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond) // Simulate some processing time
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	client := DefaultHTTPClientV2()

	// Make concurrent requests
	const numRequests = 10
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req, err := http.NewRequest("GET", server.URL, nil)
			if err != nil {
				results <- err
				return
			}

			ctx := context.Background()
			resp, err := client.Do(ctx, req)
			if err != nil {
				results <- err
				return
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				results <- assert.AnError
				return
			}
			results <- nil
		}()
	}

	// Wait for all requests to complete
	for i := 0; i < numRequests; i++ {
		err := <-results
		assert.NoError(t, err)
	}
}

func TestHTTPClientV2WithTokenRefresher(t *testing.T) {
	expectedToken := "test-bearer-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authenticated"))
	}))
	defer server.Close()

	// Create a TokenRefresher with the expected token
	tokenRefresher := &TokenRefresher{
		authToken: expectedToken,
	}

	client := NewHTTPClientV2(WithAuthTokenProvider(tokenRefresher))

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := client.Do(ctx, req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTTPClientV2TokenRefreshBehavior(t *testing.T) {
	var currentExpectedToken string
	var requestCount int
	var testMutex sync.RWMutex // Protect shared test variables

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testMutex.Lock()
		requestCount++
		testMutex.Unlock()

		testMutex.RLock()
		expectedToken := currentExpectedToken
		testMutex.RUnlock()

		auth := r.Header.Get("Authorization")
		expectedAuth := "Bearer " + expectedToken

		if auth != expectedAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("X-Used-Token", expectedToken)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tokenRefresher := &TokenRefresher{authToken: "initial-token"}
	client := NewHTTPClientV2(WithAuthTokenProvider(tokenRefresher))
	ctx := context.Background()

	t.Run("token lifecycle progression", func(t *testing.T) {
		// Test token evolution through multiple refreshes in a single client
		tokenProgression := []string{"initial-token", "refresh-1", "refresh-2", "refresh-3", "refresh-4"}

		for i, token := range tokenProgression {
			if i > 0 {
				// Simulate token refresh
				tokenRefresher.rwMutex.Lock()
				tokenRefresher.authToken = token
				tokenRefresher.authTokenRefreshTimestamp = time.Now().Unix()
				tokenRefresher.rwMutex.Unlock()
			}

			testMutex.Lock()
			currentExpectedToken = token
			testMutex.Unlock()

			req, err := http.NewRequest("GET", server.URL+fmt.Sprintf("?stage=%d", i), nil)
			require.NoError(t, err)

			resp, err := client.Do(ctx, req)
			require.NoError(t, err)
			resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, token, resp.Header.Get("X-Used-Token"))
		}

		t.Logf("Successfully progressed through %d token refresh stages", len(tokenProgression))
	})

	t.Run("concurrent access during token refresh", func(t *testing.T) {
		// Focus on thread safety during token access
		stableToken := "concurrent-stable-token"
		tokenRefresher.rwMutex.Lock()
		tokenRefresher.authToken = stableToken
		tokenRefresher.rwMutex.Unlock()

		testMutex.Lock()
		currentExpectedToken = stableToken
		testMutex.Unlock()

		const numGoroutines = 10
		results := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(goroutineID int) {
				req, err := http.NewRequest("GET", server.URL+fmt.Sprintf("?goroutine=%d", goroutineID), nil)
				if err != nil {
					results <- err
					return
				}

				resp, err := client.Do(ctx, req)
				if err != nil {
					results <- err
					return
				}
				resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					results <- fmt.Errorf("goroutine %d failed with status %d", goroutineID, resp.StatusCode)
					return
				}
				results <- nil
			}(i)
		}

		for i := 0; i < numGoroutines; i++ {
			assert.NoError(t, <-results, "Concurrent request %d should succeed", i)
		}
	})

	t.Run("token method consistency", func(t *testing.T) {
		// Verify Token() method returns consistent results
		verificationToken := "consistency-check-token"
		tokenRefresher.rwMutex.Lock()
		tokenRefresher.authToken = verificationToken
		tokenRefresher.rwMutex.Unlock()

		// Multiple calls to Token() should return the same value
		assert.Equal(t, verificationToken, tokenRefresher.Token())
		assert.Equal(t, verificationToken, tokenRefresher.Token())
		assert.Equal(t, verificationToken, tokenRefresher.Token())

		// HTTP request should use the same token
		testMutex.Lock()
		currentExpectedToken = verificationToken
		testMutex.Unlock()

		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)

		resp, err := client.Do(ctx, req)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, verificationToken, resp.Header.Get("X-Used-Token"))
	})

	testMutex.RLock()
	finalRequestCount := requestCount
	testMutex.RUnlock()

	t.Logf("Token refresh behavior test completed with %d requests", finalRequestCount)
}

func TestMultipleOptionsApplication(t *testing.T) {
	client := NewHTTPClientV2(
		WithRetryMax(10),
		WithRetryWait(200*time.Millisecond, 5*time.Second),
		WithConnectionLimits(300, 30, 150),
		WithErrorHandler(retryablehttp.PassthroughErrorHandler),
	)

	assert.NotNil(t, client)
	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.authTokenProvider)
}

func TestHTTPClientV2MultiClientTokenIsolation(t *testing.T) {
	type tokenUsage struct {
		clientID string
		token    string
	}
	var tokenUsages []tokenUsage
	var usageMutex sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		clientID := r.Header.Get("X-Client-ID")

		if strings.HasPrefix(auth, "Bearer ") && clientID != "" {
			token := strings.TrimPrefix(auth, "Bearer ")

			usageMutex.Lock()
			tokenUsages = append(tokenUsages, tokenUsage{clientID: clientID, token: token})
			usageMutex.Unlock()

			w.Header().Set("X-Used-Token", token)
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	// Create shared underlying HTTP client to test token isolation
	sharedClient := &http.Client{Timeout: 30 * time.Second}

	// Create distinct token refreshers for different use cases
	serviceATokens := &TokenRefresher{authToken: "service-a-initial"}
	serviceBTokens := &TokenRefresher{authToken: "service-b-initial"}
	userTokens := &TokenRefresher{authToken: "user-initial"}

	// All clients share the same underlying HTTP client but have different tokens
	clientA := NewHTTPClientV2WithClient(sharedClient, WithAuthTokenProvider(serviceATokens))
	clientB := NewHTTPClientV2WithClient(sharedClient, WithAuthTokenProvider(serviceBTokens))
	clientUser := NewHTTPClientV2WithClient(sharedClient, WithAuthTokenProvider(userTokens))

	ctx := context.Background()

	t.Run("token isolation between clients", func(t *testing.T) {
		// Each client should use its own token, never mixing them
		testCases := []struct {
			client        *HTTPClientV2
			clientID      string
			expectedToken string
		}{
			{clientA, "service-a", "service-a-initial"},
			{clientB, "service-b", "service-b-initial"},
			{clientUser, "user-client", "user-initial"},
		}

		for _, tc := range testCases {
			req, err := http.NewRequest("GET", server.URL+"/test", nil)
			require.NoError(t, err)
			req.Header.Set("X-Client-ID", tc.clientID)

			resp, err := tc.client.Do(ctx, req)
			require.NoError(t, err)
			resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tc.expectedToken, resp.Header.Get("X-Used-Token"))
		}
	})

	t.Run("concurrent multi-client token usage", func(t *testing.T) {
		// Test that concurrent requests from different clients maintain token isolation
		const requestsPerClient = 5

		clients := []struct {
			client   *HTTPClientV2
			clientID string
			token    string
		}{
			{clientA, "concurrent-service-a", "service-a-initial"},
			{clientB, "concurrent-service-b", "service-b-initial"},
			{clientUser, "concurrent-user", "user-initial"},
		}

		var wg sync.WaitGroup
		errors := make(chan error, len(clients)*requestsPerClient)

		for _, clientInfo := range clients {
			for i := 0; i < requestsPerClient; i++ {
				wg.Add(1)
				go func(c *HTTPClientV2, id, token string, reqNum int) {
					defer wg.Done()

					req, err := http.NewRequest("GET", server.URL+fmt.Sprintf("/concurrent/%d", reqNum), nil)
					if err != nil {
						errors <- err
						return
					}
					req.Header.Set("X-Client-ID", id)

					resp, err := c.Do(ctx, req)
					if err != nil {
						errors <- err
						return
					}
					resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						errors <- fmt.Errorf("client %s request failed", id)
						return
					}

					if resp.Header.Get("X-Used-Token") != token {
						errors <- fmt.Errorf("client %s got wrong token", id)
						return
					}

					errors <- nil
				}(clientInfo.client, clientInfo.clientID, clientInfo.token, i)
			}
		}

		wg.Wait()
		close(errors)

		// Verify all requests succeeded
		for err := range errors {
			assert.NoError(t, err)
		}
	})

	t.Run("independent token refresh per client", func(t *testing.T) {
		// Refresh tokens independently - each client should only see its own changes
		serviceATokens.rwMutex.Lock()
		serviceATokens.authToken = "service-a-refreshed"
		serviceATokens.rwMutex.Unlock()

		userTokens.rwMutex.Lock()
		userTokens.authToken = "user-refreshed"
		userTokens.rwMutex.Unlock()

		// Service B token remains unchanged

		refreshTests := []struct {
			client        *HTTPClientV2
			clientID      string
			expectedToken string
		}{
			{clientA, "refresh-service-a", "service-a-refreshed"}, // Changed
			{clientB, "refresh-service-b", "service-b-initial"},   // Unchanged
			{clientUser, "refresh-user", "user-refreshed"},        // Changed
		}

		for _, tc := range refreshTests {
			req, err := http.NewRequest("GET", server.URL+"/refresh-test", nil)
			require.NoError(t, err)
			req.Header.Set("X-Client-ID", tc.clientID)

			resp, err := tc.client.Do(ctx, req)
			require.NoError(t, err)
			resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tc.expectedToken, resp.Header.Get("X-Used-Token"))
		}
	})

	// Verify client architecture isolation
	t.Run("verify shared client architecture", func(t *testing.T) {
		// All clients share the same underlying HTTP client
		assert.Same(t, sharedClient, clientA.httpClient)
		assert.Same(t, sharedClient, clientB.httpClient)
		assert.Same(t, sharedClient, clientUser.httpClient)

		// But have completely separate token providers
		assert.NotSame(t, clientA.authTokenProvider, clientB.authTokenProvider)
		assert.NotSame(t, clientB.authTokenProvider, clientUser.authTokenProvider)
		assert.NotSame(t, clientA.authTokenProvider, clientUser.authTokenProvider)
	})

	// Analyze token usage patterns
	usageMutex.Lock()
	tokensByClient := make(map[string][]string)
	for _, usage := range tokenUsages {
		tokensByClient[usage.clientID] = append(tokensByClient[usage.clientID], usage.token)
	}
	usageMutex.Unlock()

	t.Logf("Token isolation test completed")
	for clientID, tokens := range tokensByClient {
		uniqueTokens := make(map[string]bool)
		for _, token := range tokens {
			uniqueTokens[token] = true
		}
		t.Logf("Client %s used %d unique tokens across %d requests", clientID, len(uniqueTokens), len(tokens))
	}
}

func TestHTTPClientV2SharingStrategy(t *testing.T) {
	t.Run("singleton sharing rules", func(t *testing.T) {
		// Rule 1: Default clients share singleton
		defaultClient1 := DefaultHTTPClientV2()
		defaultClient2 := DefaultHTTPClientV2()

		assert.Same(t, defaultClient1.httpClient, defaultClient2.httpClient)
		t.Logf("✅ Default clients share singleton: %p", defaultClient1.httpClient)

		// Rule 2: Auth clients also share singleton
		authClient1 := DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: "token1"})
		authClient2 := DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: "token2"})

		assert.Same(t, authClient1.httpClient, authClient2.httpClient)
		assert.Same(t, defaultClient1.httpClient, authClient1.httpClient) // Same as default too
		t.Logf("✅ Auth clients share singleton: %p", authClient1.httpClient)

		// Rule 3: Custom clients create dedicated clients
		customClient := NewHTTPClientV2(WithRetryMax(5))

		assert.NotSame(t, defaultClient1.httpClient, customClient.httpClient)
		t.Logf("🆕 Custom client creates dedicated client: %p", customClient.httpClient)
	})

	t.Run("api-based sharing matrix", func(t *testing.T) {
		baselineClient := DefaultHTTPClientV2()

		sharingTests := []struct {
			name         string
			createClient func() *HTTPClientV2
			shouldShare  bool
		}{
			{"DefaultHTTPClientV2", func() *HTTPClientV2 { return DefaultHTTPClientV2() }, true},
			{"DefaultHTTPClientV2WithAuth", func() *HTTPClientV2 {
				return DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: "test"})
			}, true},
			{"NewHTTPClientV2 no options", func() *HTTPClientV2 { return NewHTTPClientV2() }, false},
			{"NewHTTPClientV2 with retry", func() *HTTPClientV2 { return NewHTTPClientV2(WithRetryMax(10)) }, false},
			{"NewHTTPClientV2 with auth", func() *HTTPClientV2 {
				return NewHTTPClientV2(WithAuthTokenProvider(&TokenRefresher{authToken: "test"}))
			}, false},
		}

		for _, test := range sharingTests {
			t.Run(test.name, func(t *testing.T) {
				client := test.createClient()

				if test.shouldShare {
					assert.Same(t, baselineClient.httpClient, client.httpClient)
					t.Logf("✅ %s shares singleton", test.name)
				} else {
					assert.NotSame(t, baselineClient.httpClient, client.httpClient)
					t.Logf("🆕 %s creates new client", test.name)
				}
			})
		}
	})

	t.Run("custom configs create unique clients", func(t *testing.T) {
		// Each custom config creates its own unique client
		clients := []*HTTPClientV2{
			NewHTTPClientV2(WithRetryMax(3)),
			NewHTTPClientV2(WithRetryMax(5)), // Different retry max
			NewHTTPClientV2(WithRetryMax(3)), // Same retry max - should still be different instance
			NewHTTPClientV2(WithConnectionLimits(100, 10, 50)),
			NewHTTPClientV2(WithRetryWait(time.Second, 5*time.Second)),
		}

		// All custom clients should have unique underlying HTTP clients
		for i := 0; i < len(clients); i++ {
			for j := i + 1; j < len(clients); j++ {
				assert.NotSame(t, clients[i].httpClient, clients[j].httpClient,
					"Custom client %d and %d should have different underlying clients", i, j)
			}
		}

		t.Logf("Created %d custom clients, all with unique underlying HTTP clients", len(clients))
	})

	t.Run("explicit client sharing", func(t *testing.T) {
		// When explicitly providing an HTTP client, multiple HTTPClientV2 can share it
		sharedHTTPClient := &http.Client{Timeout: 10 * time.Second}

		client1 := NewHTTPClientV2WithClient(sharedHTTPClient)
		client2 := NewHTTPClientV2WithClient(sharedHTTPClient, WithAuthTokenProvider(&TokenRefresher{authToken: "test"}))
		client3 := NewHTTPClientV2WithClient(sharedHTTPClient, WithAuthTokenProvider(&TokenRefresher{authToken: "other"}))

		// All explicitly share the same underlying client
		assert.Same(t, sharedHTTPClient, client1.httpClient)
		assert.Same(t, sharedHTTPClient, client2.httpClient)
		assert.Same(t, sharedHTTPClient, client3.httpClient)

		// But singleton clients don't share with explicit clients
		singletonClient := NewHTTPClientV2()
		assert.NotSame(t, sharedHTTPClient, singletonClient.httpClient)

		t.Logf("Explicit sharing: 3 clients share provided HTTP client %p", sharedHTTPClient)
		t.Logf("Singleton isolation: singleton uses %p", singletonClient.httpClient)
	})
}

func TestHTTPClientV2ResourceEfficiency(t *testing.T) {
	t.Run("singleton resource sharing", func(t *testing.T) {
		// Large number of default clients should share resources efficiently
		const numClients = 100
		clients := make([]*HTTPClientV2, numClients)

		for i := 0; i < numClients; i++ {
			clients[i] = DefaultHTTPClientV2()
		}

		// All should share the same underlying HTTP client
		sharedHTTPClient := clients[0].httpClient
		for i := 1; i < numClients; i++ {
			assert.Same(t, sharedHTTPClient, clients[i].httpClient)
		}

		t.Logf("Resource efficiency: %d HTTPClientV2 instances share 1 underlying HTTP client", numClients)
		t.Logf("Memory footprint: 1 HTTP client (%p) serves %d logical clients", sharedHTTPClient, numClients)
	})

	t.Run("mixed usage patterns", func(t *testing.T) {
		// Simulate realistic usage patterns in a microservices environment
		const (
			numDefaultClients = 50
			numAuthClients    = 30
			numCustomClients  = 10
		)

		var allClients []*HTTPClientV2
		httpClientSet := make(map[*http.Client]bool)

		// Default clients (no options)
		for i := 0; i < numDefaultClients; i++ {
			client := DefaultHTTPClientV2()
			allClients = append(allClients, client)
			httpClientSet[client.httpClient] = true
		}

		// Auth-only clients (should still share singleton)
		for i := 0; i < numAuthClients; i++ {
			client := DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: fmt.Sprintf("token-%d", i)})
			allClients = append(allClients, client)
			httpClientSet[client.httpClient] = true
		}

		// Custom config clients (each creates new HTTP client)
		customConfigs := []func() *HTTPClientV2{
			func() *HTTPClientV2 { return NewHTTPClientV2(WithRetryMax(3)) },
			func() *HTTPClientV2 { return NewHTTPClientV2(WithRetryMax(5)) },
			func() *HTTPClientV2 { return NewHTTPClientV2(WithConnectionLimits(200, 20, 100)) },
			func() *HTTPClientV2 { return NewHTTPClientV2(WithRetryWait(100, 1000)) },
		}

		for i := 0; i < numCustomClients; i++ {
			configIndex := i % len(customConfigs)
			client := customConfigs[configIndex]()
			allClients = append(allClients, client)
			httpClientSet[client.httpClient] = true
		}

		totalLogicalClients := len(allClients)
		uniqueHTTPClients := len(httpClientSet)

		// Expected: 1 shared singleton + numCustomClients unique clients
		expectedUniqueClients := 1 + numCustomClients

		assert.Equal(t, totalLogicalClients, numDefaultClients+numAuthClients+numCustomClients)
		assert.Equal(t, expectedUniqueClients, uniqueHTTPClients)

		t.Logf("Mixed usage efficiency:")
		t.Logf("  %d default clients + %d auth clients = share 1 HTTP client", numDefaultClients, numAuthClients)
		t.Logf("  %d custom clients = %d unique HTTP clients", numCustomClients, numCustomClients)
		t.Logf("  Total: %d logical clients use %d underlying HTTP clients", totalLogicalClients, uniqueHTTPClients)
		t.Logf("  Resource ratio: %.1f logical clients per HTTP client", float64(totalLogicalClients)/float64(uniqueHTTPClients))
	})

	t.Run("memory usage pattern analysis", func(t *testing.T) {
		// Analyze memory patterns for different client creation patterns
		patterns := []struct {
			name                string
			createFunc          func() []*HTTPClientV2
			expectedHTTPClients int
		}{
			{
				name: "all default",
				createFunc: func() []*HTTPClientV2 {
					clients := make([]*HTTPClientV2, 20)
					for i := 0; i < 20; i++ {
						clients[i] = DefaultHTTPClientV2()
					}
					return clients
				},
				expectedHTTPClients: 1, // All share singleton
			},
			{
				name: "all auth-only",
				createFunc: func() []*HTTPClientV2 {
					clients := make([]*HTTPClientV2, 20)
					for i := 0; i < 20; i++ {
						clients[i] = DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: fmt.Sprintf("auth-%d", i)})
					}
					return clients
				},
				expectedHTTPClients: 1, // All share singleton
			},
			{
				name: "all custom",
				createFunc: func() []*HTTPClientV2 {
					clients := make([]*HTTPClientV2, 20)
					for i := 0; i < 20; i++ {
						clients[i] = NewHTTPClientV2(WithRetryMax(3 + i%5)) // Varying retry configs
					}
					return clients
				},
				expectedHTTPClients: 20, // Each creates new HTTP client
			},
		}

		for _, pattern := range patterns {
			t.Run(pattern.name, func(t *testing.T) {
				clients := pattern.createFunc()

				httpClientSet := make(map[*http.Client]bool)
				for _, client := range clients {
					httpClientSet[client.httpClient] = true
				}

				actualHTTPClients := len(httpClientSet)
				assert.Equal(t, pattern.expectedHTTPClients, actualHTTPClients)

				efficiency := float64(len(clients)) / float64(actualHTTPClients)
				t.Logf("%s: %d logical clients → %d HTTP clients (%.1fx efficiency)",
					pattern.name, len(clients), actualHTTPClients, efficiency)
			})
		}
	})

	t.Run("connection pool resource sharing", func(t *testing.T) {
		// Test that shared HTTP clients actually share connection pools
		const numSharedClients = 10

		// Create clients that share the singleton
		sharedClients := make([]*HTTPClientV2, numSharedClients)
		for i := 0; i < numSharedClients; i++ {
			sharedClients[i] = DefaultHTTPClientV2WithAuth(&TokenRefresher{authToken: fmt.Sprintf("shared-%d", i)})
		}

		// Verify they all share the same transport (connection pool)
		baseTransport := sharedClients[0].httpClient.Transport
		for i := 1; i < numSharedClients; i++ {
			assert.Same(t, baseTransport, sharedClients[i].httpClient.Transport,
				"Client %d should share the same transport/connection pool", i)
		}

		// Create a custom client with different connection limits
		customClient := NewHTTPClientV2(WithConnectionLimits(50, 5, 25))

		// Custom client should have a different transport
		assert.NotSame(t, baseTransport, customClient.httpClient.Transport)

		t.Logf("Connection pool sharing verified:")
		t.Logf("  %d clients share same transport/connection pool", numSharedClients)
		t.Logf("  Custom client uses isolated connection pool")
	})
}

func TestDefaultHTTPClientV2(t *testing.T) {
	t.Run("basic default client", func(t *testing.T) {
		client := DefaultHTTPClientV2()
		assert.NotNil(t, client)
		assert.NotNil(t, client.httpClient)
		assert.Nil(t, client.authTokenProvider)
	})

	t.Run("multiple default clients share singleton", func(t *testing.T) {
		client1 := DefaultHTTPClientV2()
		client2 := DefaultHTTPClientV2()

		// Should share the same underlying HTTP client
		assert.Same(t, client1.httpClient, client2.httpClient)
		assert.NotSame(t, client1, client2) // Different HTTPClientV2 instances
	})
}

func TestDefaultHTTPClientV2WithAuth(t *testing.T) {
	tokenRefresher := &TokenRefresher{authToken: "test-token"}

	t.Run("auth client creation", func(t *testing.T) {
		client := DefaultHTTPClientV2WithAuth(tokenRefresher)
		assert.NotNil(t, client)
		assert.NotNil(t, client.httpClient)
		assert.Same(t, tokenRefresher, client.authTokenProvider)
	})

	t.Run("multiple auth clients share singleton HTTP client", func(t *testing.T) {
		tokenA := &TokenRefresher{authToken: "token-A"}
		tokenB := &TokenRefresher{authToken: "token-B"}

		clientA := DefaultHTTPClientV2WithAuth(tokenA)
		clientB := DefaultHTTPClientV2WithAuth(tokenB)

		// Should share the same underlying HTTP client
		assert.Same(t, clientA.httpClient, clientB.httpClient)

		// But have different auth providers
		assert.Same(t, tokenA, clientA.authTokenProvider)
		assert.Same(t, tokenB, clientB.authTokenProvider)
		assert.NotSame(t, clientA.authTokenProvider, clientB.authTokenProvider)
	})

	t.Run("default and auth clients share singleton", func(t *testing.T) {
		defaultClient := DefaultHTTPClientV2()
		authClient := DefaultHTTPClientV2WithAuth(tokenRefresher)

		// Both should share the same underlying HTTP client
		assert.Same(t, defaultClient.httpClient, authClient.httpClient)

		// But have different auth providers
		assert.Nil(t, defaultClient.authTokenProvider)
		assert.Same(t, tokenRefresher, authClient.authTokenProvider)
	})
}
