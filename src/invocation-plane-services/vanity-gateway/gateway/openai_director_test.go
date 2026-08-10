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
	config "ai-api-gateway-service/gateway_config"
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingResponseWriter struct {
	header http.Header
}

func newFailingResponseWriter() *failingResponseWriter {
	return &failingResponseWriter{header: http.Header{}}
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client response write failed")
}

func (w *failingResponseWriter) WriteHeader(int) {}

func TestPrimaryProxyObserverKeepsShadowAfterCompletedErrorResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	observer := &primaryProxyObserver{}
	observer.markStatus(http.StatusInternalServerError)
	cancel()

	require.NoError(t, observer.shadowFinalizationError(req, nil))
}

func TestExtractOpenAIJSONBody(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    string
		expectedModel  string
		expectedStream bool
		expectError    bool
	}{
		{
			name:           "Valid JSON with model and stream",
			requestBody:    `{"model":"gpt-4","stream":true}`,
			expectedModel:  "gpt-4",
			expectedStream: true,
		},
		{
			name:           "Valid JSON with only model",
			requestBody:    `{"model":"gpt-3.5-turbo"}`,
			expectedModel:  "gpt-3.5-turbo",
			expectedStream: false,
		},
		{
			name:        "Invalid JSON",
			requestBody: `{invalid json}`,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(tc.requestBody))
			require.NoError(t, err)

			result, err := extractOpenAIJSONBody(req)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tc.expectedModel, result.Model)
			assert.Equal(t, tc.expectedStream, result.Stream)

			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.requestBody, string(body))
			require.NoError(t, req.Body.Close())
		})
	}

	t.Run("Nil body", func(t *testing.T) {
		req, err := http.NewRequest("POST", "/v1/chat/completions", nil)
		require.NoError(t, err)

		_, err = extractOpenAIJSONBody(req)
		require.Error(t, err)
	})

	t.Run("Empty JSON object returns errModelFieldMissing", func(t *testing.T) {
		req, err := http.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{}`))
		require.NoError(t, err)

		_, err = extractOpenAIJSONBody(req)
		require.ErrorIs(t, err, errModelFieldMissing)
	})

	t.Run("JSON without model field returns errModelFieldMissing", func(t *testing.T) {
		req, err := http.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"stream":true}`))
		require.NoError(t, err)

		_, err = extractOpenAIJSONBody(req)
		require.ErrorIs(t, err, errModelFieldMissing)
	})
}

func TestExtractOpenAIJSONBodyDoesNotRetainPayload(t *testing.T) {
	const (
		modelName   = "model-retention-probe"
		payloadSize = 32 << 20
	)

	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	model := func() string {
		requestBody := append([]byte(`{"model":"`+modelName+`","padding":"`), bytes.Repeat([]byte{'x'}, payloadSize)...)
		requestBody = append(requestBody, '"', '}')
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(requestBody))

		body, err := extractOpenAIJSONBody(req)
		require.NoError(t, err)
		require.NoError(t, req.Body.Close())
		return body.Model
	}()

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	var retained uint64
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	require.Less(t, retained, uint64(payloadSize/2))
	require.Equal(t, modelName, model)
	runtime.KeepAlive(model)
}

