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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactRequestBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "telemetry create masks an object secret value",
			body: `{"endpoint":"https://otlp.example.com","secret":{"name":"grafana","value":{"instanceId":"123","apiKey":"synthetic-key"}}}`,
			want: `{"endpoint":"https://otlp.example.com","secret":{"name":"grafana","value":"[REDACTED]"}}`,
		},
		{
			name: "telemetry update-secret masks a string secret value",
			body: `{"secret":{"name":"datadog","value":"synthetic-token"}}`,
			want: `{"secret":{"name":"datadog","value":"[REDACTED]"}}`,
		},
		{
			name: "function secrets list masks every value",
			body: `{"secrets":[{"name":"a","value":"one"},{"name":"b","value":{"k":"two"}}]}`,
			want: `{"secrets":[{"name":"a","value":"[REDACTED]"},{"name":"b","value":"[REDACTED]"}]}`,
		},
		{
			name: "nested secret key is masked",
			body: `{"function":{"secrets":[{"name":"a","value":"one"}]},"n":12345678901234567890}`,
			want: `{"function":{"secrets":[{"name":"a","value":"[REDACTED]"}]},"n":12345678901234567890}`,
		},
		{
			name: "non-string name is masked",
			body: `{"secret":{"name":{"x":"y"},"value":"v"}}`,
			want: `{"secret":{"name":"[REDACTED]","value":"[REDACTED]"}}`,
		},
		{
			name: "scalar secret is masked",
			body: `{"secret":"synthetic-token"}`,
			want: `{"secret":"[REDACTED]"}`,
		},
		{
			name: "body without secrets is logged unchanged",
			body: `{"name":"fn","inferenceUrl":"/v1/<path>"}`,
			want: `{"name":"fn","inferenceUrl":"/v1/<path>"}`,
		},
		{
			name: "non-JSON body is omitted",
			body: `secret=synthetic-token`,
			want: `[22 bytes, not JSON, omitted]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redactRequestBody([]byte(tt.body)))
		})
	}
}

func TestDebugTransportRedactsSecretRequestBody(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	var received []byte
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		received, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})

	payload := UpdateTelemetrySecretRequest{Secret: SecretDto{
		Name:  "grafana",
		Value: map[string]string{"instanceId": "123", "apiKey": "synthetic-grafana-key"},
	}}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		"http://api.example.com/v2/nvcf/secrets/telemetries/tel-1", bytes.NewReader(data))
	require.NoError(t, err)

	resp, err := newDebugTransport(base).RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Contains(t, string(received), "synthetic-grafana-key", "the request sent upstream must keep the secret")
	out := logs.String()
	assert.Contains(t, out, "Request Body:")
	assert.Contains(t, out, `"name":"grafana"`)
	assert.Contains(t, out, redactedValue)
	assert.False(t, strings.Contains(out, "synthetic-grafana-key"), "debug log leaked the secret:\n%s", out)
	assert.False(t, strings.Contains(out, `"instanceId":"123"`), "debug log leaked the secret:\n%s", out)
}
