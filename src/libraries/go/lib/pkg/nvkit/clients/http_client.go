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
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// HTTPClientV2 offers two usage patterns:
//
// # Default Clients (Shared Resources)
//
// DefaultHTTPClientV2() returns clients with sane defaults that share a singleton HTTP client
// for optimal memory efficiency and connection pool reuse. Use this for most scenarios.
//
//	// Default shared client
//	client := DefaultHTTPClientV2()
//
//	// Shared clients with different auth tokens
//	clientA := DefaultHTTPClientV2WithAuth(tokenA)
//	clientB := DefaultHTTPClientV2WithAuth(tokenB)
//
// # Custom Clients (Isolated Resources)
//
// NewHTTPClientV2() creates clients with dedicated HTTP clients for custom configuration
// and complete isolation. Use this when you need custom retry, connection, or error handling.
//
//	// Custom clients each get their own underlying HTTP client
//	fastClient := NewHTTPClientV2(WithRetryMax(1))
//	slowClient := NewHTTPClientV2(WithRetryMax(10))
//	authClient := NewHTTPClientV2(WithAuthTokenProvider(token), WithRetryMax(5))
//
// # Benefits of Each Approach
//
// Default clients (shared):
//   - Memory efficient: 100+ logical clients can share 1 underlying HTTP client
//   - Faster initialization: reuses existing HTTP client
//   - Shared connection pools: better resource utilization
//
// Custom clients (isolated):
//   - Complete configuration control: custom retry, connection, error handling
//   - Performance isolation: one client's settings don't affect others
//   - Resource boundaries: separate connection pools per use case

// HTTPClientV2 is an HTTP client with retry logic, authentication, and telemetry support.
//
// Use DefaultHTTPClientV2() or DefaultHTTPClientV2WithAuth() for shared resources,
// or NewHTTPClientV2() for custom configuration.
//
// Example usage:
//
//	// Default clients (recommended for most use cases)
//	client := DefaultHTTPClientV2()
//	authClient := DefaultHTTPClientV2WithAuth(tokenRefresher)
//
//	// Custom client (for specialized configuration)
//	client := NewHTTPClientV2(WithRetryMax(5), WithAuthTokenProvider(tokenRefresher))
type HTTPClientV2 struct {
	httpClient        *http.Client
	authTokenProvider *TokenRefresher
}

// httpClientConfigV2 holds configuration options for HTTP client creation.
// This is an internal type used by functional options and should not be accessed directly.
type httpClientConfigV2 struct {
	retryWaitMin        time.Duration
	retryWaitMax        time.Duration
	retryMax            int
	maxIdleConns        int
	maxIdleConnsPerHost int
	maxConnsPerHost     int
	errorHandler        retryablehttp.ErrorHandler
	authTokenProvider   *TokenRefresher
}

// HTTPClientOption configures HTTPClientV2 using the functional options pattern.
type HTTPClientOption func(*httpClientConfigV2)

var (
	singletonClient *http.Client
	once            sync.Once
)

// WithAuthTokenProvider sets the authentication token provider for custom clients.
// The token is automatically added as a Bearer token in the Authorization header.
//
// For default shared clients, use DefaultHTTPClientV2WithAuth() instead.
//
// Example:
//
//	// Custom client with auth
//	client := NewHTTPClientV2(WithAuthTokenProvider(tokenRefresher), WithRetryMax(5))
func WithAuthTokenProvider(authTokenProvider *TokenRefresher) HTTPClientOption {
	return func(c *httpClientConfigV2) {
		c.authTokenProvider = authTokenProvider
	}
}

// WithRetryWait sets the minimum and maximum retry wait times.
// Default values are 50ms minimum and 300ms maximum.
// This option requires a dedicated client - use with NewHTTPClientV2.
//
// Example:
//
//	client := NewHTTPClientV2(WithRetryWait(100*time.Millisecond, 2*time.Second))
func WithRetryWait(min, max time.Duration) HTTPClientOption {
	return func(c *httpClientConfigV2) {
		c.retryWaitMin = min
		c.retryWaitMax = max
	}
}