func TestExtractOpenAIMultipartBody(t *testing.T) {
	buildMultipart := func(t *testing.T, fields map[string]string) (body *bytes.Buffer, contentType string) {
		t.Helper()
		body = &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		for name, value := range fields {
			require.NoError(t, mw.WriteField(name, value))
		}
		// A binary-looking file part, to ensure we preserve non-model parts too.
		fw, err := mw.CreateFormFile("image", "input.jpg")
		require.NoError(t, err)
		_, err = fw.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x01, 0x02, 0x03})
		require.NoError(t, err)
		require.NoError(t, mw.Close())
		return body, mw.FormDataContentType()
	}

	t.Run("reads model field and leaves body readable", func(t *testing.T) {
		body, ct := buildMultipart(t, map[string]string{
			"model":  "qwen/qwen-image-edit-2511",
			"prompt": "make sky pink",
		})
		original := bytes.Clone(body.Bytes())

		req, err := http.NewRequest(http.MethodPost, "/v1/images/edits", body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", ct)

		result, err := extractOpenAIRequestBody(req)
		require.NoError(t, err)
		assert.Equal(t, "qwen/qwen-image-edit-2511", result.Model)
		assert.False(t, result.Stream)

		forwarded, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, original, forwarded)
		require.NoError(t, req.Body.Close())
	})

	t.Run("returns errModelFieldMissing when model field is absent", func(t *testing.T) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		require.NoError(t, mw.WriteField("prompt", "hello"))
		require.NoError(t, mw.Close())

		req, err := http.NewRequest(http.MethodPost, "/v1/images/edits", body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		_, err = extractOpenAIRequestBody(req)
		require.ErrorIs(t, err, errModelFieldMissing)
	})

	t.Run("returns errModelFieldMissing when model field is empty", func(t *testing.T) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		require.NoError(t, mw.WriteField("model", ""))
		require.NoError(t, mw.WriteField("prompt", "hello"))
		require.NoError(t, mw.Close())

		req, err := http.NewRequest(http.MethodPost, "/v1/images/edits", body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		_, err = extractOpenAIRequestBody(req)
		require.ErrorIs(t, err, errModelFieldMissing)
	})

	t.Run("rejects multipart without boundary", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewBufferString("x"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "multipart/form-data")

		_, err = extractOpenAIRequestBody(req)
		require.Error(t, err)
	})

	t.Run("non-form-data multipart subtype falls through to JSON extractor", func(t *testing.T) {
		// multipart/related or other multipart/* subtypes are not accepted on
		// image endpoints; they should dispatch to the JSON extractor, which
		// fails decoding the multipart body.
		req, err := http.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewBufferString("not json"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "multipart/related; boundary=abc")

		_, err = extractOpenAIRequestBody(req)
		require.Error(t, err)
		require.NotErrorIs(t, err, errModelFieldMissing)
	})
}

func TestRewriteShadowRequestModel(t *testing.T) {
	original := []byte(`{"model":"facebook/opt-125m","messages":[{"role":"user","content":"hello"}],"stream":true}`)

	rewritten, err := rewriteShadowRequestModel(bytes.Clone(original), "private/facebook/opt-125m-shadow")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rewritten, &body))
	assert.Equal(t, "private/facebook/opt-125m-shadow", body["model"])
	assert.Equal(t, true, body["stream"])

	var originalBody map[string]any
	require.NoError(t, json.Unmarshal(original, &originalBody))
	assert.Equal(t, "facebook/opt-125m", originalBody["model"])
}

func TestBuildModelMapping(t *testing.T) {
	shadowPct := 25
	privateModelMatcher := regexp.MustCompile("^private/")

	mapping, err := buildModelMapping(map[string]ModelNameToFunctionIdVersionId{
		"facebook/opt-125m": {
			FunctionId:             "func-123",
			FunctionVersionId:      "version-456",
			OutgoingPathOverride:   "/custom/path",
			UsePexec:               true,
			TooManyRequestsMessage: "Try a partner API!",
			ShadowModelNames:       []string{"private/facebook/opt-125m-shadow"},
			ShadowPercentage:       &shadowPct,
		},
		"private/facebook/opt-125m-shadow": {
			FunctionId:        "shadow-func",
			FunctionVersionId: "shadow-ver",
		},
	}, privateModelMatcher)
	require.NoError(t, err)

	functionInfo, ok := mapping.modelNameToNVCFUrl["facebook/opt-125m"]
	require.True(t, ok)
	assert.Equal(t, "func-123", functionInfo.functionId)
	assert.Equal(t, "version-456", functionInfo.functionVersionId)
	assert.Equal(t, "/custom/path", *functionInfo.pathOverride)
	assert.True(t, functionInfo.usePexec)
	assert.Equal(t, "Try a partner API!", functionInfo.tooManyRequestsMessage)
	assert.Equal(t, []string{"private/facebook/opt-125m-shadow"}, functionInfo.shadowModelNames)
	assert.Equal(t, shadowPct, functionInfo.shadowPercentage)

	_, shadowIsPublic := mapping.modelNameToModelInfo["private/facebook/opt-125m-shadow"]
	assert.False(t, shadowIsPublic)
}

