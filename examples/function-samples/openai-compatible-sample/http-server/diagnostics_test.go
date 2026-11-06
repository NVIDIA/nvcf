// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMessagesDiagnosticsOffByDefault(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := responseMap(t, recorder)["x_backend"]; ok {
		t.Fatalf("x_backend present without the control: %s", recorder.Body.String())
	}
	if got := recorder.Header().Get(replicaHeader); got != "" {
		t.Fatalf("%s = %q without the control", replicaHeader, got)
	}
}

func TestMessagesDiagnosticsReportsReplicaAndHeaders(t *testing.T) {
	t.Setenv("NVCF_INSTANCE_ID", "0-sr-test-replica")
	t.Setenv("NVCF_FUNCTION_ID", "fn-id")
	t.Setenv("NVCF_FUNCTION_VERSION_ID", "ver-id")
	recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"x_load_tester_echo_diagnostics":true}`, map[string]string{
		"X-Test-Header": "spoofed",
		"X-Api-Key":     "synthetic-secret",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(replicaHeader); got != "0-sr-test-replica" {
		t.Fatalf("%s = %q, want 0-sr-test-replica", replicaHeader, got)
	}
	if got := recorder.Header().Get(functionVersionHeader); got != "ver-id" {
		t.Fatalf("%s = %q, want ver-id", functionVersionHeader, got)
	}
	body := responseMap(t, recorder)
	backend, ok := body["x_backend"].(map[string]any)
	if !ok {
		t.Fatalf("x_backend = %#v, want object", body["x_backend"])
	}
	if backend["replica"] != "0-sr-test-replica" || backend["function_id"] != "fn-id" || backend["path"] != "/v1/messages" || backend["method"] != "POST" {
		t.Fatalf("x_backend = %#v", backend)
	}
	headers, ok := backend["received_headers"].(map[string]any)
	if !ok {
		t.Fatalf("received_headers = %#v, want object", backend["received_headers"])
	}
	if got, ok := headers["x-test-header"].([]any); !ok || len(got) != 1 || got[0] != "spoofed" {
		t.Fatalf("received_headers[x-test-header] = %#v", headers["x-test-header"])
	}
	if got, ok := headers["x-api-key"].([]any); !ok || len(got) != 1 || got[0] != "<redacted>" {
		t.Fatalf("received_headers[x-api-key] = %#v, want [\"<redacted>\"]", headers["x-api-key"])
	}
}

func TestMessagesDiagnosticsFallsBackToHostname(t *testing.T) {
	t.Setenv("NVCF_INSTANCE_ID", "")
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname: %v", err)
	}
	if got := replicaID(); got != host {
		t.Fatalf("replicaID() = %q, want hostname %q", got, host)
	}
}

func TestMessagesDiagnosticsStreamStart(t *testing.T) {
	t.Setenv("NVCF_INSTANCE_ID", "0-sr-test-replica")
	recorder := newFlushCountingRecorder()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages",
		strings.NewReader(messagesBody+`,"stream":true,"x_load_tester_echo_diagnostics":true}`))
	request.Header.Set("Content-Type", "application/json")
	newRouter().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(replicaHeader); got != "0-sr-test-replica" {
		t.Fatalf("%s = %q on the stream", replicaHeader, got)
	}
	start := strings.SplitN(recorder.Body.String(), "event: content_block_start", 2)[0]
	if !strings.Contains(start, `"x_backend"`) || !strings.Contains(start, `"replica":"0-sr-test-replica"`) {
		t.Fatalf("message_start lacks x_backend: %s", start)
	}
}
