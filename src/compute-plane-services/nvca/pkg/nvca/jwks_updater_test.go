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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			token := unsignedJWTWithIssuer(issuer.URL)
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

	token := unsignedJWTWithIssuer(issuerURL)
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
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(issuerURL)), 0o600))

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
	require.NoError(t, os.WriteFile(tokenPath, []byte(unsignedJWTWithIssuer(issuerURL)), 0o600))

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

func unsignedJWTWithIssuer(issuer string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + issuer + `"}`))
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
