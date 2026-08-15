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
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.uber.org/zap"

	pdpv1 "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"
)

// ApiKeysClient implements Authorizer for self-managed deployments where the
// api-keys-api evaluation endpoint requires no authentication.
type ApiKeysClient struct {
	evaluatorAddr string
	policyCfg     *PolicyConfig
	httpClient    *http.Client
}

// NewApiKeysClient creates an Authorizer that calls the api-keys-api evaluation
// endpoint without authentication, for use in self-managed deployments.
func NewApiKeysClient(evaluatorAddr string, policyCfg *PolicyConfig, httpClient *http.Client) Authorizer {
	return &ApiKeysClient{
		evaluatorAddr: evaluatorAddr,
		policyCfg:     policyCfg,
		httpClient:    httpClient,
	}
}

func (c *ApiKeysClient) PolicyConfig() *PolicyConfig {
	return c.policyCfg
}

func (c *ApiKeysClient) Evaluate(ctx context.Context, req *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rule request: %w", err)
	}

	url := c.evaluatorAddr + getEvaluationURL(c.policyCfg.Namespace, c.policyCfg.PolicyFQDN)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build evaluation request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("evaluation request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readPolicyResponseBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read evaluation response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		zap.L().Error("api-keys-api returned non-200",
			zap.Int("status_code", resp.StatusCode),
		)
		return nil, fmt.Errorf("api-keys-api returned status %d", resp.StatusCode)
	}

	var ruleResp pdpv1.RuleResponse
	if err := json.Unmarshal(body, &ruleResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal evaluation response: %w", err)
	}

	return &ruleResp, nil
}
