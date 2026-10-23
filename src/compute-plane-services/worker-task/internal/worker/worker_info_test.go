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

package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/health"
	taskmetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

func TestInfoEndpoint(t *testing.T) {
	originalVersion, originalCommit := version.Version, version.GitHash
	t.Cleanup(func() {
		version.Version, version.GitHash = originalVersion, originalCommit
	})

	for _, test := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "stamped", version: "1.2.3", want: "1.2.3"},
		{name: "unstamped", want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			version.Version = test.version
			version.GitHash = "0123456789abcdef0123456789abcdef01234567"
			mux := infoTestMux(health.New())
			for _, target := range []string{"/info", "/info?verbose=true"} {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

				require.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
				var body map[string]string
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, map[string]string{
					"service": "nvcf-worker-task",
					"version": test.want,
					"commit":  version.GitHash,
				}, body)
			}
		})
	}
}

func TestInfoEndpointRejectsNonGET(t *testing.T) {
	mux := infoTestMux(health.New())
	for _, method := range []string{
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodTrace,
		http.MethodConnect,
		"CUSTOM",
	} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/info", nil)
			if method == http.MethodOptions {
				req.Header.Set("Origin", "https://example.com")
				req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
			assert.Empty(t, rec.Body.String())
		})
	}
}

func TestInfoEndpointPreservesReadiness(t *testing.T) {
	ready := health.New()
	mux := infoTestMux(ready)
	for _, test := range []struct {
		status pb.ExecutionStatus
		want   int
	}{
		{status: pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING, want: http.StatusServiceUnavailable},
		{status: pb.ExecutionStatus_RUNNING, want: http.StatusOK},
	} {
		ready.SetExecutionStatus(test.status)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health/ready", nil))
		assert.Equal(t, test.want, rec.Code)
		assert.Empty(t, rec.Body.String())
	}
}

func TestInfoEndpointMetrics(t *testing.T) {
	mux := infoTestMux(health.New())
	for _, test := range []struct {
		method string
		code   string
	}{
		{method: http.MethodGet, code: "200"},
		{method: http.MethodPost, code: "405"},
	} {
		t.Run(test.method, func(t *testing.T) {
			metric := taskmetrics.InfoRequestDuration.WithLabelValues(test.code).(prometheus.Metric)
			var before, after dto.Metric
			require.NoError(t, metric.Write(&before))
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), test.method, "/info", nil))
			require.NoError(t, metric.Write(&after))
			assert.Equal(t, before.GetHistogram().GetSampleCount()+1, after.GetHistogram().GetSampleCount())
			assert.Greater(t, after.GetHistogram().GetSampleSum(), before.GetHistogram().GetSampleSum())
		})
	}
}

func infoTestMux(ready http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	for path, handler := range additionalHTTPHandlers(ready) {
		mux.Handle(path, handler)
	}
	return mux
}
