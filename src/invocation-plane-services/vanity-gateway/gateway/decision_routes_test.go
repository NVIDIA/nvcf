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
	"ai-api-gateway-service/middleware"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

const (
	decisionModel        = "acme/decision-model"
	imagesHeader         = "TypeSafe-Danger-Zone-Images"
	decisionFunctionID   = "decision-func-1"
	fastAPIValidationErr = `{"detail":[{"loc":["body","questions"],"msg":"field required","type":"missing"}]}`
)

type systemOneErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// decisionMappings enables decisionModel on the listed routes only.
func decisionMappings(decisions, systemOne bool, mutate func(*config.ModelFunctionDetails)) *config.GatewayConfig {
	entry := config.ModelFunctionDetails{ModelName: decisionModel, FunctionID: decisionFunctionID}
	if mutate != nil {
		mutate(&entry)
	}
	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	if decisions {
		mappings.OpenAI.Decisions = map[string]config.ModelFunctionDetails{"d": entry}
	}
	if systemOne {
		mappings.TypeSafeSystemOne = map[string]config.ModelFunctionDetails{"s": entry}
	}
	return mappings
}

func llmDecision(entry *config.ModelFunctionDetails) { entry.FunctionType = config.FunctionTypeLLM }

func postDecision(t *testing.T, mux http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := openAIRequest(t, path, body)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func requireOpenAIErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int) OpenAIError {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var payload OpenAIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
	require.NotEmpty(t, payload.Error.Message)
	require.NotEmpty(t, payload.Error.Code)
	return payload
}

func requireSystemOneErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) systemOneErrorBody {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var payload systemOneErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
	require.Equal(t, code, payload.Error.Code)
	require.NotEmpty(t, payload.Error.Message)
	requestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, requestID)
	require.Equal(t, requestID, payload.RequestID, "top-level request_id must equal the X-Request-Id header")
	require.Equal(t, requestID, payload.Error.RequestID, "error.request_id must equal the X-Request-Id header")
	return payload
}

func TestDecisionRoutesForwardDirectWithoutTranslation(t *testing.T) {
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"answers":{"q1":{"yes":0.9}}}`))
			})
			mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")

			body := `{"model":"` + decisionModel + `","input":"is it red?","questions":[{"type":"predicate","text":"red?"}],"extra":{"keep":true}}`
			rec := postDecision(t, mux, path, body, map[string]string{imagesHeader: "true"})

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"answers":{"q1":{"yes":0.9}}}`, rec.Body.String())
			received := awaitRequest(t, requests)
			assert.Equal(t, path, received.path, "the route is kept when usePexec is false")
			assert.Equal(t, decisionFunctionID, received.headers.Get("function-id"))
			assert.Equal(t, "true", received.headers.Get(imagesHeader), "vendor image header must pass unchanged")
			assert.Equal(t, body, received.body, "the body must pass through unchanged")
		})
	}
}

func TestDecisionRoutesForwardThroughLLMGatewayWithImageHeader(t *testing.T) {
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path, func(t *testing.T) {
			nvcfRequests := make(chan capturedRequest, 1)
			nvcfBackend := captureServer(t, nvcfRequests, nil)
			llmRequests := make(chan capturedRequest, 1)
			llmBackend := captureServer(t, llmRequests, nil)
			mux := openAIMux(t, decisionMappings(true, true, llmDecision), nvcfBackend.URL, llmBackend.URL)

			rec := postDecision(t, mux, path, `{"model":"`+decisionModel+`","input":"x"}`, map[string]string{imagesHeader: "true"})

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			received := awaitRequest(t, llmRequests)
			assert.Equal(t, path, received.path)
			assert.Contains(t, received.body, `"model":"`+decisionFunctionID+"/"+decisionModel+`"`)
			assert.Equal(t, "true", received.headers.Get(imagesHeader))
			assert.Empty(t, received.headers.Get("function-id"))
			assert.Empty(t, nvcfRequests)
		})
	}
}

