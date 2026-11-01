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

package api

import (
	"context"
	"fmt"
	"net/http"
)

func (c *Sys) MFAValidate(requestID string, payload map[string]interface{}) (*Secret, error) {
	return c.MFAValidateWithContext(context.Background(), requestID, payload)
}

func (c *Sys) MFAValidateWithContext(ctx context.Context, requestID string, payload map[string]interface{}) (*Secret, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	body := map[string]interface{}{
		"mfa_request_id": requestID,
		"mfa_payload":    payload,
	}

	r := c.c.NewRequest(http.MethodPost, fmt.Sprintf("/v1/sys/mfa/validate"))
	if err := r.SetJSONBody(body); err != nil {
		return nil, fmt.Errorf("failed to set request body: %w", err)
	}

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	secret, err := ParseSecret(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse secret from response: %w", err)
	}

	if secret == nil {
		return nil, fmt.Errorf("data from server response is empty")
	}

	return secret, nil
}
