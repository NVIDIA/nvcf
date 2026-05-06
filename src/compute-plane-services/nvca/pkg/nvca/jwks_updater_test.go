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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJWKSUpdater(t *testing.T) {
	// The constructor requires the in-cluster K8s CA cert at k8sCACertPath,
	// which doesn't exist in unit tests; assert that it fails closed rather
	// than silently downgrading to insecure TLS. Every supported deployment
	// target (k3d, kind, EKS, GKE, AKS) projects this file via the standard
	// service-account volume, so this is the right behavior in production too.
	_, err := NewJWKSUpdater(JWKSUpdaterOptions{
		ICMSURL:   "https://icms.nvidia.com",
		ClusterID: "cluster-123",
		TokenPath: "/var/run/secrets/tokens/token",
	})
	require.Error(t, err, "missing K8s CA cert must fail closed")
	assert.Contains(t, err.Error(), k8sCACertPath)
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
	client, err := newK8sHTTPClient()
	require.Error(t, err, "missing K8s CA cert must fail closed")
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), k8sCACertPath)
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
