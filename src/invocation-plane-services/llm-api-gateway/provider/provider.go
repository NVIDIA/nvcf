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

package provider

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

var ErrProxyNotSupported = errors.New("openai proxy endpoints are not supported by this provider")

type AdmissionPlan interface {
	FinalizeTokens(
		ctx context.Context,
		request ratelimit.ResourceRequest,
	) (map[ratelimit.LimitDimension]*ratelimit.RateLimitResult, error)
	ReleaseOutputReservation(
		ctx context.Context,
	) (map[ratelimit.LimitDimension]*ratelimit.RateLimitResult, error)
}

type NormalizedRequest struct {
	ChatRequest     *models.ChatCompletionRequest
	InputTokens     int
	MaxOutputTokens int
	AdmissionPlan   AdmissionPlan
}

type StreamEvent struct {
	Chunk *models.ChatCompletionChunk
	Err   error
}

type ProxyRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     io.ReadCloser
	// InputTokens and TokenEstimate are forwarded as Stargate proxy headers when
	// known. Stargate requires X-Input-Tokens for native /v1/responses and
	// /v1/embeddings routing metadata.
	InputTokens   int
	TokenEstimate int
}

type ProxyResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type InferenceProvider interface {
	Complete(
		ctx context.Context,
		reqCtx *requestctx.RequestContext,
		request *NormalizedRequest,
	) (*models.ChatCompletionResponse, error)
	Stream(
		ctx context.Context,
		reqCtx *requestctx.RequestContext,
		request *NormalizedRequest,
	) (<-chan StreamEvent, error)
}

type OpenAIProxyProvider interface {
	Proxy(
		ctx context.Context,
		reqCtx *requestctx.RequestContext,
		request *ProxyRequest,
	) (*ProxyResponse, error)
}
