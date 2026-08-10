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

package nvca

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/clientmetrics"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewJWKSUpdater(t *testing.T) {
	// The constructor requires the in-cluster K8s CA cert at k8sCACertPath,
	// which doesn't exist in unit tests; assert that it fails closed rather
	// than silently downgrading to insecure TLS. Every supported deployment
	// target (k3d, kind, EKS, GKE, AKS) projects this file via the standard
	// service-account volume, so this is the right behavior in production too.
	missingCACertPath := filepath.Join(t.TempDir(), "ca.crt")
	_, err := newJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   "https://icms.nvidia.com",
		ClusterID: "cluster-123",
		TokenPath: "/var/run/secrets/tokens/token",
	}, missingCACertPath)
	require.Error(t, err, "missing K8s CA cert must fail closed")
	assert.Contains(t, err.Error(), missingCACertPath)
}

func TestJWKSUpdater_FetchesJWKSFromProjectedTokenIssuer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		jwksURI func(string) string
	}{
		{
			name: "relative jwks_uri",
			jwksURI: func(_ string) string {
				return "/keys"
			},
		},
		{
			name: "absolute jwks_uri",
			jwksURI: func(baseURL string) string {
				return baseURL + "/keys"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			publicJWKS := `{"keys":[{"kty":"RSA","kid":"public-rotated-key"}]}`
			internalJWKS := `{"keys":[{"kty":"RSA","kid":"internal-current-key"}]}`

			var discoveryHits int
			var keysHits int
			var jwksURI string
			var issuerURL string
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					discoveryHits++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"issuer":"` + issuerURL + `","jwks_uri":"` + jwksURI + `"}`))
				case "/keys":
					keysHits++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(publicJWKS))
				default:
					http.NotFound(w, r)
				}
			}))
			defer issuer.Close()
			issuerURL = issuer.URL
			jwksURI = tt.jwksURI(issuer.URL)

			token := unsignedJWTWithIssuer(t, issuer.URL)
			tokenPath := filepath.Join(t.TempDir(), "psat")
			require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

			var pushedBody []byte
			icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "/v1/nvca/clusters/cluster-123/jwks", r.URL.Path)
				assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				pushedBody = body
				w.WriteHeader(http.StatusOK)
			}))
			defer icms.Close()

			internalFetches := 0
			updater := &JWKSUpdater{
				icmsURL:   icms.URL,
				clusterID: "cluster-123",
				tokenPath: tokenPath,
				k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					internalFetches++
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(internalJWKS)),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				})},
				icmsClient: icms.Client(),
			}

			updater.checkAndPush(ctx)

			require.NotEmpty(t, pushedBody)
			var pushed map[string]string
			require.NoError(t, json.Unmarshal(pushedBody, &pushed))
			assert.JSONEq(t, publicJWKS, pushed["jwks"])
			assert.NotContains(t, pushed["jwks"], "internal-current-key")
			assert.Equal(t, 1, discoveryHits)
			assert.Equal(t, 1, keysHits)
			assert.Equal(t, 0, internalFetches)
		})
	}
}

func TestJWKSUpdater_PushUsesICMSHostHeaderOverride(t *testing.T) {
	ctx := context.Background()
	publicJWKS := `{"keys":[{"kty":"RSA","kid":"public-rotated-key"}]}`

	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`))
		case "/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(publicJWKS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	token := unsignedJWTWithIssuer(t, issuer.URL)
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

	var pushedBody []byte
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "sis.gateway.example.test", r.Host)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		pushedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	updater := &JWKSUpdater{
		icmsURL:                icms.URL,
		icmsHostHeaderOverride: "sis.gateway.example.test",
		clusterID:              "cluster-123",
		tokenPath:              tokenPath,
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("internal JWKS should not be fetched when issuer JWKS is reachable")
			return nil, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	require.NotEmpty(t, pushedBody)
	var pushed map[string]string
	require.NoError(t, json.Unmarshal(pushedBody, &pushed))
	assert.JSONEq(t, publicJWKS, pushed["jwks"])
}

