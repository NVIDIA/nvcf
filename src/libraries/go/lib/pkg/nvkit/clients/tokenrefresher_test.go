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
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testCredsFile        = "./testdata/test-creds.yaml"
	testInvalidCredsFile = "./testdata/test-invalid-creds.yaml"
)

func TestNewTokenRefresher(t *testing.T) {
	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))
	credsFile := "./testdata/test-creds.yaml"
	scopes := []string{"read", "write"}

	oidcConfig := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "test",
	}
	tr, err := NewTokenRefresher(oidcConfig, "test_new_token_refresher")
	assert.Nil(t, err)
	assert.NotNil(t, tr)
	assert.NotNil(t, tr.httpClient) // Verify HTTP client is properly initialized
	assert.Equal(t, server.URL+"/token", tr.tokenEndpoint)
	assert.Equal(t, credsFile, tr.credsFile)
	assert.Contains(t, tr.tokenRequestPayload, "scope=read write&grant_type=client_credentials")
}

func TestNewTokenRefresher_FailInit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("error"))
	}))
	credsFile := "./testdata/test-creds.yaml"
	scopes := []string{"read", "write"}

	oidcConfig := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "test",
	}
	tr, err := NewTokenRefresher(oidcConfig, "test_fail_init")
	assert.NotNil(t, err)
	assert.Nil(t, tr)
}

func TestToken(t *testing.T) {
	tr := &TokenRefresher{
		authToken: "validToken123",
		rwMutex:   sync.RWMutex{},
	}
	token := tr.Token()
	assert.Equal(t, "validToken123", token)
}

func TestGetTokenData(t *testing.T) {
	tr := &TokenRefresher{
		authToken:                 "validToken123",
		authTokenRefreshTimestamp: 1609459200,
		rwMutex:                   sync.RWMutex{},
	}
	token, timestamp := tr.getTokenData()
	assert.Equal(t, "validToken123", token)
	assert.Equal(t, int64(1609459200), timestamp)
}

func TestRefreshTokenOnce_CredentialMismatch(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_credential_mismatch")

	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))
	defer server.Close()
	tr := &TokenRefresher{
		httpClient:    server.Client(),
		clientId:      "client-id-1",
		clientSecret:  "client-secret-1",
		authToken:     "currentAuthToken",
		credsFile:     testCredsFile,
		tokenEndpoint: server.URL + "/token",
		clientName:    "test_client",
		rwMutex:       sync.RWMutex{},
	}

	code, err := tr.refreshTokenOnce()
	assert.Equal(t, 200, code)
	assert.Nil(t, err)
	assert.Equal(t, "test-client-id", tr.clientId)
	assert.Equal(t, "test-client-secret", tr.clientSecret)
}

func TestRefreshTokenOnce_CredentialFileError(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_credential_file_error")

	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))
	defer server.Close()
	tr := &TokenRefresher{
		httpClient:    server.Client(),
		clientId:      "client-id-1",
		clientSecret:  "client-secret-1",
		authToken:     "currentAuthToken",
		credsFile:     testInvalidCredsFile,
		tokenEndpoint: server.URL + "/token",
		clientName:    "test_client",
		rwMutex:       sync.RWMutex{},
	}

	code, err := tr.refreshTokenOnce()
	assert.Equal(t, 0, code)
	assert.NotNil(t, err)
	assert.Equal(t, "client-id-1", tr.clientId)
	assert.Equal(t, "client-secret-1", tr.clientSecret)
}

func TestRefreshTokenOnce_TokenExpired(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_token_expired")

	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))

	tr := &TokenRefresher{
		httpClient:                server.Client(),
		tokenEndpoint:             server.URL + "/token",
		clientId:                  "client123",
		clientSecret:              "secret123",
		authToken:                 "oldToken123",
		credsFile:                 testCredsFile,
		authTokenRefreshTimestamp: time.Now().Unix() - 1001,
		clientName:                "test_client",
		rwMutex:                   sync.RWMutex{},
	}

	code, err := tr.refreshTokenOnce()
	assert.NoError(t, err)
	assert.Equal(t, 200, code)
	assert.Equal(t, "test-token", tr.authToken)
}

func TestRefreshTokenOnce_EmptyToken(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_empty_token")

	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))

	tr := &TokenRefresher{
		httpClient:                server.Client(),
		tokenEndpoint:             server.URL + "/token",
		clientId:                  "client123",
		clientSecret:              "secret123",
		authToken:                 "",
		credsFile:                 testCredsFile,
		authTokenRefreshTimestamp: time.Now().Unix(),
		clientName:                "test_client",
		rwMutex:                   sync.RWMutex{},
	}

	code, err := tr.refreshTokenOnce()
	assert.NoError(t, err)
	assert.Equal(t, 200, code)
	assert.Equal(t, "test-token", tr.authToken)
}

