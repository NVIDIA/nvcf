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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvcf-cli/internal/state"

	"github.com/spf13/viper"
)

func ptrInt(v int) *int { return &v }

type invokeRequestCaptureTransport struct {
	req *http.Request
}

func (t *invokeRequestCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

type updateRequestCaptureTransport struct {
	req  *http.Request
	body []byte
}

func (t *updateRequestCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.req = req
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.body = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

type invokeFunctionDetailsTransport struct {
	t              *testing.T
	functionType   string
	inferenceURL   string
	detailsStatus  int
	invocationReq  *http.Request
	invocationBody []byte
	functionDetail *http.Request
}

func (t *invokeFunctionDetailsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/v2/nvcf/functions/"):
		t.functionDetail = req
		status := t.detailsStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			return &http.Response{
				StatusCode: status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"message":"details failed"}`)),
				Request:    req,
			}, nil
		}
		body, err := json.Marshal(struct {
			Function FunctionDto `json:"function"`
		}{
			Function: FunctionDto{
				ID:           "func-123",
				VersionID:    "ver-456",
				FunctionType: t.functionType,
				InferenceURL: t.inferenceURL,
			},
		})
		if err != nil {
			t.t.Fatalf("marshal function details: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	case req.Method == http.MethodPost:
		t.invocationReq = req
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.t.Fatalf("read invocation body: %v", err)
		}
		t.invocationBody = body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	default:
		t.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		return nil, nil
	}
}

func TestBaseHTTPURLHost(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"simple http", "http://elb.example.com", "elb.example.com"},
		{"https with path", "https://api.nvcf.nvidia.com/v2", "api.nvcf.nvidia.com"},
		{"with port", "http://elb.example.com:8080", "elb.example.com:8080"},
		{"with port and path", "http://elb.example.com:8080/foo", "elb.example.com:8080"},
		{"malformed returns empty", "://broken", ""},
		{"no scheme returns empty host", "elb.example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := baseHTTPURLHost(tt.in); got != tt.want {
				t.Errorf("baseHTTPURLHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInvokeFunctionWithOptionsUsesFunctionHostnameRouting(t *testing.T) {
	tests := []struct {
		name          string
		baseInvokeURL string
		inferenceURL  string
		invokeHost    string
		wantURL       string
		wantHost      string
	}{
		{
			name:          "invocation host gets function prefix",
			baseInvokeURL: "https://invocation.example.com",
			inferenceURL:  "/echo",
			wantURL:       "https://func-123.invocation.example.com/echo",
			wantHost:      "func-123.invocation.example.com",
		},
		{
			name:          "api host gets function invocation domain",
			baseInvokeURL: "https://api.example.com",
			inferenceURL:  "/echo",
			wantURL:       "https://func-123.invocation.example.com/echo",
			wantHost:      "func-123.invocation.example.com",
		},
		{
			name:          "preserves port and base path",
			baseInvokeURL: "http://invocation.example.com:8080/base",
			inferenceURL:  "echo",
			wantURL:       "http://func-123.invocation.example.com:8080/base/echo",
			wantHost:      "func-123.invocation.example.com:8080",
		},
		{
			name:          "self hosted host header keeps gateway transport host",
			baseInvokeURL: "http://127.0.0.1:8080",
			inferenceURL:  "/echo",
			invokeHost:    "invocation.localhost",
			wantURL:       "http://127.0.0.1:8080/echo",
			wantHost:      "func-123.invocation.localhost",
		},
		{
			name:          "self hosted elb host header keeps elb transport host",
			baseInvokeURL: "http://elb.example.com",
			inferenceURL:  "/echo",
			invokeHost:    "invocation.elb.example.com",
			wantURL:       "http://elb.example.com/echo",
			wantHost:      "func-123.invocation.elb.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &invokeRequestCaptureTransport{}
			client := &Client{
				config: &Config{
					BaseInvokeURL: tt.baseInvokeURL,
					InvokeHost:    tt.invokeHost,
				},
				httpClient: &http.Client{Transport: capture},
			}

			_, err := client.InvokeFunctionWithOptions(context.Background(), "func-123", "ver-456", map[string]interface{}{"message": "hello"}, 0, &InvokeFunctionOptions{
				InferenceURL:        tt.inferenceURL,
				PollDurationSeconds: 10,
			})
			if err != nil {
				t.Fatalf("InvokeFunctionWithOptions returned error: %v", err)
			}
			if capture.req == nil {
				t.Fatal("expected invocation request to be captured")
			}
			if got := capture.req.URL.String(); got != tt.wantURL {
				t.Fatalf("request URL = %q, want %q", got, tt.wantURL)
			}
			if got := capture.req.Host; got != tt.wantHost {
				t.Fatalf("request Host = %q, want %q", got, tt.wantHost)
			}
			if got := capture.req.Header.Get("function-id"); got != "" {
				t.Fatalf("function-id header = %q, want empty", got)
			}
			if got := capture.req.Header.Get("function-version-id"); got != "" {
				t.Fatalf("function-version-id header = %q, want empty", got)
			}
			if got := capture.req.Header.Get("NVCF-POLL-SECONDS"); got != "10" {
				t.Fatalf("NVCF-POLL-SECONDS = %q, want 10", got)
			}
		})
	}
}

func TestInvokeFunctionRoutesLLMFunctionsThroughLLMGateway(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:            t,
		functionType: "LLM",
		inferenceURL: "/health",
	}
	client := &Client{
		config: &Config{
			BaseHTTPURL:   "http://api.localhost:8080",
			BaseInvokeURL: "http://127.0.0.1:8080",
			InvokeHost:    "invocation.localhost",
		},
		baseURL:    "http://api.localhost:8080",
		httpClient: &http.Client{Transport: capture},
	}

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"stream": true},
		0,
		&InvokeFunctionOptions{InferenceURL: "/v1/chat/completions", ModelName: "dummy-model"},
	)
	if err != nil {
		t.Fatalf("InvokeFunctionWithOptions returned error: %v", err)
	}
	if capture.functionDetail == nil {
		t.Fatal("expected function details request")
	}
	if capture.invocationReq == nil {
		t.Fatal("expected invocation request")
	}
	if got, want := capture.invocationReq.URL.String(), "http://127.0.0.1:8080/v1/chat/completions"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
	if got, want := capture.invocationReq.Host, "llm.localhost"; got != want {
		t.Fatalf("request Host = %q, want %q", got, want)
	}
	assertInvocationBodyField(t, capture.invocationBody, "model", "func-123/dummy-model")
}

func TestInvokeFunctionRoutesLLMFunctionsWithExplicitOpenAIPath(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:            t,
		functionType: "LLM",
		inferenceURL: "/health",
	}
	client := &Client{
		config: &Config{
			BaseHTTPURL:   "http://api.localhost:8080",
			BaseInvokeURL: "http://127.0.0.1:8080",
			InvokeHost:    "invocation.localhost",
		},
		baseURL:    "http://api.localhost:8080",
		httpClient: &http.Client{Transport: capture},
	}

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"input": "hello"},
		0,
		&InvokeFunctionOptions{InferenceURL: "/v1/embeddings", ModelName: "dummy-model"},
	)
	if err != nil {
		t.Fatalf("InvokeFunctionWithOptions returned error: %v", err)
	}
	if capture.functionDetail == nil {
		t.Fatal("expected function details request")
	}
	if capture.invocationReq == nil {
		t.Fatal("expected invocation request")
	}
	if got, want := capture.invocationReq.URL.String(), "http://127.0.0.1:8080/v1/embeddings"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
	if got, want := capture.invocationReq.Host, "llm.localhost"; got != want {
		t.Fatalf("request Host = %q, want %q", got, want)
	}
	assertInvocationBodyField(t, capture.invocationBody, "model", "func-123/dummy-model")
}

func TestInvokeFunctionOverridesRequestBodyModelForLLMFunctions(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:            t,
		functionType: "LLM",
		inferenceURL: "/health",
	}
	client := &Client{
		config: &Config{
			BaseHTTPURL:   "http://api.localhost:8080",
			BaseInvokeURL: "http://127.0.0.1:8080",
			InvokeHost:    "invocation.localhost",
		},
		baseURL:    "http://api.localhost:8080",
		httpClient: &http.Client{Transport: capture},
	}
	var logOutput bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
	})

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"model": "user-supplied-model", "input": "hello"},
		0,
		&InvokeFunctionOptions{InferenceURL: "/v1/embeddings", ModelName: "dummy-model"},
	)
	if err != nil {
		t.Fatalf("InvokeFunctionWithOptions returned error: %v", err)
	}

	assertInvocationBodyField(t, capture.invocationBody, "model", "func-123/dummy-model")
	if got := logOutput.String(); !strings.Contains(got, "WARNING: request body model") {
		t.Fatalf("log output = %q, want request body model warning", got)
	}
}

func TestInvokeFunctionRequiresModelNameForLLMFunctions(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:            t,
		functionType: "LLM",
		inferenceURL: "/health",
	}
	client := &Client{
		config: &Config{
			BaseHTTPURL:   "http://api.localhost:8080",
			BaseInvokeURL: "http://127.0.0.1:8080",
			InvokeHost:    "invocation.localhost",
		},
		baseURL:    "http://api.localhost:8080",
		httpClient: &http.Client{Transport: capture},
	}

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"stream": true},
		0,
		&InvokeFunctionOptions{InferenceURL: "/v1/chat/completions"},
	)
	if err == nil {
		t.Fatal("expected model-name required error")
	}
	if !strings.Contains(err.Error(), "model-name is required") {
		t.Fatalf("error = %q, want model-name required", err.Error())
	}
	if capture.invocationReq != nil {
		t.Fatal("unexpected invocation request")
	}
}

func TestInvokeFunctionRequiresInferenceURLForLLMFunctions(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:            t,
		functionType: "LLM",
		inferenceURL: "/health",
	}
	client := &Client{
		config: &Config{
			BaseHTTPURL:   "http://api.localhost:8080",
			BaseInvokeURL: "http://127.0.0.1:8080",
			InvokeHost:    "invocation.localhost",
		},
		baseURL:    "http://api.localhost:8080",
		httpClient: &http.Client{Transport: capture},
	}

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"stream": true},
		0,
		&InvokeFunctionOptions{ModelName: "dummy-model"},
	)
	if err == nil {
		t.Fatal("expected inference-url required error")
	}
	if !strings.Contains(err.Error(), "inference-url is required") {
		t.Fatalf("error = %q, want inference-url required", err.Error())
	}
	if capture.invocationReq != nil {
		t.Fatal("unexpected invocation request")
	}
}

func TestInvokeFunctionFallsBackToExplicitPathWhenDetailsLookupFails(t *testing.T) {
	capture := &invokeFunctionDetailsTransport{
		t:             t,
		detailsStatus: http.StatusInternalServerError,
	}
	client := &Client{
		config: &Config{
			BaseInvokeURL: "https://invocation.example.com",
		},
		httpClient: &http.Client{Transport: capture},
	}
	var logOutput bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
	})

	_, err := client.InvokeFunctionWithOptions(
		context.Background(),
		"func-123",
		"ver-456",
		map[string]interface{}{"message": "hello"},
		0,
		&InvokeFunctionOptions{InferenceURL: "/echo", ModelName: "dummy-model"},
	)
	if err != nil {
		t.Fatalf("InvokeFunctionWithOptions returned error: %v", err)
	}
	if capture.functionDetail == nil {
		t.Fatal("expected function details request")
	}
	if capture.invocationReq == nil {
		t.Fatal("expected invocation request")
	}
	if got, want := capture.invocationReq.URL.String(), "https://func-123.invocation.example.com/echo"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
	assertInvocationBodyFieldAbsent(t, capture.invocationBody, "model")
	if got := logOutput.String(); !strings.Contains(got, "WARNING: model-name ignored") {
		t.Fatalf("log output = %q, want model-name ignored warning", got)
	}
}

func TestUpdateFunctionMetadataSendsModelUpdatesToFunctionEndpoint(t *testing.T) {
	routingMethod := "round_robin"
	tokenRateLimit := "1000-M"
	capture := &updateRequestCaptureTransport{}
	client := &Client{
		config:     &Config{Token: "token"},
		baseURL:    "https://api.example.com",
		httpClient: &http.Client{Transport: capture},
	}

	err := client.UpdateFunctionMetadata(context.Background(), "func-123", "ver-456", &UpdateFunctionMetadataRequest{
		Tags: []string{"production"},
		ModelUpdates: []ModelUpdateDto{
			{
				ModelName: "dummy-model",
				LLMConfig: &LLMConfigUpdateDto{
					RoutingMethod:  &routingMethod,
					TokenRateLimit: &tokenRateLimit,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateFunctionMetadata returned error: %v", err)
	}
	if capture.req == nil {
		t.Fatal("expected update request")
	}
	if got, want := capture.req.Method, http.MethodPut; got != want {
		t.Fatalf("method = %q, want %q", got, want)
	}
	if got, want := capture.req.URL.Path, "/v2/nvcf/functions/func-123/versions/ver-456"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(capture.body, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	modelUpdates, ok := payload["modelUpdates"].([]interface{})
	if !ok || len(modelUpdates) != 1 {
		t.Fatalf("modelUpdates = %#v, want one update", payload["modelUpdates"])
	}
	modelUpdate := modelUpdates[0].(map[string]interface{})
	if got, want := modelUpdate["modelName"], "dummy-model"; got != want {
		t.Fatalf("modelName = %#v, want %q", got, want)
	}
	llmConfig := modelUpdate["llmConfig"].(map[string]interface{})
	if got, want := llmConfig["routingMethod"], "round_robin"; got != want {
		t.Fatalf("routingMethod = %#v, want %q", got, want)
	}
	if got, want := llmConfig["tokenRateLimit"], "1000-M"; got != want {
		t.Fatalf("tokenRateLimit = %#v, want %q", got, want)
	}
}

func TestLLMInvocationURLDerivesHostFromInvocationURL(t *testing.T) {
	tests := []struct {
		name          string
		baseInvokeURL string
		want          string
	}{
		{
			name:          "invocation domain",
			baseInvokeURL: "https://invocation.example.com",
			want:          "https://llm.invocation.example.com/v1/chat/completions",
		},
		{
			name:          "invocation domain with port",
			baseInvokeURL: "https://invocation.example.com:8443",
			want:          "https://llm.invocation.example.com:8443/v1/chat/completions",
		},
		{
			name:          "api domain",
			baseInvokeURL: "https://api.example.com",
			want:          "https://llm.invocation.example.com/v1/chat/completions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := llmInvocationURL(tt.baseInvokeURL, "/v1/chat/completions")
			if err != nil {
				t.Fatalf("llmInvocationURL returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("llmInvocationURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLLMInvocationHostPreservesPort(t *testing.T) {
	if got, want := llmInvocationHost("invocation.localhost:8080"), "llm.localhost:8080"; got != want {
		t.Fatalf("llmInvocationHost = %q, want %q", got, want)
	}
}

func assertInvocationBodyField(t *testing.T, body []byte, key, want string) {
	t.Helper()

	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal invocation body: %v", err)
	}
	if got[key] != want {
		t.Fatalf("request body %s = %v, want %q", key, got[key], want)
	}
}

func assertInvocationBodyFieldAbsent(t *testing.T, body []byte, key string) {
	t.Helper()

	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal invocation body: %v", err)
	}
	if _, ok := got[key]; ok {
		t.Fatalf("request body unexpectedly has %s = %v", key, got[key])
	}
}

// TestJWTRequirementForWriteOperations tests that all write operations require JWT token
func TestJWTRequirementForWriteOperations(t *testing.T) {
	tests := []struct {
		name          string
		operation     func(*Client) error
		expectedError string
	}{
		{
			name: "CreateFunction requires JWT",
			operation: func(c *Client) error {
				_, err := c.CreateFunction(context.Background(), &CreateFunctionRequest{
					Name:           "test",
					InferenceURL:   "/test",
					ContainerImage: "test:latest",
					InferencePort:  8080,
				})
				return err
			},
			expectedError: "function creation requires NVCF_TOKEN or NVCF_API_KEY with 'register_function' scope",
		},
		{
			name: "DeployFunction requires JWT",
			operation: func(c *Client) error {
				return c.DeployFunction(context.Background(), "func-id", "ver-id", &FunctionDeploymentRequest{
					DeploymentSpecifications: []GPUSpecificationDto{
						{
							GPU:          "L40S",
							InstanceType: "test",
							MinInstances: 0,
							MaxInstances: 1,
						},
					},
				})
			},
			expectedError: "function deployment requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope",
		},
		{
			name: "UpdateFunctionMetadata requires JWT",
			operation: func(c *Client) error {
				return c.UpdateFunctionMetadata(context.Background(), "func-id", "ver-id", &UpdateFunctionMetadataRequest{
					Tags: []string{"test"},
				})
			},
			expectedError: "function update requires NVCF_TOKEN or NVCF_API_KEY with 'update_function' scope",
		},
		{
			name: "DeleteFunction requires JWT",
			operation: func(c *Client) error {
				return c.DeleteFunction(context.Background(), "func-id", "ver-id")
			},
			expectedError: "function deletion requires NVCF_TOKEN or NVCF_API_KEY with 'delete_function' scope",
		},
		{
			name: "UpdateGpuSpecification requires JWT",
			operation: func(c *Client) error {
				_, err := c.UpdateGpuSpecification(context.Background(), "dep-id", "spec-id", &UpdateGpuSpecificationRequest{
					MaxInstances: ptrInt(2),
				})
				return err
			},
			expectedError: "deployment update requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope",
		},
		{
			name: "DeleteDeployment requires JWT",
			operation: func(c *Client) error {
				return c.DeleteDeployment(context.Background(), "func-id", "ver-id", false)
			},
			expectedError: "deployment deletion requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope",
		},
	}

	// Create client without JWT token and without API key
	client := &Client{
		config: &Config{
			APIKey: "", // No API key
			Token:  "", // No JWT token
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.operation(client)
			if err == nil {
				t.Errorf("Expected error but got nil")
				return
			}
			if err.Error() != tt.expectedError {
				t.Errorf("Expected error %q, got %q", tt.expectedError, err.Error())
			}
		})
	}
}

// TestIsAdminOperation tests the isAdminOperation function
func TestIsAdminOperation(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		expected bool
		reason   string
	}{
		// ADMIN OPERATIONS - Should return TRUE (use JWT)
		{
			name:     "Function creation",
			method:   "POST",
			path:     "/v2/nvcf/functions",
			expected: true,
			reason:   "Function creation requires register_function",
		},
		{
			name:     "Function deployment",
			method:   "POST",
			path:     "/v2/nvcf/deployments/functions/id/versions/vid",
			expected: true,
			reason:   "Deployment requires admin:deploy_function",
		},
		{
			name:     "Function deletion",
			method:   "DELETE",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: true,
			reason:   "Deletion requires admin:delete_function",
		},
		{
			name:     "Deployment deletion",
			method:   "DELETE",
			path:     "/v2/nvcf/deployments/functions/id/versions/vid",
			expected: true,
			reason:   "Deployment deletion requires admin:deploy_function",
		},
		{
			name:     "Function update",
			method:   "PUT",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: true,
			reason:   "Function update requires admin:update_function",
		},
		{
			name:     "Deployment update (GPU spec PATCH)",
			method:   "PATCH",
			path:     "/v2/nvcf/deployments/dep-id/gpu-specifications/spec-id",
			expected: true,
			reason:   "Deployment update requires admin:deploy_function",
		},
		{
			name:     "Registry credentials",
			method:   "POST",
			path:     "/v2/nvcf/registry-credentials",
			expected: true,
			reason:   "Registry management requires admin scope",
		},
		{
			name:     "Recognized registries",
			method:   "GET",
			path:     "/v2/nvcf/recognized-registries",
			expected: true,
			reason:   "Registry management requires admin scope",
		},
		{
			name:     "Secret management",
			method:   "PUT",
			path:     "/v2/nvcf/accounts/test/secrets/functions/id/versions/vid",
			expected: true,
			reason:   "Secret management requires admin:update_secrets",
		},
		{
			name:     "List functions admin",
			method:   "GET",
			path:     "/v2/nvcf/functions",
			expected: true,
			reason:   "List all functions requires admin:list_functions",
		},
		{
			name:     "Account management",
			method:   "GET",
			path:     "/v2/nvcf/accounts",
			expected: true,
			reason:   "Account management requires account_setup scope",
		},

		// USER OPERATIONS - Should return FALSE (allow API key)
		{
			name:     "Get function details",
			method:   "GET",
			path:     "/v2/nvcf/functions/id/versions/vid",
			expected: false,
			reason:   "Get function details is a read operation",
		},
		{
			name:     "List function versions",
			method:   "GET",
			path:     "/v2/nvcf/functions/id/versions",
			expected: false,
			reason:   "List versions is a read operation",
		},
		{
			name:     "Direct invocation path",
			method:   "POST",
			path:     "/echo",
			expected: false,
			reason:   "Direct invocation is a user operation",
		},
		{
			name:     "Queue position",
			method:   "GET",
			path:     "/v2/nvcf/queues/request-id/position",
			expected: false,
			reason:   "Queue position is a user operation",
		},
		{
			name:     "Queue details for function",
			method:   "GET",
			path:     "/v2/nvcf/queues/functions/id",
			expected: false,
			reason:   "Queue details is a user operation with queue_details scope",
		},
		{
			name:     "Queue details for function version",
			method:   "GET",
			path:     "/v2/nvcf/queues/functions/id/versions/vid",
			expected: false,
			reason:   "Queue details is a user operation with queue_details scope",
		},
		{
			name:     "List cluster groups",
			method:   "GET",
			path:     "/v2/nvcf/clusterGroups",
			expected: false,
			reason:   "Cluster groups listing is a user operation",
		},
	}

	client := &Client{
		config: &Config{},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.isAdminOperation(tt.method, tt.path)
			if result != tt.expected {
				t.Errorf("isAdminOperation(%s, %s) = %v, expected %v\nReason: %s",
					tt.method, tt.path, result, tt.expected, tt.reason)
			}
		})
	}
}

// TestUpdateGpuSpecificationHappyPath verifies the CLI sends PATCH to the
// per-GPU-spec endpoint with the narrowed body.
func TestUpdateGpuSpecificationHappyPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gpuSpecification":{"gpuSpecificationId":"spec-id","gpu":"H100","instanceType":"NCP.GPU.H100_1x","minInstances":0,"maxInstances":3}}`))
	}))
	defer srv.Close()

	c := &Client{
		config:     &Config{Token: "jwt", BaseHTTPURL: srv.URL},
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}

	resp, err := c.UpdateGpuSpecification(context.Background(), "dep-id", "spec-id",
		&UpdateGpuSpecificationRequest{MaxInstances: ptrInt(3)})
	if err != nil {
		t.Fatalf("UpdateGpuSpecification returned error: %v", err)
	}

	if gotMethod != "PATCH" {
		t.Errorf("expected PATCH, got %s", gotMethod)
	}
	wantPath := "/v2/nvcf/deployments/dep-id/gpu-specifications/spec-id"
	if gotPath != wantPath {
		t.Errorf("expected path %s, got %s", wantPath, gotPath)
	}
	if v, ok := gotBody["maxInstances"].(float64); !ok || int(v) != 3 {
		t.Errorf("expected body maxInstances=3, got %v", gotBody["maxInstances"])
	}
	for _, forbidden := range []string{"gpu", "instanceType", "deploymentSpecifications", "backend", "clusters", "availabilityZones", "maxRequestConcurrency", "preferredOrder"} {
		if _, present := gotBody[forbidden]; present {
			t.Errorf("body should not contain %q (PATCH rejects it); got body keys: %v", forbidden, keys(gotBody))
		}
	}
	if resp.GpuSpecification.MaxInstances != 3 {
		t.Errorf("expected response maxInstances=3, got %d", resp.GpuSpecification.MaxInstances)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Silence unused-import warning when the file has no other strings usage.
var _ = strings.Join

// TestGetAccountID tests the getAccountID function
func TestGetAccountID(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected string
	}{
		{
			name: "Use cluster config account",
			config: &Config{
				ClusterConfig: &ClusterConfig{
					NVCFAccount: "test-cluster-account",
				},
				ClientID: "test-client",
			},
			expected: "test-cluster-account",
		},
		{
			name: "Use client ID when no cluster config",
			config: &Config{
				ClientID: "test-client-id",
			},
			expected: "test-client-id",
		},
		{
			name:     "Default to nvcf-default",
			config:   &Config{},
			expected: "nvcf-default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: tt.config,
			}
			result := client.getAccountID()
			if result != tt.expected {
				t.Errorf("getAccountID() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

// TestAPIKeyScopeRestrictions tests that API keys are limited to read-only operations
func TestAPIKeyScopeRestrictions(t *testing.T) {
	// This test documents which operations should work with API key only
	readOnlyOperations := []string{
		"invoke_function",
		"list_functions",
		"queue_details",
		"list_functions_details",
	}

	writeOperations := []string{
		"register_function",
		"deploy_function",
		"update_function",
		"delete_function",
		"manage_registry_credentials",
		"manage_telemetries",
		"authorize_clients",
	}

	t.Run("Read-only operations allowed with API key", func(t *testing.T) {
		for _, op := range readOnlyOperations {
			t.Logf("API key should support: %s", op)
		}
	})

	t.Run("Write operations NOT allowed with API key", func(t *testing.T) {
		for _, op := range writeOperations {
			t.Logf("API key should NOT support: %s (requires JWT)", op)
		}
	})
}

// TestCrossAccountEndpointSelection tests that JWT operations use cross-account endpoints
func TestCrossAccountEndpointSelection(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		operation       string
		expectedAccount string
	}{
		{
			name: "Function creation with JWT and cluster config",
			config: &Config{
				Token: "jwt-token",
				ClusterConfig: &ClusterConfig{
					NVCFAccount: "test-account",
				},
			},
			operation:       "create",
			expectedAccount: "test-account",
		},
		{
			name: "Function creation with JWT and client ID",
			config: &Config{
				Token:    "jwt-token",
				ClientID: "client-123",
			},
			operation:       "create",
			expectedAccount: "client-123",
		},
		{
			name: "Function creation with JWT and no account info",
			config: &Config{
				Token: "jwt-token",
			},
			operation:       "create",
			expectedAccount: "nvcf-default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: tt.config,
			}

			account := client.getAccountID()
			if account != tt.expectedAccount {
				t.Errorf("getAccountID() = %q, expected %q", account, tt.expectedAccount)
			}

			// Verify the account ID is correctly determined (for logging/debugging purposes)
			// Note: We now use regular endpoints (/v2/nvcf/functions) instead of cross-account endpoints
			switch tt.operation {
			case "create":
				expectedEndpoint := "/v2/nvcf/functions"
				t.Logf("Create function would use endpoint: %s (account: %s)", expectedEndpoint, account)
			case "deploy":
				expectedEndpoint := "/v2/nvcf/deployments/functions/{id}/versions/{vid}"
				t.Logf("Deploy function would use endpoint: %s (account: %s)", expectedEndpoint, account)
			case "update":
				expectedEndpoint := "/v2/nvcf/functions/{id}/versions/{vid}"
				t.Logf("Update function would use endpoint: %s (account: %s)", expectedEndpoint, account)
			case "delete":
				expectedEndpoint := "/v2/nvcf/functions/{id}/versions/{vid}"
				t.Logf("Delete function would use endpoint: %s (account: %s)", expectedEndpoint, account)
			}
		})
	}
}

// TestGetTokenWithFallback tests token loading priority: env > config > state
func TestGetTokenWithFallback(t *testing.T) {
	// Helper to create temp config file
	createTempConfig := func(t *testing.T, content string) string {
		tmpFile, err := os.CreateTemp("", "nvcf-test-*.yaml")
		if err != nil {
			t.Fatal(err)
		}
		tmpFile.WriteString(content)
		tmpFile.Close()
		return tmpFile.Name()
	}

	tests := []struct {
		name           string
		envToken       string
		configContent  string
		stateToken     string
		stateExpired   bool
		expectedToken  string
		expectedSource string
	}{
		{
			name:           "Env takes priority over config and state",
			envToken:       "env-token",
			configContent:  "token: config-token",
			stateToken:     "state-token",
			stateExpired:   false,
			expectedToken:  "env-token",
			expectedSource: "environment",
		},
		{
			name:           "Config takes priority over state",
			envToken:       "",
			configContent:  "token: config-token",
			stateToken:     "state-token",
			stateExpired:   false,
			expectedToken:  "config-token",
			expectedSource: "config_file",
		},
		{
			name:           "State used as fallback",
			envToken:       "",
			configContent:  "",
			stateToken:     "state-token",
			stateExpired:   false,
			expectedToken:  "state-token",
			expectedSource: "state",
		},
		{
			name:           "Expired state token ignored",
			envToken:       "",
			configContent:  "",
			stateToken:     "expired-state-token",
			stateExpired:   true,
			expectedToken:  "",
			expectedSource: "none",
		},
		{
			name:           "No token available",
			envToken:       "",
			configContent:  "",
			stateToken:     "",
			stateExpired:   false,
			expectedToken:  "",
			expectedSource: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper
			viper.Reset()
			viper.SetEnvPrefix("NVCF")
			viper.AutomaticEnv()

			// Setup config file if needed
			if tt.configContent != "" {
				configPath := createTempConfig(t, tt.configContent)
				defer os.Remove(configPath)
				viper.SetConfigFile(configPath)
				viper.ReadInConfig()
			}

			// Setup env
			os.Unsetenv("NVCF_TOKEN")
			if tt.envToken != "" {
				os.Setenv("NVCF_TOKEN", tt.envToken)
			}

			// Setup state expiration
			var expiration time.Time
			if tt.stateExpired {
				expiration = time.Now().Add(-1 * time.Hour)
			} else if tt.stateToken != "" {
				expiration = time.Now().Add(1 * time.Hour)
			}

			token := getTokenWithFallback("token", tt.stateToken, expiration)
			source := getTokenSource("token", tt.stateToken, expiration)

			if token != tt.expectedToken {
				t.Errorf("token: expected %q, got %q", tt.expectedToken, token)
			}
			if source != tt.expectedSource {
				t.Errorf("source: expected %q, got %q", tt.expectedSource, source)
			}
			t.Logf("token=%q source=%q", token, source)
		})
	}
}