func TestDecisionRoutesIgnoreStreamTrueForAccept(t *testing.T) {
	for _, tc := range []struct {
		path       string
		wantAccept string
	}{
		{decisionsPath, ""},
		{systemOnePath, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, nil)
			mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")

			rec := postDecision(t, mux, tc.path, `{"model":"`+decisionModel+`","stream":true}`, nil)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.wantAccept, awaitRequest(t, requests).headers.Get("Accept"))
		})
	}

	t.Run("control: chat completions still sets Accept for stream true", func(t *testing.T) {
		requests := make(chan capturedRequest, 1)
		backend := captureServer(t, requests, nil)
		mappings := decisionMappings(true, true, nil)
		mappings.OpenAI.ChatCompletions = mappings.OpenAI.Decisions
		mux := openAIMux(t, mappings, backend.URL, "http://llm.invalid")

		rec := postDecision(t, mux, "/v1/chat/completions", `{"model":"`+decisionModel+`","stream":true}`, nil)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/event-stream", awaitRequest(t, requests).headers.Get("Accept"))
	})
}

func TestDecisionRoutesAcceptLargeImageBodies(t *testing.T) {
	image := strings.Repeat("A", 8*1024*1024)
	body := `{"model":"` + decisionModel + `","state":{"image":"data:image/png;base64,` + image + `"}}`
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, nil)
			mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")

			rec := postDecision(t, mux, path, body, map[string]string{imagesHeader: "true"})

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, len(body), len(awaitRequest(t, requests).body))
		})
	}
}

func TestDecisionRoutesUnknownModelReturnsNativeJSON404(t *testing.T) {
	mux := openAIMux(t, decisionMappings(true, true, nil), "http://nvcf.invalid", "http://llm.invalid")

	rec := postDecision(t, mux, decisionsPath, `{"model":"nope"}`, nil)
	assert.Equal(t, "model_not_found", requireOpenAIErrorBody(t, rec, http.StatusNotFound).Error.Code)
	assert.Equal(t, "gateway_rejected", rec.Header().Get("NVCF-Error-Source"))

	rec = postDecision(t, mux, systemOnePath, `{"model":"nope"}`, nil)
	requireSystemOneErrorBody(t, rec, http.StatusNotFound, "not_found")
	assert.Equal(t, "gateway_rejected", rec.Header().Get("NVCF-Error-Source"))
}

func TestDecisionModelEnabledForOneRouteIsNotFoundOnTheOther(t *testing.T) {
	t.Run("decisions only", func(t *testing.T) {
		requests := make(chan capturedRequest, 2)
		backend := captureServer(t, requests, nil)
		mux := openAIMux(t, decisionMappings(true, false, nil), backend.URL, "http://llm.invalid")

		requireSystemOneErrorBody(t, postDecision(t, mux, systemOnePath, `{"model":"`+decisionModel+`"}`, nil), http.StatusNotFound, "not_found")
		assert.Empty(t, requests, "the other route must not reach the backend")
		require.Equal(t, http.StatusOK, postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil).Code)
	})
	t.Run("systemone only", func(t *testing.T) {
		requests := make(chan capturedRequest, 2)
		backend := captureServer(t, requests, nil)
		mux := openAIMux(t, decisionMappings(false, true, nil), backend.URL, "http://llm.invalid")

		rec := postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil)
		requireOpenAIErrorBody(t, rec, http.StatusNotFound)
		assert.Empty(t, requests)
		require.Equal(t, http.StatusOK, postDecision(t, mux, systemOnePath, `{"model":"`+decisionModel+`"}`, nil).Code)
	})
}

func TestDecisionRoutesLocalErrorsUseNativeShape(t *testing.T) {
	tooLarge := `{"model":"` + strings.Repeat("a", maxRequestSize) + `"}`
	for _, tc := range []struct {
		name, body string
		status     int
		s1Code     string
	}{
		{"malformed json", `{`, http.StatusBadRequest, "invalid_json"},
		{"trailing data", `{"model":"x"} x`, http.StatusBadRequest, "invalid_json"},
		{"model wrong type", `{"model":1}`, http.StatusBadRequest, "invalid_request"},
		{"body is an array", `[1]`, http.StatusBadRequest, "invalid_request"},
		{"body is null", `null`, http.StatusBadRequest, "invalid_request"},
		{"missing model", `{"input":"x"}`, http.StatusBadRequest, "invalid_request"},
		{"body too large", tooLarge, http.StatusRequestEntityTooLarge, "request_too_large"},
	} {
		t.Run("decisions "+tc.name, func(t *testing.T) {
			mux := openAIMux(t, decisionMappings(true, true, nil), "http://nvcf.invalid", "http://llm.invalid")
			rec := postDecision(t, mux, decisionsPath, tc.body, nil)
			assert.Equal(t, tc.s1Code, requireOpenAIErrorBody(t, rec, tc.status).Error.Code)
		})
		t.Run("systemone "+tc.name, func(t *testing.T) {
			mux := openAIMux(t, decisionMappings(true, true, nil), "http://nvcf.invalid", "http://llm.invalid")
			requireSystemOneErrorBody(t, postDecision(t, mux, systemOnePath, tc.body, nil), tc.status, tc.s1Code)
		})
	}
}

