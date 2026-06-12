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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// ============================================================================
// NVCT (NVIDIA Cloud Tasks) DTOs - mirror schemas in nvct-openapi.json
// ============================================================================

// KubernetesType represents a Kubernetes API group/version/kind tuple.
type KubernetesType struct {
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// HelmValidationPolicyDto represents Helm validation policy for a task.
type HelmValidationPolicyDto struct {
	Name                 string           `json:"name"` // "Default" or "Unrestricted"
	ExtraKubernetesTypes []KubernetesType `json:"extraKubernetesTypes,omitempty"`
}

// GpuSpecificationDto is the per-task GPU specification used by NVCT.
// Distinct from GPUSpecificationDto used for function deployments because the
// NVCT shape carries a free-form `configuration` map and Helm policy.
type GpuSpecificationDto struct {
	GPU                  string                   `json:"gpu"`
	Backend              string                   `json:"backend,omitempty"`
	Clusters             []string                 `json:"clusters,omitempty"`
	InstanceType         string                   `json:"instanceType"`
	Configuration        map[string]any           `json:"configuration,omitempty"`
	HelmValidationPolicy *HelmValidationPolicyDto `json:"helmValidationPolicy,omitempty"`
}

// InstanceDto represents a single instance backing a running task.
type InstanceDto struct {
	InstanceID        string `json:"instanceId"`
	TaskID            string `json:"taskId"`
	InstanceType      string `json:"instanceType"`
	InstanceState     string `json:"instanceState,omitempty"`
	ICMSRequestID     string `json:"icmsRequestId"`
	NCAID             string `json:"ncaId"`
	GPU               string `json:"gpu"`
	Backend           string `json:"backend"`
	Location          string `json:"location"`
	InstanceCreatedAt string `json:"instanceCreatedAt"`
	InstanceUpdatedAt string `json:"instanceUpdatedAt"`
}

// TaskDto is the full task representation returned by the API.
type TaskDto struct {
	ID                             string                      `json:"id"`
	NCAID                          string                      `json:"ncaId"`
	Name                           string                      `json:"name"`
	Status                         string                      `json:"status"`
	GpuSpecification               *GpuSpecificationDto        `json:"gpuSpecification,omitempty"`
	ContainerImage                 string                      `json:"containerImage,omitempty"`
	ContainerArgs                  string                      `json:"containerArgs,omitempty"`
	ContainerEnvironment           []ContainerEnvironmentEntry `json:"containerEnvironment,omitempty"`
	Models                         []ArtifactDto               `json:"models,omitempty"`
	Resources                      []ArtifactDto               `json:"resources,omitempty"`
	Tags                           []string                    `json:"tags,omitempty"`
	Description                    string                      `json:"description,omitempty"`
	ResultHandlingStrategy         string                      `json:"resultHandlingStrategy,omitempty"`
	ResultsLocation                string                      `json:"resultsLocation,omitempty"`
	MaxRuntimeDuration             string                      `json:"maxRuntimeDuration,omitempty"`
	MaxQueuedDuration              string                      `json:"maxQueuedDuration,omitempty"`
	TerminationGracePeriodDuration string                      `json:"terminationGracePeriodDuration,omitempty"`
	HelmChart                      string                      `json:"helmChart,omitempty"`
	HealthInfo                     *HealthDto                  `json:"healthInfo,omitempty"`
	Secrets                        []string                    `json:"secrets,omitempty"`
	PercentComplete                int                         `json:"percentComplete,omitempty"`
	LastHeartbeatAt                string                      `json:"lastHeartbeatAt,omitempty"`
	LastUpdatedAt                  string                      `json:"lastUpdatedAt,omitempty"`
	Telemetries                    *TelemetriesDto             `json:"telemetries,omitempty"`
	CreatedAt                      string                      `json:"createdAt,omitempty"`
	Instances                      []InstanceDto               `json:"instances,omitempty"`
}

// BasicTaskDto is the trimmed representation returned by the bulk endpoint.
type BasicTaskDto struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CreateTaskRequest is the request body for POST /v1/nvct/tasks.
type CreateTaskRequest struct {
	Name                           string                      `json:"name"`
	GpuSpecification               *GpuSpecificationDto        `json:"gpuSpecification"`
	ContainerImage                 string                      `json:"containerImage,omitempty"`
	ContainerArgs                  string                      `json:"containerArgs,omitempty"`
	ContainerEnvironment           []ContainerEnvironmentEntry `json:"containerEnvironment,omitempty"`
	Models                         []ArtifactDto               `json:"models,omitempty"`
	Resources                      []ArtifactDto               `json:"resources,omitempty"`
	Tags                           []string                    `json:"tags,omitempty"`
	Description                    string                      `json:"description,omitempty"`
	MaxRuntimeDuration             string                      `json:"maxRuntimeDuration,omitempty"`
	MaxQueuedDuration              string                      `json:"maxQueuedDuration,omitempty"`
	TerminationGracePeriodDuration string                      `json:"terminationGracePeriodDuration,omitempty"`
	ResultHandlingStrategy         string                      `json:"resultHandlingStrategy,omitempty"`
	ResultsLocation                string                      `json:"resultsLocation,omitempty"`
	HelmChart                      string                      `json:"helmChart,omitempty"`
	Telemetries                    *TelemetriesDto             `json:"telemetries,omitempty"`
	Secrets                        []SecretDto                 `json:"secrets,omitempty"`
}

// TaskResponse wraps a TaskDto for create/get/cancel responses.
type TaskResponse struct {
	Task TaskDto `json:"task"`
}

// EventDto represents a single task event entry.
type EventDto struct {
	EventID   string `json:"eventId"`
	TaskID    string `json:"taskId"`
	NCAID     string `json:"ncaId"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}

// ResultDto represents a result/output emitted by a task.
type ResultDto struct {
	ResultID  string                 `json:"resultId"`
	TaskID    string                 `json:"taskId"`
	NCAID     string                 `json:"ncaId"`
	Name      string                 `json:"name"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt string                 `json:"createdAt"`
}

// ListTasksResponse is the response body for GET /v1/nvct/tasks.
type ListTasksResponse struct {
	Tasks  []TaskDto `json:"tasks"`
	Limit  int       `json:"limit,omitempty"`
	Cursor string    `json:"cursor,omitempty"`
}

// ListBasicTaskDetailsResponse is the response body for the bulk endpoint.
type ListBasicTaskDetailsResponse struct {
	NCAID string         `json:"ncaId"`
	Tasks []BasicTaskDto `json:"tasks"`
}

// BulkTaskDetailsRequest is the request body for POST /v1/nvct/tasks/bulk.
type BulkTaskDetailsRequest struct {
	TaskIDs []string `json:"taskIds"`
}

// ListEventsResponse is the response body for the events endpoint.
type ListEventsResponse struct {
	Events []EventDto `json:"events"`
	Limit  int        `json:"limit,omitempty"`
	Cursor string     `json:"cursor,omitempty"`
}

// ListResultsResponse is the response body for the results endpoint.
type ListResultsResponse struct {
	Results []ResultDto `json:"results"`
	Limit   int         `json:"limit,omitempty"`
	Cursor  string      `json:"cursor,omitempty"`
}

// UpdateTaskSecretsRequest is the request body for the update-secrets endpoint.
type UpdateTaskSecretsRequest struct {
	Secrets []SecretDto `json:"secrets"`
}

// GpuPlacementDto describes where a GPU is currently being used.
type GpuPlacementDto struct {
	ClusterID       string `json:"clusterId"`
	Cluster         string `json:"cluster"`
	ClusterGroupID  string `json:"clusterGroupId"`
	ClusterGroup    string `json:"clusterGroup"`
	CloudProvider   string `json:"cloudProvider"`
	Region          string `json:"region"`
	CurrentMaxUsage int    `json:"currentMaxUsage,omitempty"`
	CurrentMinUsage int    `json:"currentMinUsage,omitempty"`
}

// GpuUsageDto represents per-GPU usage information for an account.
type GpuUsageDto struct {
	GPU             string            `json:"gpu"`
	InstanceType    string            `json:"instanceType"`
	CurrentMaxUsage int               `json:"currentMaxUsage,omitempty"`
	CurrentMinUsage int               `json:"currentMinUsage,omitempty"`
	Placements      []GpuPlacementDto `json:"placements"`
}

// ListGpuUsageResponse is the response body for the GPU usage endpoint.
type ListGpuUsageResponse struct {
	GPUs []GpuUsageDto `json:"gpus"`
}

// ListTasksOptions captures optional query parameters for ListTasks.
type ListTasksOptions struct {
	Limit  int    // 0 = omit
	Status string // empty = omit
	Cursor string // empty = omit
}

// PaginationOptions captures optional pagination query parameters used by the
// events and results endpoints.
type PaginationOptions struct {
	Limit  int
	Cursor string
}

// ============================================================================
// NVCT request helper
// ============================================================================

// makeNVCTRequest sends an authenticated request to the NVCT base URL using the
// shared HTTP client (which already injects the bearer token).
func (c *Client) makeNVCTRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	base := c.config.BaseNVCTURL
	if base == "" {
		return nil, fmt.Errorf("NVCT base URL is not configured (set base_nvct_url in config or NVCF_BASE_NVCT_URL)")
	}

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	fullURL := base + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	httpClient := c.httpClient
	if c.nvctHTTPClient != nil {
		httpClient = c.nvctHTTPClient
	}
	return httpClient.Do(req)
}

