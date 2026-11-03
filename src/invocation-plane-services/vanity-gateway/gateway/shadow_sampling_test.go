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
	"strconv"
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

func TestPromptCacheKeyBodyValueIsTrimmedLikeHeaders(t *testing.T) {
	for _, body := range []string{
		`{"prompt_cache_key":"session-123"}`,
		`{"prompt_cache_key":"  session-123\n"}`,
		`{"prompt_cache_key":"\tsession-123 "}`,
	} {
		t.Run(body, func(t *testing.T) {
			sampler := newShadowRequestSampler(
				httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				[]byte(body),
				shadowSamplingEndpointChatCompletions,
				nil,
				fixedRandomBucket(0),
			)

			bucket, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
			require.True(t, ok)
			assert.Equal(t, 95, bucket)
		})
	}
}

func TestPromptCacheKeyWhitespaceOnlyBodyValueFallsThrough(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"prompt_cache_key":"   "}`)

	noHeader := newShadowRequestSampler(req, body, shadowSamplingEndpointChatCompletions, []string{"X-Session-ID"}, fixedRandomBucket(0))
	_, ok := noHeader.bucket(config.ShadowSamplingMethodPromptCacheKey)
	assert.False(t, ok)

	req.Header.Set("X-Session-ID", "session-123")
	withHeader := newShadowRequestSampler(req, body, shadowSamplingEndpointChatCompletions, []string{"X-Session-ID"}, fixedRandomBucket(0))
	bucket, ok := withHeader.bucket(config.ShadowSamplingMethodPromptCacheKey)
	require.True(t, ok)
	assert.Equal(t, 95, bucket)
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

func responsesSampler(body string, headers map[string]string, endpoint shadowSamplingEndpoint, randomBucket int) *shadowRequestSampler {
	path := "/v1/responses"
	if endpoint == shadowSamplingEndpointChatCompletions {
		path = "/v1/chat/completions"
	}
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return newShadowRequestSampler(req, []byte(body), endpoint, []string{"X-Session-ID"}, fixedRandomBucket(randomBucket))
}

func TestFirstMessageHashIsUnavailableWhenResponsesRequestContinuesEarlierTurns(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{name: "previous response id", fields: `"previous_response_id":"resp_1"`},
		{name: "previous response id non-string", fields: `"previous_response_id":7`},
		{name: "conversation string", fields: `"conversation":"conv_123"`},
		{name: "conversation object id", fields: `"conversation":{"id":"conv_123"}`},
		{name: "conversation object without id", fields: `"conversation":{"name":"x"}`},
		{name: "conversation object null id", fields: `"conversation":{"id":null}`},
		{name: "conversation number", fields: `"conversation":5`},
		{name: "conversation boolean", fields: `"conversation":true`},
		{name: "conversation array", fields: `"conversation":["conv_123"]`},
		{name: "both fields", fields: `"previous_response_id":"resp_1","conversation":{"id":"conv_123"}`},
	}
	inputs := map[string]string{
		"string input":    `"input":"Explain pods"`,
		"user item input": `"input":[{"role":"user","content":"Explain pods"}]`,
		"with run config": `"instructions":"Be helpful.","input":"thanks"`,
	}

	for _, tc := range tests {
		for inputName, input := range inputs {
			t.Run(tc.name+"/"+inputName, func(t *testing.T) {
				sampler := responsesSampler("{"+tc.fields+","+input+"}", nil, shadowSamplingEndpointResponses, 0)
				_, ok := sampler.bucket(config.ShadowSamplingMethodFirstMessageHash)
				assert.False(t, ok)
			})
		}
	}
}

func TestFirstMessageHashKeepsExistingBehaviorWithoutEarlierTurns(t *testing.T) {
	bucketFor := func(body string) int {
		bucket, ok := responsesSampler(body, nil, shadowSamplingEndpointResponses, 0).bucket(config.ShadowSamplingMethodFirstMessageHash)
		require.True(t, ok, body)
		return bucket
	}

	want := bucketFor(`{"input":"Explain pods"}`)
	for _, fields := range []string{
		`"previous_response_id":null`,
		`"previous_response_id":""`,
		`"conversation":null`,
		`"conversation":""`,
		`"conversation":{}`,
		`"previous_response_id":"","conversation":{}`,
	} {
		t.Run(fields, func(t *testing.T) {
			assert.Equal(t, want, bucketFor(`{`+fields+`,"input":"Explain pods"}`))
		})
	}
}

func TestFirstMessageHashChatCompletionsIgnoresEarlierTurnFields(t *testing.T) {
	body := `{"previous_response_id":"resp_1","conversation":{"id":"conv_123"},"messages":[{"role":"user","content":"Hello"}]}`
	_, ok := responsesSampler(body, nil, shadowSamplingEndpointChatCompletions, 0).bucket(config.ShadowSamplingMethodFirstMessageHash)
	assert.True(t, ok)
}

func TestResponsesRequestsContinuingEarlierTurnsUseTheNextConfiguredMethod(t *testing.T) {
	// The key and session header rank first, then the first message, then the
	// bearer credential, then the implicit random fallback.
	shadows := []shadowConfig{{
		modelName:  "shadow",
		percentage: 50,
		samplingMethods: testShadowSamplingMethods(
			config.ShadowSamplingMethodPromptCacheKey,
			config.ShadowSamplingMethodFirstMessageHash,
			config.ShadowSamplingMethodPerBearerKey,
		),
	}}
	continuations := map[string]string{
		"previous response id": `"previous_response_id":"resp_1"`,
		"conversation string":  `"conversation":"conv_123"`,
		"conversation object":  `"conversation":{"id":"conv_123"}`,
	}

	for name, continuation := range continuations {
		body := func(extra string) string {
			return `{` + continuation + extra + `,"input":"Explain pods"}`
		}
		decide := func(body string, headers map[string]string, randomBucket int) (int, []config.ShadowSamplingMethod) {
			sampler := responsesSampler(body, headers, shadowSamplingEndpointResponses, randomBucket)
			return len(admittedShadowsWithSampler(shadows, sampler)), sampler.decidedMethods
		}

		t.Run(name+"/body key decides", func(t *testing.T) {
			// Bucket 95 for session-123 is rejected at 50 percent.
			admitted, decidedBy := decide(body(`,"prompt_cache_key":"session-123"`), nil, 0)
			assert.Zero(t, admitted)
			assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodPromptCacheKey}, decidedBy)
		})
		t.Run(name+"/session header decides", func(t *testing.T) {
			admitted, decidedBy := decide(body(""), map[string]string{"X-Session-ID": "session-123"}, 0)
			assert.Zero(t, admitted)
			assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodPromptCacheKey}, decidedBy)
		})
		t.Run(name+"/bearer decides before random", func(t *testing.T) {
			_, decidedBy := decide(body(""), map[string]string{"Authorization": "Bearer key-1"}, 0)
			assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodPerBearerKey}, decidedBy)
		})
		t.Run(name+"/random is the last resort", func(t *testing.T) {
			admitted, decidedBy := decide(body(""), nil, 10)
			assert.Equal(t, 1, admitted)
			assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodRandom}, decidedBy)
			admitted, _ = decide(body(""), nil, 90)
			assert.Zero(t, admitted)
		})
	}
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

func messagesSampler(body string, headers map[string]string, headerNames []string, randomBucket int) *shadowRequestSampler {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return newShadowRequestSampler(req, []byte(body), shadowSamplingEndpointMessages, headerNames, fixedRandomBucket(randomBucket))
}

func messagesDefaultHeaders() []string {
	return config.ModelFunctionDetails{}.EffectivePromptCacheKeyHeadersForSection("anthropicMessages")
}

func TestMessagesPromptCacheKeyUsesSessionHeaders(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		headers   map[string]string
		names     []string
		available bool
	}{
		{
			name:      "multi-turn session header",
			body:      `{"messages":[{"role":"user","content":"a"}]}`,
			headers:   map[string]string{config.DefaultPromptCacheKeyHeader: "session-123"},
			names:     messagesDefaultHeaders(),
			available: true,
		},
		{
			name:      "Claude Code session header when the first is absent",
			body:      `{"messages":[{"role":"user","content":"b"}]}`,
			headers:   map[string]string{config.ClaudeCodeSessionIDHeader: " session-123 "},
			names:     messagesDefaultHeaders(),
			available: true,
		},
		{
			name: "multi-turn session header wins over Claude Code",
			body: `{"messages":[{"role":"user","content":"c"}]}`,
			headers: map[string]string{
				config.DefaultPromptCacheKeyHeader: "session-123",
				config.ClaudeCodeSessionIDHeader:   "other-session",
			},
			names:     messagesDefaultHeaders(),
			available: true,
		},
		{
			name:  "body prompt_cache_key is not a Messages field",
			body:  `{"prompt_cache_key":"session-123","metadata":{"user_id":"session-123"}}`,
			names: messagesDefaultHeaders(),
		},
		{
			name:    "empty header list disables lookup",
			body:    `{}`,
			headers: map[string]string{config.ClaudeCodeSessionIDHeader: "session-123"},
			names:   []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sampler := messagesSampler(tc.body, tc.headers, tc.names, 0)
			bucket, ok := sampler.bucket(config.ShadowSamplingMethodPromptCacheKey)
			require.Equal(t, tc.available, ok)
			if tc.available {
				assert.Equal(t, 95, bucket)
			}
		})
	}
}

func TestPromptCacheKeySessionValueHasSameBucketOnChatAndMessages(t *testing.T) {
	chat := responsesSampler(`{}`, map[string]string{"X-Session-ID": "session-123"}, shadowSamplingEndpointChatCompletions, 0)
	messages := messagesSampler(`{}`, map[string]string{config.ClaudeCodeSessionIDHeader: "session-123"}, messagesDefaultHeaders(), 0)

	chatBucket, chatOK := chat.bucket(config.ShadowSamplingMethodPromptCacheKey)
	messagesBucket, messagesOK := messages.bucket(config.ShadowSamplingMethodPromptCacheKey)
	require.True(t, chatOK)
	require.True(t, messagesOK)
	assert.Equal(t, chatBucket, messagesBucket)
}

func TestCanonicalMessagesFirstMessageFixedVectors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		material string
	}{
		{
			name:     "string system and user",
			body:     `{"system":"Be helpful.","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Later"}]}`,
			material: `{"version":1,"endpoint":"messages","messages":[{"role":"system","content":"Be helpful."},{"role":"user","content":"Hello"}]}`,
		},
		{
			name:     "block system and user without cache_control",
			body:     `{"system":[{"type":"text","text":"Stay concise","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Describe this","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
			material: `{"version":1,"endpoint":"messages","messages":[{"role":"system","content":[{"text":"Stay concise","type":"text"}]},{"role":"user","content":[{"text":"Describe this","type":"text"},{"source":{"data":"AAAA","media_type":"image/png","type":"base64"},"type":"image"}]}]}`,
		},
		{
			name:     "no system",
			body:     `{"messages":[{"role":"user","content":"Hello"}]}`,
			material: `{"version":1,"endpoint":"messages","messages":[{"role":"user","content":"Hello"}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			material, ok := messagesSampler(tc.body, nil, nil, 0).firstMessageMaterial()
			require.True(t, ok)
			assert.Equal(t, tc.material, string(material))
		})
	}
}

func TestMessagesFirstMessageHashIsStableAcrossTurns(t *testing.T) {
	turn1 := `{"system":"Be helpful.","messages":[{"role":"user","content":[{"type":"text","text":"Hello","cache_control":{"type":"ephemeral"}}]}]}`
	turn3 := `{"system":"Be helpful.","messages":[
		{"content":[{"text":"Hello","type":"text"}],"role":"user"},
		{"role":"assistant","content":"Hi"},
		{"role":"user","content":[{"type":"text","text":"More","cache_control":{"type":"ephemeral"}}]}]}`
	other := `{"system":"Be helpful.","messages":[{"role":"user","content":"Different"}]}`

	first, ok := messagesSampler(turn1, nil, nil, 0).bucket(config.ShadowSamplingMethodFirstMessageHash)
	require.True(t, ok)
	later, ok := messagesSampler(turn3, nil, nil, 0).bucket(config.ShadowSamplingMethodFirstMessageHash)
	require.True(t, ok)
	assert.Equal(t, first, later)

	firstMaterial, _ := messagesSampler(turn1, nil, nil, 0).firstMessageMaterial()
	otherMaterial, ok := messagesSampler(other, nil, nil, 0).firstMessageMaterial()
	require.True(t, ok)
	assert.NotEqual(t, string(firstMaterial), string(otherMaterial))
}

func TestMessagesFirstMessageHashRequiresNonemptyUserContent(t *testing.T) {
	methods := testShadowSamplingMethods(
		config.ShadowSamplingMethodPromptCacheKey,
		config.ShadowSamplingMethodFirstMessageHash,
	)
	for _, body := range []string{
		`{"system":"Be helpful.","messages":[]}`,
		`{"messages":[{"role":"assistant","content":"Hi"}]}`,
		`{"messages":[{"role":"user","content":""}]}`,
		`{"messages":[{"role":"user","content":[]}]}`,
		`{"messages":"bad"}`,
	} {
		t.Run(body, func(t *testing.T) {
			sampler := messagesSampler(body, nil, messagesDefaultHeaders(), 5)
			_, ok := sampler.bucket(config.ShadowSamplingMethodFirstMessageHash)
			assert.False(t, ok)

			admitted := admittedShadowsWithSampler([]shadowConfig{{modelName: "shadow", percentage: 10, samplingMethods: methods}}, sampler)
			assert.Equal(t, []string{"shadow"}, shadowModelNames(admitted))
			assert.Equal(t, []config.ShadowSamplingMethod{config.ShadowSamplingMethodRandom}, sampler.decidedMethods)
		})
	}
}

func TestStickyShadowSamplingAdmitsConfiguredShareOfConversations(t *testing.T) {
	const conversations = 10_000
	bodies := map[config.ShadowSamplingMethod]func(int) string{
		config.ShadowSamplingMethodPromptCacheKey: func(i int) string {
			return `{"prompt_cache_key":"session-` + strconv.Itoa(i) + `"}`
		},
		config.ShadowSamplingMethodFirstMessageHash: func(i int) string {
			return `{"messages":[{"role":"user","content":"conversation ` + strconv.Itoa(i) + `"}]}`
		},
	}
	for method, body := range bodies {
		for _, percentage := range []int{1, 10, 25, 50, 90} {
			t.Run(string(method)+"/"+strconv.Itoa(percentage), func(t *testing.T) {
				shadow := shadowConfig{
					modelName:       "shadow",
					percentage:      percentage,
					samplingMethods: testShadowSamplingMethods(method),
				}
				admitted := 0
				for i := range conversations {
					sampler := newShadowRequestSampler(
						httptest.NewRequest(http.MethodPost, "/", nil),
						[]byte(body(i)),
						shadowSamplingEndpointChatCompletions,
						nil,
						func() int { t.Fatal("random fallback used"); return 0 },
					)
					admitted += len(admittedShadowsWithSampler([]shadowConfig{shadow}, sampler))
				}
				lower := conversations * max(percentage-3, 0) / 100
				upper := conversations * (percentage + 3) / 100
				assert.GreaterOrEqual(t, admitted, lower, "admitted %d of %d at %d%%", admitted, conversations, percentage)
				assert.LessOrEqual(t, admitted, upper, "admitted %d of %d at %d%%", admitted, conversations, percentage)
			})
		}
	}
}
