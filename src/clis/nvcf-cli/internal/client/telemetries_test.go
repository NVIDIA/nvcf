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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTelemetryJSON = `{"telemetry":{"telemetryId":"tel-1","name":"grafana","endpoint":"https://otlp.example.com","protocol":"HTTP","provider":"GRAFANA_CLOUD","types":["LOGS","TRACES"],"createdAt":"2026-01-02T03:04:05Z"}}`

func newTelemetryTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		config:     &Config{Token: "jwt", BaseHTTPURL: srv.URL},
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}
}

func TestCreateTelemetrySendsRequest(t *testing.T) {
	var body map[string]any
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v2/nvcf/telemetries", r.URL.Path)
		body = readJSONBody(t, r)
		_, _ = w.Write([]byte(testTelemetryJSON))
	})

	resp, err := c.CreateTelemetry(context.Background(), &TelemetryRequest{
		Endpoint: "https://otlp.example.com",
		Protocol: "HTTP",
		Provider: "GRAFANA_CLOUD",
		Types:    []string{"LOGS", "TRACES"},
		Secret: SecretDto{Name: "grafana", Value: map[string]any{
			"instanceId": "123",
			"apiKey":     "key",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "tel-1", resp.Telemetry.TelemetryID)
	assert.Equal(t, []string{"LOGS", "TRACES"}, resp.Telemetry.Types)

	assert.Equal(t, "https://otlp.example.com", body["endpoint"])
	assert.Equal(t, "HTTP", body["protocol"])
	assert.Equal(t, "GRAFANA_CLOUD", body["provider"])
	assert.Equal(t, []any{"LOGS", "TRACES"}, body["types"])
	assert.Equal(t, map[string]any{
		"name":  "grafana",
		"value": map[string]any{"instanceId": "123", "apiKey": "key"},
	}, body["secret"])
}

func TestCreateTelemetryReturnsAPIError(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"name exists"}`))
	})
	_, err := c.CreateTelemetry(context.Background(), &TelemetryRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error 400")
	assert.Contains(t, err.Error(), "name exists")
}

func TestListTelemetries(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v2/nvcf/telemetries", r.URL.Path)
		_, _ = w.Write([]byte(`{"telemetries":[{"telemetryId":"tel-1","name":"a"},{"telemetryId":"tel-2","name":"b"}]}`))
	})
	resp, err := c.ListTelemetries(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Telemetries, 2)
	assert.Equal(t, "tel-2", resp.Telemetries[1].TelemetryID)
}

func TestGetTelemetry(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v2/nvcf/telemetries/tel-1", r.URL.Path)
		_, _ = w.Write([]byte(testTelemetryJSON))
	})
	resp, err := c.GetTelemetry(context.Background(), "tel-1")
	require.NoError(t, err)
	assert.Equal(t, "GRAFANA_CLOUD", resp.Telemetry.Provider)
}

func TestGetTelemetryNotFound(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.GetTelemetry(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error 404")
}

func TestDeleteTelemetry(t *testing.T) {
	var gotMethod, gotPath string
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, c.DeleteTelemetry(context.Background(), "tel-1"))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/v2/nvcf/telemetries/tel-1", gotPath)
}

func TestDeleteTelemetryInUse(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`in use by function`))
	})
	err := c.DeleteTelemetry(context.Background(), "tel-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by function")
}

func TestUpdateTelemetrySecretsSendsSecret(t *testing.T) {
	var body map[string]any
	var gotMethod, gotPath string
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body = readJSONBody(t, r)
		w.WriteHeader(http.StatusNoContent)
	})
	err := c.UpdateTelemetrySecrets(context.Background(), "tel-1",
		&UpdateTelemetrySecretRequest{Secret: SecretDto{Name: "splunk", Value: "token"}})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/v2/nvcf/secrets/telemetries/tel-1", gotPath)
	assert.Equal(t, map[string]any{"secret": map[string]any{"name": "splunk", "value": "token"}}, body)
}

func TestFunctionDtoDecodesTelemetries(t *testing.T) {
	c := newTelemetryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"function":{"id":"fn","versionId":"v","telemetries":{"logsTelemetryId":"tel-logs","tracesTelemetryId":"tel-traces"}}}`))
	})
	fn, err := c.GetFunctionDetails(context.Background(), "fn", "v")
	require.NoError(t, err)
	require.NotNil(t, fn.Telemetries)
	assert.Equal(t, "tel-logs", fn.Telemetries.LogsTelemetryId)
	assert.Empty(t, fn.Telemetries.MetricsTelemetryId)
	assert.Equal(t, "tel-traces", fn.Telemetries.TracesTelemetryId)
}
