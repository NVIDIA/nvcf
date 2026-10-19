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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const telemetryFixture = `{"telemetryId":"tel-1","name":"grafana","endpoint":"https://otlp.example.com","protocol":"HTTP","provider":"GRAFANA_CLOUD","types":["LOGS","TRACES"],"createdAt":"2026-01-02T03:04:05Z"}`

type recordedRequest struct {
	method string
	path   string
	body   map[string]any
}

// fakeTelemetryServer answers the telemetry and telemetry-secret endpoints
// and records every request it receives.
func fakeTelemetryServer(t *testing.T) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var requests []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{method: r.Method, path: r.URL.Path}
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			require.NoError(t, json.Unmarshal(data, &rec.body))
		}
		requests = append(requests, rec)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/nvcf/telemetries":
			_, _ = w.Write([]byte(`{"telemetry":` + telemetryFixture + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/nvcf/telemetries":
			_, _ = w.Write([]byte(`{"telemetries":[` + telemetryFixture + `]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/nvcf/telemetries/tel-1":
			_, _ = w.Write([]byte(`{"telemetry":` + telemetryFixture + `}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/nvcf/telemetries/tel-1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/nvcf/secrets/telemetries/tel-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func withTelemetryJSONOutput(t *testing.T) {
	t.Helper()
	jsonOutput = true
	logging.SetJSONOutput(true)
	t.Cleanup(func() {
		jsonOutput = false
		logging.SetJSONOutput(false)
	})
}

func resetTelemetryFlags(t *testing.T) {
	t.Helper()
	oldCreate := telemetryCreateFlags
	oldSecret := telemetrySecretFlags
	oldDelete := telemetryDeleteFlags
	oldStdin := telemetryStdin
	t.Cleanup(func() {
		telemetryCreateFlags = oldCreate
		telemetrySecretFlags = oldSecret
		telemetryDeleteFlags = oldDelete
		telemetryStdin = oldStdin
	})
	telemetryCreateFlags.protocol = "HTTP"
}

func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

func TestTelemetryCommandStructure(t *testing.T) {
	assert.Equal(t, "telemetry", telemetryCmd.Use)
	expected := map[string]bool{
		"create":                       false,
		"list":                         false,
		"get <telemetry-id>":           false,
		"delete <telemetry-id>":        false,
		"update-secret <telemetry-id>": false,
	}
	for _, sub := range telemetryCmd.Commands() {
		if _, ok := expected[sub.Use]; ok {
			expected[sub.Use] = true
		}
	}
	for use, found := range expected {
		assert.Truef(t, found, "expected subcommand %q", use)
	}

	found := false
	for _, c := range rootCmd.Commands() {
		if c == telemetryCmd {
			found = true
		}
	}
	assert.True(t, found, "telemetry must be registered on the root command")

	for _, name := range []string{"name", "endpoint", "provider", "protocol", "type", "secret", "secret-file"} {
		assert.NotNilf(t, telemetryCreateCmd.Flags().Lookup(name), "telemetry create should expose --%s", name)
	}
	for _, name := range []string{"secret", "secret-file"} {
		assert.NotNilf(t, telemetryUpdateSecretCmd.Flags().Lookup(name), "telemetry update-secret should expose --%s", name)
	}
	assert.NotNil(t, telemetryDeleteCmd.Flags().Lookup("force"))
}

func TestTelemetryHelpListsEveryProvider(t *testing.T) {
	for provider := range telemetryProviderSpecs {
		assert.Contains(t, telemetryCreateCmd.Long, provider)
	}
}

func TestNormalizeTelemetryProvider(t *testing.T) {
	got, err := normalizeTelemetryProvider(" grafana_cloud ")
	require.NoError(t, err)
	assert.Equal(t, "GRAFANA_CLOUD", got)

	for _, unsupported := range []string{"TIMESTREAM", "VICTORIAMETRICS", ""} {
		_, err := normalizeTelemetryProvider(unsupported)
		require.Errorf(t, err, "provider %q must be rejected", unsupported)
		assert.Contains(t, err.Error(), "OTEL_COLLECTOR", "error must list supported providers")
	}
}

func TestNormalizeTelemetryProtocol(t *testing.T) {
	for in, want := range map[string]string{"http": "HTTP", "GRPC": "GRPC", " grpc ": "GRPC"} {
		got, err := normalizeTelemetryProtocol(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := normalizeTelemetryProtocol("tcp")
	require.Error(t, err)
}

func TestNormalizeTelemetryTypes(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		types    []string
		want     []string
		wantErr  string
	}{
		{"dedupes and upper-cases", "GRAFANA_CLOUD", []string{"logs", "LOGS", "traces"}, []string{"LOGS", "TRACES"}, ""},
		{"all signals", "OTEL_COLLECTOR", []string{"LOGS", "METRICS", "TRACES"}, []string{"LOGS", "METRICS", "TRACES"}, ""},
		{"splunk logs only", "SPLUNK", []string{"METRICS"}, nil, "does not support METRICS"},
		{"prometheus metrics only", "PROMETHEUS", []string{"LOGS"}, nil, "does not support LOGS"},
		{"servicenow traces only", "SERVICENOW", []string{"LOGS"}, nil, "does not support LOGS"},
		{"kratos logs only", "KRATOS", []string{"TRACES"}, nil, "does not support TRACES"},
		{"unknown type", "DATADOG", []string{"EVENTS"}, nil, "unsupported telemetry type"},
		{"empty", "DATADOG", nil, nil, "at least one --type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTelemetryTypes(tt.provider, tt.types)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadTelemetrySecret(t *testing.T) {
	t.Run("string from file trims trailing newline", func(t *testing.T) {
		got, err := readTelemetrySecret("", writeSecretFile(t, "token-value\n"), nil)
		require.NoError(t, err)
		assert.Equal(t, "token-value", got)
	})
	t.Run("JSON object from file", func(t *testing.T) {
		got, err := readTelemetrySecret("", writeSecretFile(t, `{"instanceId":"1","apiKey":"k"}`), nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]interface{}{"instanceId": "1", "apiKey": "k"}, got)
	})
	t.Run("stdin", func(t *testing.T) {
		got, err := readTelemetrySecret("", "-", strings.NewReader("from-stdin"))
		require.NoError(t, err)
		assert.Equal(t, "from-stdin", got)
	})
	t.Run("inline", func(t *testing.T) {
		got, err := readTelemetrySecret("inline", "", nil)
		require.NoError(t, err)
		assert.Equal(t, "inline", got)
	})
	t.Run("both", func(t *testing.T) {
		_, err := readTelemetrySecret("a", "b", nil)
		require.Error(t, err)
	})
	t.Run("neither", func(t *testing.T) {
		_, err := readTelemetrySecret("", "", nil)
		require.Error(t, err)
	})
	t.Run("empty file", func(t *testing.T) {
		_, err := readTelemetrySecret("", writeSecretFile(t, "\n"), nil)
		require.Error(t, err)
	})
	t.Run("invalid JSON object", func(t *testing.T) {
		_, err := readTelemetrySecret("", writeSecretFile(t, `{"apiKey":`), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a valid JSON object")
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := readTelemetrySecret("", filepath.Join(t.TempDir(), "missing"), nil)
		require.Error(t, err)
	})
}

func TestValidateTelemetrySecret(t *testing.T) {
	tests := []struct {
		provider string
		value    interface{}
		wantErr  string
	}{
		{"SPLUNK", "token", ""},
		{"DATADOG", "key", ""},
		{"SERVICENOW", "token", ""},
		{"OTEL_COLLECTOR", "token", ""},
		{"OTEL_COLLECTOR", map[string]interface{}{"token": "x"}, "string token"},
		{"GRAFANA_CLOUD", map[string]interface{}{"instanceId": "1", "apiKey": "k"}, ""},
		{"GRAFANA_CLOUD", "plain", "JSON object"},
		{"GRAFANA_CLOUD", map[string]interface{}{"instanceId": "1"}, `missing "apiKey"`},
		{"GRAFANA_CLOUD", map[string]interface{}{"instanceId": 1, "apiKey": "k"}, "must be a string"},
		{"AZURE_MONITOR", map[string]interface{}{"instrumentationKey": "i", "applicationId": "a", "liveEndpoint": "l"}, ""},
		{"AZURE_MONITOR", map[string]interface{}{"instrumentationKey": "i"}, `"applicationId", "liveEndpoint"`},
		{"KRATOS", map[string]interface{}{"collectorId": "c", "clientCert": "cert", "clientKey": "key"}, ""},
		{"KRATOS", map[string]interface{}{"collectorId": "c"}, "missing"},
		{"PROMETHEUS", map[string]interface{}{"caFile": "ca"}, ""},
		{"PROMETHEUS", map[string]interface{}{"clientCert": "c", "clientKey": "k"}, ""},
		{"KRATOS_THANOS", map[string]interface{}{"clientCert": "c", "clientKey": "k", "caFile": "ca"}, ""},
		{"PROMETHEUS", map[string]interface{}{"clientCert": "c"}, "together"},
		{"PROMETHEUS", map[string]interface{}{"caFile": " "}, `"caFile", or both`},
		{"PROMETHEUS", map[string]interface{}{"caFile": "ca", "password": "p"}, "unknown key"},
		{"PROMETHEUS", "token", "JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			err := validateTelemetrySecret(tt.provider, tt.value)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRunTelemetryCreateSendsRequest(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)
	withTelemetryJSONOutput(t)

	telemetryCreateFlags.name = "grafana"
	telemetryCreateFlags.endpoint = "https://otlp.example.com"
	telemetryCreateFlags.provider = "grafana_cloud"
	telemetryCreateFlags.protocol = "grpc"
	telemetryCreateFlags.types = []string{"logs", "traces"}
	telemetryCreateFlags.secretFile = writeSecretFile(t, `{"instanceId":"123","apiKey":"key"}`)

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryCreate(telemetryCreateCmd, nil) })
	require.NoError(t, runErr)

	require.Len(t, *requests, 1)
	req := (*requests)[0]
	assert.Equal(t, http.MethodPost, req.method)
	assert.Equal(t, "/v2/nvcf/telemetries", req.path)
	assert.Equal(t, "GRAFANA_CLOUD", req.body["provider"])
	assert.Equal(t, "GRPC", req.body["protocol"])
	assert.Equal(t, []any{"LOGS", "TRACES"}, req.body["types"])
	assert.Equal(t, map[string]any{
		"name":  "grafana",
		"value": map[string]any{"instanceId": "123", "apiKey": "key"},
	}, req.body["secret"])

	var parsed client.TelemetryResponse
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "tel-1", parsed.Telemetry.TelemetryID)
}

func TestRunTelemetryCreateRejectsBeforeCallingAPI(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)

	telemetryCreateFlags.name = "grafana"
	telemetryCreateFlags.endpoint = "https://otlp.example.com"
	telemetryCreateFlags.provider = "GRAFANA_CLOUD"
	telemetryCreateFlags.types = []string{"LOGS"}
	telemetryCreateFlags.secretFile = writeSecretFile(t, "plain-token")

	err := runTelemetryCreate(telemetryCreateCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GRAFANA_CLOUD")
	assert.Empty(t, *requests, "invalid input must not reach the API")
}

func TestRunTelemetryListText(t *testing.T) {
	resetTelemetryFlags(t)
	srv, _ := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryList(telemetryListCmd, nil) })
	require.NoError(t, runErr)
	assert.Contains(t, out, "Found 1 telemetry endpoint(s)")
	assert.Contains(t, out, "ID: tel-1")
	assert.Contains(t, out, "Provider: GRAFANA_CLOUD")
	assert.Contains(t, out, "Types: LOGS, TRACES")
}

func TestRunTelemetryListJSON(t *testing.T) {
	resetTelemetryFlags(t)
	srv, _ := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)
	withTelemetryJSONOutput(t)

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryList(telemetryListCmd, nil) })
	require.NoError(t, runErr)
	var parsed client.ListTelemetriesResponse
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Len(t, parsed.Telemetries, 1)
	assert.Equal(t, "grafana", parsed.Telemetries[0].Name)
}

func TestRunTelemetryGet(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryGet(telemetryGetCmd, []string{"tel-1"}) })
	require.NoError(t, runErr)
	assert.Equal(t, "/v2/nvcf/telemetries/tel-1", (*requests)[0].path)
	assert.Contains(t, out, "Endpoint: https://otlp.example.com")
	assert.Contains(t, out, "Protocol: HTTP")
}

