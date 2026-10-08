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

package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	config "ai-api-gateway-service/gateway_config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShadowBucketForPromptCacheKeyFixedVector(t *testing.T) {
	assert.Equal(t, 95, shadowBucketForMaterial([]byte("session-123")))
}

func TestPromptCacheKeyUsesBodyThenConfiguredHeaders(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		headers map[string][]string
		names   []string
	}{
		{
			name: "body wins",
			body: `{"prompt_cache_key":"session-123"}`,
			headers: map[string][]string{
				"X-Session-ID": {"test-key"},
			},
			names: []string{"X-Session-ID"},
		},
		{
			name: "header fallback",
			body: `{}`,
			headers: map[string][]string{
				"X-Session-ID": {" session-123 "},
			},
			names: []string{"X-Session-ID"},
		},
		{
			name: "non-string body falls back",
			body: `{"prompt_cache_key":123}`,
			headers: map[string][]string{
				"X-Session-ID": {"session-123"},
			},
			names: []string{"X-Session-ID"},
		},
		{
			name: "ambiguous header falls through list",
			body: `{}`,
			headers: map[string][]string{
				"X-First":  {"one", "two"},
				"X-Second": {"session-123"},
			},
			names: []string{"X-First", "X-Second"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for name, values := range tc.headers {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}
			sampler := newShadowRequestSampler(
				req,
				[]byte(tc.body),
				shadowSamplingEndpointChatCompletions,
				tc.names,
				fixedRandomBucket(0),
			)

			bucket, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
			require.True(t, ok)
			assert.Equal(t, 95, bucket)
		})
	}
}

func TestPromptCacheKeyCachesFirstUsableSource(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("X-Session-ID", "session-123")
	sampler := newShadowRequestSampler(
		req,
		[]byte(`{}`),
		shadowSamplingEndpointResponses,
		[]string{"X-Session-ID"},
		fixedRandomBucket(0),
	)

	first, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
	require.True(t, ok)
	req.Header.Set("X-Session-ID", "test-key")
	second, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
	require.True(t, ok)
	assert.Equal(t, 95, first)
	assert.Equal(t, first, second)
}

func TestPromptCacheKeyValueHasSameBucketAcrossSourcesAndEndpoints(t *testing.T) {
	bodySampler := newShadowRequestSampler(
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		[]byte(`{"prompt_cache_key":"session-123"}`),
		shadowSamplingEndpointChatCompletions,
		nil,
		fixedRandomBucket(0),
	)
	headerRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	headerRequest.Header.Set("X-Request-Group", "session-123")
	headerSampler := newShadowRequestSampler(
		headerRequest,
		[]byte(`{}`),
		shadowSamplingEndpointResponses,
		[]string{"X-Request-Group"},
		fixedRandomBucket(0),
	)

	bodyBucket, bodyAvailable := bodySampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
	headerBucket, headerAvailable := headerSampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
	require.True(t, bodyAvailable)
	require.True(t, headerAvailable)
	assert.Equal(t, 95, bodyBucket)
	assert.Equal(t, bodyBucket, headerBucket)
}

func TestPromptCacheKeyEmptyHeaderListDisablesHeaderLookup(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set(config.DefaultPromptCacheKeyHeader, "session-123")
	sampler := newShadowRequestSampler(
		req,
		[]byte(`{}`),
		shadowSamplingEndpointResponses,
		[]string{},
		fixedRandomBucket(0),
	)

	_, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
	assert.False(t, ok)
}

func TestCanonicalChatFirstMessageFixedVectors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		material   string
		wantBucket int
	}{
		{
			name:       "text",
			body:       `{"messages":[{"role":"system","content":"Be helpful."},{"role":"user","content":"Hello"},{"role":"assistant","content":"Later"}]}`,
			material:   `{"version":1,"endpoint":"chatCompletions","messages":[{"role":"system","content":"Be helpful."},{"role":"user","content":"Hello"}]}`,
			wantBucket: 67,
		},
		{
			name:       "structured content",
			body:       `{"messages":[{"content":[{"type":"text","text":"Stay concise"}],"role":"developer"},{"role":"user","content":[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"https://example.test/image.png","detail":"low"}}]}]}`,
			material:   `{"version":1,"endpoint":"chatCompletions","messages":[{"role":"developer","content":[{"text":"Stay concise","type":"text"}]},{"role":"user","content":[{"text":"Describe this","type":"text"},{"image_url":{"detail":"low","url":"https://example.test/image.png"},"type":"image_url"}]}]}`,
			wantBucket: 16,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			material, ok := canonicalChatFirstMessage(mustJSONFields(t, tc.body))
			require.True(t, ok)
			assert.Equal(t, tc.material, string(material))
			assert.Equal(t, tc.wantBucket, shadowBucketForMaterial(material))
		})
	}
}