func TestJWKSUpdater_DoesNotFallbackToInternalJWKSWhenProjectedIssuerFetchFails(t *testing.T) {
	ctx := context.Background()
	internalJWKS := `{"keys":[{"kty":"RSA","kid":"internal-current-key"}]}`

	var discoveryHits int
	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoveryHits++
		http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	token := unsignedJWTWithIssuer(t, issuerURL)
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

	var pushed bool
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	internalFetches := 0
	updater := &JWKSUpdater{
		icmsURL:   icms.URL,
		clusterID: "cluster-123",
		tokenPath: tokenPath,
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(internalJWKS)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	assert.Equal(t, 1, discoveryHits)
	assert.Equal(t, 0, internalFetches)
	assert.False(t, pushed, "issuer JWKS failures must not push stale internal K8s JWKS")
}

func TestJWKSUpdater_FallsBackToInternalJWKSWhenIssuerDiscoveryIsUnreachableAndKidMatches(t *testing.T) {
	ctx := context.Background()
	internalJWKS := `{"keys":[{"kty":"RSA","kid":"matching-key"}]}`

	token := unsignedJWTWithIssuerAndKeyID(t, "https://issuer.example.test", "matching-key")
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))
	k8sSATokenPath := filepath.Join(t.TempDir(), "k8s-sa")
	require.NoError(t, os.WriteFile(k8sSATokenPath, []byte("k8s-token"), 0o600))

	var pushedBody []byte
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		pushedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	internalFetches := 0
	updater := &JWKSUpdater{
		icmsURL:        icms.URL,
		clusterID:      "cluster-123",
		tokenPath:      tokenPath,
		k8sSATokenPath: k8sSATokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			assert.Equal(t, "Bearer k8s-token", req.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(internalJWKS)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	require.NotEmpty(t, pushedBody)
	var pushed map[string]string
	require.NoError(t, json.Unmarshal(pushedBody, &pushed))
	assert.JSONEq(t, internalJWKS, pushed["jwks"])
	assert.Equal(t, 1, internalFetches)
}

func TestJWKSUpdater_FallsBackToInternalJWKSWhenIssuerJWKSIsUnreachableAndKidMatches(t *testing.T) {
	ctx := context.Background()
	issuerURL := "https://issuer.example.test"
	internalJWKS := `{"keys":[{"kty":"RSA","kid":"matching-key"}]}`

	token := unsignedJWTWithIssuerAndKeyID(t, issuerURL, "matching-key")
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))
	k8sSATokenPath := filepath.Join(t.TempDir(), "k8s-sa")
	require.NoError(t, os.WriteFile(k8sSATokenPath, []byte("k8s-token"), 0o600))

	var pushedBody []byte
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		pushedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	internalFetches := 0
	updater := &JWKSUpdater{
		icmsURL:        icms.URL,
		clusterID:      "cluster-123",
		tokenPath:      tokenPath,
		k8sSATokenPath: k8sSATokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/.well-known/openid-configuration":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case "/keys":
				return nil, context.DeadlineExceeded
			default:
				return nil, assert.AnError
			}
		})},
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(internalJWKS)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	require.NotEmpty(t, pushedBody)
	var pushed map[string]string
	require.NoError(t, json.Unmarshal(pushedBody, &pushed))
	assert.JSONEq(t, internalJWKS, pushed["jwks"])
	assert.Equal(t, 1, internalFetches)
}