func TestBuildModelMappingPreservesAndDefaultsShadowSamplingMethod(t *testing.T) {
	privateModelMatcher := regexp.MustCompile("^private/")

	mapping, err := buildModelMapping(map[string]ModelNameToFunctionIdVersionId{
		"facebook/opt-125m": {
			FunctionId:           "func-123",
			ShadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
			ShadowSamplingMethod: config.ShadowSamplingMethodPerBearerKey,
		},
		"meta/llama-3.1-8b": {
			FunctionId:       "func-456",
			ShadowModelNames: []string{"private/meta/llama-3.1-8b-shadow"},
		},
		"private/facebook/opt-125m-shadow": {
			FunctionId: "shadow-func",
		},
		"private/meta/llama-3.1-8b-shadow": {
			FunctionId: "shadow-func-2",
		},
	}, privateModelMatcher)
	require.NoError(t, err)

	assert.Equal(t, config.ShadowSamplingMethodPerBearerKey, mapping.modelNameToNVCFUrl["facebook/opt-125m"].shadowSamplingMethod)
	assert.Equal(t, config.ShadowSamplingMethodRandom, mapping.modelNameToNVCFUrl["meta/llama-3.1-8b"].shadowSamplingMethod)
}

func TestResolveModelMappedRequestAddsMetricAttributes(t *testing.T) {
	director := &OpenAIDirector{}
	modelToNVCFURL := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId: "func-123",
		},
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"facebook/opt-125m"}`),
	)
	labeler := &otelhttp.Labeler{}
	req = req.WithContext(otelhttp.ContextWithLabeler(req.Context(), labeler))
	rec := httptest.NewRecorder()

	resolved, handled := director.resolveModelMappedRequest(rec, req, modelToNVCFURL)
	require.False(t, handled)
	require.NoError(t, resolved.request.Body.Close())

	assertLabelerHasAttribute(t, labeler.Get(), "openai_model_name", attribute.StringValue("facebook/opt-125m"))
	assertLabelerHasAttribute(t, labeler.Get(), "function_id", attribute.StringValue("func-123"))
}

func assertLabelerHasAttribute(t *testing.T, attrs []attribute.KeyValue, key attribute.Key, want attribute.Value) {
	t.Helper()

	for _, attr := range attrs {
		if attr.Key == key {
			require.Equal(t, want, attr.Value)
			return
		}
	}
	t.Fatalf("missing metric attribute %q in %#v", key, attrs)
}

func TestBuildModelMappingPreservesMultipleShadowTargets(t *testing.T) {
	privateModelMatcher := regexp.MustCompile("^private/")

	mapping, err := buildModelMapping(map[string]ModelNameToFunctionIdVersionId{
		"facebook/opt-125m": {
			FunctionId: "func-123",
			ShadowModelNames: []string{
				"private/facebook/opt-125m-shadow-a",
				"private/facebook/opt-125m-shadow-b",
				"private/facebook/opt-125m-shadow-c",
			},
		},
		"private/facebook/opt-125m-shadow-a": {FunctionId: "shadow-a-func"},
		"private/facebook/opt-125m-shadow-b": {FunctionId: "shadow-b-func"},
		"private/facebook/opt-125m-shadow-c": {FunctionId: "shadow-c-func"},
	}, privateModelMatcher)
	require.NoError(t, err)

	functionInfo, ok := mapping.modelNameToNVCFUrl["facebook/opt-125m"]
	require.True(t, ok)
	assert.Equal(t, []string{
		"private/facebook/opt-125m-shadow-a",
		"private/facebook/opt-125m-shadow-b",
		"private/facebook/opt-125m-shadow-c",
	}, functionInfo.shadowModelNames)
}

func TestConvertIntoModelNameToFunctionIdAndVersionIdMappingV2(t *testing.T) {
	eolDate, _ := time.Parse("2006-01-02", "2025-12-31")
	shadowPct := 25

	result := convertIntoModelNameToFunctionIdAndVersionIdMappingV2(map[string]config.ModelFunctionDetails{
		"model-key": {
			ModelName:                      "facebook/opt-125m",
			FunctionID:                     "func-123",
			FunctionVersionID:              "version-456",
			OutgoingPathOverride:           "/custom/path",
			UsePexec:                       true,
			SessionTimeout:                 900,
			EOL:                            eolDate,
			TooManyRequestsMessage:         "Try a partner API!",
			ShadowModelName:                "private/facebook/opt-125m-shadow",
			ShadowModelNames:               []string{"private/facebook/opt-125m-shadow-b"},
			ShadowPercentage:               &shadowPct,
			ShadowSamplingMethod:           config.ShadowSamplingMethodPerBearerKey,
			ShadowCancelOnClientDisconnect: true,
		},
	})

	expected, ok := result["facebook/opt-125m"]
	require.True(t, ok)
	assert.Equal(t, "func-123", expected.FunctionId)
	assert.Equal(t, "version-456", expected.FunctionVersionId)
	assert.Equal(t, "/custom/path", expected.OutgoingPathOverride)
	assert.True(t, expected.UsePexec)
	assert.Equal(t, config.SessionTimeoutSeconds(900), expected.SessionTimeout)
	assert.Equal(t, eolDate, expected.EOL)
	assert.Equal(t, "Try a partner API!", expected.TooManyRequestsMessage)
	assert.Equal(t, []string{
		"private/facebook/opt-125m-shadow",
		"private/facebook/opt-125m-shadow-b",
	}, expected.ShadowModelNames)
	assert.Equal(t, &shadowPct, expected.ShadowPercentage)
	assert.Equal(t, config.ShadowSamplingMethodPerBearerKey, expected.ShadowSamplingMethod)
	assert.True(t, expected.ShadowCancelOnClientDisconnect)
}

func TestDefaultShadowPercentage(t *testing.T) {
	custom := 25

	assert.Equal(t, 100, defaultShadowPercentage(nil))
	assert.Equal(t, custom, defaultShadowPercentage(&custom))
}

func TestResolveModelMappedRequestPreservesShadowConfig(t *testing.T) {
	director := &OpenAIDirector{}
	writer := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m"}`))

	modelMapping := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId:       "primary-func",
			shadowModelNames: []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage: 100,
		},
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	}

	resolved, handled := director.resolveModelMappedRequest(writer, req, modelMapping)
	require.False(t, handled)
	assert.Equal(t, []string{"private/facebook/opt-125m-shadow"}, resolved.functionInfo.shadowModelNames)
	assert.Equal(t, 100, resolved.functionInfo.shadowPercentage)

	body, err := io.ReadAll(resolved.request.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"model":"facebook/opt-125m"}`, string(body))
}

func TestDispatchShadowIfNeededReplaysHandlerAndRewritesBody(t *testing.T) {
	var receivedBody string
	var received atomic.Bool

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		received.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	vanity, err := NewVanityDirector(backend.URL, backend.Client().Transport)
	require.NoError(t, err)

	modelMapping := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId:       "primary-func",
			shadowModelNames: []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage: 100,
		},
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	}

	director := &OpenAIDirector{
		shadower:       NewTrafficShadower(10, 30*time.Second),
		vanityDirector: vanity,
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m","stream":true}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")

	resolved := resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames: []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage: 100,
		},
	}
	director.dispatchShadowIfNeeded(resolved, modelMapping)

	assert.Eventually(t, func() bool { return received.Load() }, 5*time.Second, 10*time.Millisecond)

	var shadowBody map[string]any
	require.NoError(t, json.Unmarshal([]byte(receivedBody), &shadowBody))
	assert.Equal(t, "private/facebook/opt-125m-shadow", shadowBody["model"])
	assert.Equal(t, true, shadowBody["stream"])
}

func TestDispatchShadowIfNeededPerBearerKeyUsesBearerBucket(t *testing.T) {
	var receivedBody string
	var received atomic.Bool

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		received.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	vanity, err := NewVanityDirector(backend.URL, backend.Client().Transport)
	require.NoError(t, err)

	modelMapping := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId:           "primary-func",
			shadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage:     47,
			shadowSamplingMethod: config.ShadowSamplingMethodPerBearerKey,
		},
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	}

	director := &OpenAIDirector{
		shadower:           NewTrafficShadower(10, 30*time.Second),
		vanityDirector:     vanity,
		shadowRandomBucket: func() int { return 99 },
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m","stream":true}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")

	director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
		request:      req,
		functionInfo: modelMapping["facebook/opt-125m"],
	}, modelMapping)

	assert.Eventually(t, func() bool { return received.Load() }, 5*time.Second, 10*time.Millisecond)

	var shadowBody map[string]any
	require.NoError(t, json.Unmarshal([]byte(receivedBody), &shadowBody))
	assert.Equal(t, "private/facebook/opt-125m-shadow", shadowBody["model"])
	assert.Equal(t, true, shadowBody["stream"])
}

func TestShadowCancelledWhenPrimaryProxyErrorsBeforeRequestContextCancels(t *testing.T) {
	shadowStarted := make(chan struct{})
	shadowCanceled := make(chan struct{})
	primaryErr := errors.New("primary proxy error")

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Header.Get("function-id") {
		case "primary-func":
			return nil, primaryErr
		case "shadow-func":
			close(shadowStarted)
			<-req.Context().Done()
			close(shadowCanceled)
			return nil, req.Context().Err()
		default:
			t.Fatalf("unexpected function-id %q", req.Header.Get("function-id"))
			return nil, nil
		}
	})

	vanity, err := NewVanityDirector("https://nvcf.example.test", transport)
	require.NoError(t, err)

	director := &OpenAIDirector{
		shadower:       NewTrafficShadower(10, 30*time.Second),
		vanityDirector: vanity,
	}
	modelMapping := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId:                     "primary-func",
			shadowModelNames:               []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage:               100,
			shadowCancelOnClientDisconnect: true,
		},
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m"}`))
	req.Header.Set("Content-Type", "application/json")

	require.NoError(t, req.Context().Err())
	director.proxyModelMappedRequest(httptest.NewRecorder(), req, modelMapping)

	select {
	case <-shadowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("shadow request did not start")
	}

	select {
	case <-shadowCanceled:
	case <-time.After(1 * time.Second):
		t.Fatal("shadow request was not canceled after primary proxy error")
	}
}

