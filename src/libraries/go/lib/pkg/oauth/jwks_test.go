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

/*
 * SECURITY NOTICE: Test Keys Only
 *
 * This test file contains RSA private keys that are DUMMY KEYS for testing purposes only.
 * These keys were generated using openssl and are NOT secure for any production use.
 * They are safe to commit to public repositories.
 *
 * DO NOT use these keys in production environments.
 */

package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWKS(t *testing.T) {
	// WARNING: The RSA keys in this test file are DUMMY KEYS for testing purposes only.
	// These keys are NOT secure and should NEVER be used in production.
	// They are safe to commit to public repositories.
	//
	// Generated with:
	// openssl genrsa -out /dev/stdout 2048 >_output/priv.key
	// openssl rsa -in _output/priv.key -pubout >_output/pub.key
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)

	pubKeyBytes := []byte(`-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAvnDKuvQljyjqb8PFlBJ3
B+p8SiBGW6RC5HRu7lpcnP+OHNljxkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8ww
Cfcy3/F9Dtl2knzGihF206hif6nxcTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy3
3NrBUm1UphudsfoIPBsmzi6QJvtzwWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnY
d+Kvqdwxf1yEYAv79V7Nn+6XHSseXENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lM
MGQl+J0d2Ttf0Y8A0MHHNaObChRwZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0
rQIDAQAB
-----END PUBLIC KEY-----
`)

	privKey, err := parsePrivateKey(privKeyBytes)
	assert.Nil(t, err)

	jwksServerHandler := NewJWKSHandler([]*rsa.PublicKey{
		&privKey.PublicKey,
	})

	jwksServer := httptest.NewServer(jwksServerHandler)
	defer jwksServer.Close()

	jc := NewJWKSCache(jwksServer.URL)

	// No refresh called, nothing in the cache
	pub, err := jc.Get("2d2d2d2d2d424547494e")
	assert.NotNil(t, err)
	assert.Nil(t, pub)

	err = jc.Refresh()
	assert.Nil(t, err)

	pub, err = jc.Get("2d2d2d2d2d424547494e")
	assert.Nil(t, err)
	assert.NotNil(t, pub)

	pem, err := PublicKeyToPEM(pub)
	assert.Nil(t, err)
	assert.Equal(t, string(pubKeyBytes), string(pem))

	pub, err = jc.Get("not-exist")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Nil(t, pub)

	jc.URL = "https://not-exist-url"
	err = jc.Refresh()
	assert.NotNil(t, err)
}

func parsePrivateKey(keyBytes []byte) (*rsa.PrivateKey, error) {
	keyDecoded, _ := pem.Decode(keyBytes)
	privKey, err := x509.ParsePKCS1PrivateKey(keyDecoded.Bytes)
	if err != nil {
		return nil, err
	}

	return privKey, nil
}

