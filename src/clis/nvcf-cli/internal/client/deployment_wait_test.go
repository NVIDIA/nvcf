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

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type deploymentTransportFunc func(*http.Request) (*http.Response, error)

func (f deploymentTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeploymentStatusRecovery(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v2/nvcf/functions/function/versions/version" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if call == 1 {
					w.WriteHeader(status)
					return
				}
				_, _ = fmt.Fprint(w, `{"function":{"status":"ACTIVE"}}`)
			}))
			defer server.Close()
			c := &Client{config: &Config{}, baseURL: server.URL, httpClient: server.Client()}
			if err := c.waitForFunctionDeployment(context.Background(), "function", "version", time.Second, time.Millisecond, time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("got %d requests, want 2", calls.Load())
			}
		})
	}
}

func TestDeploymentStatusTerminalErrors(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status, calls    int
	}{
		{"unauthorized", "denied", "could not query", 401, 1},
		{"forbidden", "denied", "could not query", 403, 1},
		{"missing", "missing", "could not query", 404, 1},
		{"server error", "failed", "could not query", 500, 1},
		{"exhausted", "upstream unavailable", "could not query", 503, 4},
		{"failed", `{"function":{"status":"FAILED"}}`, "deployment failed with status: FAILED", 200, 1},
		{"error", `{"function":{"status":"ERROR"}}`, "deployment failed with status: ERROR", 200, 1},
		{"invalid JSON", `not json`, "failed to decode response", 200, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := &Client{config: &Config{}, baseURL: "http://example.test", httpClient: &http.Client{Transport: deploymentTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
			})}}
			err := c.waitForFunctionDeployment(context.Background(), "function", "version", time.Second, time.Millisecond, time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
			if calls != test.calls {
				t.Fatalf("got %d requests, want %d", calls, test.calls)
			}
			if test.status != 200 {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != test.status {
					t.Fatalf("HTTP status lost: %v", err)
				}
				if apiErr.Error() != fmt.Sprintf("API error %d: %s", test.status, test.body) {
					t.Fatal(apiErr.Error())
				}
			}
		})
	}
}

func TestDeploymentStatusTransportRecovery(t *testing.T) {
	for _, failure := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			calls := 0
			c := &Client{config: &Config{}, baseURL: "http://example.test", httpClient: &http.Client{Transport: deploymentTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, failure
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"function":{"status":"ACTIVE"}}`)), Header: make(http.Header)}, nil
			})}}
			if err := c.waitForFunctionDeployment(context.Background(), "function", "version", time.Second, time.Millisecond, time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("got %d requests, want 2", calls)
			}
		})
	}
	if retryableStatusQuery(context.Canceled) || retryableStatusQuery(errors.New("invalid certificate")) {
		t.Fatal("permanent errors must not be retried")
	}
}

func TestDeploymentStatusResetsRetryBudget(t *testing.T) {
	responses := []string{"503", "503", "503", "DEPLOYING", "503", "503", "503", "ACTIVE"}
	calls := 0
	c := &Client{config: &Config{}, baseURL: "http://example.test", httpClient: &http.Client{Transport: deploymentTransportFunc(func(r *http.Request) (*http.Response, error) {
		status := responses[calls]
		calls++
		code := 200
		if status == "503" {
			code = 503
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"function":{"status":%q}}`, status))), Header: make(http.Header)}, nil
	})}}
	if err := c.waitForFunctionDeployment(context.Background(), "function", "version", time.Second, time.Millisecond, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls != len(responses) {
		t.Fatalf("got %d requests", calls)
	}
}

func TestDeploymentStatusWaitCancellation(t *testing.T) {
	for _, phase := range []string{"request", "poll", "retry"} {
		t.Run(phase, func(t *testing.T) {
			c := &Client{config: &Config{}, baseURL: "http://example.test", httpClient: &http.Client{Transport: deploymentTransportFunc(func(r *http.Request) (*http.Response, error) {
				if phase == "request" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				code := 200
				if phase == "retry" {
					code = 503
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"function":{"status":"DEPLOYING"}}`)), Header: make(http.Header)}, nil
			})}}
			start := time.Now()
			err := c.waitForFunctionDeployment(context.Background(), "function", "version", 30*time.Millisecond, time.Hour, time.Hour)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("got %v", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("wait exceeded the original deadline")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := c.waitForFunctionDeployment(ctx, "function", "version", time.Hour, time.Hour, time.Hour); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestDeploymentPOSTIsNotRetried(t *testing.T) {
	calls := 0
	c := &Client{config: &Config{Token: "test-token"}, baseURL: "http://example.test", httpClient: &http.Client{Transport: deploymentTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("got %s", r.Method)
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
	})}}
	err := c.DeployFunction(context.Background(), "function", "version", &FunctionDeploymentRequest{DeploymentSpecifications: []GPUSpecificationDto{{GPU: "test", InstanceType: "test", MaxInstances: 1}}})
	if err == nil || calls != 1 {
		t.Fatalf("got err=%v requests=%d", err, calls)
	}
}