func TestShadowCancelledWhenPrimaryResponseWriteFails(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    string
		responseHeader http.Header
		responseBody   string
	}{
		{
			name:           "non-streaming",
			requestBody:    `{"model":"facebook/opt-125m","stream":false}`,
			responseHeader: http.Header{"Content-Type": []string{"application/json"}},
			responseBody:   `{"id":"chatcmpl-test","choices":[]}`,
		},
		{
			name:           "streaming",
			requestBody:    `{"model":"facebook/opt-125m","stream":true}`,
			responseHeader: http.Header{"Content-Type": []string{"text/event-stream"}},
			responseBody:   "data: {\"id\":\"chatcmpl-test\",\"choices\":[]}\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shadowStarted := make(chan struct{})
			shadowCanceled := make(chan struct{})

			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.Header.Get("function-id") {
				case "primary-func":
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Header:     tc.responseHeader,
						Body:       io.NopCloser(bytes.NewBufferString(tc.responseBody)),
						Request:    req,
					}, nil
				case "shadow-func":
					close(shadowStarted)
					<-req.Context().Done()
					close(shadowCanceled)
					return nil, req.Context().Err()
				default:
					t.Fatalf("unexpected function-id %q", req.Header.Get("function-id"))
					return nil, nil
				}
			})

			vanity, err := NewVanityDirector("https://nvcf.example.test", transport)
			require.NoError(t, err)

			director := &OpenAIDirector{
				shadower:       NewTrafficShadower(10, 30*time.Second),
				vanityDirector: vanity,
			}
			modelMapping := map[string]FunctionInfo{
				"facebook/opt-125m": {
					functionId:                     "primary-func",
					shadowModelNames:               []string{"private/facebook/opt-125m-shadow"},
					shadowPercentage:               100,
					shadowCancelOnClientDisconnect: true,
				},
				"private/facebook/opt-125m-shadow": {
					functionId: "shadow-func",
				},
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(tc.requestBody))
			req.Header.Set("Content-Type", "application/json")

			director.proxyModelMappedRequest(newFailingResponseWriter(), req, modelMapping)

			select {
			case <-shadowStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("shadow request did not start")
			}

			select {
			case <-shadowCanceled:
			case <-time.After(1 * time.Second):
				t.Fatal("shadow request was not canceled after primary response write failure")
			}
		})
	}
}