func TestJWKS_JWKSVerifier_VerifyToken(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "edge-") {
			parts := strings.Split(r.URL.Path, "-")
			b, err := os.ReadFile(fmt.Sprintf("test/edge-%s.jwks.json", parts[len(parts)-1]))
			require.NoError(t, err)
			_, err = w.Write(b)
			require.NoError(t, err)
		} else if strings.Contains(r.URL.Path, "empty") {
			_, err := w.Write([]byte("{\"keys\":[]}"))
			require.NoError(t, err)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()

	token1, err := os.ReadFile("test/edge-1.token")
	require.NoError(t, err)
	token2, err := os.ReadFile("test/edge-2.token")
	require.NoError(t, err)

	// verify token 1 - PASS
	jwksTTL := 1 * time.Hour
	ctx := context.Background()
	jwksVerifier := NewJWKSVerifier(fmt.Sprintf("%s/edge-1", s.URL), WithJWKSVerifierCacheTTL(jwksTTL))
	ok, err := jwksVerifier.VerifyToken(ctx, string(token1))
	assert.NoError(t, err)
	assert.True(t, ok)

	// verify token 2 - FAIL
	ok, err = jwksVerifier.VerifyToken(ctx, string(token2))
	assert.NoError(t, err)
	assert.False(t, ok)

	// verify token 1 - FAIL with empty URL
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/empty", s.URL)
	jwksVerifier.cacheExpiry = time.Time{}
	ok, err = jwksVerifier.VerifyToken(ctx, string(token1))
	assert.NoError(t, err)
	assert.False(t, ok)

	// verify token 2 - PASS
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/edge-2", s.URL)
	jwksVerifier.cacheExpiry = time.Time{}
	ok, err = jwksVerifier.VerifyToken(ctx, string(token2))
	assert.NoError(t, err)
	assert.True(t, ok)

	// verify token 1 passes again
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/edge-1", s.URL)
	jwksVerifier.cacheExpiry = time.Time{}
	ok, err = jwksVerifier.VerifyToken(ctx, string(token1))
	assert.NoError(t, err)
	assert.True(t, ok)

	// verify request fails
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/does-not-exist", s.URL)
	jwksVerifier.cacheExpiry = time.Time{}
	ok, err = jwksVerifier.VerifyToken(ctx, string(token1))
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, time.Time{}, jwksVerifier.cacheExpiry)

	// Verify token 1 passes after re-check
	tNow := time.Time{}.Add(5 * time.Hour)
	jwksVerifier.nowFunc = func() time.Time {
		return tNow
	}
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/edge-1", s.URL)
	ok, err = jwksVerifier.VerifyToken(ctx, string(token1))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, tNow.Add(jwksTTL), jwksVerifier.cacheExpiry)

	// check token 1 with refresh of cache - PASS
	tNow = tNow.Add(jwksTTL).Add(1 * time.Minute)
	ok, err = jwksVerifier.VerifyToken(ctx, string(token1))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, tNow.Add(jwksTTL), jwksVerifier.cacheExpiry)

	// check token 2 with unexpired cache - FAIL
	jwksVerifier.jwksCache.URL = fmt.Sprintf("%s/edge-2", s.URL)
	ok, err = jwksVerifier.VerifyToken(ctx, string(token2))
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, tNow.Add(jwksTTL), jwksVerifier.cacheExpiry)

	// check token 2 with expired cache - PASS
	tNow = tNow.Add(jwksTTL).Add(1 * time.Minute)
	ok, err = jwksVerifier.VerifyToken(ctx, string(token2))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, tNow.Add(jwksTTL), jwksVerifier.cacheExpiry)
}

func TestGetJWKSKeysURL(t *testing.T) {
	url := GetJWKSKeysURL("https://ets.example.com")
	assert.Equal(t, "https://ets.example.com/v1/identity/oidc/.well-known/keys", url)

	url2 := GetJWKSKeysURL("")
	assert.Equal(t, "/v1/identity/oidc/.well-known/keys", url2)
}

func TestJWKSCache_GetOrRefresh_CacheHit(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	handler := NewJWKSHandler([]*rsa.PublicKey{&privKey.PublicKey})
	server := httptest.NewServer(handler)
	defer server.Close()

	jc := NewJWKSCache(server.URL)
	err = jc.Refresh()
	require.NoError(t, err)

	// Key should be in cache - GetOrRefresh should return it without network call
	keys := jc.GetJSONWebKeySet()
	require.Len(t, keys.Keys, 1)
	kid := keys.Keys[0].KeyID

	pub, err := jc.GetOrRefresh(kid)
	assert.NoError(t, err)
	assert.NotNil(t, pub)
}

func TestJWKSCache_GetOrRefresh_CacheMiss_ThenRefresh(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	handler := NewJWKSHandler([]*rsa.PublicKey{&privKey.PublicKey})
	server := httptest.NewServer(handler)
	defer server.Close()

	jc := NewJWKSCache(server.URL)
	// Do NOT call Refresh() first  - cache is empty, so GetOrRefresh must refresh

	// Compute what the kid will be
	jwk, err := NewJWKFromRSAPub(&privKey.PublicKey)
	require.NoError(t, err)
	kid := jwk.KeyID

	// GetOrRefresh should fetch from server since cache is empty
	pub, err := jc.GetOrRefresh(kid)
	assert.NoError(t, err)
	assert.NotNil(t, pub)
}

func TestJWKSCache_GetOrRefresh_RefreshFails(t *testing.T) {
	jc := NewJWKSCache("http://localhost:0/jwks") // unreachable
	pub, err := jc.GetOrRefresh("nonexistent-kid")
	assert.Error(t, err)
	assert.Nil(t, pub)
}