func TestRunTelemetryDeleteForce(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)
	withTelemetryJSONOutput(t)
	telemetryDeleteFlags.force = true

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryDelete(telemetryDeleteCmd, []string{"tel-1"}) })
	require.NoError(t, runErr)
	require.Len(t, *requests, 1)
	assert.Equal(t, http.MethodDelete, (*requests)[0].method)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "deleted", parsed["status"])
	assert.Equal(t, "tel-1", parsed["telemetryId"])
}

func TestRunTelemetryDeleteConfirmation(t *testing.T) {
	for answer, wantDelete := range map[string]bool{"yes\n": true, "no\n": false} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			resetTelemetryFlags(t)
			srv, requests := fakeTelemetryServer(t)
			configureAdminTest(t, srv.URL)
			telemetryStdin = strings.NewReader(answer)

			var runErr error
			_ = captureStdout(t, func() { runErr = runTelemetryDelete(telemetryDeleteCmd, []string{"tel-1"}) })
			require.NoError(t, runErr)

			deleted := false
			for _, r := range *requests {
				if r.method == http.MethodDelete {
					deleted = true
				}
			}
			assert.Equal(t, wantDelete, deleted)
		})
	}
}

func TestRunTelemetryUpdateSecret(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)
	withTelemetryJSONOutput(t)
	telemetrySecretFlags.secretFile = "-"
	telemetryStdin = strings.NewReader(`{"instanceId":"456","apiKey":"rotated"}`)

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetryUpdateSecret(telemetryUpdateSecretCmd, []string{"tel-1"}) })
	require.NoError(t, runErr)

	require.Len(t, *requests, 2)
	assert.Equal(t, http.MethodGet, (*requests)[0].method)
	put := (*requests)[1]
	assert.Equal(t, http.MethodPut, put.method)
	assert.Equal(t, "/v2/nvcf/secrets/telemetries/tel-1", put.path)
	assert.Equal(t, map[string]any{"secret": map[string]any{
		"name":  "grafana",
		"value": map[string]any{"instanceId": "456", "apiKey": "rotated"},
	}}, put.body)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, "ok", parsed["status"])
}