func TestShadowCancelledWhenPrimaryProxyPanics(t *testing.T) {
	shadowStarted := make(chan struct{})
	shadowCanceled := make(chan struct{})

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Header.Get("function-id") {
		case "primary-func":
			panic("primary proxy panic")
		case "shadow-func":
			close(shadowStarted)
			<-req.Context().Done()
			close(shadowCanceled)
			return nil, req.Context().Err()
		default:
			t.Fatalf("unexpected function-id %q", req.Header.Get("function-id"))
			return nil, nil
		}
	})

	vanity, err := NewVanityDirector("https://nvcf.example.test", transport)
	require.NoError(t, err)

	director := &OpenAIDirector{
		shadower:       NewTrafficShadower(10, 30*time.Second),
		vanityDirector: vanity,
	}
	modelMapping := map[string]FunctionInfo{
		"facebook/opt-125m": {
			functionId:                     "primary-func",
			shadowModelNames:               []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage:               100,
			shadowCancelOnClientDisconnect: true,
		},
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m"}`))
	req.Header.Set("Content-Type", "application/json")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("expected primary proxy panic")
			}
		}()
		director.proxyModelMappedRequest(httptest.NewRecorder(), req, modelMapping)
	}()

	select {
	case <-shadowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("shadow request did not start")
	}

	select {
	case <-shadowCanceled:
	case <-time.After(1 * time.Second):
		t.Fatal("shadow request was not canceled after primary proxy panic")
	}
}