func TestJWKS_JWKSVerifier_ExtractVerifiedToken(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksB, err := os.ReadFile("test/auth-1.jwks.json")
		assert.NoError(t, err)
		_, err = w.Write(jwksB)
		assert.NoError(t, err)
	}))
	defer s.Close()
	jwksVerifier := NewJWKSVerifier(s.URL, WithJWKSVerifierCacheTTL(60*time.Second))
	assert.Equal(t, s.URL, jwksVerifier.JWKSURL())

	jwt1B, err := os.ReadFile("test/auth-1.token")
	require.NoError(t, err)
	jwt1 := string(jwt1B)
	badJWT, err := os.ReadFile("test/edge-1.token")
	require.NoError(t, err)

	ctx := context.Background()
	jwtToken, err := jwksVerifier.ExtractVerifiedToken(ctx, string(jwt1))
	assert.NoError(t, err)
	assert.Equal(t, "service_account", jwtToken.TokenType)
	assert.Equal(t, []string{"oauth-test-client-abc123", "s:test-service-xyz789"}, jwtToken.Audience)
	assert.Equal(t, "oauth-test-client-abc123", jwtToken.AuthorizedParties)
	assert.Equal(t, "https://test.oauth.example.com", jwtToken.Issuer)
	assert.Equal(t, "test-service-xyz789", jwtToken.Service.ID)
	assert.Equal(t, "Fleet Command Platform", jwtToken.Service.Name)
	assert.Equal(t, "011a1807-9b2c-4e9a-a962-da44e92782b1", jwtToken.JWTID)
	assert.Equal(t, []string{"ecm"}, jwtToken.Scopes)
	assert.Equal(t, "oauth-test-client-abc123", jwtToken.Subject)
	assert.Equal(t, int64(1674252566), jwtToken.Expiration)
	assert.Equal(t, time.Unix(jwtToken.Expiration, 0), jwtToken.ExpirationTime())
	assert.Equal(t, int64(1674248966), jwtToken.IssuedAt)
	assert.Equal(t, time.Unix(jwtToken.IssuedAt, 0), jwtToken.IssuedAtTime())

	// Negative test with bad token
	badJwt, err := jwksVerifier.ExtractVerifiedToken(ctx, string(badJWT))
	assert.Error(t, err)
	assert.Empty(t, badJwt)

	// test to ensure we do not receive a race condition
	jwksVerifier = NewJWKSVerifier(s.URL, WithJWKSVerifierCacheTTL(1*time.Millisecond))
	jobQueue := make(chan int, 1000)
	for i := 0; i < cap(jobQueue); i++ {
		jobQueue <- i
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			for range jobQueue {
				_, err := jwksVerifier.ExtractVerifiedToken(ctx, jwt1)
				assert.NoError(t, err)
			}
			wg.Done()
		}()
	}
	close(jobQueue)
	wg.Wait()
}

func TestJWKS_RefreshError(t *testing.T) {
	jc := NewJWKSCache("http://invalid-url-that-does-not-exist-for-testing:99999/jwks.json")
	err := jc.Refresh()
	assert.Error(t, err)
}

func TestJWKS_GetWithoutRefresh(t *testing.T) {
	jc := NewJWKSCache("http://example.com/jwks.json")
	pub, err := jc.Get("some-kid")
	assert.Error(t, err)
	assert.Nil(t, pub)
	// Error message should indicate that cache is nil or uninitialized
	assert.NotEmpty(t, err.Error())
}

func TestNewJWKFromRSAPub_Success(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	jwk, err := NewJWKFromRSAPub(&privKey.PublicKey)
	assert.NoError(t, err)
	assert.NotNil(t, jwk)
	assert.Equal(t, "RS256", jwk.Algorithm)
	assert.Equal(t, "sig", jwk.Use)
	assert.NotEmpty(t, jwk.KeyID)
}

func TestNewJWKSFromRSAPubKeys_MultipleKeys(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	keyset, err := NewJWKSFromRSAPubKeys([]*rsa.PublicKey{&privKey.PublicKey, &privKey.PublicKey})
	assert.NoError(t, err)
	assert.NotNil(t, keyset)
	assert.Len(t, keyset.Keys, 2)
}

func TestNewJWKSHandler_Success(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	handler := NewJWKSHandler([]*rsa.PublicKey{&privKey.PublicKey})
	req := httptest.NewRequest("GET", "/jwks.json", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "keys")
}