func decodeNVCT[T any](resp *http.Response) (*T, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("NVCT API error %d: %s", resp.StatusCode, string(body))
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ============================================================================
// Task management
// ============================================================================

// CreateTask creates and launches a new task.
func (c *Client) CreateTask(ctx context.Context, req *CreateTaskRequest) (*TaskResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("task name is required")
	}
	if req.GpuSpecification == nil {
		return nil, fmt.Errorf("gpuSpecification is required")
	}
	if req.GpuSpecification.GPU == "" {
		return nil, fmt.Errorf("gpuSpecification.gpu is required")
	}
	if req.GpuSpecification.InstanceType == "" {
		return nil, fmt.Errorf("gpuSpecification.instanceType is required")
	}

	resp, err := c.makeNVCTRequest(ctx, "POST", "/v1/nvct/tasks", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[TaskResponse](resp)
}

// ListTasks lists tasks for the authenticated account.
func (c *Client) ListTasks(ctx context.Context, opts *ListTasksOptions) (*ListTasksResponse, error) {
	endpoint := "/v1/nvct/tasks"
	if opts != nil {
		q := url.Values{}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Status != "" {
			q.Set("status", opts.Status)
		}
		if opts.Cursor != "" {
			q.Set("cursor", opts.Cursor)
		}
		if encoded := q.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
	}

	resp, err := c.makeNVCTRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[ListTasksResponse](resp)
}

// ListBasicTaskDetails returns trimmed details for the supplied task IDs.
func (c *Client) ListBasicTaskDetails(ctx context.Context, taskIDs []string) (*ListBasicTaskDetailsResponse, error) {
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("at least one taskId is required")
	}
	body := &BulkTaskDetailsRequest{TaskIDs: taskIDs}
	resp, err := c.makeNVCTRequest(ctx, "POST", "/v1/nvct/tasks/bulk", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[ListBasicTaskDetailsResponse](resp)
}

// GetTask returns full details for a single task. When includeSecrets is true,
// secret values are included in the response (subject to API authorization).
func (c *Client) GetTask(ctx context.Context, taskID string, includeSecrets bool) (*TaskResponse, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/tasks/%s", url.PathEscape(taskID))
	if includeSecrets {
		endpoint += "?includeSecrets=true"
	}

	resp, err := c.makeNVCTRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[TaskResponse](resp)
}

// DeleteTask permanently removes a task.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("taskId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/tasks/%s", url.PathEscape(taskID))
	resp, err := c.makeNVCTRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("NVCT API error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// CancelTask cancels a task that is queued or running.
func (c *Client) CancelTask(ctx context.Context, taskID string) (*TaskResponse, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/tasks/%s/cancel", url.PathEscape(taskID))
	resp, err := c.makeNVCTRequest(ctx, "POST", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[TaskResponse](resp)
}

// GetTaskEvents lists task lifecycle events (paginated).
func (c *Client) GetTaskEvents(ctx context.Context, taskID string, opts *PaginationOptions) (*ListEventsResponse, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/tasks/%s/events", url.PathEscape(taskID))
	endpoint += paginationQuery(opts)

	resp, err := c.makeNVCTRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[ListEventsResponse](resp)
}

// GetTaskResults lists task results/outputs (paginated).
func (c *Client) GetTaskResults(ctx context.Context, taskID string, opts *PaginationOptions) (*ListResultsResponse, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/tasks/%s/results", url.PathEscape(taskID))
	endpoint += paginationQuery(opts)

	resp, err := c.makeNVCTRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[ListResultsResponse](resp)
}

// UpdateTaskSecrets replaces the user secrets associated with a task.
func (c *Client) UpdateTaskSecrets(ctx context.Context, taskID string, secrets []SecretDto) error {
	if taskID == "" {
		return fmt.Errorf("taskId is required")
	}
	if len(secrets) == 0 {
		return fmt.Errorf("at least one secret is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/secrets/tasks/%s", url.PathEscape(taskID))
	body := &UpdateTaskSecretsRequest{Secrets: secrets}
	resp, err := c.makeNVCTRequest(ctx, "PUT", endpoint, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("NVCT API error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetTaskGpuUsage returns the current GPU usage breakdown for the supplied
// NVIDIA Cloud Account. This is a Super-Admin endpoint per the OpenAPI spec.
func (c *Client) GetTaskGpuUsage(ctx context.Context, ncaID string) (*ListGpuUsageResponse, error) {
	if ncaID == "" {
		return nil, fmt.Errorf("ncaId is required")
	}
	endpoint := fmt.Sprintf("/v1/nvct/accounts/%s/usage/gpus", url.PathEscape(ncaID))
	resp, err := c.makeNVCTRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeNVCT[ListGpuUsageResponse](resp)
}

func paginationQuery(opts *PaginationOptions) string {
	if opts == nil {
		return ""
	}
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if encoded := q.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}