func TestDecisionRoutesOfflineAndEndOfLifeUseNativeShape(t *testing.T) {
	for _, llm := range []bool{false, true} {
		name := "direct"
		if llm {
			name = "llm"
		}
		t.Run(name+" offline", func(t *testing.T) {
			mutate := func(e *config.ModelFunctionDetails) {
				if llm {
					llmDecision(e)
				}
				e.OfflineMessage = "down for maintenance"
			}
			mux := openAIMux(t, decisionMappings(true, true, mutate), "http://nvcf.invalid", "http://llm.invalid")

			rec := postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil)
			assert.Equal(t, "provider_unavailable", requireOpenAIErrorBody(t, rec, http.StatusServiceUnavailable).Error.Code)
			assert.Equal(t, "10800", rec.Header().Get("Retry-After"))
			assert.Contains(t, rec.Body.String(), "down for maintenance")

			rec = postDecision(t, mux, systemOnePath, `{"model":"`+decisionModel+`"}`, nil)
			requireSystemOneErrorBody(t, rec, http.StatusServiceUnavailable, "provider_unavailable")
			assert.Equal(t, "10800", rec.Header().Get("Retry-After"))
		})
		t.Run(name+" end of life", func(t *testing.T) {
			mutate := func(e *config.ModelFunctionDetails) {
				if llm {
					llmDecision(e)
				}
				e.EOL = time.Now().Add(-24 * time.Hour)
			}
			mux := openAIMux(t, decisionMappings(true, true, mutate), "http://nvcf.invalid", "http://llm.invalid")

			assert.Equal(t, "model_expired", requireOpenAIErrorBody(t, postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil), http.StatusGone).Error.Code)
			requireSystemOneErrorBody(t, postDecision(t, mux, systemOnePath, `{"model":"`+decisionModel+`"}`, nil), http.StatusGone, "not_found")
		})
	}
}

// With usePexec the director rewrites the URL path to the pexec path before it
// proxies, so the error shape must come from the route the client called.
func TestDecisionRoutesProxyFailureKeepsNativeShapeAfterPexecPathRewrite(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	for _, usePexec := range []bool{true, false} {
		mutate := func(e *config.ModelFunctionDetails) { e.UsePexec = usePexec }
		mux := openAIMux(t, decisionMappings(true, true, mutate), downURL, "http://llm.invalid")

		name := "pexec"
		if !usePexec {
			name = "no pexec"
		}
		t.Run(name+" systemone", func(t *testing.T) {
			rec := postDecision(t, mux, systemOnePath, `{"model":"`+decisionModel+`"}`, nil)
			requireSystemOneErrorBody(t, rec, http.StatusBadGateway, "provider_unavailable")
		})
		t.Run(name+" decisions", func(t *testing.T) {
			rec := postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil)
			requireOpenAIErrorBody(t, rec, http.StatusBadGateway)
			assert.NotContains(t, rec.Header().Get("Content-Type"), "problem+json")
		})
	}
}

func TestDecisionRoutesClientCancelUsesNativeShape(t *testing.T) {
	backend := captureServer(t, make(chan capturedRequest, 1), nil)
	mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := openAIRequest(t, systemOnePath, `{"model":"`+decisionModel+`"}`).WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	requireSystemOneErrorBody(t, rec, 499, "request_cancelled")

	req = openAIRequest(t, decisionsPath, `{"model":"`+decisionModel+`"}`).WithContext(ctx)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, "request_cancelled", requireOpenAIErrorBody(t, rec, 499).Error.Code)
}

