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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		{"model wrong type", `{"model":1}`, http.StatusBadRequest, "invalid_json"},
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

			requireOpenAIErrorBody(t, postDecision(t, mux, decisionsPath, `{"model":"`+decisionModel+`"}`, nil), http.StatusGone)
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
	requireSystemOneErrorBody(t, rec, 499, "internal_error")

	req = openAIRequest(t, decisionsPath, `{"model":"`+decisionModel+`"}`).WithContext(ctx)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	requireOpenAIErrorBody(t, rec, 499)
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