func TestLoadConfigUsesStateForActiveConfigFile(t *testing.T) {
	viper.Reset()
	viper.SetEnvPrefix("NVCF")
	viper.AutomaticEnv()
	t.Cleanup(func() { viper.Reset() })
	t.Setenv("NVCF_TOKEN", "")
	t.Setenv("NVCF_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	configPath := filepath.Join(t.TempDir(), "nvcf-cli-local.yaml")
	configBody := []byte(`
base_http_url: "http://api.localhost:8080"
invoke_url: "http://invocation.localhost:8080"
client_id: "nvcf-default"
`)
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaultState := state.NewStateManager()
	if err := defaultState.Load(); err != nil {
		t.Fatalf("load default state: %v", err)
	}
	defaultState.SetTokens("default-token", "", time.Now().Add(time.Hour), time.Time{})
	if err := defaultState.Save(); err != nil {
		t.Fatalf("save default state: %v", err)
	}

	configState := state.GetStateManagerForConfig(configPath)
	if err := configState.Load(); err != nil {
		t.Fatalf("load config state: %v", err)
	}
	configState.SetTokens("config-token", "", time.Now().Add(time.Hour), time.Time{})
	if err := configState.Save(); err != nil {
		t.Fatalf("save config state: %v", err)
	}

	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}

	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.Token != "config-token" {
		t.Fatalf("Token = %q, want config-token", config.Token)
	}
	if config.BaseHTTPURL != "http://api.localhost:8080" {
		t.Fatalf("BaseHTTPURL = %q, want http://api.localhost:8080", config.BaseHTTPURL)
	}
}
