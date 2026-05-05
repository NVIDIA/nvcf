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
	"net/http/httptrace"
	"net/url"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	fndstypes "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
)

type LedgerClient interface {
	ListInstances(ctx context.Context, functionVersionId uuid.UUID) ([]fndstypes.Instance, error)
	ListInstancesV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) ([]fndstypes.Instance, error)
	NewStageTransitionEvent(
		ncaId string,
		functionId uuid.UUID,
		functionVersionId uuid.UUID,
		instanceId string,
		event string,
		eventType string,
		detailsJSON []byte,
	) (fndstypes.StageTransitionEvent, error)
	NewDeploymentStageTransitionEvent(
		ncaId string,
		functionId uuid.UUID,
		functionVersionId uuid.UUID,
		deploymentId uuid.UUID,
		instanceId string,
		event string,
		eventType string,
		detailsJSON []byte,
	) (fndstypes.DeploymentStageTransitionEvent, error)
	CreateEvent(ctx context.Context, eventData fndstypes.StageTransitionEvent) error
	CreateEventV2(ctx context.Context, eventData fndstypes.DeploymentStageTransitionEvent) error
	GetEvents(ctx context.Context, functionVersionId uuid.UUID, instanceId string) ([]fndstypes.StageTransitionEvent, error)
	GetEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID, instanceId string) ([]fndstypes.DeploymentStageTransitionEvent, error)
	GetEventByEventType(ctx context.Context, functionVersionId uuid.UUID, instanceId string, eventType string) (fndstypes.StageTransitionEvent, error)
	GetEventByEventTypeV2(
		ctx context.Context,
		functionVersionId uuid.UUID,
		deploymentId uuid.UUID,
		instanceId string,
		eventType string,
	) (fndstypes.DeploymentStageTransitionEvent, error)
	DeleteInstanceEvents(ctx context.Context, functionVersionId uuid.UUID, instanceId string) error
	DeleteInstanceEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID, instanceId string) error
	DeleteFunctionVersionEvents(ctx context.Context, functionVersionId uuid.UUID) error
	DeleteFunctionVersionEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) error
	GetStats(ctx context.Context, functionVersionId uuid.UUID) (fndstypes.DeploymentStats, error)
	GetStatsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) (fndstypes.DeploymentStats, error)
}

// TokenFetcher is an interface for fetching authentication tokens
type TokenFetcher interface {
	FetchToken(ctx context.Context) (string, error)
}

type FndsClient struct {
	httpClient   *http.Client
	BaseURL      string
	tokenFetcher TokenFetcher
	NcaId        string
}

const (
	ListInstancesEndpointV1               = "/v1/ledger/versions/%s/instances"
	CreateEventEndpointV1                 = "/v1/ledger/versions/%s/instances/%s"
	GetEventsEndpointV1                   = "/v1/ledger/versions/%s/instances/%s"
	GetEventByEventTypeEndpointV1         = "/v1/ledger/versions/%s/instances/%s/events/%s"
	DeleteInstanceEventsEndpointV1        = "/v1/ledger/versions/%s/instances/%s"
	DeleteFunctionVersionEventsEndpointV1 = "/v1/ledger/versions/%s/instances"
	GetStatsEndpointV1                    = "/v1/ledger/versions/%s/stats"

	ListInstancesEndpointV2               = "/v2/ledger/versions/%s/deployments/%s/instances"
	CreateEventEndpointV2                 = "/v2/ledger/versions/%s/deployments/%s/instances/%s"
	GetEventsEndpointV2                   = "/v2/ledger/versions/%s/deployments/%s/instances/%s"
	GetEventByEventTypeEndpointV2         = "/v2/ledger/versions/%s/deployments/%s/instances/%s/events/%s"
	DeleteInstanceEventsEndpointV2        = "/v2/ledger/versions/%s/deployments/%s/instances/%s"
	DeleteFunctionVersionEventsEndpointV2 = "/v2/ledger/versions/%s/deployments/%s/instances"
	GetStatsEndpointV2                    = "/v2/ledger/versions/%s/deployments/%s/stats"
)

// SimpleAPIError is a minimal wrapper that contains the HTTP status code
// and the raw JSON error response as a string
type SimpleAPIError struct {
	StatusCode int
	JSONString string
}

func (e *SimpleAPIError) Error() string {
	return e.JSONString
}

type FndsClientOption func(*FndsClient)

