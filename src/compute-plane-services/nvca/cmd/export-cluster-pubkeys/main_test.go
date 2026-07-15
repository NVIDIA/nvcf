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

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// mockStreamer implements the streamer interface for testing
type mockStreamer struct {
	responses map[string]string
	errors    map[string]error
	delays    map[string]time.Duration
}

func (m *mockStreamer) streamGetURI(ctx context.Context, u *url.URL) (io.ReadCloser, error) {
	if err, ok := m.errors[u.Path]; ok {
		return nil, err
	}
	if delay, ok := m.delays[u.Path]; ok {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if resp, ok := m.responses[u.Path]; ok {
		return io.NopCloser(bytes.NewReader([]byte(resp))), nil
	}
	return nil, &url.Error{Op: "Get", URL: u.String(), Err: os.ErrNotExist}
}

func TestJWKSToPEMStrs(t *testing.T) {
	tests := []struct {
		name          string
		jwks          jose.JSONWebKeySet
		expectedError bool
	}{
		{
			name: "valid public key",
			jwks: jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{
					{
						Key:       generateTestKey(t),
						KeyID:     "test-key-1",
						Algorithm: "RS256",
						Use:       "sig",
					},
				},
			},
			expectedError: false,
		},
		{
			name: "empty keys",
			jwks: jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{},
			},
			expectedError: false,
		},
		{
			name:          "invalid JSON",
			jwks:          jose.JSONWebKeySet{},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input io.Reader
			if tt.name == "invalid JSON" {
				input = bytes.NewReader([]byte("invalid json"))
			} else {
				jwksJSON, err := json.Marshal(tt.jwks)
				require.NoError(t, err)
				input = bytes.NewReader(jwksJSON)
			}

			pemStrs, err := jwksToPEMStrs(input)
			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			if tt.name != "empty keys" {
				assert.NotEmpty(t, pemStrs)
				assert.Contains(t, pemStrs[0], "BEGIN PUBLIC KEY")
			} else {
				assert.Empty(t, pemStrs)
			}
		})
	}
}

func TestRunWithMockStreamer(t *testing.T) {
	tests := []struct {
		name           string
		oidcConfig     string
		jwksResponse   string
		opts           options
		expectedError  bool
		expectedOutput string
		errors         map[string]error
		delays         map[string]time.Duration
	}{
		{
			name: "successful JSON output",
			oidcConfig: `{
				"issuer": "https://test-issuer",
				"id_token_signing_alg_values_supported": ["RS256"],
				"jwks_uri": "/keys"
			}`,
			jwksResponse: `{
				"keys": [{
					"kty": "RSA",
					"kid": "test-key",
					"use": "sig",
					"alg": "RS256",
					"n": "test-n",
					"e": "AQAB"
				}]
			}`,
			opts: options{
				outputFormat: "json",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			expectedError: false,
		},
		{
			name: "successful YAML output",
			oidcConfig: `{
				"issuer": "https://test-issuer",
				"id_token_signing_alg_values_supported": ["RS256"],
				"jwks_uri": "/keys"
			}`,
			jwksResponse: `{
				"keys": [{
					"kty": "RSA",
					"kid": "test-key",
					"use": "sig",
					"alg": "RS256",
					"n": "test-n",
					"e": "AQAB"
				}]
			}`,
			opts: options{
				outputFormat: "yaml",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			expectedError: false,
		},
		{
			name: "invalid output format",
			opts: options{
				outputFormat: "invalid",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			expectedError: true,
		},
		{
			name: "timeout on JWKS fetch",
			oidcConfig: `{
				"issuer": "https://test-issuer",
				"id_token_signing_alg_values_supported": ["RS256"],
				"jwks_uri": "/keys"
			}`,
			opts: options{
				outputFormat: "json",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			delays: map[string]time.Duration{
				"/keys": 2 * time.Second,
			},
			expectedError: true,
		},
		{
			name: "connection refused on JWKS fetch",
			oidcConfig: `{
				"issuer": "https://test-issuer",
				"id_token_signing_alg_values_supported": ["RS256"],
				"jwks_uri": "/keys"
			}`,
			opts: options{
				outputFormat: "json",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			errors: map[string]error{
				"/keys": syscall.ECONNREFUSED,
			},
			expectedError: true,
		},
		{
			name: "invalid OIDC config",
			oidcConfig: `{
				"invalid": "json"
			}`,
			opts: options{
				outputFormat: "json",
				egressCIDRs:  []string{"10.0.0.0/24"},
				out:          &bytes.Buffer{},
			},
			expectedError: true,
		},
		{
			name: "custom JWKS URI",
			oidcConfig: `{
				"issuer": "https://test-issuer",
				"id_token_signing_alg_values_supported": ["RS256"],
				"jwks_uri": "/original-keys"
			}`,
			jwksResponse: `{
				"keys": [{
					"kty": "RSA",
					"kid": "test-key",
					"use": "sig",
					"alg": "RS256",
					"n": "test-n",
					"e": "AQAB"
				}]
			}`,
			opts: options{
				outputFormat: "json",
				egressCIDRs:  []string{"10.0.0.0/24"},
				jwksURI:      "/custom-keys",
				out:          &bytes.Buffer{},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streamer := &mockStreamer{
				responses: map[string]string{
					"/.well-known/openid-configuration": tt.oidcConfig,
					"/keys":                             tt.jwksResponse,
					"/custom-keys":                      tt.jwksResponse,
				},
				errors: tt.errors,
				delays: tt.delays,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			err := run(ctx, streamer, tt.opts, "https://test-server:6443")
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if buf, ok := tt.opts.out.(*bytes.Buffer); ok {
					assert.NotEmpty(t, buf.String())
				}
			}
		})
	}
}

func TestK8sStreamer(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("test response"))
	}))
	defer server.Close()

	// Create a real REST client with custom http.Client and fake API server
	restConfig := &rest.Config{
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &schema.GroupVersion{Group: "", Version: "v1"},
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
		Host:      server.URL,
		APIPath:   "/api",
		Transport: server.Client().Transport,
	}
	restClient, err := rest.RESTClientFor(restConfig)
	require.NoError(t, err)

	streamer := &k8sStreamer{
		k8sRESTClient: restClient,
		k8sHTTPClient: server.Client(),
	}

	// Test absolute URL
	absURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	rc, err := streamer.streamGetURI(context.Background(), absURL)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "test response", string(data))

	// Test relative URL
	relURL, err := url.Parse("/test")
	require.NoError(t, err)
	rc, err = streamer.streamGetURI(context.Background(), relURL)
	assert.NoError(t, err)
	data, err = io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "test response", string(data))
}

// Helper function to generate a test key
func generateTestKey(t *testing.T) interface{} {
	// Create a test RSA key
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key.Public()
}