func TestRunTelemetryUpdateSecretRejectsWrongShape(t *testing.T) {
	resetTelemetryFlags(t)
	srv, requests := fakeTelemetryServer(t)
	configureAdminTest(t, srv.URL)
	telemetrySecretFlags.secretFile = writeSecretFile(t, "plain-token")

	err := runTelemetryUpdateSecret(telemetryUpdateSecretCmd, []string{"tel-1"})
	require.Error(t, err)
	for _, r := range *requests {
		assert.NotEqual(t, http.MethodPut, r.method, "an invalid secret must not be sent")
	}
}

func TestAttachedTelemetryLines(t *testing.T) {
	assert.Nil(t, attachedTelemetryLines(nil))
	assert.Empty(t, attachedTelemetryLines(&client.TelemetriesDto{}))
	assert.Equal(t, []string{"Logs: l", "Metrics: m", "Traces: t"}, attachedTelemetryLines(&client.TelemetriesDto{
		LogsTelemetryId: "l", MetricsTelemetryId: "m", TracesTelemetryId: "t",
	}))
	assert.Equal(t, []string{"Traces: t"}, attachedTelemetryLines(&client.TelemetriesDto{TracesTelemetryId: "t"}))
}

const functionWithTelemetriesJSON = `{"function":{"id":"fn-1","versionId":"ver-1","name":"echo","status":"INACTIVE","createdAt":"2026-01-02T03:04:05Z","functionType":"DEFAULT","telemetries":{"logsTelemetryId":"tel-logs","metricsTelemetryId":"tel-metrics"}}}`