func WithHTTPClient(httpClient *http.Client) FndsClientOption {
	return func(c *FndsClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func NewFndsClient(endpoint string, ncaId string, tokenFetcher TokenFetcher, opts ...FndsClientOption) *FndsClient {
	return newFndsClient(endpoint, ncaId, tokenFetcher, opts...)
}

func newFndsClient(endpoint string, ncaId string, tokenFetcher TokenFetcher, opts ...FndsClientOption) *FndsClient {
	client := &FndsClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: otelhttp.NewTransport(
				http.DefaultTransport,
				otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
					return otelhttptrace.NewClientTrace(ctx)
				}),
			),
		},
		BaseURL:      endpoint,
		tokenFetcher: tokenFetcher,
		NcaId:        ncaId,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func (c *FndsClient) GetNcaId() string {
	return c.NcaId
}

func (c *FndsClient) ListInstances(ctx context.Context, functionVersionId uuid.UUID) ([]fndstypes.Instance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(ListInstancesEndpointV1, functionVersionId)

	var instances []fndstypes.Instance

	err := c.doRequest(ctx, "GET", endpoint, nil, &instances)
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func (c *FndsClient) ListInstancesV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) ([]fndstypes.Instance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(ListInstancesEndpointV2, functionVersionId, deploymentId)

	var instances []fndstypes.Instance

	err := c.doRequest(ctx, "GET", endpoint, nil, &instances)
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func (c *FndsClient) NewStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	detailsJSON []byte,
) (fndstypes.StageTransitionEvent, error) {
	return newStageTransitionEvent(ncaId, functionId, functionVersionId, instanceId, event, eventType, detailsJSON)
}

func (c *FndsClient) NewDeploymentStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	deploymentId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	detailsJSON []byte,
) (fndstypes.DeploymentStageTransitionEvent, error) {
	return newDeploymentStageTransitionEvent(ncaId, functionId, functionVersionId, deploymentId, instanceId, event, eventType, detailsJSON)
}

func newStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	detailsJSON []byte,
) (fndstypes.StageTransitionEvent, error) {
	if ncaId == "" {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("ncaId cannot be empty")
	}
	if instanceId == "" {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("instanceId cannot be empty")
	}
	if event == "" {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("event cannot be empty")
	}
	if eventType == "" {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("eventType cannot be empty")
	}
	if functionId == uuid.Nil {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("functionId cannot be nil UUID")
	}
	if functionVersionId == uuid.Nil {
		return fndstypes.StageTransitionEvent{}, fmt.Errorf("functionVersionId cannot be nil UUID")
	}

	stageTransitionEvent := fndstypes.StageTransitionEvent{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		InstanceId:        instanceId,
		Event:             event,
		EventType:         eventType,
		Timestamp:         time.Now(),
		Details:           detailsJSON,
	}
	return stageTransitionEvent, nil
}

func newDeploymentStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	deploymentId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	detailsJSON []byte,
) (fndstypes.DeploymentStageTransitionEvent, error) {
	if ncaId == "" {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("ncaId cannot be empty")
	}
	if instanceId == "" {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("instanceId cannot be empty")
	}
	if event == "" {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("event cannot be empty")
	}
	if eventType == "" {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("eventType cannot be empty")
	}
	if functionId == uuid.Nil {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("functionId cannot be nil UUID")
	}
	if functionVersionId == uuid.Nil {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("functionVersionId cannot be nil UUID")
	}
	if deploymentId == uuid.Nil {
		return fndstypes.DeploymentStageTransitionEvent{}, fmt.Errorf("deploymentId cannot be nil UUID")
	}

	deploymentStageTransitionEvent := fndstypes.DeploymentStageTransitionEvent{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        instanceId,
		Event:             event,
		EventType:         eventType,
		Timestamp:         time.Now(),
		Details:           detailsJSON,
	}
	return deploymentStageTransitionEvent, nil
}

func (c *FndsClient) CreateEvent(ctx context.Context, eventData fndstypes.StageTransitionEvent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(CreateEventEndpointV1, eventData.FunctionVersionId, eventData.InstanceId)

	err := c.doRequest(ctx, "POST", endpoint, eventData, nil)
	if err != nil {
		return err
	}
	return nil
}

func (c *FndsClient) CreateEventV2(ctx context.Context, eventData fndstypes.DeploymentStageTransitionEvent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(CreateEventEndpointV2, eventData.FunctionVersionId, eventData.DeploymentId, eventData.InstanceId)

	err := c.doRequest(ctx, "POST", endpoint, eventData, nil)
	if err != nil {
		return err
	}
	return nil
}

func (c *FndsClient) GetEvents(ctx context.Context, functionVersionId uuid.UUID, instanceId string) ([]fndstypes.StageTransitionEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetEventsEndpointV1, functionVersionId, instanceId)

	var events []fndstypes.StageTransitionEvent
	err := c.doRequest(ctx, "GET", endpoint, nil, &events)
	if err != nil {
		return []fndstypes.StageTransitionEvent{}, err
	}

	return events, nil
}

