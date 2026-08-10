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
)

// RegisterClusterRequest represents the request to register a cluster with ICMS
type RegisterClusterRequest struct {
	ClusterName      string   `json:"clusterName"`
	ClusterGroupName string   `json:"clusterGroupName"`
	NcaID            string   `json:"ncaId"`
	CloudProvider    string   `json:"cloudProvider"`
	Region           string   `json:"region"`
	NvcaVersion      string   `json:"nvcaVersion"`
	Capabilities     []string `json:"capabilities,omitempty"`
	JWKS             *string  `json:"jwks,omitempty"`
	OIDCIssuer       *string  `json:"oidcIssuer,omitempty"`
}

// RegisterClusterResponse represents the response from cluster registration
type RegisterClusterResponse struct {
	ClusterGroup ClusterGroup `json:"clusterGroup,omitempty"`
	// Flat fields for alternative response shapes
	ClusterGroupID string `json:"clusterGroupId,omitempty"`
	ClusterID      string `json:"clusterId,omitempty"`
}

// UpdateClusterJWKSRequest represents the request to update cluster JWKS
type UpdateClusterJWKSRequest struct {
	JWKS       string  `json:"jwks"`
	OIDCIssuer *string `json:"oidcIssuer,omitempty"`
}

const (
	errFailedToSendRequest = "failed to send request: %w"
	errICMSAPI              = "ICMS API error %d: %s"
)

// RegisterCluster registers a new cluster with ICMS
func (c *Client) RegisterCluster(ctx context.Context, sisURL, ncaID string, req *RegisterClusterRequest) (*RegisterClusterResponse, error) {
	endpoint := fmt.Sprintf("/v1/accounts/%s/clusters", url.PathEscape(ncaID))

	resp, err := c.makeICMSRequest(ctx, "POST", sisURL, endpoint, req)
	if err != nil {
		return nil, fmt.Errorf(errFailedToSendRequest, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf(errICMSAPI, resp.StatusCode, string(body))
	}

	var result RegisterClusterResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w (body: %s)", err, string(body))
	}

	return &result, nil
}

// UpdateClusterJWKS updates the JWKS for an existing cluster
func (c *Client) UpdateClusterJWKS(ctx context.Context, sisURL, clusterID string, req *UpdateClusterJWKSRequest) error {
	endpoint := fmt.Sprintf("/v1/nvca/clusters/%s/jwks", url.PathEscape(clusterID))

	resp, err := c.makeICMSRequest(ctx, "PUT", sisURL, endpoint, req)
	if err != nil {
		return fmt.Errorf(errFailedToSendRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("ICMS API error %d (failed to read error body: %v)", resp.StatusCode, readErr)
		}
		return fmt.Errorf(errICMSAPI, resp.StatusCode, string(body))
	}

	return nil
}

// ICMSCluster represents a cluster returned by the ICMS list-clusters endpoint.
//
// The enrichment fields below (NVCAVersion, ClusterStatus, NVCALastConnected,
// HealthyHeartbeatReportTime) exist in the SIS cluster_by_cluster_id table and
// populate only if the list endpoint serializes them. They back the control-
// plane view of `cluster agent status`. Follow-up: SIS should expose a richer
// GET /v1/accounts/{ncaId}/clusters/{clusterId} so callers do not list-and-match.
type ICMSCluster struct {
	ClusterID      string `json:"clusterId,omitempty"`
	ClusterName    string `json:"clusterName,omitempty"`
	ClusterGroupID string `json:"clusterGroupId,omitempty"`

	NVCAVersion                string `json:"nvcaVersion,omitempty"`
	ClusterStatus              string `json:"clusterStatus,omitempty"`
	NVCALastConnected          string `json:"nvcaLastConnected,omitempty"`
	HealthyHeartbeatReportTime string `json:"healthyHeartbeatReportTime,omitempty"`
}

// ListClusters lists clusters for an account via the ICMS endpoint
// GET /v1/accounts/{ncaId}/clusters. This uses cluster-management auth,
// unlike the NVCF API's ListClusterGroups which requires NVCF API auth.
func (c *Client) ListClusters(ctx context.Context, sisURL, ncaID string) ([]ICMSCluster, error) {
	endpoint := fmt.Sprintf("/v1/accounts/%s/clusters", url.PathEscape(ncaID))

	resp, err := c.makeICMSRequest(ctx, "GET", sisURL, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf(errFailedToSendRequest, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(errICMSAPI, resp.StatusCode, string(body))
	}

	var result []ICMSCluster
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w (body: %s)", err, string(body))
	}

	return result, nil
}

// DeleteCluster deletes a cluster registration from ICMS using the
// account-scoped endpoint DELETE /v1/accounts/{ncaId}/clusters/{clusterId}.
// The legacy /v1/nvca/clusters/{clusterId} route is not a valid SIS path and
// returns 404.
func (c *Client) DeleteCluster(ctx context.Context, sisURL, ncaID, clusterID string) error {
	endpoint := fmt.Sprintf("/v1/accounts/%s/clusters/%s", url.PathEscape(ncaID), url.PathEscape(clusterID))

	resp, err := c.makeICMSRequest(ctx, "DELETE", sisURL, endpoint, nil)
	if err != nil {
		return fmt.Errorf(errFailedToSendRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("ICMS API error %d (failed to read error body: %v)", resp.StatusCode, readErr)
		}
		return fmt.Errorf(errICMSAPI, resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) makeICMSRequest(ctx context.Context, method, sisURL, endpoint string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	fullURL := sisURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// Host header: http.NewRequestWithContext already populates req.Host from
	// req.URL.Host, so we only need to override when icms_host (NVCF_ICMS_HOST)
	// is set explicitly. That setting is required for gateway-routed deployments
	// where the URL must dial the bare ELB (DNS-resolvable) but the gateway
	// HTTPRoute matches Host: sis.<gateway-addr>. The api_host transport in
	// debug_transport.go respects this via its "req.Host != req.URL.Host"
	// override-detected branch, so an explicit ICMSHost survives the transport
	// without being rewritten to api_host.
	if c.config != nil && c.config.ICMSHost != "" {
		req.Host = c.config.ICMSHost
	}

	return c.httpClient.Do(req)
}