func TestDecisionRoutesPassBackendErrorsThroughUnchanged(t *testing.T) {
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path+" validation body", func(t *testing.T) {
			backend := captureServer(t, make(chan capturedRequest, 1), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-System-One-Error-Code", "invalid_request")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(fastAPIValidationErr))
			})
			mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")

			rec := postDecision(t, mux, path, `{"model":"`+decisionModel+`"}`, nil)

			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
			assert.Equal(t, fastAPIValidationErr, rec.Body.String())
			assert.Equal(t, "invalid_request", rec.Header().Get("X-System-One-Error-Code"))
		})
		t.Run(path+" rate limit with Retry-After", func(t *testing.T) {
			backend := captureServer(t, make(chan capturedRequest, 1), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"slow down"}`))
			})
			mux := openAIMux(t, decisionMappings(true, true, nil), backend.URL, "http://llm.invalid")

			rec := postDecision(t, mux, path, `{"model":"`+decisionModel+`"}`, nil)

			assert.Equal(t, http.StatusTooManyRequests, rec.Code)
			assert.Equal(t, `{"detail":"slow down"}`, rec.Body.String())
			assert.Equal(t, "7", rec.Header().Get("Retry-After"))
		})
	}
}

func TestDecisionRoutesAreNotReachableForChatOnlyModels(t *testing.T) {
	llmRequests := make(chan capturedRequest, 1)
	llmBackend := captureServer(t, llmRequests, nil)
	mux := openAIMux(t, llmMappings(llmModelEntry()), "http://nvcf.invalid", llmBackend.URL)

	for _, path := range []string{decisionsPath, systemOnePath} {
		rec := postDecision(t, mux, path, `{"model":"`+publicModel+`"}`, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"), path)
	}
	assert.Empty(t, llmRequests)
}

func TestDecisionRoutesRejectNonPost(t *testing.T) {
	mux := openAIMux(t, decisionMappings(true, true, nil), "http://nvcf.invalid", "http://llm.invalid")
	for _, path := range []string{decisionsPath, systemOnePath} {
		req := httptest.NewRequest(http.MethodGet, path, bytes.NewBufferString(""))
		req.Host = openAIHost
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, path)
	}
}

// With usePexec the invocation service receives the pexec path, so a function
// serving both routes reaches only its default path. The image header and the
// body still pass through.
func TestDecisionRoutesUsePexecRewritesPathAndKeepsHeaderAndBody(t *testing.T) {
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path, func(t *testing.T) {
			requests := make(chan capturedRequest, 1)
			backend := captureServer(t, requests, nil)
			mux := openAIMux(t, decisionMappings(true, true, func(e *config.ModelFunctionDetails) { e.UsePexec = true }), backend.URL, "http://llm.invalid")

			body := `{"model":"` + decisionModel + `","state":{}}`
			rec := postDecision(t, mux, path, body, map[string]string{imagesHeader: "true"})

			require.Equal(t, http.StatusOK, rec.Code)
			received := awaitRequest(t, requests)
			assert.Equal(t, "/v2/nvcf/pexec/functions/"+decisionFunctionID, received.path)
			assert.Equal(t, "true", received.headers.Get(imagesHeader))
			assert.Equal(t, body, received.body)
		})
	}
}

func TestDecisionRoutesSetEndpointTypeSpanAttribute(t *testing.T) {
	for _, tc := range []struct {
		path, want string
		llm        bool
	}{
		{decisionsPath, "decisions", false},
		{systemOnePath, "systemone", false},
		{decisionsPath, "decisions", true},
		{systemOnePath, "systemone", true},
	} {
		name := tc.path
		if tc.llm {
			name += " llm"
		}
		t.Run(name, func(t *testing.T) {
			tp, exp := newTestTracerProvider()
			defer tp.Shutdown(context.Background())
			backend := captureServer(t, make(chan capturedRequest, 1), nil)
			var mutate func(*config.ModelFunctionDetails)
			if tc.llm {
				mutate = llmDecision
			}
			mappings := decisionMappings(true, true, mutate)
			llm, err := NewLLMGatewayDirector(backend.URL, http.DefaultTransport)
			require.NoError(t, err)
			vanity, err := NewVanityDirector(backend.URL, http.DefaultTransport)
			require.NoError(t, err)
			director, err := NewModelDirector(mappings, regexp.MustCompile("^$"), vanity, llm, nil)
			require.NoError(t, err)

			req := openAIRequest(t, tc.path, `{"model":"`+decisionModel+`"}`)
			ctx, span := tp.Tracer("test").Start(req.Context(), "decision-test")
			rec := httptest.NewRecorder()
			if tc.path == decisionsPath {
				director.ServeDecisions(rec, req.WithContext(ctx))
			} else {
				director.ServeSystemOne(rec, req.WithContext(ctx))
			}
			span.End()

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got, ok := spanAttr(exp.GetSpans()[0], attribute.Key("endpoint.type"))
			require.True(t, ok)
			require.Equal(t, tc.want, got.AsString())
		})
	}
}

func TestDecisionRoutesRelayBackend429BodiesUnchangedWithCustomMessage(t *testing.T) {
	bodies := map[string]string{
		decisionsPath: `{"error":{"message":"slow down","type":"rate_limit","param":null,"code":"rate_limited"}}`,
		systemOnePath: `{"error":{"code":"rate_limit_exceeded","message":"slow down","request_id":"r1"},"request_id":"r1"}`,
	}
	for path, native := range bodies {
		for name, body := range map[string]string{"native": native, "fastapi": fastAPIValidationErr} {
			t.Run(path+" "+name, func(t *testing.T) {
				backend := captureServer(t, make(chan capturedRequest, 1), func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(body))
				})
				mux := openAIMux(t, decisionMappings(true, true, func(e *config.ModelFunctionDetails) {
					e.TooManyRequestsMessage = "Upgrade your plan."
				}), backend.URL, "http://llm.invalid")

				rec := postDecision(t, mux, path, `{"model":"`+decisionModel+`"}`, nil)

				assert.Equal(t, http.StatusTooManyRequests, rec.Code)
				assert.Equal(t, body, rec.Body.String())
				assert.Equal(t, "7", rec.Header().Get("Retry-After"))
			})
		}
	}
}

// With usePexec the director rewrites the URL path, so Messages errors must
// take their shape from the route the client called.
func TestMessagesProxyFailureKeepsAnthropicShapeAfterPexecPathRewrite(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	mappings := &config.GatewayConfig{}
	mappings.OpenAI.Host = openAIHost
	mappings.AnthropicMessages = map[string]config.ModelFunctionDetails{
		"m": {ModelName: decisionModel, FunctionID: decisionFunctionID, UsePexec: true},
	}
	mux := openAIMux(t, mappings, downURL, "http://llm.invalid")

	rec := postDecision(t, mux, messagesPath, `{"model":"`+decisionModel+`","max_tokens":8,"messages":[]}`, nil)

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"upstream request failed"}}`, rec.Body.String())
}

