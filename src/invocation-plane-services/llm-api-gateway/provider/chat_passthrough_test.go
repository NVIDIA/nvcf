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
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

// Upstream SSE written as literal JSON so fields the gateway structs do not
// model are exercised, rather than chunks built from those structs.
const passthroughUpstreamSSE = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me "},"logprobs":{"content":[{"token":"let","logprob":-0.1,"bytes":[108,101,116],"top_logprobs":[],"vendor_rank":1}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{"reasoning_content":"think"},"logprobs":{"content":[{"token":" think","logprob":-0.2,"bytes":null,"top_logprobs":[]}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{"content":"ok"},"logprobs":{"content":[{"token":"ok","logprob":-0.3,"bytes":[111,107],"top_logprobs":[]}]},"finish_reason":"stop","stop_reason":null}],"nvext":{"worker_id":"w-1"}}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}

data: [DONE]

`

func normalizedChatRequestFromBody(t *testing.T, body string) *NormalizedRequest {
	t.Helper()

	var request models.ChatCompletionRequest
	require.NoError(t, json.Unmarshal([]byte(body), &request))
	request.Model = "upstream-model"
	return &NormalizedRequest{ChatRequest: &request, RawBody: []byte(body)}
}

func decodeJSONObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()

	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &object))
	return object
}

func TestOutboundChatRequestBodyForwardsClientFieldsVerbatim(t *testing.T) {
	t.Parallel()

	clientBody := `{
		"model": "fn-alpha/company-name/model-name",
		"Model": "shadowed-model",
		"messages": [
			{"role": "system", "content": [{"type": "text", "text": "be brief", "cache_control": {"type": "ephemeral"}}]},
			{"role": "assistant", "content": "earlier", "reasoning_content": "earlier thought"},
			{"role": "user", "content": "Reply with the word ok."}
		],
		"chat_template_kwargs": {"enable_thinking": false},
		"top_k": 20,
		"min_p": 0.05,
		"repetition_penalty": 1.1,
		"temperature": 0.1234567890123456789,
		"logprobs": true,
		"nvext": {"guided_json": {"type": "object"}, "priority": 3},
		"debug": true,
		"stream": false,
		"stream_options": {"continuous_usage_stats": true, "include_usage": false}
	}`
	request := normalizedChatRequestFromBody(t, clientBody)

	body, err := outboundChatRequestBody(request, true)
	require.NoError(t, err)

	client := decodeJSONObject(t, []byte(clientBody))
	outbound := decodeJSONObject(t, body)

	for _, key := range []string{
		"messages",
		"chat_template_kwargs",
		"top_k",
		"min_p",
		"repetition_penalty",
		"logprobs",
		"nvext",
	} {
		require.Contains(t, outbound, key)
		require.JSONEq(t, string(client[key]), string(outbound[key]), key)
	}
	require.Equal(t, "0.1234567890123456789", string(outbound["temperature"]),
		"numbers are forwarded as the client wrote them")

	require.JSONEq(t, `"upstream-model"`, string(outbound["model"]))
	require.JSONEq(t, `true`, string(outbound["stream"]))
	require.JSONEq(t, `"auto"`, string(outbound["service_tier"]))
	require.JSONEq(t, `{"continuous_usage_stats":true,"include_usage":true}`, string(outbound["stream_options"]))
	require.NotContains(t, outbound, "Model")
	require.NotContains(t, outbound, "debug")
}

func TestOutboundChatRequestBodyKeepsStreamingClientStreamOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		body              string
		wantStreamOptions string
	}{
		{
			name:              "client options kept verbatim",
			body:              `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false,"continuous_usage_stats":true}}`,
			wantStreamOptions: `{"include_usage":false,"continuous_usage_stats":true}`,
		},
		{
			name: "no options added",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, err := outboundChatRequestBody(normalizedChatRequestFromBody(t, tt.body), true)
			require.NoError(t, err)

			outbound := decodeJSONObject(t, body)
			if tt.wantStreamOptions == "" {
				require.NotContains(t, outbound, "stream_options")
				return
			}
			require.JSONEq(t, tt.wantStreamOptions, string(outbound["stream_options"]))
		})
	}
}

func TestOutboundChatRequestBodyUsesResolvedServiceTier(t *testing.T) {
	t.Parallel()

	// normalizeChatRequest fills an omitted tier from the configured default.
	request := normalizedChatRequestFromBody(
		t,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"SERVICE_TIER":null}`,
	)
	request.ChatRequest.ServiceTier = servicetier.Flex

	body, err := outboundChatRequestBody(request, true)
	require.NoError(t, err)

	outbound := decodeJSONObject(t, body)
	require.JSONEq(t, `"flex"`, string(outbound["service_tier"]))
	require.NotContains(t, outbound, "SERVICE_TIER")
}