func TestRefreshTokenOnce_TokenFetchError(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_token_fetch_error")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.WriteHeader(401)
		w.Write([]byte("error"))
	}))

	tr := &TokenRefresher{
		httpClient:                server.Client(),
		tokenEndpoint:             server.URL + "/token",
		clientId:                  "client123",
		clientSecret:              "secret123",
		authToken:                 "oldToken123",
		credsFile:                 testCredsFile,
		authTokenRefreshTimestamp: time.Now().Unix() - 1001,
		clientName:                "test_client",
		rwMutex:                   sync.RWMutex{},
	}

	code, err := tr.refreshTokenOnce()
	assert.Error(t, err)
	assert.Equal(t, 401, code)
	assert.Equal(t, "oldToken123", tr.authToken)
}

func TestMultipleTokenRefreshers(t *testing.T) {
	// Initialize global metrics for testing
	initTokenRefresherMetrics("test_multiple_refreshers")

	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))

	var trs []*TokenRefresher
	for i := 0; i < 100; i++ {
		tr := &TokenRefresher{
			httpClient:                server.Client(),
			tokenEndpoint:             server.URL + "/token",
			clientId:                  "client123",
			clientSecret:              "secret123",
			authToken:                 "oldToken123",
			credsFile:                 testCredsFile,
			authTokenRefreshTimestamp: time.Now().Unix() - 1001,
			clientName:                fmt.Sprintf("test_client_%d", i),
			rwMutex:                   sync.RWMutex{},
		}
		trs = append(trs, tr)
	}
	var wg sync.WaitGroup
	for _, tr := range trs {
		wg.Add(1)
		go func(tokenRefresher *TokenRefresher) {
			defer wg.Done()
			code, err := tokenRefresher.refreshTokenOnce()
			assert.NoError(t, err)
			assert.Equal(t, 200, code)
			assert.Equal(t, "test-token", tokenRefresher.authToken)
			assert.Equal(t, "test-client-id", tokenRefresher.clientId)
			assert.Equal(t, "test-client-secret", tokenRefresher.clientSecret)
		}(tr)
	}
	wg.Wait()
}

func TestTwoTokenRefreshersWithSameNamespace(t *testing.T) {
	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))
	defer server.Close()

	credsFile := "./testdata/test-creds.yaml"
	scopes := []string{"read", "write"}
	sharedNamespace := "shared_test_namespace"

	// Create first TokenRefresher with shared namespace
	oidcConfig1 := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "client1",
	}
	tr1, err1 := NewTokenRefresher(oidcConfig1, sharedNamespace)
	assert.Nil(t, err1)
	assert.NotNil(t, tr1)
	assert.Equal(t, "client1", tr1.clientName)

	// Create second TokenRefresher with the same namespace - should not cause duplicate metric error
	oidcConfig2 := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "client2",
	}
	tr2, err2 := NewTokenRefresher(oidcConfig2, sharedNamespace)
	assert.Nil(t, err2)
	assert.NotNil(t, tr2)
	assert.Equal(t, "client2", tr2.clientName)

	// Verify both can get tokens successfully
	token1 := tr1.Token()
	token2 := tr2.Token()
	assert.NotEmpty(t, token1)
	assert.NotEmpty(t, token2)
	assert.Equal(t, "test-token", token1)
	assert.Equal(t, "test-token", token2)

	// Verify both can refresh tokens without errors
	status1, err1 := tr1.RefreshToken()
	status2, err2 := tr2.RefreshToken()
	assert.Nil(t, err1)
	assert.Nil(t, err2)
	assert.Equal(t, 200, status1)
	assert.Equal(t, 200, status2)
}

func TestTwoTokenRefreshersWithDifferentNamespaces(t *testing.T) {
	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token", r.URL.Path)
		w.Write(tokenJson)
	}))
	defer server.Close()

	credsFile := "./testdata/test-creds.yaml"
	scopes := []string{"read", "write"}

	// Create first TokenRefresher with namespace1
	oidcConfig1 := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "client1",
	}
	tr1, err1 := NewTokenRefresher(oidcConfig1, "namespace1")
	assert.Nil(t, err1)
	assert.NotNil(t, tr1)
	assert.Equal(t, "client1", tr1.clientName)
	assert.Equal(t, "namespace1", tr1.metricsNamespace)

	// Create second TokenRefresher with namespace2
	oidcConfig2 := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: credsFile,
		ClientID:        "",
		ClientSecret:    "",
		Scopes:          scopes,
		ClientName:      "client2",
	}
	tr2, err2 := NewTokenRefresher(oidcConfig2, "namespace2")
	assert.Nil(t, err2)
	assert.NotNil(t, tr2)
	assert.Equal(t, "client2", tr2.clientName)
	assert.Equal(t, "namespace2", tr2.metricsNamespace)

	// Verify both can get tokens successfully
	token1 := tr1.Token()
	token2 := tr2.Token()
	assert.NotEmpty(t, token1)
	assert.NotEmpty(t, token2)
	assert.Equal(t, "test-token", token1)
	assert.Equal(t, "test-token", token2)

	// Verify both can refresh tokens without errors
	status1, err1 := tr1.RefreshToken()
	status2, err2 := tr2.RefreshToken()
	assert.Nil(t, err1)
	assert.Nil(t, err2)
	assert.Equal(t, 200, status1)
	assert.Equal(t, 200, status2)

	// Verify that both TokenRefreshers have separate metrics instances
	metrics1 := tr1.getMetricsInstance()
	metrics2 := tr2.getMetricsInstance()
	assert.NotNil(t, metrics1)
	assert.NotNil(t, metrics2)

	// The metrics instances should be different objects because they have different namespaces
	assert.NotEqual(t, tr1.metricsNamespace, tr2.metricsNamespace)
}

