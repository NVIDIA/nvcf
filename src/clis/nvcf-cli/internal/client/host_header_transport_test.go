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
	"net/http"
	"testing"
)

// hostCapturingRoundTripper captures the Host header for verification
type hostCapturingRoundTripper struct {
	capturedHost string
}

func (m *hostCapturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.capturedHost = req.Host
	return &http.Response{
		StatusCode: 200,
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// TestHostHeaderTransport_NewRequestWithContext is the regression test for the
// bug where http.NewRequestWithContext pre-populates req.Host with URL.Host,
// causing the host header transport's "preserve" check to always trigger and
// silently drop the configured api_host override.
func TestHostHeaderTransport_NewRequestWithContext(t *testing.T) {
	const (
		// Self-hosted setup: a single ELB hostname fronting Envoy
		// hostname-based routing. The base_http_url points at the bare
		// ELB; api_host carries the value we want sent as Host.
		elbHost  = "elb.example.com"
		apiHost  = "api.elb.example.com"
		baseURL  = "http://" + elbHost
		otherURL = "https://sis.elb.example.com/v1/accounts/x/clusters"
	)

	tests := []struct {
		name         string
		buildReq     func() *http.Request
		expectedHost string
		desc         string
	}{
		{
			name: "default request to api host gets api_host override",
			buildReq: func() *http.Request {
				req, err := http.NewRequestWithContext(context.Background(), "POST", baseURL+"/v2/nvcf/functions", nil)
				if err != nil {
					t.Fatalf("NewRequestWithContext failed: %v", err)
				}
				return req
			},
			expectedHost: apiHost,
			desc:         "regression: NewRequestWithContext sets req.Host=URL.Host, transport must still apply api_host override",
		},
		{
			name: "explicit per-request override (e.g. invoke_host) is preserved",
			buildReq: func() *http.Request {
				req, err := http.NewRequestWithContext(context.Background(), "POST", baseURL+"/echo", nil)
				if err != nil {
					t.Fatalf("NewRequestWithContext failed: %v", err)
				}
				req.Host = "invocation.elb.example.com"
				return req
			},
			expectedHost: "invocation.elb.example.com",
			desc:         "InvokeFunctionWithOptions sets req.Host = invoke_host; transport must not clobber it",
		},
		{
			name: "request to a different host (SIS) is not touched",
			buildReq: func() *http.Request {
				req, err := http.NewRequestWithContext(context.Background(), "POST", otherURL, nil)
				if err != nil {
					t.Fatalf("NewRequestWithContext failed: %v", err)
				}
				return req
			},
			expectedHost: "sis.elb.example.com",
			desc:         "SIS calls go to a different host; api_host must not be applied",
		},
		{
			name: "request to api host with port matches when configured with port",
			buildReq: func() *http.Request {
				req, err := http.NewRequestWithContext(context.Background(), "GET", "http://"+elbHost+":8080/v2/nvcf/functions", nil)
				if err != nil {
					t.Fatalf("NewRequestWithContext failed: %v", err)
				}
				return req
			},
			// apiURLHost is "elb.example.com" (no port) so a request to
			// elb.example.com:8080 has a different URL.Host and should be
			// left alone. This guards against accidental partial matches.
			expectedHost: elbHost + ":8080",
			desc:         "transport matches URL.Host exactly, including port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockBase := &hostCapturingRoundTripper{}
			transport := newHostHeaderTransport(elbHost, apiHost, false, mockBase)

			req := tt.buildReq()
			if _, err := transport.RoundTrip(req); err != nil {
				t.Fatalf("RoundTrip failed: %v", err)
			}

			if mockBase.capturedHost != tt.expectedHost {
				t.Errorf("%s\nExpected Host: %q\nGot Host:      %q", tt.desc, tt.expectedHost, mockBase.capturedHost)
			}
		})
	}
}

func TestHostHeaderTransport_PortInAPIHost(t *testing.T) {
	// When base_http_url includes a port, apiURLHost must include the port too
	// (Go's url.Parse keeps "host:port" in URL.Host). Verify the transport
	// still rewrites Host correctly.
	mockBase := &hostCapturingRoundTripper{}
	transport := newHostHeaderTransport("elb.example.com:8080", "api.elb.example.com:8443", false, mockBase)

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://elb.example.com:8080/v2/nvcf/functions", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	if mockBase.capturedHost != "api.elb.example.com:8443" {
		t.Errorf("Expected Host: %q, got: %q", "api.elb.example.com:8443", mockBase.capturedHost)
	}
}

func TestHostHeaderTransport_EmptyAPIURLHostIsNoOp(t *testing.T) {
	// If we somehow end up with an empty apiURLHost (e.g. base_http_url
	// failed to parse), the transport must not rewrite anything; it should
	// behave as a passthrough.
	mockBase := &hostCapturingRoundTripper{}
	transport := newHostHeaderTransport("", "api.elb.example.com", false, mockBase)

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://elb.example.com/v2/nvcf/functions", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	if mockBase.capturedHost != "elb.example.com" {
		t.Errorf("Expected passthrough Host: %q, got: %q", "elb.example.com", mockBase.capturedHost)
	}
}

func TestHostHeaderTransportPassthrough(t *testing.T) {
	t.Run("nil base defaults to http.DefaultTransport", func(t *testing.T) {
		transport := newHostHeaderTransport("elb.example.com", "api.elb.example.com", false, nil)
		hht, ok := transport.(*hostHeaderTransport)
		if !ok {
			t.Fatal("Expected *hostHeaderTransport type")
		}
		if hht.base == nil {
			t.Error("Expected base transport to be set to DefaultTransport, got nil")
		}
	})
}

func TestHostHeaderTransportChaining(t *testing.T) {
	t.Run("Works in transport chain with auth transport", func(t *testing.T) {
		type chainCapture struct {
			capturedHost string
			capturedAuth string
		}
		capture := &chainCapture{}

		mockBase := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			capture.capturedHost = req.Host
			capture.capturedAuth = req.Header.Get("Authorization")
			return &http.Response{
				StatusCode: 200,
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})

		// Build transport chain: Host Header -> Bearer Token -> Mock Base
		authTransport := &BearerTokenTransport{
			Token: "test-token",
			Base:  mockBase,
		}
		hostTransport := newHostHeaderTransport("elb.example.com", "api.elb.example.com", false, authTransport)

		req, err := http.NewRequestWithContext(context.Background(), "GET", "https://elb.example.com/v2/nvcf/functions", nil)
		if err != nil {
			t.Fatalf("NewRequestWithContext failed: %v", err)
		}

		if _, err := hostTransport.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip failed: %v", err)
		}

		if capture.capturedHost != "api.elb.example.com" {
			t.Errorf("Expected Host: %q, got: %q", "api.elb.example.com", capture.capturedHost)
		}
		if capture.capturedAuth != "Bearer test-token" {
			t.Errorf("Expected Authorization: %q, got: %q", "Bearer test-token", capture.capturedAuth)
		}
	})
}

// roundTripFunc is a helper type for creating simple mock transports
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