func TestOutboundChatRequestBodyFallsBackToTypedRequestWithoutRawBody(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model:    "upstream-model",
			Messages: &[]models.ChatMessage{{Role: models.ChatCompletionRoleUser, Content: models.SingleTextContent("hi")}},
		},
	}

	body, err := outboundChatRequestBody(request, true)
	require.NoError(t, err)

	outbound := decodeJSONObject(t, body)
	require.JSONEq(t, `"upstream-model"`, string(outbound["model"]))
	require.JSONEq(t, `[{"role":"user","content":"hi"}]`, string(outbound["messages"]))
}

func TestOutboundChatRequestBodyRejectsNonObjectRawBody(t *testing.T) {
	t.Parallel()

	request := &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{Model: "upstream-model"},
		RawBody:     []byte(`["not","an","object"]`),
	}

	_, err := outboundChatRequestBody(request, true)
	require.ErrorContains(t, err, "decode chat request body")
}

func newPassthroughStargateProvider(t *testing.T, captured *[]byte) *StargateProvider {
	t.Helper()

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		*captured = body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{headerContentType: []string{contentTypeSSE}},
			Body:       io.NopCloser(strings.NewReader(passthroughUpstreamSSE)),
		}, nil
	})}
	return provider
}

func TestStargateProviderCompleteForwardsRawBodyAndAggregatesReasoningContentAndLogprobs(t *testing.T) {
	t.Parallel()

	var captured []byte
	provider := newPassthroughStargateProvider(t, &captured)
	request := normalizedChatRequestFromBody(
		t,
		`{"model":"fn-alpha/m","messages":[{"role":"user","content":"Reply with the word ok."}],"chat_template_kwargs":{"enable_thinking":false},"logprobs":true}`,
	)

	response, err := provider.Complete(
		context.Background(),
		&requestctx.RequestContext{RequestID: "req-1", RoutingKey: "fn-alpha"},
		request,
	)
	require.NoError(t, err)

	outbound := decodeJSONObject(t, captured)
	require.JSONEq(t, `{"enable_thinking":false}`, string(outbound["chat_template_kwargs"]))

	require.Len(t, response.Choices, 1)
	choice := response.Choices[0]
	require.Equal(t, "ok", ptr.Deref(choice.Message.Content))
	require.Equal(t, "let me think", ptr.Deref(choice.Message.ReasoningContent))
	require.Nil(t, choice.Message.Reasoning)
	require.Equal(t, models.FinishReasonStop, choice.FinishReason)
	require.NotNil(t, choice.Logprobs)
	require.Len(t, choice.Logprobs.Content, 3)
	require.JSONEq(
		t,
		`{"token":"let","logprob":-0.1,"bytes":[108,101,116],"top_logprobs":[],"vendor_rank":1}`,
		string(choice.Logprobs.Content[0]),
	)
	require.Equal(t, uint32(3), response.Usage.CompletionTokens)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"reasoning_content":"let me think"`)
	require.Contains(t, string(encoded), `"logprobs":{"content":[`)
}

func TestStargateProviderCompleteOmitsLogprobsWhenUpstreamSendsNone(t *testing.T) {
	t.Parallel()

	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{headerContentType: []string{contentTypeSSE}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"logprobs\":null,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			)),
		}, nil
	})}

	response, err := provider.Complete(
		context.Background(),
		&requestctx.RequestContext{RequestID: "req-1"},
		normalizedChatRequestFromBody(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	)
	require.NoError(t, err)

	require.Nil(t, response.Choices[0].Logprobs)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "logprobs")
	require.NotContains(t, string(encoded), "reasoning_content")
}

func TestStargateProviderStreamCarriesRawUpstreamChunks(t *testing.T) {
	t.Parallel()

	var captured []byte
	provider := newPassthroughStargateProvider(t, &captured)
	request := normalizedChatRequestFromBody(
		t,
		`{"model":"fn-alpha/m","messages":[{"role":"user","content":"hi"}],"stream":true,"chat_template_kwargs":{"enable_thinking":false}}`,
	)

	events, err := provider.Stream(
		context.Background(),
		&requestctx.RequestContext{RequestID: "req-1"},
		request,
	)
	require.NoError(t, err)

	var raws []string
	for event := range events {
		require.NoError(t, event.Err)
		require.NotNil(t, event.Chunk)
		raws = append(raws, string(event.Raw))
	}

	var wantRaws []string
	for _, line := range strings.Split(passthroughUpstreamSSE, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok && payload != "[DONE]" {
			wantRaws = append(wantRaws, payload)
		}
	}
	require.Equal(t, wantRaws, raws)

	outbound := decodeJSONObject(t, captured)
	require.JSONEq(t, `{"enable_thinking":false}`, string(outbound["chat_template_kwargs"]))
	require.NotContains(t, outbound, "stream_options")
}

func TestStargateProviderCompleteCarriesUnmodeledChunkMembers(t *testing.T) {
	t.Parallel()

	upstream := `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"o"},"finish_reason":null,"stop_reason":null}],"nvext":{"worker_id":"w-1"},"kv_transfer_params":{"remote":"a"}}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"k"},"finish_reason":"stop","stop_reason":42}],"kv_transfer_params":null}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9,"num_cached_tokens":4}}

