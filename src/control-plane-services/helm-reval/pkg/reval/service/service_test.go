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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/render"
	"github.com/stretchr/testify/assert"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval"
)

func TestErrsToStrs(t *testing.T) {
	tests := []struct {
		name string
		errs []error
		want []string
	}{
		{
			name: "nil errors",
			errs: nil,
			want: []string{},
		},
		{
			name: "empty errors",
			errs: []error{},
			want: []string{},
		},
		{
			name: "single error",
			errs: []error{errors.New("error 1")},
			want: []string{"error 1"},
		},
		{
			name: "multiple errors",
			errs: []error{errors.New("error 1"), errors.New("error 2")},
			want: []string{"error 1", "error 2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errsToStrs(tt.errs)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMakeReValConfig tests the makeReValConfig function with special cases:
// in HelmChartServiceName or HelmChartServicePort are empty we skip them and the
// health endpoint.
func TestMakeReValConfig(t *testing.T) {
	fullRequest := &RevalRequest{
		HelmChart:                   "oci://chart",
		Namespace:                   "sr-1234",
		ReleaseName:                 "mini-service",
		Configuration:               json.RawMessage(`{"key": "value"}`),
		K8SVersion:                  "1.25",
		ApiVersions:                 []string{"v1", "apps/v1"},
		ApiKey:                      "test-api-key",
		HelmRegistryAuthConfig:      common.RegistryAuthConfig{},
		ImageRegistryAuthConfig:     common.RegistryAuthConfig{},
		HelmChartServiceName:        "my-service",
		HelmChartServicePort:        8080,
		HelmChartHTTPHealthEndpoint: "/healthz",
	}

	// By value to copy later
	miniConfig := reval.Config{
		ChartURL:                fullRequest.HelmChart,
		Namespace:               fullRequest.Namespace,
		ReleaseName:             fullRequest.ReleaseName,
		Values:                  fullRequest.Configuration,
		K8sVersion:              fullRequest.K8SVersion,
		APIVersions:             fullRequest.ApiVersions,
		NGCAPIKey:               fullRequest.ApiKey,
		HelmRegistryAuthConfig:  fullRequest.HelmRegistryAuthConfig,
		ImageRegistryAuthConfig: fullRequest.ImageRegistryAuthConfig,
	}

	// Copy by value
	fullConfig := miniConfig
	fullConfig.TargetServiceName = fullRequest.HelmChartServiceName
	fullConfig.TargetServicePort = fullRequest.HelmChartServicePort
	fullConfig.TargetHTTPHealthEndpoint = fullRequest.HelmChartHTTPHealthEndpoint

	tests := []struct {
		name    string
		req     func(RevalRequest) *RevalRequest
		wantCfg reval.Config
	}{
		{
			name:    "all fields provided",
			req:     func(r RevalRequest) *RevalRequest { return &r },
			wantCfg: fullConfig,
		},
		{
			name: "service name provided, port is zero",
			req: func(r RevalRequest) *RevalRequest {
				r.HelmChartServicePort = 0
				return &r
			},
			wantCfg: miniConfig,
		},
		{
			name: "service port provided, name is empty",
			req: func(r RevalRequest) *RevalRequest {
				r.HelmChartServiceName = ""
				return &r
			},
			wantCfg: miniConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCfg := makeReValConfig(tt.req(*fullRequest))
			assert.Equal(t, tt.wantCfg, gotCfg)
		})
	}
}

// MockRevalRunner is a mock implementation of RevalRunner.
type MockRevalRunner struct {
	RunFunc func(ctx context.Context, cfg reval.Config, out io.Writer) (reval.Result, error)
	Cfg     reval.Config
	DidRun  bool
	Error   error
	Result  *reval.Result
}

// Run calls the underlying RunFunc.
func (m *MockRevalRunner) Run(ctx context.Context, cfg reval.Config, out io.Writer) (reval.Result, error) {
	m.DidRun = true
	m.Cfg = cfg
	if m.Error != nil {
		return reval.Result{}, m.Error
	}
	if m.Result != nil {
		return *m.Result, nil
	}
	if out != nil {
		out.Write([]byte(`["spam", "eggs"]`))
	}

	return reval.Result{Valid: true}, nil
}

const valPolStr = `{"id": "foo", "name": "Default", "allowedExtraKubernetesTypes": [{"group": "foo.com", "version": "v1", "kind": "Foo"}]}`

// Probably the TestNewHttpService can be simplified further
func TestNewHttpService_Validate(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    string
		result         *reval.Result
		err            error
		expectedStatus int
		expectedCfg    reval.Config
		expectedBody   string
		expectedDidRun bool
	}{
		{
			name:        "valid request - validation success",
			requestBody: `{"helmChart": "oci://chart", "configuration": {"key": "value"}}`,
			expectedCfg: reval.Config{
				ChartURL: "oci://chart",
				Values:   json.RawMessage(`{"key": "value"}`),
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"valid":true,"validationErrors":[]}`,
			expectedDidRun: true,
		},
		{
			name:        "valid request - validation success with policy",
			requestBody: `{"helmChart": "oci://chart", "configuration": {"key": "value"}, "validationPolicies": [` + valPolStr + `]}`,
			expectedCfg: reval.Config{
				ChartURL: "oci://chart",
				Values:   json.RawMessage(`{"key": "value"}`),
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"valid":true,"validationErrors":[]}`,
			expectedDidRun: true,
		},
		{
			name:           "valid request - validation failure",
			requestBody:    `{"helmChart": "oci://chart", "configuration": {"key": "value"}}`,
			result:         &reval.Result{Valid: false, ValidationErrors: []error{errors.New("val error")}},
			err:            errors.New("internal error"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   `{"error":"internal error"}`,
			expectedDidRun: true,
		},
		{
			name:           "invalid request - bad json format",
			requestBody:    `{"helmChart": "oci://chart", "configuration": {"key": "value"}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Error parsing JSON", // Error from render.Bind due to malformed JSON
			expectedDidRun: false,
		},
	}

	// Add another dimension for calling Render and Validate in another inner loop

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRunner := &MockRevalRunner{Result: tt.result, Error: tt.err}
			service := NewHttpService(mockRunner, metricnoop.NewMeterProvider().Meter("foobar"))

			req := httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			// For render.Bind to work correctly with ReValRequest.Bind
			req = req.WithContext(context.WithValue(req.Context(), render.ContentTypeCtxKey, "application/json"))

			rr := httptest.NewRecorder()

			service.Validate.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code, "status code mismatch")
			responseBody := strings.TrimSpace(rr.Body.String())

			if tt.expectedStatus == http.StatusOK {
				assert.JSONEq(t, tt.expectedBody, responseBody, "body mismatch for OK status")
			} else {
				assert.Contains(t, responseBody, tt.expectedBody, "body mismatch for error status")
			}
			assert.Equal(t, tt.expectedDidRun, mockRunner.DidRun, "did run mismatch")
		})
	}
}

func TestNewHttpService_Render(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    string
		result         *reval.Result
		err            error
		expectedStatus int
		expectedCfg    reval.Config
		expectedBody   string
		expectedDidRun bool
	}{
		{
			name:        "valid request - validation success",
			requestBody: `{"helmChart": "oci://chart", "configuration": {"key": "value"}}`,
			expectedCfg: reval.Config{
				ChartURL: "oci://chart",
				Values:   json.RawMessage(`{"key": "value"}`),
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"valid":true,"validationErrors":[],"output":["spam","eggs"]}`,
			expectedDidRun: true,
		},
		{
			name:        "valid request - validation success with policy",
			requestBody: `{"helmChart": "oci://chart", "configuration": {"key": "value"}, "validationPolicy": ` + valPolStr + `}`,
			expectedCfg: reval.Config{
				ChartURL: "oci://chart",
				Values:   json.RawMessage(`{"key": "value"}`),
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"valid":true,"validationErrors":[],"output":["spam","eggs"]}`,
			expectedDidRun: true,
		},
		{
			name:           "valid request - validation failure",
			requestBody:    `{"helmChart": "oci://chart", "configuration": {"key": "value"}}`,
			result:         &reval.Result{Valid: false, ValidationErrors: []error{errors.New("val error")}},
			err:            errors.New("internal error"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   `{"error":"internal error"}`,
			expectedDidRun: true,
		},
		{
			name:           "invalid request - bad json format",
			requestBody:    `{"helmChart": "oci://chart", "configuration": {"key": "value"}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Error parsing JSON", // Error from render.Bind due to malformed JSON
			expectedDidRun: false,
		},
	}

	// Add another dimension for calling Render and Validate in another inner loop

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRunner := &MockRevalRunner{Result: tt.result, Error: tt.err}
			service := NewHttpService(mockRunner, metricnoop.NewMeterProvider().Meter("foobar"))

			req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			// For render.Bind to work correctly with ReValRequest.Bind
			req = req.WithContext(context.WithValue(req.Context(), render.ContentTypeCtxKey, "application/json"))

			rr := httptest.NewRecorder()

			service.Render.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code, "status code mismatch")
			responseBody := strings.TrimSpace(rr.Body.String())

			if tt.expectedStatus == http.StatusOK {
				assert.JSONEq(t, tt.expectedBody, responseBody, "body mismatch for OK status")
			} else {
				assert.Contains(t, responseBody, tt.expectedBody, "body mismatch for error status")
			}
			assert.Equal(t, tt.expectedDidRun, mockRunner.DidRun, "did run mismatch")
		})
	}
}
