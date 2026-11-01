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
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// AdminQueueDto represents queue information for a function version (admin view)
type AdminQueueDto struct {
	FunctionVersionID string `json:"functionVersionId"`    // Function version ID
	FunctionName      string `json:"functionName"`         // Function name
	FunctionStatus    string `json:"functionStatus"`       // Function status (ACTIVE, DEPLOYING, etc.)
	QueueDepth        int    `json:"queueDepth,omitempty"` // Approximate number of messages in queue
}

// CrossAccountQueuesResponse represents queue details response for cross-account operations
type CrossAccountQueuesResponse struct {
	FunctionID string          `json:"functionId"` // Function ID
	Queues     []AdminQueueDto `json:"queues"`     // Details of all queues for this function
}

// GetFunctionQueueDetails retrieves queue details for the specified function across accounts
func (c *Client) GetFunctionQueueDetails(ctx context.Context, ncaId, functionId string) (*CrossAccountQueuesResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s/queues/functions/%s",
		url.PathEscape(ncaId), url.PathEscape(functionId))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result CrossAccountQueuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetFunctionVersionQueueDetails retrieves queue details for the specified function version across accounts
func (c *Client) GetFunctionVersionQueueDetails(ctx context.Context, ncaId, functionId, versionId string) (*CrossAccountQueuesResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s/queues/functions/%s/versions/%s",
		url.PathEscape(ncaId), url.PathEscape(functionId), url.PathEscape(versionId))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result CrossAccountQueuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