data: [DONE]

`
	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{headerContentType: []string{contentTypeSSE}},
			Body:       io.NopCloser(strings.NewReader(upstream)),
		}, nil
	})}

	response, err := provider.Complete(
		context.Background(),
		&requestctx.RequestContext{RequestID: "req-1"},
		normalizedChatRequestFromBody(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	)
	require.NoError(t, err)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	got := decodeJSONObject(t, encoded)

	require.JSONEq(t, `{"worker_id":"w-1"}`, string(got["nvext"]))
	require.JSONEq(t, `{"remote":"a"}`, string(got["kv_transfer_params"]),
		"a later null does not erase an earlier value")
	require.JSONEq(t, `"chat.completion"`, string(got["object"]),
		"typed members are not overridden by chunk members")

	var choices []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got["choices"], &choices))
	require.Len(t, choices, 1)
	require.JSONEq(t, `42`, string(choices[0]["stop_reason"]))
	require.JSONEq(t, `{"role":"assistant","content":"ok"}`, string(choices[0]["message"]))

	usage := decodeJSONObject(t, got["usage"])
	require.JSONEq(t, `4`, string(usage["num_cached_tokens"]))
	require.JSONEq(t, `7`, string(usage["prompt_tokens"]))
}

func TestStargateProviderCompleteMergesUnmodeledDeltaAndLogprobsMembers(t *testing.T) {
	t.Parallel()

	upstream := `data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","refusal":"I can","audio":{"id":"a1"}},"logprobs":{"content":[],"vendor_rank":1},"finish_reason":null}]}

data: {"id":"c","choices":[{"index":0,"delta":{"refusal":"not help","audio":null},"logprobs":{"content":[],"vendor_rank":null},"finish_reason":null}]}

data: {"id":"c","choices":[{"index":0,"delta":{"refusal":".","audio":{"id":"a2"}},"logprobs":{"content":[],"vendor_rank":2},"finish_reason":"stop"}]}

data: [DONE]

`
	provider, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{headerContentType: []string{contentTypeSSE}},
			Body:       io.NopCloser(strings.NewReader(upstream)),
		}, nil
	})}

	response, err := provider.Complete(
		context.Background(),
		&requestctx.RequestContext{RequestID: "req-1"},
		normalizedChatRequestFromBody(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	)
	require.NoError(t, err)

	encoded, err := json.Marshal(response.Choices[0])
	require.NoError(t, err)
	choice := decodeJSONObject(t, encoded)

	message := decodeJSONObject(t, choice["message"])
	require.JSONEq(t, `"I cannot help."`, string(message["refusal"]), "string delta members concatenate")
	require.JSONEq(t, `{"id":"a2"}`, string(message["audio"]), "other delta members keep the latest non-null value")
	require.JSONEq(t, `"assistant"`, string(message["role"]))

	logprobs := decodeJSONObject(t, choice["logprobs"])
	require.JSONEq(t, `2`, string(logprobs["vendor_rank"]))
}