// TestTokenRefresher_CredentialUpdateRace tests for race conditions when credentials
// are updated concurrently with token refresh operations.
// Run with: go test -race -run TestTokenRefresher_CredentialUpdateRace ./clients/...
func TestTokenRefresher_CredentialUpdateRace(t *testing.T) {
	tokenJson, err := os.ReadFile("./testdata/test-token.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add small delay to increase chance of race
		time.Sleep(1 * time.Millisecond)
		w.Write(tokenJson)
	}))
	defer server.Close()

	// Create a temp credentials file that we'll update during the test
	tmpDir := t.TempDir()
	credsFile := tmpDir + "/creds.yaml"
	initialCreds := `id: "initial-client-id"
secret: "initial-client-secret"`
	err = os.WriteFile(credsFile, []byte(initialCreds), 0644)
	require.NoError(t, err)

	tr := &TokenRefresher{
		httpClient:                server.Client(),
		tokenEndpoint:             server.URL + "/token",
		clientId:                  "initial-client-id",
		clientSecret:              "initial-client-secret",
		authToken:                 "",
		credsFile:                 credsFile,
		authTokenRefreshTimestamp: 0,
		clientName:                "race_test_client",
		rwMutex:                   sync.RWMutex{},
		metricsNamespace:          "test_credential_update_race",
	}

	// Run multiple goroutines that:
	// 1. Call RefreshToken() which reads clientId/clientSecret and may update them
	// 2. Concurrently update the credentials file
	var wg sync.WaitGroup
	const numGoroutines = 50

	// Goroutines that refresh tokens (reads and potentially writes clientId/clientSecret)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				// This calls readClientCreds() -> compares -> updateClientCreds() -> refreshTokenInternal()
				// The race is between updateClientCreds() writing and refreshTokenInternal() reading
				tr.RefreshToken()
			}
		}(i)
	}

	// Goroutines that update the credentials file (triggers credential mismatch detection)
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				// Alternate between two different credentials
				var creds string
				if j%2 == 0 {
					creds = fmt.Sprintf(`id: "client-id-%d"
secret: "client-secret-%d"`, id, j)
				} else {
					creds = `id: "test-client-id"
secret: "test-client-secret"`
				}
				writeErr := os.WriteFile(credsFile, []byte(creds), 0644)
				if writeErr != nil {
					t.Logf("WriteFile error (expected in race conditions): %v", writeErr)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}(i)
	}

	wg.Wait()

	// If we get here without the race detector complaining, the test passes
	// But when run with -race, this should detect the race condition
	t.Log("Test completed - run with -race flag to detect race conditions")
}
func TestTokenRefresher_GetLastRefreshTimestamp(t *testing.T) {
	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tokenJson)
	}))
	defer server.Close()

	oidcConfig := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: testCredsFile,
		Scopes:          []string{"read"},
		ClientName:      "test-glt",
	}
	tr, err := NewTokenRefresher(oidcConfig, "test_get_last_refresh")
	require.NoError(t, err)
	require.NotNil(t, tr)

	ts := tr.GetLastRefreshTimestamp()
	assert.Greater(t, ts, int64(0))
}

func TestNewPassiveTokenRefresher(t *testing.T) {
	tokenJson, _ := ioutil.ReadFile("./testdata/test-token.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tokenJson)
	}))
	defer server.Close()

	oidcConfig := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: testCredsFile,
		Scopes:          []string{"read"},
		ClientName:      "test-passive",
	}
	tr, err := NewPassiveTokenRefresher(oidcConfig, "test_passive_refresher")
	require.NoError(t, err)
	require.NotNil(t, tr)
	assert.NotEmpty(t, tr.Token())
}

func TestNewPassiveTokenRefresher_Fail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	oidcConfig := &auth.ProviderConfig{
		Host:            server.URL,
		CredentialsFile: testCredsFile,
		Scopes:          []string{"read"},
		ClientName:      "test-passive-fail",
	}
	tr, err := NewPassiveTokenRefresher(oidcConfig, "test_passive_fail")
	assert.Error(t, err)
	assert.Nil(t, tr)
}