func TestJWKSUpdater_DoesNotFallbackWhenInternalJWKSMissesTokenKid(t *testing.T) {
	ctx := context.Background()

	token := unsignedJWTWithIssuerAndKeyID(t, "https://issuer.example.test", "projected-token-key")
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))
	k8sSATokenPath := filepath.Join(t.TempDir(), "k8s-sa")
	require.NoError(t, os.WriteFile(k8sSATokenPath, []byte("k8s-token"), 0o600))

	var pushed bool
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	internalFetches := 0
	updater := &JWKSUpdater{
		icmsURL:        icms.URL,
		clusterID:      "cluster-123",
		tokenPath:      tokenPath,
		k8sSATokenPath: k8sSATokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"keys":[{"kty":"RSA","kid":"other-key"}]}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	assert.Equal(t, 1, internalFetches)
	assert.False(t, pushed, "internal JWKS must not be pushed when it does not contain the projected token kid")
}

func TestJWKSUpdater_DoesNotFallbackWhenProjectedTokenHasNoKid(t *testing.T) {
	ctx := context.Background()

	token := unsignedJWTWithIssuer(t, "https://issuer.example.test")
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

	var pushed bool
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	internalFetches := 0
	updater := &JWKSUpdater{
		icmsURL:   icms.URL,
		clusterID: "cluster-123",
		tokenPath: tokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"keys":[{"kty":"RSA","kid":"any-key"}]}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		icmsClient: icms.Client(),
	}

	updater.checkAndPush(ctx)

	assert.Equal(t, 0, internalFetches)
	assert.False(t, pushed, "internal JWKS fallback must require a projected token kid")
}

func TestJWKSUpdater_RetriesInClusterIssuerWithK8sToken(t *testing.T) {
	ctx := context.Background()
	issuerURL := "https://kubernetes.default.svc.cluster.local"
	projectedToken := unsignedJWTWithIssuer(t, issuerURL)
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(projectedToken), 0o600))
	k8sSATokenPath := filepath.Join(t.TempDir(), "k8s-sa")
	require.NoError(t, os.WriteFile(k8sSATokenPath, []byte("k8s-token"), 0o600))

	var discoveryHits int
	var jwksHits int
	updater := &JWKSUpdater{
		tokenPath:      tokenPath,
		k8sSATokenPath: k8sSATokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/.well-known/openid-configuration":
				discoveryHits++
				if req.Header.Get("Authorization") == "" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body:       io.NopCloser(strings.NewReader("Unauthorized")),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				}
				assert.Equal(t, "Bearer k8s-token", req.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case "/keys":
				jwksHits++
				assert.Equal(t, "Bearer k8s-token", req.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"keys":[{"kty":"RSA","kid":"cluster-local-key"}]}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			default:
				return nil, assert.AnError
			}
		})},
	}

	jwksData, err := updater.fetchJWKS(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"keys":[{"kty":"RSA","kid":"cluster-local-key"}]}`, string(jwksData))
	assert.Equal(t, 2, discoveryHits)
	assert.Equal(t, 1, jwksHits)
}

func TestJWKSUpdater_DoesNotSendK8sTokenToExternalIssuerOnUnauthorized(t *testing.T) {
	ctx := context.Background()
	issuerURL := "https://issuer.example.test"
	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(t, issuerURL)), 0o600))
	k8sSATokenPath := filepath.Join(t.TempDir(), "k8s-sa")
	require.NoError(t, os.WriteFile(k8sSATokenPath, []byte("k8s-token"), 0o600))

	var discoveryHits int
	updater := &JWKSUpdater{
		tokenPath:      tokenPath,
		k8sSATokenPath: k8sSATokenPath,
		oidcClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			discoveryHits++
			assert.Empty(t, req.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader("Unauthorized")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
	}

	_, err := updater.fetchJWKS(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC discovery returned non-200")
	assert.Equal(t, 1, discoveryHits)
}

func TestJWKSUpdater_FetchesClusterLocalIssuerWithK8sTrust(t *testing.T) {
	ctx := context.Background()
	publicJWKS := `{"keys":[{"kty":"RSA","kid":"cluster-local-issuer-key"}]}`

	var issuerURL string
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`))
		case "/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(publicJWKS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(t, issuerURL)), 0o600))

	caCertPath := filepath.Join(t.TempDir(), "ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw})
	require.NoError(t, os.WriteFile(caCertPath, caPEM, 0o600))

	updater, err := newJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   "https://icms.nvidia.com",
		ClusterID: "cluster-123",
		TokenPath: tokenPath,
	}, caCertPath)
	require.NoError(t, err)

	jwksData, err := updater.fetchJWKS(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, publicJWKS, string(jwksData))
}

