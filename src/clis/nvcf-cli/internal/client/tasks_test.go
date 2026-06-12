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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newNVCTTestClient wires a Client to point exclusively at an httptest server
// for the NVCT base URL while leaving the NVCF base URL pointed somewhere
// inert.
func newNVCTTestClient(srv *httptest.Server) *Client {
	return &Client{
		config: &Config{
			Token:       "test-jwt",
			BaseHTTPURL: "http://nvcf.invalid",
			BaseNVCTURL: srv.URL,
		},
		httpClient: srv.Client(),
		baseURL:    "http://nvcf.invalid",
	}
}

// readJSONBody decodes the request body into a generic map for assertions.
func readJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("failed reading request body: %v", err)
	}
	if len(body) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("failed unmarshaling request body %q: %v", string(body), err)
	}
	return out
}

// TestPaginationQuery exercises the small helper directly so the test does
// not need to spin up an HTTP server just for the query-string check.
func TestPaginationQuery(t *testing.T) {
	tests := []struct {
		name string
		opts *PaginationOptions
		want string
	}{
		{"nil", nil, ""},
		{"empty", &PaginationOptions{}, ""},
		{"limit only", &PaginationOptions{Limit: 50}, "?limit=50"},
		{"cursor only", &PaginationOptions{Cursor: "abc"}, "?cursor=abc"},
		{"both", &PaginationOptions{Limit: 25, Cursor: "abc"}, "?cursor=abc&limit=25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paginationQuery(tt.opts); got != tt.want {
				t.Errorf("paginationQuery(%+v) = %q, want %q", tt.opts, got, tt.want)
			}
		})
	}
}

// TestMakeNVCTRequestRequiresBaseURL ensures we surface a friendly error when
// the user has no NVCT base URL configured rather than letting the request go
// to a malformed URL.
func TestMakeNVCTRequestRequiresBaseURL(t *testing.T) {
	c := &Client{config: &Config{}, httpClient: http.DefaultClient}
	_, err := c.makeNVCTRequest(context.Background(), "GET", "/v1/nvct/tasks", nil)
	if err == nil || !strings.Contains(err.Error(), "NVCT base URL is not configured") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestMakeNVCTRequestUsesNVCTClient(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	c := &Client{
		config:         &Config{BaseNVCTURL: srv.URL},
		httpClient:     &http.Client{Transport: &BearerTokenTransport{Token: "nvcf-token"}},
		nvctHTTPClient: &http.Client{Transport: &BearerTokenTransport{Token: "nvct-token"}},
	}
	_, _ = c.makeNVCTRequest(context.Background(), "GET", "/v1/nvct/tasks", nil)
	if gotAuth != "Bearer nvct-token" {
		t.Errorf("expected nvct-token in Authorization header, got %q", gotAuth)
	}
}

func TestMakeNVCTRequestFallsBackToDefaultClient(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	c := &Client{
		config:     &Config{BaseNVCTURL: srv.URL},
		httpClient: &http.Client{Transport: &BearerTokenTransport{Token: "nvcf-token"}},
		// nvctHTTPClient is nil
	}
	_, _ = c.makeNVCTRequest(context.Background(), "GET", "/v1/nvct/tasks", nil)
	if gotAuth != "Bearer nvcf-token" {
		t.Errorf("expected nvcf-token fallback in Authorization header, got %q", gotAuth)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	c := newNVCTTestClient(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be invoked when validation fails: %s %s", r.Method, r.URL.Path)
	})))
	defer c.httpClient.CloseIdleConnections()

	cases := []struct {
		name string
		req  *CreateTaskRequest
		want string
	}{
		{"nil request", nil, "request is required"},
		{"missing name", &CreateTaskRequest{GpuSpecification: &GpuSpecificationDto{GPU: "H100", InstanceType: "GPU.H100_1x"}}, "task name is required"},
		{"missing gpu spec", &CreateTaskRequest{Name: "ok"}, "gpuSpecification is required"},
		{"missing gpu", &CreateTaskRequest{Name: "ok", GpuSpecification: &GpuSpecificationDto{InstanceType: "GPU.H100_1x"}}, "gpuSpecification.gpu is required"},
		{"missing instanceType", &CreateTaskRequest{Name: "ok", GpuSpecification: &GpuSpecificationDto{GPU: "H100"}}, "gpuSpecification.instanceType is required"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.CreateTask(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestCreateTaskHappyPath(t *testing.T) {
	var gotMethod, gotPath, gotContentType, gotAccept string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotBody = readJSONBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":{"id":"task-1","name":"my-job","status":"QUEUED","gpuSpecification":{"gpu":"H100","instanceType":"GPU.H100_1x"}}}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.CreateTask(context.Background(), &CreateTaskRequest{
		Name:             "my-job",
		ContainerImage:   "registry/example:latest",
		GpuSpecification: &GpuSpecificationDto{GPU: "H100", InstanceType: "GPU.H100_1x"},
	})
	if err != nil {
		t.Fatalf("CreateTask returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/nvct/tasks" {
		t.Errorf("expected /v1/nvct/tasks, got %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if gotBody["name"] != "my-job" {
		t.Errorf("expected body name=my-job, got %v", gotBody["name"])
	}
	gpu, _ := gotBody["gpuSpecification"].(map[string]any)
	if gpu == nil || gpu["gpu"] != "H100" || gpu["instanceType"] != "GPU.H100_1x" {
		t.Errorf("expected nested gpuSpecification, got %v", gotBody["gpuSpecification"])
	}
	if resp.Task.ID != "task-1" || resp.Task.Status != "QUEUED" {
		t.Errorf("unexpected response %+v", resp.Task)
	}
}

func TestListTasksQueryEncoding(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	if _, err := c.ListTasks(context.Background(), &ListTasksOptions{Limit: 10, Status: "RUNNING", Cursor: "abc"}); err != nil {
		t.Fatalf("ListTasks returned error: %v", err)
	}
	for _, want := range []string{"limit=10", "status=RUNNING", "cursor=abc"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("expected query %q to contain %q", gotQuery, want)
		}
	}
}

func TestListTasksParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[{"id":"t-1","name":"n","status":"RUNNING"}],"limit":10,"cursor":"next"}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.ListTasks(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTasks returned error: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != "t-1" {
		t.Errorf("unexpected tasks: %+v", resp.Tasks)
	}
	if resp.Cursor != "next" || resp.Limit != 10 {
		t.Errorf("unexpected pagination metadata: cursor=%q limit=%d", resp.Cursor, resp.Limit)
	}
}

func TestGetTaskWithSecrets(t *testing.T) {
	tests := []struct {
		name     string
		flag     bool
		wantPath string
		wantQS   string
	}{
		{"without flag", false, "/v1/nvct/tasks/task-id", ""},
		{"with flag", true, "/v1/nvct/tasks/task-id", "includeSecrets=true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"task":{"id":"task-id","name":"n","status":"RUNNING"}}`))
			}))
			defer srv.Close()

			c := newNVCTTestClient(srv)
			if _, err := c.GetTask(context.Background(), "task-id", tt.flag); err != nil {
				t.Fatalf("GetTask returned error: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotQuery != tt.wantQS {
				t.Errorf("query = %q, want %q", gotQuery, tt.wantQS)
			}
		})
	}
}

