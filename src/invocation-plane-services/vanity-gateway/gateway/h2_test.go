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
	"bufio"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildChiMux_RejectsSpoofedShadowHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	serverConfig := Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
	}

	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = "test.host"
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"test-model": {
			ModelName:  "test-model",
			FunctionID: "test-func-id",
		},
	}

	mux, err := buildChiMux(mappings, serverConfig)
	assert.NoError(t, err)

	t.Run("rejects request with shadow header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"test-model"}`))
		req.Host = "test.host"
		req.Header.Set("NVCF-Shadow", "true")
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "reserved header")
	})

	t.Run("allows normal request without shadow header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"model":"test-model"}`))
		req.Host = "test.host"
		mux.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusBadRequest, rec.Code)
	})
}

func TestBuildChiMux_ReplaysAllShadowTargets(t *testing.T) {
	type receivedRequest struct {
		body     string
		isShadow bool
	}
	receivedRequests := make(chan receivedRequest, 3)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedRequests <- receivedRequest{
			body:     string(body),
			isShadow: r.Header.Get(shadowHeader) == "true",
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	shadowPct := 100
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = "test.host"
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"primary": {
			ModelName:        "facebook/opt-125m",
			FunctionID:       "primary-func",
			ShadowModelName:  "private/facebook/opt-125m-shadow-a",
			ShadowModelNames: []string{"private/facebook/opt-125m-shadow-b"},
			ShadowPercentage: &shadowPct,
		},
		"shadow-a": {
			ModelName:  "private/facebook/opt-125m-shadow-a",
			FunctionID: "shadow-a-func",
		},
		"shadow-b": {
			ModelName:  "private/facebook/opt-125m-shadow-b",
			FunctionID: "shadow-b-func",
		},
	}

	serverConfig := Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^private/",
		ShadowMaxConcurrent:          10,
	}

	mux, err := buildChiMux(mappings, serverConfig)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"facebook/opt-125m","stream":true}`))
	req.Host = "test.host"
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	primarySeen := false
	seenShadows := map[string]struct{}{}
	timeout := time.After(5 * time.Second)
	for !primarySeen || len(seenShadows) < 2 {
		select {
		case received := <-receivedRequests:
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(received.body), &body))

			modelName, ok := body["model"].(string)
			require.True(t, ok)
			assert.Equal(t, true, body["stream"])
			if received.isShadow {
				seenShadows[modelName] = struct{}{}
				continue
			}
			primarySeen = true
			assert.Equal(t, "facebook/opt-125m", modelName)
		case <-timeout:
			t.Fatalf("timed out waiting for primary and shadow requests; primarySeen=%v seenShadows=%v", primarySeen, seenShadows)
		}
	}

	assert.Equal(t, map[string]struct{}{
		"private/facebook/opt-125m-shadow-a": {},
		"private/facebook/opt-125m-shadow-b": {},
	}, seenShadows)
}

func TestBuildChiMux_CancelsShadowWhenClientDisconnectsBeforePrimaryResponse(t *testing.T) {
	const (
		primaryModel   = "openai-gateway-primary-disconnect"
		primaryFuncID  = "c0ab27d4-2ee1-47cb-9696-84179228c1f7"
		primaryVerID   = "2c2f044c-d6ea-4f8c-93e6-d0ac42baad06"
		shadowModel    = "openai-gateway-shadow"
		shadowFuncID   = "fc22d075-7a86-4d53-8cad-113a2778f658"
		shadowVerID    = "7ea0e0bf-c797-4f5d-ac96-6d49a68b6fa0"
		openAIHost     = "local.integrate.test"
		primaryTTFT    = 500 * time.Millisecond
		cancelDelay    = 100 * time.Millisecond
		cancelDeadline = 2 * time.Second
	)

	type shadowTarget struct {
		modelName string
		funcID    string
		verID     string
	}

	shadowTargets := []shadowTarget{
		{
			modelName: shadowModel,
			funcID:    shadowFuncID,
			verID:     shadowVerID,
		},
		{
			modelName: "openai-gateway-shadow-b",
			funcID:    "ea22d075-7a86-4d53-8cad-113a2778f658",
			verID:     "aea0e0bf-c797-4f5d-ac96-6d49a68b6fa0",
		},
	}

	tests := []struct {
		name               string
		streamLiteral      string
		primaryContentType string
		primaryBody        string
		shadowTargets      []shadowTarget
	}{
		{
			name:               "non-streaming single shadow",
			streamLiteral:      "false",
			primaryContentType: "application/json",
			primaryBody:        `{"id":"chatcmpl-test","choices":[]}`,
			shadowTargets:      shadowTargets[:1],
		},
		{
			name:               "streaming single shadow",
			streamLiteral:      "true",
			primaryContentType: "text/event-stream",
			primaryBody:        "data: {\"id\":\"chatcmpl-test\",\"choices\":[]}\n\n",
			shadowTargets:      shadowTargets[:1],
		},
		{
			name:               "non-streaming multiple shadows",
			streamLiteral:      "false",
			primaryContentType: "application/json",
			primaryBody:        `{"id":"chatcmpl-test","choices":[]}`,
			shadowTargets:      shadowTargets,
		},
		{
			name:               "streaming multiple shadows",
			streamLiteral:      "true",
			primaryContentType: "text/event-stream",
			primaryBody:        "data: {\"id\":\"chatcmpl-test\",\"choices\":[]}\n\n",
			shadowTargets:      shadowTargets,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			primaryStarted := make(chan struct{}, 1)
			releaseShadow := make(chan struct{})
			var releaseShadowOnce sync.Once
			releaseShadows := func() {
				releaseShadowOnce.Do(func() {
					close(releaseShadow)
				})
			}
			defer releaseShadows()
			signal := func(ch chan<- struct{}) {
				select {
				case ch <- struct{}{}:
				default:
				}
			}

			targetsByFuncID := make(map[string]shadowTarget, len(tc.shadowTargets))
			shadowStarted := make(map[string]chan struct{}, len(tc.shadowTargets))
			shadowCanceled := make(map[string]chan struct{}, len(tc.shadowTargets))
			for _, target := range tc.shadowTargets {
				targetsByFuncID[target.funcID] = target
				shadowStarted[target.funcID] = make(chan struct{}, 1)
				shadowCanceled[target.funcID] = make(chan struct{}, 1)
			}

			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Header.Get("function-id") {
				case primaryFuncID:
					assert.Equal(t, primaryVerID, r.Header.Get("function-version-id"))
					signal(primaryStarted)
					time.Sleep(primaryTTFT)
					w.Header().Set("Content-Type", tc.primaryContentType)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(tc.primaryBody))
				default:
					target, ok := targetsByFuncID[r.Header.Get("function-id")]
					if !ok {
						t.Errorf("unexpected function-id %q", r.Header.Get("function-id"))
						w.WriteHeader(http.StatusNotFound)
						return
					}
					assert.Equal(t, target.verID, r.Header.Get("function-version-id"))
					assert.Equal(t, shadowHeaderValue, r.Header.Get(shadowHeader))
					if _, err := io.ReadAll(r.Body); err != nil {
						t.Errorf("failed to read shadow request body: %v", err)
						return
					}
					signal(shadowStarted[target.funcID])
					select {
					case <-r.Context().Done():
						signal(shadowCanceled[target.funcID])
					case <-releaseShadow:
						w.WriteHeader(http.StatusOK)
					}
				}
			}))
			t.Cleanup(backend.Close)

			shadowPct := 100
			mappings := &config.GatewayConfig{}
			mappings.OpenAI.Host = openAIHost
			mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
				"openai-gateway-shadow": {
					ModelName:         shadowModel,
					FunctionID:        shadowFuncID,
					FunctionVersionID: shadowVerID,
				},
			}
			primaryDetails := config.ModelFunctionDetails{
				ModelName:                      primaryModel,
				FunctionID:                     primaryFuncID,
				FunctionVersionID:              primaryVerID,
				ShadowModelName:                tc.shadowTargets[0].modelName,
				ShadowPercentage:               &shadowPct,
				ShadowCancelOnClientDisconnect: true,
			}
			if len(tc.shadowTargets) > 1 {
				for _, target := range tc.shadowTargets[1:] {
					primaryDetails.ShadowModelNames = append(primaryDetails.ShadowModelNames, target.modelName)
					mappings.OpenAI.ChatCompletions[target.modelName] = config.ModelFunctionDetails{
						ModelName:         target.modelName,
						FunctionID:        target.funcID,
						FunctionVersionID: target.verID,
					}
				}
			}
			mappings.OpenAI.ChatCompletions["openai-gateway-primary-disconnect"] = primaryDetails

			mux, err := buildChiMux(mappings, Config{
				NvcfApiEndpoint:              backend.URL,
				PrivateModelNameRegexPattern: "^$",
				ShadowMaxConcurrent:          10,
			})
			require.NoError(t, err)

			frontend := httptest.NewUnstartedServer(mux)
			frontend.EnableHTTP2 = true
			frontend.StartTLS()
			t.Cleanup(frontend.Close)

			body := `{
				"model": "openai-gateway-primary-disconnect",
				"messages": [{"content": "Hello there.", "role": "user"}],
				"repeat_count": 1,
				"ttft_ms": 5000,
				"stream": ` + tc.streamLiteral + `
			}`
			ctx, cancel := context.WithCancel(context.Background())
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, frontend.URL+"/v1/chat/completions", bytes.NewBufferString(body))
			require.NoError(t, err)
			// Route through the local HostRouter without sending any traffic to staging.
			req.Host = openAIHost
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-token")

			type clientResult struct {
				res *http.Response
				err error
			}
			result := make(chan clientResult, 1)
			go func() {
				res, err := frontend.Client().Do(req)
				if res != nil {
					_ = res.Body.Close()
				}
				result <- clientResult{res: res, err: err}
			}()

			select {
			case <-primaryStarted:
			case <-time.After(time.Second):
				t.Fatal("primary request did not start")
			}
			for _, target := range tc.shadowTargets {
				select {
				case <-shadowStarted[target.funcID]:
				case <-time.After(time.Second):
					t.Fatalf("shadow request %q did not start", target.modelName)
				}
			}

			time.Sleep(cancelDelay)
			cancel()

			select {
			case got := <-result:
				require.ErrorIs(t, got.err, context.Canceled)
			case <-time.After(time.Second):
				releaseShadows()
				t.Fatal("client request did not exit after cancel")
			}

			for _, target := range tc.shadowTargets {
				select {
				case <-shadowCanceled[target.funcID]:
				case <-time.After(cancelDeadline):
					releaseShadows()
					t.Fatalf("shadow request %q was not canceled after pre-response client disconnect", target.modelName)
				}
			}
		})
	}
}

func TestBuildChiMux_CancelsStreamingShadowAfterPrimaryStreamStarts(t *testing.T) {
	const (
		primaryModel  = "openai-gateway-primary-disconnect"
		primaryFuncID = "c0ab27d4-2ee1-47cb-9696-84179228c1f7"
		primaryVerID  = "2c2f044c-d6ea-4f5d-ac96-6d49a68b6fa0"
		shadowModel   = "openai-gateway-shadow"
		shadowFuncID  = "fc22d075-7a86-4d53-8cad-113a2778f658"
		shadowVerID   = "7ea0e0bf-c797-4f5d-ac96-6d49a68b6fa0"
		openAIHost    = "local.integrate.test"
	)

	primaryStreamStarted := make(chan struct{}, 1)
	primaryCanceled := make(chan struct{}, 1)
	shadowStreamStarted := make(chan struct{}, 1)
	shadowCanceled := make(chan struct{}, 1)
	releaseStreams := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseStreams)
		})
	}
	defer release()

	signal := func(ch chan<- struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("function-id") {
		case primaryFuncID:
			assert.Equal(t, primaryVerID, r.Header.Get("function-version-id"))
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-primary\",\"choices\":[]}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			signal(primaryStreamStarted)
			select {
			case <-r.Context().Done():
				signal(primaryCanceled)
			case <-releaseStreams:
			}
		case shadowFuncID:
			assert.Equal(t, shadowVerID, r.Header.Get("function-version-id"))
			assert.Equal(t, shadowHeaderValue, r.Header.Get(shadowHeader))
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Contains(t, string(body), `"model":"openai-gateway-shadow"`)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-shadow\",\"choices\":[]}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			signal(shadowStreamStarted)
			select {
			case <-r.Context().Done():
				signal(shadowCanceled)
			case <-releaseStreams:
			}
		default:
			t.Errorf("unexpected function-id %q", r.Header.Get("function-id"))
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(backend.Close)

	shadowPct := 100
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
		"openai-gateway-primary-disconnect": {
			ModelName:                      primaryModel,
			FunctionID:                     primaryFuncID,
			FunctionVersionID:              primaryVerID,
			ShadowModelName:                shadowModel,
			ShadowPercentage:               &shadowPct,
			ShadowCancelOnClientDisconnect: true,
		},
		"openai-gateway-shadow": {
			ModelName:         shadowModel,
			FunctionID:        shadowFuncID,
			FunctionVersionID: shadowVerID,
		},
	}

	mux, err := buildChiMux(mappings, Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
		ShadowMaxConcurrent:          10,
	})
	require.NoError(t, err)

	frontend := httptest.NewUnstartedServer(mux)
	frontend.EnableHTTP2 = true
	frontend.StartTLS()
	t.Cleanup(frontend.Close)

	body := `{
		"model": "openai-gateway-primary-disconnect",
		"messages": [{"content": "Hello there.", "role": "user"}],
		"repeat_count": 1,
		"ttft_ms": 10000,
		"stream": true
	}`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, frontend.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Host = openAIHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	res, err := frontend.Client().Do(req)
	require.NoError(t, err)
	defer func() {
		_ = res.Body.Close()
	}()
	require.Equal(t, http.StatusOK, res.StatusCode)

	select {
	case <-primaryStreamStarted:
	case <-time.After(time.Second):
		t.Fatal("primary stream did not start")
	}
	type readResult struct {
		line string
		err  error
	}
	readLine := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(res.Body).ReadString('\n')
		readLine <- readResult{line: line, err: err}
	}()
	select {
	case result := <-readLine:
		require.NoError(t, result.err)
		assert.Contains(t, result.line, "chatcmpl-primary")
	case <-ctx.Done():
		_ = res.Body.Close()
		t.Fatal("primary stream line did not arrive before request timeout")
	}

	select {
	case <-shadowStreamStarted:
	case <-time.After(time.Second):
		t.Fatal("shadow stream did not start")
	}

	cancel()
	_ = res.Body.Close()

	select {
	case <-primaryCanceled:
	case <-time.After(time.Second):
		t.Fatal("primary stream did not observe client disconnect")
	}

	select {
	case <-shadowCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("streaming shadow did not observe client disconnect")
	}
}

func TestBuildChiMux_OfflineMessage(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	serverConfig := Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
	}

	t.Run("returns 503 when model has offline message", func(t *testing.T) {
		mappings := &config.GatewayConfig{}
		mappings.OpenAI.Host = "test.host"
		mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
			"test-model": {
				ModelName:      "test-model",
				FunctionID:     "test-func-id",
				OfflineMessage: "Model is under maintenance",
			},
		}

		mux, err := buildChiMux(mappings, serverConfig)
		assert.NoError(t, err)

		rec := httptest.NewRecorder()
		body := `{"model":"test-model"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		req.Host = "test.host"
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
		assert.Equal(t, "10800", rec.Header().Get("Retry-After"))

		var pd ProblemDetails
		err = json.Unmarshal(rec.Body.Bytes(), &pd)
		assert.NoError(t, err)
		assert.Equal(t, "about:blank", pd.Type)
		assert.Equal(t, "Service Unavailable", pd.Title)
		assert.Equal(t, http.StatusServiceUnavailable, pd.Status)
		assert.Equal(t, "Model is under maintenance", pd.Detail)
	})

	t.Run("other models still work when one has offline message", func(t *testing.T) {
		mappings := &config.GatewayConfig{}
		mappings.OpenAI.Host = "test.host"
		mappings.OpenAI.ChatCompletions = map[string]config.ModelFunctionDetails{
			"offline-model": {
				ModelName:      "offline-model",
				FunctionID:     "test-func-id",
				OfflineMessage: "Model is under maintenance",
			},
			"online-model": {
				ModelName:  "online-model",
				FunctionID: "test-func-id",
			},
		}

		mux, err := buildChiMux(mappings, serverConfig)
		assert.NoError(t, err)

		// The offline model should return 503
		rec := httptest.NewRecorder()
		body := `{"model":"offline-model"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		req.Host = "test.host"
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

		// The online model should not return 503
		rec = httptest.NewRecorder()
		body = `{"model":"online-model"}`
		req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		req.Host = "test.host"
		mux.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("passes through when no offline message is set", func(t *testing.T) {
		mappings := &config.GatewayConfig{}
		mappings.OpenAI.Host = "test.host"

		mux, err := buildChiMux(mappings, serverConfig)
		assert.NoError(t, err)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		mux.ServeHTTP(rec, req)

		assert.NotEqual(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestBuildChiMux_ImageEndpoints(t *testing.T) {
	var forwardedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	serverConfig := Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
	}

	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = "test.host"
	mappings.OpenAI.ImageGenerations = map[string]config.ModelFunctionDetails{
		"gen": {ModelName: "qwen/qwen-image-gen", FunctionID: "gen-id"},
	}
	mappings.OpenAI.ImageEdits = map[string]config.ModelFunctionDetails{
		"edit": {ModelName: "qwen/qwen-image-edit-2511", FunctionID: "edit-id"},
	}
	mappings.OpenAI.ImageVariations = map[string]config.ModelFunctionDetails{
		"var": {ModelName: "qwen/qwen-image-var", FunctionID: "var-id"},
	}

	mux, err := buildChiMux(mappings, serverConfig)
	require.NoError(t, err)

	t.Run("routes /v1/images/generations for a known model", func(t *testing.T) {
		forwardedPath = ""
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
			bytes.NewBufferString(`{"model":"qwen/qwen-image-gen","prompt":"a cat"}`))
		req.Host = "test.host"
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotEmpty(t, forwardedPath)
	})

	t.Run("returns 404 for unknown image-generations model", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
			bytes.NewBufferString(`{"model":"unknown/model","prompt":"x"}`))
		req.Host = "test.host"
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("routes /v1/images/edits for multipart with known model", func(t *testing.T) {
		forwardedPath = ""
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		require.NoError(t, mw.WriteField("model", "qwen/qwen-image-edit-2511"))
		require.NoError(t, mw.WriteField("prompt", "make sky pink"))
		fw, err := mw.CreateFormFile("image", "input.jpg")
		require.NoError(t, err)
		_, err = io.WriteString(fw, "fake-image-bytes")
		require.NoError(t, err)
		require.NoError(t, mw.Close())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
		req.Host = "test.host"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotEmpty(t, forwardedPath)
	})

	t.Run("returns 404 for unknown image-edits model in multipart", func(t *testing.T) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		require.NoError(t, mw.WriteField("model", "unknown/model"))
		require.NoError(t, mw.Close())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
		req.Host = "test.host"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("routes /v1/images/variations for multipart with known model", func(t *testing.T) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		require.NoError(t, mw.WriteField("model", "qwen/qwen-image-var"))
		fw, err := mw.CreateFormFile("image", "input.jpg")
		require.NoError(t, err)
		_, err = io.WriteString(fw, "fake-image-bytes")
		require.NoError(t, err)
		require.NoError(t, mw.Close())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/variations", body)
		req.Host = "test.host"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