func TestCanonicalResponsesFirstMessageFixedVectors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		material   string
		wantBucket int
	}{
		{
			name:       "string input",
			body:       `{"instructions":"Be helpful.","input":"Hello"}`,
			material:   `{"version":1,"endpoint":"responses","instructions":"Be helpful.","messages":[{"role":"user","content":"Hello"}]}`,
			wantBucket: 37,
		},
		{
			name:       "structured input",
			body:       `{"input":[{"type":"message","role":"assistant","content":"Ignored"},{"content":[{"type":"input_text","text":"Describe this"},{"type":"input_image","image_url":"https://example.test/image.png"}],"role":"user"}],"instructions":"Stay concise"}`,
			material:   `{"version":1,"endpoint":"responses","instructions":"Stay concise","messages":[{"role":"user","content":[{"text":"Describe this","type":"input_text"},{"image_url":"https://example.test/image.png","type":"input_image"}]}]}`,
			wantBucket: 46,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			material, ok := canonicalResponsesFirstMessage(mustJSONFields(t, tc.body))
			require.True(t, ok)
			assert.Equal(t, tc.material, string(material))
			assert.Equal(t, tc.wantBucket, shadowBucketForMaterial(material))
		})
	}
}

func TestFirstMessageHashIgnoresFormattingObjectOrderAndLaterTurns(t *testing.T) {
	first := mustJSONFields(t, `{
  "messages": [
    {"role":"assistant","content":"ignored"},
    {"content":{"b":2,"a":1},"role":"system"},
    {"role":"tool","content":"ignored"},
    {"role":"user","content":[{"type":"text","text":"Hello"}]}
  ]
}`)
	second := mustJSONFields(t, `{"messages":[{"content":"ignored","role":"assistant"},{"role":"system","content":{"a":1,"b":2}},{"content":"ignored","role":"tool"},{"content":[{"text":"Hello","type":"text"}],"role":"user"},{"role":"assistant","content":"later"},{"role":"user","content":"later"}]}`)

	firstMaterial, ok := canonicalChatFirstMessage(first)
	require.True(t, ok)
	secondMaterial, ok := canonicalChatFirstMessage(second)
	require.True(t, ok)
	assert.Equal(t, firstMaterial, secondMaterial)
}