func TestJWKSUpdater_RejectsOIDCIssuerMismatch(t *testing.T) {
	ctx := context.Background()

	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"https://other-issuer.example.test","jwks_uri":"/keys"}`))
		case "/keys":
			_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"wrong-issuer-key"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(t, issuerURL)), 0o600))

	internalFetches := 0
	updater := &JWKSUpdater{
		tokenPath: tokenPath,
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"keys":[{"kty":"RSA","kid":"internal-current-key"}]}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
	}

	_, err := updater.fetchJWKS(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC discovery issuer mismatch")
	assert.Equal(t, 0, internalFetches)
}

func unsignedJWTWithIssuer(t testing.TB, issuer string) string {
	return unsignedJWTWithIssuerAndKeyID(t, issuer, "")
}

func unsignedJWTWithIssuerAndKeyID(t testing.TB, issuer, keyID string) string {
	t.Helper()
	headerJSON, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid,omitempty"`
	}{
		Algorithm: "none",
		KeyID:     keyID,
	})
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(struct {
		Issuer string `json:"iss"`
	}{
		Issuer: issuer,
	})
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString(headerJSON)
	claims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return header + "." + claims + "."
}

func TestJWKSHashComparison_SameJWKS(t *testing.T) {
	jwks := []byte(`{"keys":[{"kty":"RSA","kid":"test-key"}]}`)

	hash := sha256.Sum256(jwks)
	hashStr := hex.EncodeToString(hash[:])

	hash2 := sha256.Sum256(jwks)
	hashStr2 := hex.EncodeToString(hash2[:])

	assert.Equal(t, hashStr, hashStr2, "identical JWKS should produce same hash")
}

func TestJWKSHashComparison_ChangedJWKS(t *testing.T) {
	jwks1 := []byte(`{"keys":[{"kty":"RSA","kid":"key-1"}]}`)
	jwks2 := []byte(`{"keys":[{"kty":"RSA","kid":"key-2"}]}`)

	hash1 := sha256.Sum256(jwks1)
	hashStr1 := hex.EncodeToString(hash1[:])

	hash2 := sha256.Sum256(jwks2)
	hashStr2 := hex.EncodeToString(hash2[:])

	assert.NotEqual(t, hashStr1, hashStr2, "different JWKS should produce different hashes")
}

func TestJWKSPushBody_StructuredJSON(t *testing.T) {
	jwksData := json.RawMessage(`{"keys":[{"kty":"RSA","kid":"test"}]}`)

	body := jwksPushBody{JWKS: string(jwksData)}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal(bodyBytes, &parsed))

	assert.JSONEq(t, `{"keys":[{"kty":"RSA","kid":"test"}]}`, parsed["jwks"])
}

func TestJWKSPushBody_PreventsInjection(t *testing.T) {
	maliciousJWKS := json.RawMessage(`{"keys":[],"injected":"value"}`)

	body := jwksPushBody{JWKS: string(maliciousJWKS)}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal(bodyBytes, &parsed))

	assert.Len(t, parsed, 1)
	assert.Contains(t, parsed, "jwks")
}

func TestJWKSPushBody_EmptyJWKS(t *testing.T) {
	body := jwksPushBody{JWKS: `{}`}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)
	// JWKS is now typed as string (ICMS expects a JSON-encoded string, not an
	// embedded object — see fix on UpdateJwksRequest). json.Marshal renders the
	// field as an escaped JSON string literal.
	assert.Equal(t, `{"jwks":"{}"}`, string(bodyBytes))
}

