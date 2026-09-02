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

package servicecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCache_Fetch(t *testing.T) {
	validResponse := models.ServicesResponse{
		Services: []models.ServiceInfo{
			{
				ServiceID:          "test-service-id",
				ServiceName:        "nvcf-api",
				AudienceServiceIDs: []string{"test-service-id"},
			},
		},
	}

	testCases := []struct {
		name          string
		serverHandler http.HandlerFunc
		expectError   bool
		errorContains string
	}{
		{
			name: "successful fetch",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(validResponse)
			},
			expectError: false,
		},
		{
			name: "server returns 500",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			},
			expectError:   true,
			errorContains: "status 500",
		},
		{
			name: "invalid json response",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not json"))
			},
			expectError:   true,
			errorContains: "failed to decode",
		},
		{
			name: "empty services array",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{}})
			},
			expectError:   true,
			errorContains: "no services found",
		},
		{
			name: "service missing service ID",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{{
					ServiceName: "nvcf-api",
				}}})
			},
			expectError:   true,
			errorContains: "service metadata is missing service_id",
		},
		{
			name: "response missing nvcf api service",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{{
					ServiceName: "test-service",
				}}})
			},
			expectError:   true,
			errorContains: "nvcf-api",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.serverHandler)
			defer server.Close()

			cache := New(server.URL)

			err := cache.Fetch()

			if tc.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				} else if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error to contain '%s', got '%s'", tc.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !cache.IsReady() {
					t.Error("cache should be ready after successful fetch")
				}
				info := cache.Get()
				if info == nil {
					t.Error("expected service info to be cached")
				} else if info.ServiceID != "test-service-id" {
					t.Errorf("expected service_id 'test-service-id', got '%s'", info.ServiceID)
				}
			}
		})
	}
}

func TestCacheFetchSelectsNVCFAPIRegardlessOfResponseOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{
			{ServiceName: "nvct-api", ServiceID: "tasks-service-id"},
			{ServiceName: "nvcf-api", ServiceID: "functions-service-id"},
			{ServiceName: "event-ledger", ServiceID: "ledger-service-id"},
		}})
	}))
	defer server.Close()

	cache := New(server.URL)
	if err := cache.Fetch(); err != nil {
		t.Fatalf("Fetch() returned an unexpected error: %v", err)
	}
	if cache.Get() == nil {
		t.Fatal("Fetch() did not cache service metadata")
	}
	if got := cache.Get().ServiceName; got != "nvcf-api" {
		t.Fatalf("cached service name = %q, want nvcf-api", got)
	}
	if got := cache.Get().ServiceID; got != "functions-service-id" {
		t.Fatalf("cached service ID = %q, want functions-service-id", got)
	}
}

func TestCacheFetchWithRetryToleratesColdStart(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}

		_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{{
			ServiceID:   "test-service-id",
			ServiceName: "nvcf-api",
		}}})
	}))
	defer server.Close()

	cache := New(server.URL)
	err := cache.FetchWithRetry(context.Background(), RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("expected cold-start retries to recover, got %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 metadata requests, got %d", got)
	}
	if !cache.IsReady() {
		t.Fatal("cache should be ready after a successful retry")
	}
}

func TestCacheFetchWithRetryToleratesConnectionRefused(t *testing.T) {
	var requests atomic.Int32
	cache := New("http://api-keys.test/v1/services")
	cache.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if requests.Add(1) < 3 {
			return nil, &url.Error{Op: r.Method, URL: r.URL.String(), Err: syscall.ECONNREFUSED}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(newJSONReader(t, models.ServicesResponse{Services: []models.ServiceInfo{{
				ServiceID:   "test-service-id",
				ServiceName: "nvcf-api",
			}}})),
		}, nil
	})

	err := cache.FetchWithRetry(context.Background(), RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("expected connection-refused retries to recover, got %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 metadata requests, got %d", got)
	}
}

func TestCacheFetchWithRetryToleratesDNSNotFound(t *testing.T) {
	var requests atomic.Int32
	cache := New("http://api-keys.test/v1/services")
	cache.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if requests.Add(1) < 3 {
			return nil, &url.Error{
				Op:  r.Method,
				URL: r.URL.String(),
				Err: &net.DNSError{Name: "api-keys.test", IsNotFound: true},
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(newJSONReader(t, models.ServicesResponse{Services: []models.ServiceInfo{{
				ServiceID:   "test-service-id",
				ServiceName: "nvcf-api",
			}}})),
		}, nil
	})

	err := cache.FetchWithRetry(context.Background(), RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("expected DNS-not-found retries to recover, got %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 metadata requests, got %d", got)
	}
}

