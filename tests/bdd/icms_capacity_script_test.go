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

package bdd_tmp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestICMSCapacityScript waits for the control-plane admission predicate and
// fails promptly on authorization or response errors without exposing tokens.
func TestICMSCapacityScript(t *testing.T) {
	const ready = `[{"clusterName":"local-compute","region":"us-west-1","gpus":[{"name":"H100","instanceTypes":[{"name":"NCP.GPU.H100_1x"}]}],"status":"READY","gpuUsage":{"H100":{"available":1}}}]`
	const exhausted = `[{"clusterName":"local-compute","region":"us-west-1","gpus":[{"name":"H100","instanceTypes":[{"name":"NCP.GPU.H100_1x"}]}],"status":"READY","gpuUsage":{"H100":{"available":0}}}]`
	const unhealthy = `[{"clusterName":"local-compute","region":"us-west-1","gpus":[{"name":"H100","instanceTypes":[{"name":"NCP.GPU.H100_1x"}]}],"status":"UNHEALTHY","gpuUsage":{"H100":{"available":4}}}]`
	for _, tc := range []struct {
		name              string
		status            int
		bodies            []string
		dripBody          bool
		truncateFirstBody bool
		wantError         string
		wantCalls         int32
	}{
		{name: "ready", bodies: []string{ready}, wantCalls: 1},
		{name: "delayed heartbeat", bodies: []string{exhausted, ready}, wantCalls: 2},
		{name: "incomplete response retried", bodies: []string{`[{"status":"test-admin-token"`, ready}, truncateFirstBody: true, wantCalls: 2},
		{name: "response exceeds deadline", bodies: []string{ready}, dripBody: true, wantError: "before timeout", wantCalls: 1},
		{name: "capacity timeout", bodies: []string{exhausted}, wantError: "before timeout"},
		{name: "unhealthy capacity", bodies: []string{unhealthy}, wantError: "before timeout"},
		{name: "missing cluster", bodies: []string{`[]`, ready}, wantCalls: 2},
		{name: "wrong cluster", bodies: []string{strings.ReplaceAll(ready, "local-compute", "other-compute")}, wantError: "before timeout"},
		{name: "unexpected success status", status: http.StatusCreated, bodies: []string{ready}, wantError: "HTTP 201", wantCalls: 1},
		{name: "redirect rejected", status: http.StatusFound, bodies: []string{"test-admin-token"}, wantError: "HTTP 302", wantCalls: 1},
		{name: "authorization failure", status: http.StatusForbidden, bodies: []string{"test-admin-token"}, wantError: "HTTP 403", wantCalls: 1},
		{name: "authentication failure", status: http.StatusUnauthorized, bodies: []string{"test-admin-token"}, wantError: "HTTP 401", wantCalls: 1},
		{name: "server failure", status: http.StatusServiceUnavailable, bodies: []string{"test-admin-token"}, wantError: "before timeout"},
		{name: "invalid response", bodies: []string{"not JSON test-admin-token"}, wantError: "not valid JSON", wantCalls: 1},
		{name: "invalid cluster list", bodies: []string{`{"token":"test-admin-token"}`}, wantError: "must be a cluster list", wantCalls: 1},
		{name: "invalid cluster entry", bodies: []string{`["test-admin-token"]`}, wantError: "must be a cluster list", wantCalls: 1},
		{name: "missing status", bodies: []string{strings.ReplaceAll(ready, `"status":"READY",`, "")}, wantError: "invalid status", wantCalls: 1},
		{name: "untrusted status", bodies: []string{strings.ReplaceAll(ready, `"status":"READY"`, `"status":"test-admin-token"`)}, wantError: "before timeout"},
		{name: "wrong region", bodies: []string{strings.ReplaceAll(ready, "us-west-1", "us-east-1")}, wantError: "unexpected region", wantCalls: 1},
		{name: "wrong GPU", bodies: []string{strings.ReplaceAll(ready, "H100", "A100")}, wantError: "does not register", wantCalls: 1},
		{name: "wrong instance type", bodies: []string{strings.ReplaceAll(ready, "NCP.GPU.H100_1x", "NCP.GPU.H100_2x")}, wantError: "does not register", wantCalls: 1},
		{name: "invalid capacity schema", bodies: []string{strings.ReplaceAll(ready, `"available":1`, `"available":"1"`)}, wantError: "invalid available", wantCalls: 1},
		{name: "boolean capacity", bodies: []string{strings.ReplaceAll(ready, `"available":1`, `"available":true`)}, wantError: "invalid available", wantCalls: 1},
		{name: "negative capacity", bodies: []string{strings.ReplaceAll(ready, `"available":1`, `"available":-1`)}, wantError: "invalid available", wantCalls: 1},
		{name: "ambiguous cluster", bodies: []string{strings.TrimSuffix(ready, "]") + "," + strings.TrimPrefix(ready, "[")}, wantError: "multiple clusters", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v1/accounts/test-account/clusters" ||
					r.Header.Get("Authorization") != "Bearer test-admin-token" {
					http.Error(w, "unexpected capacity request", http.StatusBadRequest)
					return
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				if status == http.StatusFound {
					w.Header().Set("Location", "/redirect-target")
				}
				i := min(int(n)-1, len(tc.bodies)-1)
				if tc.truncateFirstBody && n == 1 {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.bodies[i])+1))
				}
				w.WriteHeader(status)
				if tc.dripBody {
					for offset := 0; offset < len(tc.bodies[i]); offset += 16 {
						if _, err := fmt.Fprint(w, tc.bodies[i][offset:min(offset+16, len(tc.bodies[i]))]); err != nil {
							return
						}
						w.(http.Flusher).Flush()
						time.Sleep(20 * time.Millisecond)
					}
					return
				}
				_, _ = fmt.Fprint(w, tc.bodies[i])
			}))
			defer server.Close()
			state := filepath.Join(t.TempDir(), "cli.state")
			if err := os.WriteFile(state, []byte(`{"token":"test-admin-token"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "scripts/wait-icms-capacity.py",
				"--url", server.URL+"/v1/accounts/test-account/clusters",
				"--state-file", state, "--cluster", "local-compute", "--gpu", "H100",
				"--region", "us-west-1", "--instance-type", "NCP.GPU.H100_1x",
				"--minimum-available", "1", "--timeout", "0.15", "--interval", "0.01")
			output, err := cmd.CombinedOutput()
			if tc.wantError == "" {
				if err != nil || !strings.Contains(string(output), "prerequisite satisfied") {
					t.Fatalf("capacity wait failed: %v, output %s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tc.wantError) {
				t.Fatalf("capacity wait error = %v, output %s, want %q", err, output, tc.wantError)
			}
			if strings.Contains(string(output), "test-admin-token") {
				t.Fatal("capacity wait exposed the token or untrusted response body")
			}
			if tc.truncateFirstBody && !strings.Contains(string(output), "ICMS capacity read pending: transport unavailable") {
				t.Fatal("incomplete capacity response was not reported as a transport failure")
			}
			if got := calls.Load(); got == 0 || (tc.wantCalls != 0 && got != tc.wantCalls) {
				t.Fatalf("HTTP requests = %d, want %d (zero means at least one)", got, tc.wantCalls)
			}
		})
	}
}

func assertFunctionDeploymentsWaitForICMSCapacity(t *testing.T, runs []string) {
	t.Helper()
	deployments := 0
	for i, command := range runs {
		if !strings.Contains(command, " function deploy create ") {
			continue
		}
		deployments++
		if i == 0 || !strings.HasPrefix(runs[i-1], "python3 tests/bdd/scripts/wait-icms-capacity.py ") ||
			!strings.Contains(runs[i-1], "--cluster ncp-local-compute-1 --gpu H100 --region us-west-1 --instance-type NCP.GPU.H100_1x --minimum-available 1 --timeout 600 --interval 5") {
			t.Fatalf("function deployment lacks its bounded ICMS capacity prerequisite: %s", command)
		}
	}
	if deployments == 0 {
		t.Fatal("no function deployments exercised the capacity prerequisite")
	}
}