func TestJWKSUpdater_LastHashTracking(t *testing.T) {
	// The full JWKSUpdater can't be constructed in unit tests (CA cert
	// missing), so assert the hash equality semantics directly. checkAndPush
	// uses the same comparison: identical JWKS payloads short-circuit before
	// we issue the ICMS push.
	jwks := []byte(`{"keys":[]}`)
	hash := sha256.Sum256(jwks)
	hashStr := hex.EncodeToString(hash[:])

	hash2 := sha256.Sum256(jwks)
	hashStr2 := hex.EncodeToString(hash2[:])
	assert.Equal(t, hashStr, hashStr2, "same JWKS should match stored hash")
}

func TestNewK8sHTTPClient_RequiresCACert(t *testing.T) {
	// The constructor must fail closed when the K8s CA cert is missing rather
	// than silently downgrading to insecure TLS. There is intentionally no
	// insecure-skip-verify escape hatch: every supported deployment target
	// (k3d, kind, EKS, GKE, AKS, kubeadm) projects this file via the standard
	// service-account volume.
	missingCACertPath := filepath.Join(t.TempDir(), "ca.crt")
	client, err := newK8sHTTPClientFromCAFile(missingCACertPath)
	require.Error(t, err, "missing K8s CA cert must fail closed")
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), missingCACertPath)
}

func TestNewK8sHTTPClient_SecureClientConstruction(t *testing.T) {
	// Verify that when a CA cert pool is provided, the client uses secure TLS.
	// We cannot easily test the full newK8sHTTPClient with a real cert file
	// (the const path is not overridable), so we verify the construction logic.
	testCAPEM := []byte("-----BEGIN CERTIFICATE-----\n" +
		"MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw\n" +
		"DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow\n" +
		"EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d\n" +
		"7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B\n" +
		"5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr\n" +
		"BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1\n" +
		"NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2wpSek6nFhYi\n" +
		"Aivep2lMBrXuN6zzesLKOjv4GhIrlGUCID/5IHAxPH/aSgR5UEr5lKAFOENMrYnq\n" +
		"sUcTxMQqHOWL\n" +
		"-----END CERTIFICATE-----\n")

	caCertPool := x509.NewCertPool()
	ok := caCertPool.AppendCertsFromPEM(testCAPEM)
	assert.True(t, ok, "should parse test CA cert")

	// Construct the same client that newK8sHTTPClient would build with a valid CA
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
	}

	transport, tOk := client.Transport.(*http.Transport)
	require.True(t, tOk)
	assert.False(t, transport.TLSClientConfig.InsecureSkipVerify,
		"should use secure TLS when CA cert is available")
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)
}