func (c *FndsClient) GetEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID, instanceId string) ([]fndstypes.DeploymentStageTransitionEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetEventsEndpointV2, functionVersionId, deploymentId, instanceId)

	var events []fndstypes.DeploymentStageTransitionEvent
	err := c.doRequest(ctx, "GET", endpoint, nil, &events)
	if err != nil {
		return []fndstypes.DeploymentStageTransitionEvent{}, err
	}

	return events, nil
}

func (c *FndsClient) GetEventByEventType(ctx context.Context, functionVersionId uuid.UUID, instanceId string, eventType string) (fndstypes.StageTransitionEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetEventByEventTypeEndpointV1, functionVersionId, instanceId, eventType)

	var event fndstypes.StageTransitionEvent
	err := c.doRequest(ctx, "GET", endpoint, nil, &event)
	if err != nil {
		return fndstypes.StageTransitionEvent{}, err
	}

	return event, nil
}

func (c *FndsClient) GetEventByEventTypeV2(
	ctx context.Context,
	functionVersionId uuid.UUID,
	deploymentId uuid.UUID,
	instanceId string,
	eventType string,
) (fndstypes.DeploymentStageTransitionEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetEventByEventTypeEndpointV2, functionVersionId, deploymentId, instanceId, eventType)

	var event fndstypes.DeploymentStageTransitionEvent
	err := c.doRequest(ctx, "GET", endpoint, nil, &event)
	if err != nil {
		return fndstypes.DeploymentStageTransitionEvent{}, err
	}

	return event, nil
}

func (c *FndsClient) DeleteInstanceEvents(ctx context.Context, functionVersionId uuid.UUID, instanceId string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(DeleteInstanceEventsEndpointV1, functionVersionId, instanceId)

	err := c.doRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (c *FndsClient) DeleteInstanceEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID, instanceId string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(DeleteInstanceEventsEndpointV2, functionVersionId, deploymentId, instanceId)

	err := c.doRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (c *FndsClient) DeleteFunctionVersionEvents(ctx context.Context, functionVersionId uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(DeleteFunctionVersionEventsEndpointV1, functionVersionId)

	err := c.doRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (c *FndsClient) DeleteFunctionVersionEventsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(DeleteFunctionVersionEventsEndpointV2, functionVersionId, deploymentId)

	err := c.doRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (c *FndsClient) GetStats(ctx context.Context, functionVersionId uuid.UUID) (fndstypes.DeploymentStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetStatsEndpointV1, functionVersionId)

	var stats fndstypes.DeploymentStats
	err := c.doRequest(ctx, "GET", endpoint, nil, &stats)
	if err != nil {
		return fndstypes.DeploymentStats{}, err
	}
	return stats, nil
}

func (c *FndsClient) GetStatsV2(ctx context.Context, functionVersionId uuid.UUID, deploymentId uuid.UUID) (fndstypes.DeploymentStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := fmt.Sprintf(GetStatsEndpointV2, functionVersionId, deploymentId)

	var stats fndstypes.DeploymentStats
	err := c.doRequest(ctx, "GET", endpoint, nil, &stats)
	if err != nil {
		return fndstypes.DeploymentStats{}, err
	}
	return stats, nil
}

func (c *FndsClient) doRequest(ctx context.Context, method, endpoint string, body interface{}, result interface{}) error {
	fullURL, err := url.JoinPath(c.BaseURL, endpoint)
	if err != nil {
		return fmt.Errorf("failed to build URL from base %s and endpoint %s: %w", c.BaseURL, endpoint, err)
	}

	var requestBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		requestBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, requestBody)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request for %s %s: %w", method, fullURL, err)
	}

	token, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch authentication token: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err // Return the original error from the HTTP client
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		// Read the error response body
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("failed to read error response body: %w", readErr)
		}

		// Return the raw JSON as a string in our minimalist error wrapper
		return &SimpleAPIError{
			StatusCode: resp.StatusCode,
			JSONString: string(bodyBytes),
		}
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response body: %w", err)
		}
	}

	return nil
}

// GetStatusCodeFromError extracts the HTTP status code from an API error if possible
func GetStatusCodeFromError(err error) (int, bool) {
	if apiErr, ok := err.(*SimpleAPIError); ok {
		return apiErr.StatusCode, true
	}
	return 0, false
}

// IsAPIError checks if an error is an API error
func IsAPIError(err error) bool {
	_, ok := err.(*SimpleAPIError)
	return ok
}

// Verify we're implementing the interface
var _ LedgerClient = (*FndsClient)(nil)