// WithRetryMax sets the maximum number of retries.
// Default value is 2 retries.
// This option requires a dedicated client - use with NewHTTPClientV2.
//
// Example:
//
//	client := NewHTTPClientV2(WithRetryMax(5))
func WithRetryMax(max int) HTTPClientOption {
	return func(c *httpClientConfigV2) {
		c.retryMax = max
	}
}

// WithConnectionLimits sets the connection pool limits.
// Default values are: maxIdle=100, maxIdlePerHost=5, maxPerHost=0 (unlimited).
// This option requires a dedicated client - use with NewHTTPClientV2.
//
// Example:
//
//	client := NewHTTPClientV2(WithConnectionLimits(200, 10, 50))
func WithConnectionLimits(maxIdle, maxIdlePerHost, maxPerHost int) HTTPClientOption {
	return func(c *httpClientConfigV2) {
		c.maxIdleConns = maxIdle
		c.maxIdleConnsPerHost = maxIdlePerHost
		c.maxConnsPerHost = maxPerHost
	}
}

// WithErrorHandler sets a custom error handler for retries.
// Default is retryablehttp.PassthroughErrorHandler which retries on connection errors
// and 5xx status codes.
// This option requires a dedicated client - use with NewHTTPClientV2.
//
// Example:
//
//	customHandler := func(resp *http.Response, err error, numTries int) (*http.Response, error) {
//		// Custom retry logic
//		return resp, err
//	}
//	client := NewHTTPClientV2(WithErrorHandler(customHandler))
func WithErrorHandler(handler retryablehttp.ErrorHandler) HTTPClientOption {
	return func(c *httpClientConfigV2) {
		c.errorHandler = handler
	}
}

// defaultConfig returns the default configuration for HTTPClientV2.
// Default values: maxIdleConns=100, maxIdleConnsPerHost=5, maxConnsPerHost=0,
// retryMax=2, retryWaitMin=50ms, retryWaitMax=300ms.
func defaultConfig() *httpClientConfigV2 {
	return &httpClientConfigV2{
		maxIdleConns:        100,
		maxIdleConnsPerHost: 5,
		maxConnsPerHost:     0,
		retryMax:            2,
		retryWaitMin:        50 * time.Millisecond,
		retryWaitMax:        300 * time.Millisecond,
		errorHandler:        retryablehttp.PassthroughErrorHandler,
	}
}

// initSingletonHTTPClient creates the singleton HTTP client with default configuration.
// This singleton is shared across all HTTPClientV2 instances that use default settings.
func initSingletonHTTPClient() *http.Client {
	once.Do(func() {
		cfg := defaultConfig()
		singletonClient = buildHTTPClient(cfg)
	})
	return singletonClient
}

// buildHTTPClient creates a configured HTTP client with retry and telemetry support.
// It sets up OpenTelemetry instrumentation for distributed tracing and metrics.
func buildHTTPClient(cfg *httpClientConfigV2) *http.Client {
	// Create retryable HTTP client
	retryClient := retryablehttp.NewClient()

	// Configure transport
	transport := cleanhttp.DefaultPooledTransport()
	transport.MaxConnsPerHost = cfg.maxConnsPerHost
	transport.MaxIdleConns = cfg.maxIdleConns
	transport.MaxIdleConnsPerHost = cfg.maxIdleConnsPerHost

	retryClient.HTTPClient.Transport = transport
	retryClient.RetryWaitMin = cfg.retryWaitMin
	retryClient.RetryWaitMax = cfg.retryWaitMax
	retryClient.RetryMax = cfg.retryMax

	if cfg.errorHandler != nil {
		retryClient.ErrorHandler = cfg.errorHandler
	}

	retryClient.Logger = newRetryLogger()

	// Get standard client and add telemetry
	standardClient := retryClient.StandardClient()

	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)

	standardClient.Transport = otelhttp.NewTransport(
		standardClient.Transport,
		otelhttp.WithPropagators(propagator),
		otelhttp.WithTracerProvider(otel.GetTracerProvider()),
		otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return otelhttptrace.NewClientTrace(ctx)
		}),
	)

	return standardClient
}

