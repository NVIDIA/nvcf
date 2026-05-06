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
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// UpdateFunctionSecrets updates secrets for the specified function version
// Requires: update_secrets scope
func (c *Client) UpdateFunctionSecrets(ctx context.Context, functionId, versionId string, secretData interface{}) error {
	endpoint := fmt.Sprintf("/v2/nvcf/secrets/functions/%s/versions/%s",
		url.PathEscape(functionId), url.PathEscape(versionId))

	resp, err := c.makeRequest(ctx, "PUT", endpoint, secretData)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateTelemetrySecrets updates secrets for the specified telemetry
// Requires: update_secrets scope
func (c *Client) UpdateTelemetrySecrets(ctx context.Context, telemetryId string, secretData interface{}) error {
	endpoint := fmt.Sprintf("/v2/nvcf/secrets/telemetries/%s",
		url.PathEscape(telemetryId))

	resp, err := c.makeRequest(ctx, "PUT", endpoint, secretData)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Cross-account admin versions (for admin commands only)

// UpdateFunctionSecretsAdmin updates secrets for the specified function version across accounts
// Requires: admin:update_secrets scope
func (c *Client) UpdateFunctionSecretsAdmin(ctx context.Context, ncaId, functionId, versionId string, secretData interface{}) error {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s/secrets/functions/%s/versions/%s",
		url.PathEscape(ncaId), url.PathEscape(functionId), url.PathEscape(versionId))

	resp, err := c.makeRequest(ctx, "PUT", endpoint, secretData)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateTelemetrySecretsAdmin updates secrets for the specified telemetry across accounts
// Requires: admin:update_secrets scope
func (c *Client) UpdateTelemetrySecretsAdmin(ctx context.Context, ncaId, telemetryId string, secretData interface{}) error {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s/secrets/telemetries/%s",
		url.PathEscape(ncaId), url.PathEscape(telemetryId))

	resp, err := c.makeRequest(ctx, "PUT", endpoint, secretData)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
