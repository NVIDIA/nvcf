// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// serveDecisionUpstream runs route against a backend that answers with
// status, headers and body, and returns the gateway response.
func serveDecisionUpstream(t *testing.T, route decisionRoute, cfg *config.Config, clientHeaders http.Header, status int, headers http.Header, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for key, values := range headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(upstream.Close)

	e := decisionTestAPI(t, upstream.URL, cfg, nil, decisionSpecs(route.path))
	req := decisionRequest(route, route.body)
	for key, values := range clientHeaders {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestDecisionRoutesRelayBackendErrorsUnchanged checks that a non-2xx response
// without X-Stargate-Error-Code is a backend error and reaches the client byte
// for byte, whatever its shape, size or encoding.
func TestDecisionRoutesRelayBackendErrorsUnchanged(t *testing.T) {
	t.Parallel()

	gzipped := gzipBytes(t, `{"detail":"compressed validation error"}`)
	oversized := []byte(`{"detail":[{"loc":["body","state"],"msg":"too long","input":"` + strings.Repeat("x", provider.MaxUpstreamErrorBodyBytes+1) + `"}]}`)
	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name    string
			status  int
			headers http.Header
			body    []byte
		}{
			{"FastAPI detail list", http.StatusUnprocessableEntity, http.Header{"Content-Type": {"application/json"}}, []byte(`{"detail":[{"type":"missing","loc":["body","questions"],"msg":"Field required","input":{}}]}`)},
			{"OpenAI shape", http.StatusBadRequest, http.Header{"Content-Type": {"application/json"}}, []byte(`{"error":{"message":"bad question","type":"BadRequestError","param":null,"code":400}}`)},
			{"System One shape with backend codes", http.StatusBadRequest, http.Header{"Content-Type": {"application/json"}, "X-System-One-Error-Code": {"invalid_question"}, "X-System-One-Error-Source": {"upstream"}}, []byte(`{"error":{"code":"invalid_question","message":"bad","request_id":"backend-id"},"request_id":"backend-id"}`)},
			{"body over 1 MiB", http.StatusUnprocessableEntity, http.Header{"Content-Type": {"application/json"}}, oversized},
			{"gzip body", http.StatusUnprocessableEntity, http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, gzipped},
			{"unsupported encoding", http.StatusBadRequest, http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"br"}}, []byte("\x1b\x00 compressed")},
			{"plain text", http.StatusInternalServerError, http.Header{"Content-Type": {"text/plain"}}, []byte("upstream connect error")},
			{"backend overload status", 529, http.Header{"Content-Type": {"application/json"}}, []byte(`{"detail":"busy"}`)},
			{"backend request id", http.StatusBadRequest, http.Header{"Content-Type": {"application/json"}, HeaderRequestID: {"backend-request-id"}}, []byte(`{"detail":"x"}`)},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				rec := serveDecisionUpstream(t, route, config.Default(), http.Header{echo.HeaderAcceptEncoding: {"gzip, br"}}, tc.status, tc.headers, tc.body)

				require.Equal(t, tc.status, rec.Code)
				require.True(t, bytes.Equal(tc.body, rec.Body.Bytes()), "body must be byte for byte; got %d bytes, want %d", rec.Body.Len(), len(tc.body))
				for key, values := range tc.headers {
					require.Equal(t, values, rec.Header().Values(key), key)
				}
			})
		}
	}
}