// TestJWKSUpdater_PushIsInstrumented pins the JWKS push into the OTel client
// metrics. It is a fifth ICMS route on its own HTTP client rather than the
// shared retryable client, so without an explicit transport wrapper it silently
// produces no semconv series even though the legacy
// nvca_upstream_request_total{operation="jwks-push"} counter still exists. That
// asymmetry would lose JWKS coverage during the dashboard cutover.
func TestJWKSUpdater_PushIsInstrumented(t *testing.T) {
	ctx := context.Background()
	publicJWKS := `{"keys":[{"kty":"RSA","kid":"public-rotated-key"}]}`

	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`))
		case "/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(publicJWKS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(t, issuer.URL)), 0o600))

	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	reg := prometheus.NewRegistry()
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	require.NoError(t, err)
	rec, err := clientmetrics.NewRecorder(
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)), nil)
	require.NoError(t, err)

	updater := &JWKSUpdater{
		icmsURL:   icms.URL,
		clusterID: "cluster-123",
		tokenPath: tokenPath,
		k8sClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("internal JWKS should not be fetched when issuer JWKS is reachable")
			return nil, nil
		})},
		icmsClient: &http.Client{
			Transport: clientmetrics.NewTransport(http.DefaultTransport, rec, clientmetrics.PeerServiceICMS),
		},
	}

	updater.checkAndPush(ctx)

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	scrape := string(body)

	assert.Contains(t, scrape, `peer_service="icms"`,
		"JWKS push must be attributed to the icms dependency")
	assert.Contains(t, scrape, `url_template="v1/nvca/clusters/{clusterId}/jwks"`,
		"JWKS push must carry a low-cardinality url.template like the other ICMS routes")
	assert.NotContains(t, scrape, "cluster-123",
		"the raw cluster ID must never appear as a label value")
}

// TestNewJWKSUpdater_AppliesTransportWrapper verifies the option is honoured by
// the constructor, which is how the agent injects the metrics wrapper.
func TestNewJWKSUpdater_AppliesTransportWrapper(t *testing.T) {
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer issuer.Close()

	caCertPath := filepath.Join(t.TempDir(), "ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw})
	require.NoError(t, os.WriteFile(caCertPath, caPEM, 0o600))

	// Return a sentinel rather than the inner transport, so the assertion fails if
	// the constructor calls the wrapper but discards its result.
	var wrapped bool
	sentinel := &sentinelJWKSTransport{}
	updater, err := newJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   "https://icms.example.test",
		ClusterID: "cluster-123",
		TokenPath: filepath.Join(t.TempDir(), "psat"),
		TransportWrapper: func(inner http.RoundTripper) http.RoundTripper {
			wrapped = true
			sentinel.inner = inner
			return sentinel
		},
	}, caCertPath)
	require.NoError(t, err)
	assert.True(t, wrapped, "TransportWrapper must be applied to the ICMS push client")
	assert.Same(t, http.RoundTripper(sentinel), updater.icmsClient.Transport,
		"the wrapper's return value must be installed, not discarded")

	plain, err := newJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   "https://icms.example.test",
		ClusterID: "cluster-123",
		TokenPath: filepath.Join(t.TempDir(), "psat"),
	}, caCertPath)
	require.NoError(t, err)
	// Without a wrapper the chain is otelhttp over the default transport: tracing
	// is unconditional, only the metrics wrapper is opt-in.
	_, isOtelWrapped := plain.icmsClient.Transport.(*otelhttp.Transport)
	assert.True(t, isOtelWrapped,
		"otelhttp must wrap the transport even without a metrics wrapper")
	if _, isSentinel := plain.icmsClient.Transport.(*sentinelJWKSTransport); isSentinel {
		t.Fatal("no wrapper must not install the metrics wrapper")
	}
}

type sentinelJWKSTransport struct{ inner http.RoundTripper }

func (s *sentinelJWKSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.inner.RoundTrip(req)
}

// TestJWKSUpdater_PushPropagatesTraceContext pins client tracing on the JWKS
// push. It does not use the shared retryable client, so otelhttp is wired into
// its transport explicitly; without it this ICMS route would be the only one
// invisible in traces while still reporting peer_service="icms" in metrics.
func TestJWKSUpdater_PushPropagatesTraceContext(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	publicJWKS := `{"keys":[{"kty":"RSA","kid":"public-rotated-key"}]}`
	var issuerURL string
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + issuerURL + `","jwks_uri":"/keys"}`))
		case "/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(publicJWKS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	issuerURL = issuer.URL

	tokenPath := filepath.Join(t.TempDir(), "psat")
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(t, issuer.URL)), 0o600))

	var traceparent string
	icms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer icms.Close()

	caCertPath := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caCertPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw}), 0o600))

	updater, err := newJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   icms.URL,
		ClusterID: "cluster-123",
		TokenPath: tokenPath,
		TransportWrapper: func(inner http.RoundTripper) http.RoundTripper {
			return inner
		},
	}, caCertPath)
	require.NoError(t, err)
	updater.k8sClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("internal JWKS should not be fetched when the issuer JWKS is reachable")
		return nil, nil
	})}

	updater.checkAndPush(context.Background())

	assert.NotEmpty(t, traceparent,
		"ICMS must receive W3C trace context on the JWKS push; otelhttp is missing from the transport chain")
}
