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

package selfhosted

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleResults() []CheckResult {
	return []CheckResult{
		{ID: "a", Category: "kubernetes-api", Severity: "error", Passed: true, Message: "can query the Kubernetes API"},
		{ID: "b", Category: "kubernetes-api", Severity: "error", Passed: true, Message: "is running the minimum API version (1.28+)"},
		{ID: "c", Category: "pre-kubernetes-setup", Severity: "error", Passed: false, Message: "Gateway API CRDs not installed",
			HintURL: "https://docs.nvidia.com/nvcf/self-hosted/gateway-api"},
	}
}

func TestRenderText_GroupsByCategoryAndPrintsHints(t *testing.T) {
	var buf bytes.Buffer
	RenderText(&buf, sampleResults())
	out := buf.String()
	assert.Contains(t, out, "kubernetes-api")
	assert.Contains(t, out, "pre-kubernetes-setup")
	assert.Contains(t, out, "✓ can query the Kubernetes API")
	assert.Contains(t, out, "× Gateway API CRDs not installed")
	assert.Contains(t, out, "see https://docs.nvidia.com/nvcf/self-hosted/gateway-api")
	assert.Contains(t, out, "Status check results are ×")
}

func TestRenderJSON_MatchesSchema(t *testing.T) {
	var buf bytes.Buffer
	RenderJSON(&buf, sampleResults())
	var got struct {
		Status string `json:"status"`
		Checks []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Severity string `json:"severity"`
			Passed   bool   `json:"passed"`
			Message  string `json:"message"`
			HintURL  string `json:"hint_url,omitempty"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "error", got.Status)
	assert.Len(t, got.Checks, 3)
	assert.Equal(t, "https://docs.nvidia.com/nvcf/self-hosted/gateway-api", got.Checks[2].HintURL)
}

func TestRenderText_AllPassPrintsOK(t *testing.T) {
	results := []CheckResult{
		{ID: "a", Category: "x", Severity: "error", Passed: true, Message: "one"},
	}
	var buf bytes.Buffer
	RenderText(&buf, results)
	assert.Contains(t, buf.String(), "Status check results are ✓")
}