// A generic vanity path entry is not a decision or Messages route even when
// its path matches one, so it keeps the problem+json shape and the dated end
// of life message it had before those routes existed.
func TestVanityPathEntriesAtNativeRoutePathsKeepProblemShape(t *testing.T) {
	const vanityHost = "vanity.test"
	eol := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	serve := func(t *testing.T, path string, entry config.PathFunctionDetails) *httptest.ResponseRecorder {
		t.Helper()
		entry.Path = path
		entry.FunctionID = "vanity-func"
		mappings := decisionMappings(true, true, nil)
		mappings.Vanity = map[string]config.VanityEntry{"v": {Host: vanityHost, Paths: map[string]config.PathFunctionDetails{"p": entry}}}
		mux := openAIMux(t, mappings, downURL, "http://llm.invalid")
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+decisionModel+`"}`))
		req.Host = vanityHost
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{decisionsPath, systemOnePath, messagesPath} {
		t.Run(path+" offline", func(t *testing.T) {
			rec := serve(t, path, config.PathFunctionDetails{OfflineMessage: "down for maintenance"})
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), "down for maintenance")
		})
		t.Run(path+" end of life", func(t *testing.T) {
			rec := serve(t, path, config.PathFunctionDetails{EOL: eol})
			require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), eol.Format(time.RFC3339))
		})
	}
	for _, path := range []string{decisionsPath, systemOnePath} {
		t.Run(path+" proxy failure", func(t *testing.T) {
			rec := serve(t, path, config.PathFunctionDetails{})
			require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
		})
	}
	// Messages-path proxy failures on vanity hosts used the Anthropic shape
	// before the decision routes existed; that stays.
	t.Run(messagesPath+" proxy failure", func(t *testing.T) {
		rec := serve(t, messagesPath, config.PathFunctionDetails{})
		require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), `"type":"error"`)
	})
}