func TestPublicKeyToPEM_Success(t *testing.T) {
	privKeyBytes := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAvnDKuvQljyjqb8PFlBJ3B+p8SiBGW6RC5HRu7lpcnP+OHNlj
xkb+zgHM4kvOaVl5gAkQMb1cz+Z4aVNFc8wwCfcy3/F9Dtl2knzGihF206hif6nx
cTxuidydwqA3cat0W1N0kRjwkrJoPBcgMIy33NrBUm1UphudsfoIPBsmzi6QJvtz
wWHGz8F7h/oCelCvmeixOFxELFbwa5ifhnnYd+Kvqdwxf1yEYAv79V7Nn+6XHSse
XENwWPGmvp/bLVG20qMfwV0AcG1vQRK5M2lMMGQl+J0d2Ttf0Y8A0MHHNaObChRw
ZnH5N7kqregr8uSb2R9kRAXsKjLwQbK3J6u0rQIDAQABAoIBAQC0n+g4v74r9UO9
87IfChBpqqZt7ASvgLGNWz2nxn7WzbAtfqaaddXQ8HYyIHJLC3koze/VLWStL0v/
oeJavUzG9vYC31mczvceY0gvxfatM6UQrs/4dbfl/CCJa0qK/nKi+Bm0UTJEAQDK
FakLQzxUNgtsMZQ65DCCkMJkt9/rZyttuk9rKxJkHNbCa7gMVzKZ1nhf3IYZ97U9
d1+0Xjhd9qY61Xx2j8EmwV8RQW498I3Tng56nvfL+jIJXgeqSRVqelvQJFtORJqC
XdZb08BdKa6Ue0pIUisxIBJgWUqGLBz65+AuP6gJAwRJ/PlR0u2ABY/rwMxYHwce
udM82SYFAoGBAPTcdzYgd6hMlVvxsfK6Jz9+01kexXpz5wbrQlEWAG6bYvNqafn7
rlDjquNFpPAvhEX+90YdLjs75X7MrF4y+2Utzutou/F8d7kQFYilm1ploIT7d0XY
9oYgnXf7JULwkI6p7oZsA7XW9+jrsxmiBit69lF6qALbD7qS3w/ydGSXAoGBAMca
kgZPuaBCik4apawkHKzNvPtCkXaDUnCCF9t6ORaVyOYwN5wbmgnL7fP1nljyR52X
2JypZjlr7sVMFWU6vznP5IAEVOKtRVB8YVw4Jefv2MDpEy8WdAqzJESTyHtcEsz0
hlsYe96UarKfBJzDYtWFku5AFkdFua1RN+Fh8wVbAoGAQyer8kZZSukmFX9mJIH1
fa6U3F5aHslm1Tj0iTSVjcBEFSpcQllKZ5jpJ0fUgqMljeTtgGdEZK56tJoBtBwb
YpZ7p4ij8wkF9NV6cm2o+9PfgFlPTvLAOez8Awn4IDHGE7p7VpaNNfPtLg5momMT
eh1RLOuM5Kub1rmtP7xpO6UCgYBtT8QuHOVP/FhMi0q8GNN5eDcyR5jvVSgUxwfs
Is1m/fNflcdiOLE4gbLxxr8aHGJ/PlfZoxORoRVlUuFIQ5mrVt0f/8DO9sxgZPlb
FSSSk1cQiqZSquQo37OgxvZB7AoSZonBR87yI8/0o2N34bnIet5xWdQha0GGy1l/
rzQqkwKBgCtecdjsHACqEs1DWhu197ABjZhvgjxWwKV3aIPOwtZ4ODKFr+d3m4PY
PMqHUnbnlz7qdHdidnEOtZtTqUChW6hZhFjyLOPB3of+3fHWg2BkRAfRrFwoAj+3
pyOJzE9c0NJ+QjieAEk6aHteol8JI+HxP51oaMMVVmfwy9b4B3+w
-----END RSA PRIVATE KEY-----
`)
	privKey, err := parsePrivateKey(privKeyBytes)
	require.NoError(t, err)

	pemStr, err := PublicKeyToPEM(&privKey.PublicKey)
	assert.NoError(t, err)
	assert.Contains(t, pemStr, "BEGIN PUBLIC KEY")
	assert.Contains(t, pemStr, "END PUBLIC KEY")
}

func TestNewJWKSFromRSAPubKeys_EmptyList(t *testing.T) {
	keyset, err := NewJWKSFromRSAPubKeys([]*rsa.PublicKey{})
	assert.NoError(t, err)
	assert.NotNil(t, keyset)
	assert.Empty(t, keyset.Keys)
}

func TestJWKSCache_VerifyError(t *testing.T) {
	// Create a JWK with mismatched KeyID
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	jwk, err := NewJWKFromRSAPub(&privKey.PublicKey)
	require.NoError(t, err)

	// Manually change the KeyID to cause verification failure
	jwk.KeyID = "wrong-key-id"

	jc := &JWKSCache{
		keyByID: map[string]jose.JSONWebKey{
			"wrong-key-id": *jwk,
		},
	}

	_, err = jc.Get("wrong-key-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "jwk is not valid")
}