func TestDecisionRoutesRouterErrorsUseNativeShape(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			name                  string
			routerCode            string
			status, wantStatus    int
			body                  string
			wantMessage           string
			wantSystemOneCode     string
			wantOpenAIType        string
			wantOpenAIOverloadTag bool
		}{
			{"no eligible candidates", "no_eligible_candidates", http.StatusNotFound, http.StatusNotFound, `{"error":"no eligible candidates","code":"no_eligible_candidates"}`, "no eligible candidates", "not_found", "invalid_request_error", false},
			{"overloaded", "overloaded_error", http.StatusServiceUnavailable, 529, `{"error":{"code":"overloaded_error","message":"Inference capacity is temporarily unavailable.","param":"","type":"overloaded_error"}}`, "Inference capacity is temporarily unavailable.", "provider_unavailable", "overloaded_error", true},
			{"tunnel failure with empty body", "tunnel_failed", http.StatusBadGateway, http.StatusBadGateway, ``, "Bad Gateway", "provider_unavailable", "server_error", false},
			{"request body over replay limit", "body_too_large", http.StatusRequestEntityTooLarge, http.StatusRequestEntityTooLarge, ``, "Request Entity Too Large", "request_too_large", "invalid_request_error", false},
			{"router rejection with a backend-shaped body", "unknown_method", http.StatusBadRequest, http.StatusBadRequest, `{"detail":[{"msg":"router says no"}]}`, "router says no", "invalid_request", "invalid_request_error", false},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				rec := serveDecisionUpstream(t, route, config.Default(), nil, tc.status,
					http.Header{"X-Stargate-Error-Code": {tc.routerCode}, "Retry-After": {"2"}, "Content-Type": {"application/json"}}, []byte(tc.body))

				requireNativeError(t, route, rec, tc.wantStatus, tc.wantOpenAIType, tc.wantSystemOneCode)
				require.Empty(t, rec.Header().Values("X-Stargate-Error-Code"))
				require.Equal(t, "2", rec.Header().Get("Retry-After"))
				require.Contains(t, rec.Body.String(), tc.wantMessage)
				if !tc.wantOpenAIOverloadTag {
					require.NotContains(t, rec.Body.String(), tc.routerCode, "the router code is only logged unless ExposeRouterErrorCodes is set")
				}
			})
		}
	}
}

// A router failure that is not a routing rejection is a bare status with no
// body and no X-Stargate-Error-Code. It must not be relayed raw.
func TestDecisionRoutesRenderBareRouterErrorsNatively(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		for _, tc := range []struct {
			status        int
			openAIType    string
			systemOneCode string
		}{
			{http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large"},
			{http.StatusBadGateway, "server_error", "provider_unavailable"},
			{http.StatusGatewayTimeout, "server_error", "provider_timeout"},
		} {
			t.Run(route.name+"/"+http.StatusText(tc.status), func(t *testing.T) {
				t.Parallel()
				rec := serveDecisionUpstream(t, route, config.Default(), nil, tc.status, http.Header{"Retry-After": {"3"}}, nil)
				requireNativeError(t, route, rec, tc.status, tc.openAIType, tc.systemOneCode)
				require.Equal(t, "3", rec.Header().Get("Retry-After"))
			})
		}
	}
}

type peekCountingReader struct {
	r    io.Reader
	read int
}

func (c *peekCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

func TestBackendErrorPeekDoesNotBufferBody(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("x", 4<<20)
	counter := &peekCountingReader{r: strings.NewReader(payload)}
	resp := &provider.ProxyResponse{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(counter)}
	req := httptest.NewRequest(http.MethodPost, decisionsEndpointPath, nil)

	require.True(t, isBackendErrorOnDecisionRoute(req, resp))
	require.LessOrEqual(t, counter.read, 1, "the peek must read at most one byte")
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, string(rest), "the peeked byte must not be lost")
}

func TestDecisionRoutesRequestIDHeader(t *testing.T) {
	t.Parallel()

	for _, route := range decisionRoutes {
		t.Run(route.name+"/success", func(t *testing.T) {
			t.Parallel()

			rec := serveDecisionUpstream(t, route, config.Default(), nil, http.StatusOK,
				http.Header{"Content-Type": {"application/json"}, HeaderRequestID: {"backend-request-id"}}, []byte(`{}`))

			require.Equal(t, http.StatusOK, rec.Code)
			if route.systemOne {
				// System One errors carry the NVCF ID, so every response does.
				require.Equal(t, "request-a", rec.Header().Get(HeaderRequestID))
			} else {
				require.Equal(t, "backend-request-id", rec.Header().Get(HeaderRequestID))
			}
			require.Empty(t, rec.Header().Get(headerMessagesRequestID))
		})
	}
}

func TestDecisionRoutesRouterRejectionLogsErrorCode(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := zlog.Logger
	zlog.Logger = zerolog.New(&logs)
	t.Cleanup(func() { zlog.Logger = oldLogger })

	for _, route := range decisionRoutes {
		logs.Reset()
		rec := serveDecisionUpstream(t, route, config.Default(), nil, http.StatusNotFound,
			http.Header{"X-Stargate-Error-Code": {"no_eligible_candidates"}}, []byte(`{"error":"no eligible candidates","code":"no_eligible_candidates"}`))

		require.Equal(t, http.StatusNotFound, rec.Code, route.name)
		require.NotContains(t, rec.Body.String(), "no_eligible_candidates", route.name)
		require.Contains(t, logs.String(), `"router_error_code":"no_eligible_candidates"`, route.name)
		require.Contains(t, logs.String(), `"message":"completed http request"`, route.name)
	}
}