// The gateway and Vanity emit the same error code for the same status on the
// decision routes.
func TestDecisionRouteErrorCodesFollowTheSharedTable(t *testing.T) {
	for _, tc := range []struct {
		status        int
		kind          string
		decisionsCode string
		decisionsType string
		systemOneCode string
	}{
		{400, errKindInvalidJSON, "invalid_json", "invalid_request_error", "invalid_json"},
		{400, errKindInvalidRequest, "invalid_request", "invalid_request_error", "invalid_request"},
		{401, "", "invalid_api_key", "authentication_error", "invalid_api_key"},
		{403, "", "forbidden", "permission_error", "forbidden"},
		{404, "", "model_not_found", "invalid_request_error", "not_found"},
		{410, "", "model_expired", "invalid_request_error", "not_found"},
		{413, "", "request_too_large", "invalid_request_error", "request_too_large"},
		{415, "", "unsupported_media_type", "invalid_request_error", "unsupported_media_type"},
		{422, "", "invalid_request", "invalid_request_error", "invalid_request"},
		{429, "", "rate_limit_exceeded", "rate_limit_error", "rate_limit_exceeded"},
		{499, "", "request_cancelled", "invalid_request_error", "request_cancelled"},
		{502, "", "provider_unavailable", "server_error", "provider_unavailable"},
		{503, "", "provider_unavailable", "server_error", "provider_unavailable"},
		{529, "", "provider_unavailable", "overloaded_error", "provider_unavailable"},
		{504, "", "provider_timeout", "server_error", "provider_timeout"},
		{500, "", "internal_error", "server_error", "internal_error"},
		{418, "", "invalid_request", "invalid_request_error", "invalid_request"},
	} {
		t.Run(strconv.Itoa(tc.status)+" "+tc.kind, func(t *testing.T) {
			rec := httptest.NewRecorder()
			require.True(t, writeProtocolError(rec, nativeProtocolDecisions, tc.status, tc.kind, "m"))
			var openAI OpenAIError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &openAI))
			assert.Equal(t, tc.decisionsCode, openAI.Error.Code, "decisions code")
			assert.Equal(t, tc.decisionsType, openAI.Error.Type, "decisions type")

			rec = httptest.NewRecorder()
			require.True(t, writeProtocolError(rec, nativeProtocolSystemOne, tc.status, tc.kind, "m"))
			var systemOne systemOneErrorBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &systemOne))
			assert.Equal(t, tc.systemOneCode, systemOne.Error.Code, "systemone code")
		})
	}
}

// Decision routes read only model, so a field the gateway does not use, such
// as a non-boolean stream, reaches the backend unchanged.
func TestDecisionRoutesForwardNonBooleanStream(t *testing.T) {
	for _, llm := range []bool{false, true} {
		for _, path := range []string{decisionsPath, systemOnePath} {
			name := path
			if llm {
				name += " llm"
			}
			t.Run(name, func(t *testing.T) {
				requests := make(chan capturedRequest, 1)
				backend := captureServer(t, requests, nil)
				var mutate func(*config.ModelFunctionDetails)
				nvcf, llmURL := backend.URL, "http://llm.invalid"
				if llm {
					mutate = llmDecision
					nvcf, llmURL = "http://nvcf.invalid", backend.URL
				}
				mux := openAIMux(t, decisionMappings(true, true, mutate), nvcf, llmURL)

				rec := postDecision(t, mux, path, `{"model":"`+decisionModel+`","stream":"no"}`, nil)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, awaitRequest(t, requests).body, `"stream":"no"`)
			})
		}
	}
}

