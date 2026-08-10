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

package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"nvcf-cli/internal/client"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: the NVCF_CLI_ENABLE_ADMIN env guard is evaluated in init() at package
// import time, so it cannot be re-exercised after the test binary starts. The
// tests here exercise the run* handlers directly and verify (a) the
// NVCF_TOKEN fail-fast helper, (b) --json output shapes, and (c) request
// auth headers via an httptest mock backend.

// captureStdout runs fn while redirecting os.Stdout to a pipe, returning
// whatever fn wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// configureAdminTest points the CLI at the given mock server URL and installs
// a fake admin token. Resets viper on cleanup so tests do not bleed into one
// another.
func configureAdminTest(t *testing.T, srvURL string) {
	t.Helper()
	viper.Reset()
	viper.Set("base_http_url", srvURL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("token", "test-admin-token")
	t.Cleanup(func() { viper.Reset() })
}

// withJSONOutput flips the package-level jsonOutput flag for the duration of
// the calling test.
func withJSONOutput(t *testing.T) {
	t.Helper()
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
}

func TestRequireAdminToken(t *testing.T) {
	t.Run("token set returns nil", func(t *testing.T) {
		err := requireAdminToken(&client.Config{Token: "abc"})
		assert.NoError(t, err)
	})

	t.Run("token unset returns error mentioning NVCF_TOKEN", func(t *testing.T) {
		err := requireAdminToken(&client.Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NVCF_TOKEN")
		assert.Contains(t, err.Error(), "NVCF_API_KEY is not accepted")
	})

	t.Run("api key only is rejected", func(t *testing.T) {
		err := requireAdminToken(&client.Config{APIKey: "user-key"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NVCF_TOKEN")
	})
}

func TestRunAccountsList_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/nvcf/accounts", r.URL.Path)
		assert.Equal(t, "Bearer test-admin-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudAccounts":[{"ncaId":"nca-1","name":"Acme","maxFunctionsAllowed":5,"maxTasksAllowed":3,"maxTelemetriesAllowed":2,"maxRegistryCredentialsAllowed":1,"adminClientIds":["client-1"]}]}`))
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)
	withJSONOutput(t)

	output := captureStdout(t, func() {
		require.NoError(t, runAccountsList(nil, nil))
	})

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	accounts, ok := parsed["cloudAccounts"].([]any)
	require.True(t, ok, "expected cloudAccounts array in output, got: %s", output)
	require.Len(t, accounts, 1)
	first := accounts[0].(map[string]any)
	assert.Equal(t, "nca-1", first["ncaId"])
	assert.Equal(t, "Acme", first["name"])
}

func TestRunAccountsList_NoToken_FailsFast(t *testing.T) {
	viper.Reset()
	viper.Set("base_http_url", "http://unused")
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "user-key-only")
	t.Cleanup(func() { viper.Reset() })

	err := runAccountsList(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NVCF_TOKEN")
}

func TestRunQueuesVersion_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t,
			"/v2/nvcf/accounts/nca-1/queues/functions/fn-1/versions/ver-1",
			r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"functionId":"fn-1","queues":[{"functionVersionId":"ver-1","functionName":"hello","functionStatus":"ACTIVE","queueDepth":7}]}`))
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)
	withJSONOutput(t)

	queueFlags.ncaId = "nca-1"
	queueFlags.functionId = "fn-1"
	queueFlags.versionId = "ver-1"
	t.Cleanup(func() { queueFlags = struct {
		ncaId      string
		functionId string
		versionId  string
	}{} })

	output := captureStdout(t, func() {
		require.NoError(t, runQueuesVersion(nil, nil))
	})

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	assert.Equal(t, "fn-1", parsed["functionId"])
	queues, ok := parsed["queues"].([]any)
	require.True(t, ok)
	require.Len(t, queues, 1)
	assert.Equal(t, float64(7), queues[0].(map[string]any)["queueDepth"])
}

func TestRunSecretsUpdateFunction_JSONEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t,
			"/v2/nvcf/accounts/nca-1/secrets/functions/fn-1/versions/ver-1",
			r.URL.Path)
		// Backend returns 204 with no body for secret updates.
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)
	withJSONOutput(t)

	secretUpdateFlags.ncaId = "nca-1"
	secretUpdateFlags.functionId = "fn-1"
	secretUpdateFlags.versionId = "ver-1"
	secretUpdateFlags.secretsJSON = `{"FOO":"bar"}`
	t.Cleanup(func() { secretUpdateFlags = struct {
		ncaId       string
		functionId  string
		versionId   string
		telemetryId string
		inputFile   string
		secretsJSON string
	}{} })

	output := captureStdout(t, func() {
		require.NoError(t, runSecretsUpdateFunction(nil, nil))
	})

	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	assert.Equal(t, "ok", parsed["status"])
	assert.Equal(t, "nca-1", parsed["ncaId"])
	assert.Equal(t, "fn-1", parsed["functionId"])
	assert.Equal(t, "ver-1", parsed["versionId"])
}

func TestRunAccountsList_HumanOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudAccounts":[{"ncaId":"nca-42","name":"Globex","maxFunctionsAllowed":10,"maxTasksAllowed":10,"maxTelemetriesAllowed":5,"maxRegistryCredentialsAllowed":3,"adminClientIds":[]}]}`))
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)
	// jsonOutput stays false: this exercises the human-readable table path.

	output := captureStdout(t, func() {
		require.NoError(t, runAccountsList(nil, nil))
	})

	// Human output should mention the account ID and name as a table row.
	assert.True(t, strings.Contains(output, "nca-42"), "output missing ncaId: %s", output)
	assert.True(t, strings.Contains(output, "Globex"), "output missing name: %s", output)
	// And should not be parseable as JSON.
	var parsed map[string]any
	assert.Error(t, json.Unmarshal([]byte(output), &parsed),
		"human output should not be valid JSON")
}