func runFunctionGetAgainst(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/nvcf/functions/fn-1/versions/ver-1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	configureAdminTest(t, srv.URL)

	cmd := &cobra.Command{}
	cmd.Flags().String("function-id", "fn-1", "")
	cmd.Flags().String("version-id", "ver-1", "")
	var runErr error
	out := captureStdout(t, func() { runErr = runGetFunction(cmd, nil) })
	require.NoError(t, runErr)
	return out
}

func TestFunctionGetShowsTelemetriesText(t *testing.T) {
	out := runFunctionGetAgainst(t, functionWithTelemetriesJSON)
	assert.Contains(t, out, "Telemetries:")
	assert.Contains(t, out, "Logs: tel-logs")
	assert.Contains(t, out, "Metrics: tel-metrics")
	assert.NotContains(t, out, "Traces:")
}

func TestFunctionGetOmitsTelemetriesWhenNone(t *testing.T) {
	out := runFunctionGetAgainst(t, `{"function":{"id":"fn-1","versionId":"ver-1","name":"echo"}}`)
	assert.NotContains(t, out, "Telemetries:")
}

func TestFunctionGetShowsTelemetriesJSON(t *testing.T) {
	withTelemetryJSONOutput(t)
	out := runFunctionGetAgainst(t, functionWithTelemetriesJSON)
	var parsed client.FunctionDto
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.NotNil(t, parsed.Telemetries)
	assert.Equal(t, "tel-logs", parsed.Telemetries.LogsTelemetryId)
	assert.Equal(t, "tel-metrics", parsed.Telemetries.MetricsTelemetryId)
}