// Do executes an HTTP request with retry logic and optional authentication.
// If an auth token provider is configured, it automatically adds the Bearer token
// to the Authorization header.
//
// Example:
//
//	req, _ := http.NewRequest("GET", "https://api.example.com", nil)
//	resp, err := client.Do(ctx, req)
//	if err != nil {
//		// Handle error
//	}
//	defer resp.Body.Close()
func (c *HTTPClientV2) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Add authorization header if token provider is available
	if c.authTokenProvider != nil {
		req.Header.Set("Authorization", "Bearer "+c.authTokenProvider.Token())
	}

	// Ensure the request uses the provided context
	req = req.WithContext(ctx)

	return c.httpClient.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
}

// DefaultHTTPClientV2 returns an HTTP client with sane defaults that shares a singleton HTTP client.
// Multiple instances returned by this function will share the same underlying HTTP client
// for optimal memory usage and connection pool reuse.
//
// Example:
//
//	client := DefaultHTTPClientV2()
func DefaultHTTPClientV2() *HTTPClientV2 {
	// Always use singleton for default clients
	httpClient := initSingletonHTTPClient()

	return &HTTPClientV2{
		httpClient:        httpClient,
		authTokenProvider: nil,
	}
}

// DefaultHTTPClientV2WithAuth returns an HTTP client with authentication that shares a singleton HTTP client.
// Multiple instances with different auth tokens will share the same underlying HTTP client for efficiency.
//
// Example:
//
//	tokenRefresher := &TokenRefresher{token: "your-token"}
//	client := DefaultHTTPClientV2WithAuth(tokenRefresher)
func DefaultHTTPClientV2WithAuth(authTokenProvider *TokenRefresher) *HTTPClientV2 {
	// Always use singleton for default clients
	httpClient := initSingletonHTTPClient()

	return &HTTPClientV2{
		httpClient:        httpClient,
		authTokenProvider: authTokenProvider,
	}
}

// NewHTTPClientV2 creates a new HTTP client with a dedicated underlying HTTP client.
// Each instance gets its own HTTP client for complete isolation and custom configuration.
//
// Use this constructor when you need custom behavior that differs from defaults:
//   - Custom retry settings: WithRetryMax(), WithRetryWait()
//   - Custom connection limits: WithConnectionLimits()
//   - Custom error handling: WithErrorHandler()
//   - Authentication: WithAuthTokenProvider()
//
// Example:
//
//	// Custom retry behavior (gets dedicated HTTP client)
//	client := NewHTTPClientV2(
//		WithRetryMax(10),
//		WithRetryWait(100*time.Millisecond, 5*time.Second),
//	)
//
//	// Custom connection limits (gets dedicated HTTP client)
//	client := NewHTTPClientV2(
//		WithConnectionLimits(200, 20, 100),
//		WithAuthTokenProvider(tokenRefresher),
//	)
func NewHTTPClientV2(opts ...HTTPClientOption) *HTTPClientV2 {
	cfg := defaultConfig()

	// Apply all options
	for _, opt := range opts {
		opt(cfg)
	}

	// Always create dedicated HTTP client
	httpClient := buildHTTPClient(cfg)

	return &HTTPClientV2{
		httpClient:        httpClient,
		authTokenProvider: cfg.authTokenProvider,
	}
}

// NewHTTPClientV2WithClient creates a new HTTP client using the provided http.Client.
// This allows you to use a pre-configured http.Client while still benefiting from
// HTTPClientV2's authentication features.
//
// Example:
//
//	customHTTPClient := &http.Client{Timeout: 30 * time.Second}
//	client := NewHTTPClientV2WithClient(customHTTPClient, WithAuthTokenProvider(tokenRefresher))
func NewHTTPClientV2WithClient(httpClient *http.Client, opts ...HTTPClientOption) *HTTPClientV2 {
	cfg := defaultConfig()

	// Apply options (mainly auth)
	for _, opt := range opts {
		opt(cfg)
	}

	return &HTTPClientV2{
		httpClient:        httpClient,
		authTokenProvider: cfg.authTokenProvider,
	}
}