// Decision routes take JSON only. Other media types are a client mistake, so
// they are rejected locally instead of failing later as an upstream error.
func TestDecisionRoutesRejectNonJSONContentTypes(t *testing.T) {
	multipartBody := "--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\n" + decisionModel + "\r\n--x--\r\n"
	for _, llm := range []bool{false, true} {
		for _, tc := range []struct {
			name, contentType, body string
		}{
			{"multipart without boundary", "multipart/form-data", multipartBody},
			{"multipart with boundary", "multipart/form-data; boundary=x", multipartBody},
			{"text plain", "text/plain", `{"model":"` + decisionModel + `"}`},
			{"unparseable", "application/json; =", `{"model":"` + decisionModel + `"}`},
		} {
			for _, path := range []string{decisionsPath, systemOnePath} {
				name := path + " " + tc.name
				if llm {
					name += " llm"
				}
				t.Run(name, func(t *testing.T) {
					requests := make(chan capturedRequest, 1)
					backend := captureServer(t, requests, nil)
					var mutate func(*config.ModelFunctionDetails)
					if llm {
						mutate = llmDecision
					}
					director, err := NewModelDirector(decisionMappings(true, true, mutate), regexp.MustCompile("^$"),
						mustVanityDirector(t, backend.URL), mustLLMGatewayDirector(t, backend.URL), nil)
					require.NoError(t, err)
					handler := director.ServeDecisions
					if path == systemOnePath {
						handler = director.ServeSystemOne
					}
					req := openAIRequest(t, path, tc.body)
					req.Header.Set("Content-Type", tc.contentType)

					rec, reader, spans := serveThroughTelemetry(t, handler, req)

					requireOrigin(t, rec, reader, spans, http.StatusUnsupportedMediaType, middleware.GatewayProxyOutcomeRejected)
					if path == decisionsPath {
						assert.Equal(t, "unsupported_media_type", requireOpenAIErrorBody(t, rec, http.StatusUnsupportedMediaType).Error.Code)
					} else {
						var payload systemOneErrorBody
						require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
						assert.Equal(t, "unsupported_media_type", payload.Error.Code)
					}
					assert.Empty(t, requests, "a rejected request must not reach the backend")
				})
			}
		}
	}
}

// A missing Content-Type is read as JSON, as on the chat completions route.
func TestDecisionRoutesAcceptJSONOrMissingContentType(t *testing.T) {
	for _, mode := range []struct {
		name   string
		mutate func(*config.ModelFunctionDetails)
	}{
		{"direct", nil},
		{"llm gateway", llmDecision},
	} {
		for _, contentType := range []string{"", "application/json", "application/json; charset=utf-8"} {
			for _, path := range []string{decisionsPath, systemOnePath} {
				t.Run(mode.name+" "+path+" "+contentType, func(t *testing.T) {
					requests := make(chan capturedRequest, 1)
					backend := captureServer(t, requests, nil)
					mux := openAIMux(t, decisionMappings(true, true, mode.mutate), backend.URL, backend.URL)
					req := openAIRequest(t, path, `{"model":"`+decisionModel+`"}`)
					if contentType != "" {
						req.Header.Set("Content-Type", contentType)
					}
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					awaitRequest(t, requests)
				})
			}
		}
	}
}

func mustVanityDirector(t *testing.T, endpoint string) *VanityDirector {
	t.Helper()
	director, err := NewVanityDirector(endpoint, http.DefaultTransport)
	require.NoError(t, err)
	return director
}

func mustLLMGatewayDirector(t *testing.T, endpoint string) *LLMGatewayDirector {
	t.Helper()
	director, err := NewLLMGatewayDirector(endpoint, http.DefaultTransport)
	require.NoError(t, err)
	return director
}

// On the LLM Gateway path the decision routes replace only the model value,
// so the rest of the body reaches the gateway byte for byte.
func TestDecisionRoutesLLMPathReplacesOnlyTheModelValue(t *testing.T) {
	const target = `"` + decisionFunctionID + "/" + decisionModel + `"`
	for _, tc := range []struct {
		name, body, want string
	}{
		{
			name: "key order, spacing and escapes kept",
			body: "{ \"z\":1,\n  \"input\" : \"a<b && c>d \\u00e9\",\"model\" :\t\"" + decisionModel + "\" , \"state\":{\"model\":\"inner\"} }",
			want: "{ \"z\":1,\n  \"input\" : \"a<b && c>d \\u00e9\",\"model\" :\t" + target + " , \"state\":{\"model\":\"inner\"} }",
		},
		{
			name: "every top-level model member is replaced",
			body: `{"model":"` + decisionModel + `","x":[1,2],"model":"other/fn"}`,
			want: `{"model":` + target + `,"x":[1,2],"model":` + target + `}`,
		},
		{
			name: "a model key in another case is replaced too",
			body: `{"model":"` + decisionModel + `","MODEL":"other/fn"}`,
			want: `{"model":` + target + `,"MODEL":` + target + `}`,
		},
	} {
		for _, path := range []string{decisionsPath, systemOnePath} {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				requests := make(chan capturedRequest, 1)
				backend := captureServer(t, requests, nil)
				mux := openAIMux(t, decisionMappings(true, true, llmDecision), "http://nvcf.invalid", backend.URL)

				rec := postDecision(t, mux, path, tc.body, nil)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, tc.want, awaitRequest(t, requests).body)
			})
		}
	}
}