func TestFunctionCreateJSONOutputIncludesTelemetries(t *testing.T) {
	output, err, _ := runFunctionCreateWithResponse(t, `{"function":{"id":"fn-1","versionId":"ver-1","name":"echo","status":"INACTIVE","telemetries":{"tracesTelemetryId":"tel-traces"}}}`, false)
	require.NoError(t, err)
	var parsed client.CreateFunctionResponse
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.NotNil(t, parsed.Function.Telemetries)
	assert.Equal(t, "tel-traces", parsed.Function.Telemetries.TracesTelemetryId)
}

func TestFunctionCreateTextOutputIncludesTelemetries(t *testing.T) {
	resp := &client.CreateFunctionResponse{Function: client.FunctionData{
		ID: "fn-1", VersionID: "ver-1",
		Telemetries: &client.TelemetriesDto{LogsTelemetryId: "tel-logs", TracesTelemetryId: "tel-traces"},
	}}
	out := captureStdout(t, func() { printCreateResult(resp, &CreateConfig{}, nil) })
	assert.Contains(t, out, "Telemetries:")
	assert.Contains(t, out, "Logs: tel-logs")
	assert.Contains(t, out, "Traces: tel-traces")
}

func TestTaskCreateJSONOutputIncludesTelemetries(t *testing.T) {
	output, err := runTaskCreateWithResponse(t, `{"task":{"id":"task-test","name":"task-test","status":"QUEUED","telemetries":{"metricsTelemetryId":"tel-metrics"}}}`)
	require.NoError(t, err)
	var parsed client.TaskResponse
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.NotNil(t, parsed.Task.Telemetries)
	assert.Equal(t, "tel-metrics", parsed.Task.Telemetries.MetricsTelemetryId)
}

func TestTaskCreateTextOutputIncludesTelemetries(t *testing.T) {
	resp := &client.TaskResponse{Task: client.TaskDto{
		ID: "task-1", Name: "task-1", Status: "QUEUED",
		Telemetries: &client.TelemetriesDto{MetricsTelemetryId: "tel-metrics"},
	}}
	out := captureStdout(t, func() { printTaskCreateResult(resp) })
	assert.Contains(t, out, "Telemetries:")
	assert.Contains(t, out, "Metrics: tel-metrics")
}

func runTaskGetAgainst(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/nvct/tasks/task-1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	oldCfgFile := cfgFile
	oldStateManager := configStateManager
	oldStateManagerKey := configStateManagerKey
	oldTaskGetFlags := taskGetFlags
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		configStateManager = oldStateManager
		configStateManagerKey = oldStateManagerKey
		taskGetFlags = oldTaskGetFlags
		viper.Reset()
	})
	viper.Reset()
	viper.Set("base_nvct_url", srv.URL)
	viper.Set("base_grpc_url", "localhost:50051")
	viper.Set("api_key", "test-function-api-key")
	viper.Set("nvct_api_key", "test-task-api-key")
	cfgFile = filepath.Join(t.TempDir(), "workflow.yaml")
	configStateManager = nil
	configStateManagerKey = ""
	taskGetFlags.timeoutSeconds = 5

	var runErr error
	out := captureStdout(t, func() { runErr = runTaskGet(taskGetCmd, []string{"task-1"}) })
	require.NoError(t, runErr)
	return out
}

func TestTaskGetShowsTelemetriesText(t *testing.T) {
	out := runTaskGetAgainst(t, `{"task":{"id":"task-1","name":"t","status":"RUNNING","telemetries":{"logsTelemetryId":"tel-logs","tracesTelemetryId":"tel-traces"}}}`)
	assert.Contains(t, out, "Telemetries:")
	assert.Contains(t, out, "Logs: tel-logs")
	assert.Contains(t, out, "Traces: tel-traces")
}

func TestTaskGetShowsTelemetriesJSON(t *testing.T) {
	withTelemetryJSONOutput(t)
	out := runTaskGetAgainst(t, `{"task":{"id":"task-1","name":"t","status":"RUNNING","telemetries":{"logsTelemetryId":"tel-logs"}}}`)
	var parsed client.TaskResponse
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.NotNil(t, parsed.Task.Telemetries)
	assert.Equal(t, "tel-logs", parsed.Task.Telemetries.LogsTelemetryId)
}
