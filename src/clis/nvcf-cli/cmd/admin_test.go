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
	"path/filepath"
	"strings"
	"testing"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/state"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAccountsUpdateTestCmd builds a throwaway *cobra.Command carrying the
// same flag set as accountsUpdateCmd, bound to a fresh accountUpdateFlags
// value. Using a fresh command per test avoids Changed() state leaking
// across tests via the package-level singleton.
func newAccountsUpdateTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	accountUpdateFlags = struct {
		ncaId                  string
		name                   string
		maxFunctions           int
		maxTasks               int
		maxTelemetries         int
		maxRegistryCredentials int
	}{}
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().StringVar(&accountUpdateFlags.ncaId, "nca-id", "", "")
	cmd.Flags().StringVar(&accountUpdateFlags.name, "name", "", "")
	cmd.Flags().IntVar(&accountUpdateFlags.maxFunctions, "max-functions", 0, "")
	cmd.Flags().IntVar(&accountUpdateFlags.maxTasks, "max-tasks", 0, "")
	cmd.Flags().IntVar(&accountUpdateFlags.maxTelemetries, "max-telemetries", 0, "")
	cmd.Flags().IntVar(&accountUpdateFlags.maxRegistryCredentials, "max-registry-credentials", 0, "")
	return cmd
}

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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NVCF_API_KEY", "")
	t.Setenv("NVCF_TOKEN", "")

	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	oldDefaultStateManager := state.DefaultStateManager
	viper.Reset()
	viper.Set("base_http_url", srvURL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("token", "test-admin-token")
	cfgFile = filepath.Join(home, "admin-test.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	state.DefaultStateManager = state.NewStateManager()
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		state.DefaultStateManager = oldDefaultStateManager
		viper.Reset()
	})
}

// isolateCredentialState prevents a developer's real ~/.nvcf-cli.state (or
// inherited NVCF_TOKEN/NVCF_API_KEY env vars) from leaking into credential
// fallback lookups. Config loading falls back to the on-disk state file via
// os.UserHomeDir, so redirecting HOME to an empty temp dir and rebuilding
// the package-level state manager is required for a deterministic "no
// credentials configured" test; viper.Reset alone does not cover this path.
func isolateCredentialState(t *testing.T) {
	t.Helper()
	t.Cleanup(state.ResetDefaultStateManager)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVCF_TOKEN", "")
	t.Setenv("NVCF_API_KEY", "")
	state.ResetDefaultStateManager()
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

const wantAdminTokenRequiredError = "admin commands require NVCF_TOKEN with the appropriate admin scope; NVCF_API_KEY is not accepted"

func TestRunAccountsList_NoToken_FailsFast(t *testing.T) {
	isolateCredentialState(t)
	viper.Reset()
	viper.Set("base_http_url", "http://unused")
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "user-key-only")
	t.Cleanup(func() { viper.Reset() })

	err := runAccountsList(nil, nil)
	require.Error(t, err)
	assert.Equal(t, wantAdminTokenRequiredError, err.Error())
}