func TestDispatchShadowIfNeededSkipsShadowRequests(t *testing.T) {
	var replayed atomic.Bool

	director := &OpenAIDirector{shadower: NewTrafficShadower(10, 30*time.Second)}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m"}`))
	req.Header.Set(shadowHeader, "true")

	director.dispatchShadowIfNeeded(resolvedOpenAIRequest{
		request: req,
		functionInfo: FunctionInfo{
			shadowModelNames: []string{"private/facebook/opt-125m-shadow"},
			shadowPercentage: 100,
		},
	}, map[string]FunctionInfo{
		"private/facebook/opt-125m-shadow": {
			functionId: "shadow-func",
		},
	})

	// Shadow should not be dispatched since NVCF-Shadow header is already set.
	// If it were dispatched, the shadower's goroutine would eventually call
	// proxyModelMappedRequest which would panic on nil vanityDirector.
	assert.Never(t, func() bool { return replayed.Load() }, 100*time.Millisecond, 10*time.Millisecond)
}

func TestNewOpenAIDirectorV2DeduplicatesModels(t *testing.T) {
	privateModelMatcher := regexp.MustCompile("^private/")

	tests := []struct {
		name            string
		chatCompletions map[string]config.ModelFunctionDetails
		completions     map[string]config.ModelFunctionDetails
		embeddings      map[string]config.ModelFunctionDetails
		responses       map[string]config.ModelFunctionDetails
		expectedModels  []string
	}{
		{
			name: "model in both chatCompletions and responses appears once",
			chatCompletions: map[string]config.ModelFunctionDetails{
				"acme/shared-model": {ModelName: "acme/shared-model", FunctionID: "func-1"},
				"acme/chat-only":    {ModelName: "acme/chat-only", FunctionID: "func-2"},
			},
			responses: map[string]config.ModelFunctionDetails{
				"acme/shared-model":   {ModelName: "acme/shared-model", FunctionID: "func-1"},
				"acme/responses-only": {ModelName: "acme/responses-only", FunctionID: "func-3"},
			},
			expectedModels: []string{"acme/chat-only", "acme/responses-only", "acme/shared-model"},
		},
		{
			name: "model in all four sections appears once",
			chatCompletions: map[string]config.ModelFunctionDetails{
				"acme/everywhere": {ModelName: "acme/everywhere", FunctionID: "func-1"},
			},
			completions: map[string]config.ModelFunctionDetails{
				"acme/everywhere": {ModelName: "acme/everywhere", FunctionID: "func-1"},
			},
			embeddings: map[string]config.ModelFunctionDetails{
				"acme/everywhere": {ModelName: "acme/everywhere", FunctionID: "func-1"},
			},
			responses: map[string]config.ModelFunctionDetails{
				"acme/everywhere": {ModelName: "acme/everywhere", FunctionID: "func-1"},
			},
			expectedModels: []string{"acme/everywhere"},
		},
		{
			name: "no overlap keeps all models",
			chatCompletions: map[string]config.ModelFunctionDetails{
				"acme/chat": {ModelName: "acme/chat", FunctionID: "func-1"},
			},
			completions: map[string]config.ModelFunctionDetails{
				"acme/completions": {ModelName: "acme/completions", FunctionID: "func-2"},
			},
			embeddings: map[string]config.ModelFunctionDetails{
				"acme/embeddings": {ModelName: "acme/embeddings", FunctionID: "func-3"},
			},
			responses: map[string]config.ModelFunctionDetails{
				"acme/responses": {ModelName: "acme/responses", FunctionID: "func-4"},
			},
			expectedModels: []string{"acme/chat", "acme/completions", "acme/embeddings", "acme/responses"},
		},
		{
			name: "private models excluded from public list even with overlap",
			chatCompletions: map[string]config.ModelFunctionDetails{
				"acme/public":           {ModelName: "acme/public", FunctionID: "func-1"},
				"private/acme/internal": {ModelName: "private/acme/internal", FunctionID: "func-2"},
			},
			responses: map[string]config.ModelFunctionDetails{
				"acme/public":           {ModelName: "acme/public", FunctionID: "func-1"},
				"private/acme/internal": {ModelName: "private/acme/internal", FunctionID: "func-2"},
			},
			expectedModels: []string{"acme/public"},
		},
		{
			name:            "all sections empty",
			chatCompletions: map[string]config.ModelFunctionDetails{},
			completions:     map[string]config.ModelFunctionDetails{},
			embeddings:      map[string]config.ModelFunctionDetails{},
			responses:       map[string]config.ModelFunctionDetails{},
			expectedModels:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.GatewayConfig{}
			cfg.OpenAI.ChatCompletions = tc.chatCompletions
			cfg.OpenAI.Completions = tc.completions
			cfg.OpenAI.Embeddings = tc.embeddings
			cfg.OpenAI.Responses = tc.responses

			director, err := NewOpenAIDirectorV2(cfg, privateModelMatcher, nil, nil)
			require.NoError(t, err)

			var modelIDs []string
			for _, m := range director.allPublicModels {
				modelIDs = append(modelIDs, m.Id)
			}

			assert.Equal(t, tc.expectedModels, modelIDs)
		})
	}
}

func TestIsModelExpired(t *testing.T) {
	pastDate, _ := time.Parse("2006-01-02", "2020-01-01")
	futureDate, _ := time.Parse("2006-01-02", "2099-12-31")
	justPassed := time.Now().Add(-1 * time.Second)
	yesterday := time.Now().AddDate(0, 0, -1)
	tomorrow := time.Now().AddDate(0, 0, 1)

	tests := []struct {
		name     string
		eolDate  time.Time
		expected bool
	}{
		{name: "Zero EOL date", eolDate: time.Time{}, expected: false},
		{name: "EOL date in the past", eolDate: pastDate, expected: true},
		{name: "EOL date in the future", eolDate: futureDate, expected: false},
		{name: "EOL date just passed", eolDate: justPassed, expected: true},
		{name: "EOL date yesterday", eolDate: yesterday, expected: true},
		{name: "EOL date tomorrow", eolDate: tomorrow, expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isModelExpired(tc.eolDate))
		})
	}
}