func TestDeleteTaskAcceptsNoContent(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.WriteHeader(code)
			}))
			defer srv.Close()

			c := newNVCTTestClient(srv)
			if err := c.DeleteTask(context.Background(), "task-id"); err != nil {
				t.Fatalf("DeleteTask returned error: %v", err)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", gotMethod)
			}
			if gotPath != "/v1/nvct/tasks/task-id" {
				t.Errorf("path = %s, want /v1/nvct/tasks/task-id", gotPath)
			}
		})
	}
}

func TestDeleteTaskPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	err := c.DeleteTask(context.Background(), "task-id")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

func TestCancelTask(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":{"id":"task-id","name":"n","status":"CANCELED"}}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.CancelTask(context.Background(), "task-id")
	if err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/nvct/tasks/task-id/cancel" {
		t.Errorf("path = %s, want /v1/nvct/tasks/task-id/cancel", gotPath)
	}
	if resp.Task.Status != "CANCELED" {
		t.Errorf("status = %s, want CANCELED", resp.Task.Status)
	}
}

func TestGetTaskEvents(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[{"eventId":"e1","taskId":"task-id","ncaId":"nca","message":"started","createdAt":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.GetTaskEvents(context.Background(), "task-id", &PaginationOptions{Limit: 5, Cursor: "next"})
	if err != nil {
		t.Fatalf("GetTaskEvents returned error: %v", err)
	}
	if gotPath != "/v1/nvct/tasks/task-id/events" {
		t.Errorf("path = %s, want /v1/nvct/tasks/task-id/events", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "cursor=next") {
		t.Errorf("query = %s, missing limit/cursor", gotQuery)
	}
	if len(resp.Events) != 1 || resp.Events[0].Message != "started" {
		t.Errorf("unexpected events: %+v", resp.Events)
	}
}

func TestGetTaskResults(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"resultId":"r1","taskId":"task-id","ncaId":"nca","name":"out","metadata":{"k":"v"},"createdAt":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.GetTaskResults(context.Background(), "task-id", nil)
	if err != nil {
		t.Fatalf("GetTaskResults returned error: %v", err)
	}
	if gotPath != "/v1/nvct/tasks/task-id/results" {
		t.Errorf("path = %s, want /v1/nvct/tasks/task-id/results", gotPath)
	}
	if len(resp.Results) != 1 || resp.Results[0].Metadata["k"] != "v" {
		t.Errorf("unexpected results: %+v", resp.Results)
	}
}

func TestUpdateTaskSecrets(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody = readJSONBody(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	err := c.UpdateTaskSecrets(context.Background(), "task-id", []SecretDto{
		{Name: "NGC_API_KEY", Value: "nvapi-x"},
	})
	if err != nil {
		t.Fatalf("UpdateTaskSecrets returned error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/v1/nvct/secrets/tasks/task-id" {
		t.Errorf("path = %s, want /v1/nvct/secrets/tasks/task-id", gotPath)
	}
	secrets, ok := gotBody["secrets"].([]any)
	if !ok || len(secrets) != 1 {
		t.Fatalf("expected secrets array of len 1, got %v", gotBody["secrets"])
	}
	first := secrets[0].(map[string]any)
	if first["name"] != "NGC_API_KEY" || first["value"] != "nvapi-x" {
		t.Errorf("unexpected secret entry: %v", first)
	}
}

func TestUpdateTaskSecretsValidation(t *testing.T) {
	c := newNVCTTestClient(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be invoked when validation fails")
	})))
	if err := c.UpdateTaskSecrets(context.Background(), "", []SecretDto{{Name: "x"}}); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if err := c.UpdateTaskSecrets(context.Background(), "task-id", nil); err == nil {
		t.Errorf("expected error for empty secrets slice")
	}
}

func TestListBasicTaskDetails(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody = readJSONBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ncaId":"nca","tasks":[{"id":"t1","name":"a","status":"RUNNING"},{"id":"t2","name":"b","status":"QUEUED"}]}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.ListBasicTaskDetails(context.Background(), []string{"t1", "t2"})
	if err != nil {
		t.Fatalf("ListBasicTaskDetails returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/nvct/tasks/bulk" {
		t.Errorf("path = %s, want /v1/nvct/tasks/bulk", gotPath)
	}
	ids, ok := gotBody["taskIds"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("expected 2 taskIds, got %v", gotBody["taskIds"])
	}
	if resp.NCAID != "nca" || len(resp.Tasks) != 2 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestListBasicTaskDetailsValidation(t *testing.T) {
	c := newNVCTTestClient(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be invoked when validation fails")
	})))
	if _, err := c.ListBasicTaskDetails(context.Background(), nil); err == nil {
		t.Errorf("expected error for empty taskIds")
	}
}

func TestGetTaskGpuUsage(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gpus":[{"gpu":"H100","instanceType":"GPU.H100_1x","placements":[{"clusterId":"c","cluster":"c","clusterGroupId":"g","clusterGroup":"g","cloudProvider":"GFN","region":"us-west"}]}]}`))
	}))
	defer srv.Close()

	c := newNVCTTestClient(srv)
	resp, err := c.GetTaskGpuUsage(context.Background(), "nca-1")
	if err != nil {
		t.Fatalf("GetTaskGpuUsage returned error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", gotMethod)
	}
	if gotPath != "/v1/nvct/accounts/nca-1/usage/gpus" {
		t.Errorf("path = %s, want /v1/nvct/accounts/nca-1/usage/gpus", gotPath)
	}
	if len(resp.GPUs) != 1 || resp.GPUs[0].GPU != "H100" {
		t.Errorf("unexpected GPU usage response: %+v", resp.GPUs)
	}
}

func TestGetTaskRequiresID(t *testing.T) {
	c := newNVCTTestClient(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be invoked when validation fails")
	})))
	if _, err := c.GetTask(context.Background(), "", false); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if err := c.DeleteTask(context.Background(), ""); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if _, err := c.CancelTask(context.Background(), ""); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if _, err := c.GetTaskEvents(context.Background(), "", nil); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if _, err := c.GetTaskResults(context.Background(), "", nil); err == nil {
		t.Errorf("expected error for missing taskId")
	}
	if _, err := c.GetTaskGpuUsage(context.Background(), ""); err == nil {
		t.Errorf("expected error for missing ncaId")
	}
}