// TestRunAccountsList_NoCredentialsAtAll_FailsFastWithAdminMessage is a
// regression test: when neither NVCF_TOKEN nor NVCF_API_KEY is configured,
// the generic "missing authentication credentials" error from LoadConfig
// must not fire ahead of requireAdminToken's Admin Accounts-specific
// message, and the error must not tell the user to set NVCF_API_KEY.
func TestRunAccountsList_NoCredentialsAtAll_FailsFastWithAdminMessage(t *testing.T) {
	isolateCredentialState(t)
	viper.Reset()
	viper.Set("base_http_url", "http://unused")
	viper.Set("base_grpc_url", "localhost:50051")
	t.Cleanup(func() { viper.Reset() })

	err := runAccountsList(nil, nil)
	require.Error(t, err)
	assert.Equal(t, wantAdminTokenRequiredError, err.Error())
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
	t.Cleanup(func() {
		queueFlags = struct {
			ncaId      string
			functionId string
			versionId  string
		}{}
	})

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
	t.Cleanup(func() {
		secretUpdateFlags = struct {
			ncaId       string
			functionId  string
			versionId   string
			telemetryId string
			inputFile   string
			secretsJSON string
		}{}
	})

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

func TestBuildAccountUpdateRequest_NoFieldsProvided(t *testing.T) {
	cmd := newAccountsUpdateTestCmd(t)

	req, err := buildAccountUpdateRequest(cmd)
	require.Error(t, err)
	assert.Nil(t, req)
	assert.Contains(t, err.Error(), "at least one update field must be provided")
}

func TestBuildAccountUpdateRequest_ValidQuotas(t *testing.T) {
	cmd := newAccountsUpdateTestCmd(t)
	require.NoError(t, cmd.Flags().Set("max-functions", "5"))
	require.NoError(t, cmd.Flags().Set("max-tasks", "3"))
	require.NoError(t, cmd.Flags().Set("max-telemetries", "10"))
	require.NoError(t, cmd.Flags().Set("max-registry-credentials", "2"))

	req, err := buildAccountUpdateRequest(cmd)
	require.NoError(t, err)
	require.NotNil(t, req)
	require.NotNil(t, req.MaxFunctionsAllowed)
	require.NotNil(t, req.MaxTasksAllowed)
	require.NotNil(t, req.MaxTelemetriesAllowed)
	require.NotNil(t, req.MaxRegistryCredentialsAllowed)
	assert.Equal(t, 5, *req.MaxFunctionsAllowed)
	assert.Equal(t, 3, *req.MaxTasksAllowed)
	assert.Equal(t, 10, *req.MaxTelemetriesAllowed)
	assert.Equal(t, 2, *req.MaxRegistryCredentialsAllowed)
}

func TestBuildAccountUpdateRequest_ZeroIsAllowed(t *testing.T) {
	// 0 is a legitimate quota value, not the "unset" sentinel: it must
	// reach the request when the flag is explicitly passed.
	cmd := newAccountsUpdateTestCmd(t)
	require.NoError(t, cmd.Flags().Set("max-functions", "0"))

	req, err := buildAccountUpdateRequest(cmd)
	require.NoError(t, err)
	require.NotNil(t, req.MaxFunctionsAllowed)
	assert.Equal(t, 0, *req.MaxFunctionsAllowed)
}

func TestBuildAccountUpdateRequest_NegativeQuota_RejectedPerFlag(t *testing.T) {
	tests := []struct {
		flag        string
		wantErrText string
	}{
		{"max-functions", "--max-functions must be greater than or equal to 0"},
		{"max-tasks", "--max-tasks must be greater than or equal to 0"},
		{"max-telemetries", "--max-telemetries must be greater than or equal to 0"},
		{"max-registry-credentials", "--max-registry-credentials must be greater than or equal to 0"},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			cmd := newAccountsUpdateTestCmd(t)
			require.NoError(t, cmd.Flags().Set(tt.flag, "-2"))

			req, err := buildAccountUpdateRequest(cmd)
			require.Error(t, err)
			assert.Nil(t, req)
			assert.Equal(t, tt.wantErrText, err.Error())
		})
	}
}

func TestBuildAccountUpdateRequest_QuotaAboveMax_Rejected(t *testing.T) {
	tests := []struct {
		flag        string
		wantErrText string
	}{
		{"max-telemetries", "max-telemetries cannot exceed 50"},
		{"max-registry-credentials", "max-registry-credentials cannot exceed 50"},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			cmd := newAccountsUpdateTestCmd(t)
			require.NoError(t, cmd.Flags().Set(tt.flag, "51"))

			req, err := buildAccountUpdateRequest(cmd)
			require.Error(t, err)
			assert.Nil(t, req)
			assert.Equal(t, tt.wantErrText, err.Error())
		})
	}
}

func TestRunAccountsUpdate_NegativeQuota_NoRequestSent(t *testing.T) {
	// Regression test for the empty-PATCH bug: a negative quota flag must
	// be rejected client-side before any HTTP request reaches the backend.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request sent to backend: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)

	cmd := newAccountsUpdateTestCmd(t)
	accountUpdateFlags.ncaId = "nca-1"
	require.NoError(t, cmd.Flags().Set("max-functions", "-2"))

	err := runAccountsUpdate(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, "--max-functions must be greater than or equal to 0", err.Error())
}

func TestRunAccountsUpdate_ValidQuota_SendsPatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/v2/nvcf/accounts/nca-1", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.Equal(t, float64(5), parsed["maxFunctionsAllowed"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"account":{"ncaId":"nca-1","name":"Acme","maxFunctionsAllowed":5,"maxTasksAllowed":3,"maxTelemetriesAllowed":2,"maxRegistryCredentialsAllowed":1,"adminClientIds":[]}}`))
	}))
	defer srv.Close()

	configureAdminTest(t, srv.URL)
	withJSONOutput(t)

	cmd := newAccountsUpdateTestCmd(t)
	accountUpdateFlags.ncaId = "nca-1"
	require.NoError(t, cmd.Flags().Set("max-functions", "5"))

	output := captureStdout(t, func() {
		require.NoError(t, runAccountsUpdate(cmd, nil))
	})

	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	assert.Equal(t, "nca-1", parsed["account"]["ncaId"])
}
