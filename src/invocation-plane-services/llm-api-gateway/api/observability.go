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

	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

func endpointLabel(c *GatewayContext) string {
	return requestRoute(c)
}

func (m observabilityMetrics) recordLLMUsage(
	ctx context.Context,
	endpoint string,
	usage *models.ChatCompletionUsage,
	stream bool,
) {
	if usage == nil {
		return
	}

	streamValue := boolLabel(stream)
	addTokens := func(tokenType string, value uint32) {
		if value == 0 {
			return
		}
		telemetry.AddWithContext(
			ctx,
			m.llmTokens,
			int64(value),
			attribute.String("endpoint", endpoint),
			attribute.String("token_type", tokenType),
			attribute.String("stream", streamValue),
		)
	}
	addTokens("prompt", usage.PromptTokens)
	addTokens("completion", usage.CompletionTokens)
	addTokens("total", usage.TotalTokens)

	recordProviderTime := func(phase string, value float64) {
		if value <= 0 {
			return
		}
		telemetry.RecordWithContext(
			ctx,
			m.providerTime,
			value,
			attribute.String("endpoint", endpoint),
			attribute.String("phase", phase),
			attribute.String("stream", streamValue),
		)
	}
	if usage.QueueTime != nil {
		recordProviderTime("queue", *usage.QueueTime)
	}
	recordProviderTime("prompt", usage.PromptTime)
	recordProviderTime("completion", usage.CompletionTime)
	recordProviderTime("total", usage.TotalTime)
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