// endpoint.type names the route; upstream.kind tells dashboards whether the
// request went through the LLM Gateway or straight to the function.
func TestNativeRoutesSetUpstreamKindSpanAttribute(t *testing.T) {
	for _, path := range []string{decisionsPath, systemOnePath, messagesPath} {
		for _, llm := range []bool{false, true} {
			wantKind, wantEndpoint := "direct", map[string]string{decisionsPath: "decisions", systemOnePath: "systemone", messagesPath: "anthropic"}[path]
			if llm {
				wantKind = "llm_gateway"
			}
			t.Run(path+" "+wantKind, func(t *testing.T) {
				tp, exp := newTestTracerProvider()
				defer tp.Shutdown(context.Background())
				backend := captureServer(t, make(chan capturedRequest, 1), nil)
				var mutate func(*config.ModelFunctionDetails)
				if llm {
					mutate = llmDecision
				}
				mappings := decisionMappings(true, true, mutate)
				mappings.AnthropicMessages = mappings.TypeSafeSystemOne
				director, err := NewModelDirector(mappings, regexp.MustCompile("^$"), mustVanityDirector(t, backend.URL), mustLLMGatewayDirector(t, backend.URL), nil)
				require.NoError(t, err)
				handler := map[string]http.HandlerFunc{decisionsPath: director.ServeDecisions, systemOnePath: director.ServeSystemOne, messagesPath: director.ServeMessages}[path]

				req := openAIRequest(t, path, `{"model":"`+decisionModel+`"}`)
				req.Header.Set("Content-Type", "application/json")
				ctx, span := tp.Tracer("test").Start(req.Context(), "native-test")
				rec := httptest.NewRecorder()
				handler(rec, req.WithContext(ctx))
				span.End()

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				got, ok := spanAttr(exp.GetSpans()[0], attribute.Key("upstream.kind"))
				require.True(t, ok, "upstream.kind must be set")
				assert.Equal(t, wantKind, got.AsString())
				endpoint, ok := spanAttr(exp.GetSpans()[0], attribute.Key("endpoint.type"))
				require.True(t, ok)
				assert.Equal(t, wantEndpoint, endpoint.AsString())
			})
		}
	}
}

// A shadow copy of a decision request keeps every byte of the body except the
// model value, on both the direct and the LLM Gateway path.
func TestDecisionRouteShadowsReplaceOnlyTheModelValue(t *testing.T) {
	const shadowModel = "acme/decision-shadow"
	const shadowFunctionID = "decision-shadow-func"
	body := "{ \"input\" : \"a<b && c>d\",\n \"z\":1,\"model\":\t\"" + decisionModel + "\" , \"input\":\"second\" }"
	for _, llm := range []bool{false, true} {
		for _, path := range []string{decisionsPath, systemOnePath} {
			name := path
			wantModel := `"` + shadowModel + `"`
			if llm {
				name += " llm"
				wantModel = `"` + shadowFunctionID + "/" + shadowModel + `"`
			}
			t.Run(name, func(t *testing.T) {
				requests := make(chan capturedRequest, 2)
				backend := captureServer(t, requests, nil)
				percentage := 100
				primary := config.ModelFunctionDetails{
					ModelName:  decisionModel,
					FunctionID: decisionFunctionID,
					Shadows:    []config.ShadowConfig{{ModelName: shadowModel, Percentage: &percentage}},
				}
				shadow := config.ModelFunctionDetails{ModelName: shadowModel, FunctionID: shadowFunctionID}
				if llm {
					llmDecision(&primary)
					llmDecision(&shadow)
				}
				section := map[string]config.ModelFunctionDetails{"p": primary, "s": shadow}
				mappings := &config.GatewayConfig{}
				mappings.OpenAI.Host = openAIHost
				if path == decisionsPath {
					mappings.OpenAI.Decisions = section
				} else {
					mappings.TypeSafeSystemOne = section
				}
				mux := openAIMux(t, mappings, backend.URL, backend.URL)

				rec := postDecision(t, mux, path, body, nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				want := strings.Replace(body, `"`+decisionModel+`"`, wantModel, 1)
				var shadowBody string
				for range 2 {
					received := awaitRequest(t, requests)
					if received.headers.Get(shadowHeader) != "" {
						shadowBody = received.body
					}
				}
				require.NotEmpty(t, shadowBody, "the shadow request must reach the backend")
				assert.Equal(t, want, shadowBody)
			})
		}
	}
}