func TestFirstMessageHashRequiresNonemptyUserContent(t *testing.T) {
	tests := []struct {
		name     string
		endpoint shadowSamplingEndpoint
		body     string
	}{
		{name: "chat missing", endpoint: shadowSamplingEndpointChatCompletions, body: `{"messages":[{"role":"system","content":"hello"}]}`},
		{name: "chat empty text", endpoint: shadowSamplingEndpointChatCompletions, body: `{"messages":[{"role":"user","content":""}]}`},
		{name: "chat empty list", endpoint: shadowSamplingEndpointChatCompletions, body: `{"messages":[{"role":"user","content":[]}]}`},
		{name: "chat empty object", endpoint: shadowSamplingEndpointChatCompletions, body: `{"messages":[{"role":"user","content":{}}]}`},
		{name: "chat null", endpoint: shadowSamplingEndpointChatCompletions, body: `{"messages":[{"role":"user","content":null}]}`},
		{name: "responses empty string", endpoint: shadowSamplingEndpointResponses, body: `{"input":""}`},
		{name: "responses empty item", endpoint: shadowSamplingEndpointResponses, body: `{"input":[{"role":"user","content":[]}]}`},
		{name: "responses stateless followup", endpoint: shadowSamplingEndpointResponses, body: `{"previous_response_id":"response-1"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sampler := newShadowRequestSampler(
				httptest.NewRequest(http.MethodPost, "/", nil),
				[]byte(tc.body),
				tc.endpoint,
				nil,
				fixedRandomBucket(0),
			)
			_, ok := sampler.bucket(config.ShadowSamplingMethodFirstMessageHash)
			assert.False(t, ok)
		})
	}
}

func TestAdmittedShadowsUsesFallbackOnlyWhenMaterialIsUnavailable(t *testing.T) {
	t.Run("valid rejection is final", func(t *testing.T) {
		randomCalls := 0
		sampler := newShadowRequestSampler(
			httptest.NewRequest(http.MethodPost, "/", nil),
			[]byte(`{"prompt_cache_key":"session-123","messages":[{"role":"user","content":"Hello"}]}`),
			shadowSamplingEndpointChatCompletions,
			nil,
			func() int { randomCalls++; return 0 },
		)
		shadow := shadowConfig{
			modelName:       "shadow",
			percentage:      95,
			samplingMethods: testShadowSamplingMethods(config.ShadowSamplingMethodPromptCacheKey),
		}

		assert.Empty(t, admittedShadowsWithSampler([]shadowConfig{shadow}, sampler))
		assert.Zero(t, randomCalls)
	})

	t.Run("missing prompt key uses first message", func(t *testing.T) {
		randomCalls := 0
		sampler := newShadowRequestSampler(
			httptest.NewRequest(http.MethodPost, "/", nil),
			[]byte(`{"messages":[{"role":"system","content":"Be helpful."},{"role":"user","content":"Hello"}]}`),
			shadowSamplingEndpointChatCompletions,
			nil,
			func() int { randomCalls++; return 99 },
		)
		shadow := shadowConfig{
			modelName:  "shadow",
			percentage: 68,
			samplingMethods: testShadowSamplingMethods(
				config.ShadowSamplingMethodPromptCacheKey,
				config.ShadowSamplingMethodFirstMessageHash,
			),
		}

		assert.Len(t, admittedShadowsWithSampler([]shadowConfig{shadow}, sampler), 1)
		assert.Zero(t, randomCalls)
	})

	t.Run("missing deterministic material uses random", func(t *testing.T) {
		randomCalls := 0
		sampler := newShadowRequestSampler(
			httptest.NewRequest(http.MethodPost, "/", nil),
			[]byte(`{}`),
			shadowSamplingEndpointResponses,
			nil,
			func() int { randomCalls++; return 0 },
		)
		shadow := shadowConfig{
			modelName:  "shadow",
			percentage: 1,
			samplingMethods: testShadowSamplingMethods(
				config.ShadowSamplingMethodPromptCacheKey,
				config.ShadowSamplingMethodFirstMessageHash,
			),
		}

		assert.Len(t, admittedShadowsWithSampler([]shadowConfig{shadow}, sampler), 1)
		assert.Equal(t, 1, randomCalls)
	})

	t.Run("first usable header is final", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-First", "session-123")
		req.Header.Set("X-Second", "test-key")
		sampler := newShadowRequestSampler(
			req,
			[]byte(`{}`),
			shadowSamplingEndpointResponses,
			[]string{"X-First", "X-Second"},
			fixedRandomBucket(0),
		)
		shadow := shadowConfig{
			modelName:       "shadow",
			percentage:      95,
			samplingMethods: testShadowSamplingMethods(config.ShadowSamplingMethodPromptCacheKey),
		}

		assert.Empty(t, admittedShadowsWithSampler([]shadowConfig{shadow}, sampler))
	})
}

func TestAdmittedShadowsAtFullPercentageDoesNotSample(t *testing.T) {
	sampler := newShadowRequestSampler(
		httptest.NewRequest(http.MethodPost, "/", nil),
		[]byte(`not-json`),
		shadowSamplingEndpointChatCompletions,
		nil,
		func() int {
			t.Fatal("random sampling must not run")
			return 0
		},
	)
	shadow := shadowConfig{
		modelName:  "shadow",
		percentage: 100,
		samplingMethods: testShadowSamplingMethods(
			config.ShadowSamplingMethodPromptCacheKey,
			config.ShadowSamplingMethodFirstMessageHash,
		),
	}

	assert.Len(t, admittedShadowsWithSampler([]shadowConfig{shadow}, sampler), 1)
	assert.False(t, sampler.bodyParsed)
}

func TestShadowSamplingDoesNotConsumeRequestBody(t *testing.T) {
	original := []byte(" \n{\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"model\":\"acme/model\"}\n")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(original))
	body, err := extractOpenAIJSONBody(req)
	require.NoError(t, err)

	sampler := newShadowRequestSampler(
		req,
		body.raw,
		shadowSamplingEndpointChatCompletions,
		nil,
		fixedRandomBucket(0),
	)
	_, ok := sampler.bucket(config.ShadowSamplingMethodFirstMessageHash)
	require.True(t, ok)

	forwarded, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())
	assert.Equal(t, original, forwarded)
}

func mustJSONFields(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &fields))
	return fields
}