func TestCacheFetchWithRetryStopsOnPermanentDNSError(t *testing.T) {
	var requests atomic.Int32
	cache := New("http://api-keys.test/v1/services")
	cache.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, &url.Error{
			Op:  r.Method,
			URL: r.URL.String(),
			Err: &net.DNSError{
				Err:  "resolver configuration failed",
				Name: "api-keys.test",
			},
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := cache.FetchWithRetry(ctx, RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err == nil {
		t.Fatal("expected a permanent DNS error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("permanent DNS error was retried until timeout: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("permanent DNS error made %d requests, want 1", got)
	}
}

func TestCacheFetchWithRetryStopsOnPermanentTransportConfiguration(t *testing.T) {
	testCases := []struct {
		name string
		url  string
	}{
		{name: "malformed URL", url: "://bad"},
		{name: "unsupported scheme", url: "file:///tmp/services.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			cache := New(tc.url)
			err := cache.FetchWithRetry(ctx, RetryPolicy{
				InitialBackoff: time.Millisecond,
				MaxBackoff:     2 * time.Millisecond,
			})

			if err == nil {
				t.Fatal("expected a permanent transport configuration error")
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("permanent transport error was retried until timeout: %v", err)
			}
		})
	}
}

func TestCacheFetchWithRetryStopsOnCertificateError(t *testing.T) {
	var reachedServer atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedServer.Store(true)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cache := New(server.URL)
	err := cache.FetchWithRetry(ctx, RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err == nil {
		t.Fatal("expected an untrusted certificate error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("certificate error was retried until timeout: %v", err)
	}
	if reachedServer.Load() {
		t.Fatal("request reached a server with an untrusted certificate")
	}
}

func newJSONReader(t *testing.T, value any) io.Reader {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test response: %v", err)
	}
	return bytes.NewReader(contents)
}

func TestCacheFetchWithRetryStopsOnPermanentResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	cache := New(server.URL)
	err := cache.FetchWithRetry(context.Background(), RetryPolicy{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	if err == nil {
		t.Fatal("expected a permanent metadata error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected a permanent response to stop after one request, got %d", got)
	}
	if cache.IsReady() {
		t.Fatal("cache must remain unready after a permanent response")
	}
}

func TestCacheFetchBoundsErrorResponseBody(t *testing.T) {
	const tailMarker = "response-tail-must-not-be-buffered"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", 70<<10) + tailMarker))
	}))
	defer server.Close()

	cache := New(server.URL)
	err := cache.Fetch()
	if err == nil {
		t.Fatal("expected an error response")
	}
	if strings.Contains(err.Error(), tailMarker) {
		t.Fatal("metadata error buffered content beyond the diagnostic prefix")
	}
	if len(err.Error()) >= 66<<10 {
		t.Fatalf("metadata error length = %d, want a bounded diagnostic prefix", len(err.Error()))
	}
}

func TestCacheFetchWithRetryHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "starting", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cache := New(server.URL)
	err := cache.FetchWithRetry(ctx, RetryPolicy{
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
	})

	if err != context.Canceled {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestCacheFetchWithRetryCancelsInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	cache := New(server.URL)
	cache.httpClient.Timeout = 500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- cache.FetchWithRetry(ctx, RetryPolicy{
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		})
	}()

	<-requestStarted
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("in-flight metadata request did not stop after cancellation")
	}
}

func TestCache_IsReady(t *testing.T) {
	cache := New("http://example.com")

	if cache.IsReady() {
		t.Error("cache should not be ready before fetch")
	}

	// Manually set service info to simulate successful fetch
	cache.mu.Lock()
	cache.serviceInfo = &models.ServiceInfo{
		ServiceID: "test",
	}
	cache.mu.Unlock()

	if !cache.IsReady() {
		t.Error("cache should be ready after service info is set")
	}
}

func TestCache_Get(t *testing.T) {
	cache := New("http://example.com")

	if cache.Get() != nil {
		t.Error("expected nil before fetch")
	}

	expectedInfo := &models.ServiceInfo{
		ServiceID:   "test-id",
		ServiceName: "test-name",
	}

	cache.mu.Lock()
	cache.serviceInfo = expectedInfo
	cache.mu.Unlock()

	got := cache.Get()
	if got == nil {
		t.Fatal("expected service info, got nil")
	}
	if got.ServiceID != expectedInfo.ServiceID {
		t.Errorf("expected service_id '%s', got '%s'", expectedInfo.ServiceID, got.ServiceID)
	}
}
