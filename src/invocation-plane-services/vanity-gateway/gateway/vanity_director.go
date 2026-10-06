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
	"ai-api-gateway-service/pool"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const NVCFPollSeconds string = "NVCF-POLL-SECONDS"
const defaultNVCFPollSeconds config.SessionTimeoutSeconds = 300

// 499 (nginx's non-standard "client closed request"). Telemetry-only: the
// disconnected client can't receive it; it distinguishes a disconnect from a 502.
const statusClientClosedRequest = 499

// writeFunctionStatusError writes a 503 or 410 response if the function is offline or expired.
// name is used in the EOL detail message; pass empty string for vanity/path-based endpoints.
// Returns true if an error response was written and the caller should return early.
func writeFunctionStatusError(writer http.ResponseWriter, request *http.Request, offlineMessage string, eol time.Time, name string) bool {
	if offlineMessage != "" {
		markGatewayRejected(writer, request)
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.Header().Set("Retry-After", "10800")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(writer).Encode(ProblemDetails{
			Type:   "about:blank",
			Title:  "Service Unavailable",
			Status: http.StatusServiceUnavailable,
			Detail: offlineMessage,
		})
		return true
	}
	if isModelExpired(eol) {
		var detail string
		if name != "" {
			detail = fmt.Sprintf("The model '%s' has reached its end of life on %s and is no longer available.", name, eol.Format(time.RFC3339))
		} else {
			detail = fmt.Sprintf("This endpoint has reached its end of life on %s and is no longer available.", eol.Format(time.RFC3339))
		}
		markGatewayRejected(writer, request)
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(writer).Encode(ProblemDetails{
			Type:   "about:blank",
			Title:  "Gone",
			Status: http.StatusGone,
			Detail: detail,
		})
		return true
	}
	return false
}

// clientClosedRequest is true only when the inbound request context is canceled
// (the authoritative disconnect signal); a transport error wrapping
// context.Canceled with a live context stays a genuine upstream fault (502).
func clientClosedRequest(request *http.Request) bool {
	return request != nil && errors.Is(request.Context().Err(), context.Canceled)
}

// addGatewayProxyOutcome skips shadow replays: they share the primary request's
// metric labeler and span, so their errors would mislabel the primary response.
func addGatewayProxyOutcome(request *http.Request, outcome middleware.GatewayProxyOutcome) {
	if request == nil || isShadowRequest(request) {
		return
	}
	middleware.RecordGatewayProxyOutcome(request.Context(), outcome)
}

// markGatewayRejected labels a response the gateway writes itself with no
// dependency involved. Call it before the status is written.
func markGatewayRejected(writer http.ResponseWriter, request *http.Request) {
	addGatewayProxyOutcome(request, middleware.GatewayProxyOutcomeRejected)
	writer.Header().Set(middleware.ErrorSourceHeader, string(middleware.GatewayProxyOutcomeRejected))
}

// markUpstreamResponse drops any error source header the upstream sent so a
// dependency cannot spoof it, then labels non-2xx upstream responses. request
// is the inbound request, which carries the server metric labeler and span.
func markUpstreamResponse(request *http.Request, resp *http.Response) {
	resp.Header.Del(middleware.ErrorSourceHeader)
	if resp.StatusCode < http.StatusMultipleChoices {
		return
	}
	addGatewayProxyOutcome(request, middleware.GatewayProxyOutcomeUpstreamStatus)
	resp.Header.Set(middleware.ErrorSourceHeader, string(middleware.GatewayProxyOutcomeUpstreamStatus))
}

// requestProxy returns a per-request copy of base that labels upstream
// responses against the inbound request and records the first proxy error.
func requestProxy(base *httputil.ReverseProxy, request *http.Request, proxyErr *error) *httputil.ReverseProxy {
	rp := *base
	rp.ModifyResponse = func(resp *http.Response) error {
		markUpstreamResponse(request, resp)
		return modifyTooManyRequestsResponse(resp)
	}
	rp.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		if proxyErr != nil {
			*proxyErr = err
		}
		writeProxyError(writer, request, err)
	}
	return &rp
}

// writeProxyError maps a canceled inbound request to 499; all other ReverseProxy
// ErrorHandler failures remain 502.
func writeProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	if clientClosedRequest(request) {
		addGatewayProxyOutcome(request, middleware.GatewayProxyOutcomeClientCanceled)
		writer.Header().Set(middleware.ErrorSourceHeader, string(middleware.GatewayProxyOutcomeClientCanceled))
		zap.L().Debug("proxy request canceled",
			zap.String(string(middleware.GatewayProxyOutcomeMetricAttribute), string(middleware.GatewayProxyOutcomeClientCanceled)),
			zap.Error(err),
		)
		if request.URL.Path == "/v1/messages" {
			writeAnthropicError(writer, statusClientClosedRequest, "client closed the request")
			return
		}
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(statusClientClosedRequest)
		_ = json.NewEncoder(writer).Encode(ProblemDetails{
			Type:   "about:blank",
			Title:  "Client Closed Request",
			Status: statusClientClosedRequest,
			Detail: "Client closed the request before a response was produced.",
		})
		return
	}
	writeBadGatewayProblem(writer, request, err)
}

func writeBadGatewayProblem(writer http.ResponseWriter, request *http.Request, err error) {
	addGatewayProxyOutcome(request, middleware.GatewayProxyOutcomeProxyError)
	writer.Header().Set(middleware.ErrorSourceHeader, string(middleware.GatewayProxyOutcomeProxyError))
	zap.L().Warn("proxy request failed",
		zap.String(string(middleware.GatewayProxyOutcomeMetricAttribute), string(middleware.GatewayProxyOutcomeProxyError)),
		zap.Error(err),
	)
	if request.URL.Path == "/v1/messages" {
		writeAnthropicError(writer, http.StatusBadGateway, "upstream request failed")
		return
	}
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(writer).Encode(ProblemDetails{
		Type:   "about:blank",
		Title:  "Bad Gateway",
		Status: http.StatusBadGateway,
		Detail: "Upstream request failed.",
	})
}

// Messages gateway errors use the native envelope. Upstream responses retain
// their original body, status, and headers.
func writeAnthropicError(writer http.ResponseWriter, status int, message string) {
	kind := "api_error"
	switch status {
	case 400, 405, 422:
		kind = "invalid_request_error"
	case 401:
		kind = "authentication_error"
	case 403:
		kind = "permission_error"
	case 404, 410:
		kind = "not_found_error"
	case 413:
		kind = "request_too_large"
	case 429:
		kind = "rate_limit_error"
	case 529:
		kind = "overloaded_error"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
}

type VanityDirector struct {
	rp            *httputil.ReverseProxy
	nvcfApiHost   string
	nvcfApiScheme string
}

type execTarget struct {
	path              *string
	functionID        string
	functionVersionID string
	sessionTimeout    config.SessionTimeoutSeconds
	customHeaders     config.CustomHeaders
}

type VanityExecRequest struct {
	FunctionID        string
	FunctionVersionID string
	PathOverride      *string
	UsePexec          bool
	SessionTimeout    config.SessionTimeoutSeconds
	CustomHeaders     config.CustomHeaders
	EOL               time.Time
	OfflineMessage    string
}

// ProblemDetails represents an RFC 7807 Problem Details response
type ProblemDetails struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func newGatewayReverseProxy(transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			// already directed, needed to be able to error
		},
		FlushInterval:  -1,
		BufferPool:     pool.ByteSlice,
		Transport:      transport,
		ModifyResponse: modifyTooManyRequestsResponse,
		ErrorHandler:   writeProxyError,
	}
}

func NewVanityDirector(nvcfApiHost string, transport http.RoundTripper) (*VanityDirector, error) {
	rp := newGatewayReverseProxy(transport)
	nvcfApiUrl, err := url.Parse(nvcfApiHost)
	if err != nil || nvcfApiUrl.Scheme == "" || nvcfApiUrl.Host == "" {
		return nil, fmt.Errorf("invalid NVCF API host: %s", nvcfApiHost)
	}
	return &VanityDirector{rp: rp, nvcfApiHost: nvcfApiUrl.Host, nvcfApiScheme: nvcfApiUrl.Scheme}, nil
}

func buildExecTarget(functionID string, functionVersionID string, pathOverride *string, usePexec bool, sessionTimeout config.SessionTimeoutSeconds, customHeaders config.CustomHeaders) execTarget {
	target := execTarget{
		path:              pathOverride,
		functionID:        functionID,
		functionVersionID: functionVersionID,
		sessionTimeout:    sessionTimeout,
		customHeaders:     customHeaders,
	}
	if !usePexec {
		return target
	}

	pexecPath := "/v2/nvcf/pexec/functions/" + functionID
	if functionVersionID != "" {
		pexecPath += "/versions/" + functionVersionID
	}

	target.path = &pexecPath
	target.functionID = ""
	target.functionVersionID = ""
	return target
}

func applyExecTarget(request *http.Request, apiScheme string, apiHost string, target execTarget) {
	request.URL.Host = apiHost
	request.URL.Scheme = apiScheme
	if target.path != nil {
		request.URL.Path = *target.path
	}
	if target.functionID != "" {
		request.Header.Set("function-id", target.functionID)
	} else {
		request.Header.Del("function-id")
	}
	if target.functionVersionID != "" {
		request.Header.Set("function-version-id", target.functionVersionID)
	} else {
		request.Header.Del("function-version-id")
	}
	request.Host = ""
	setPollingHeaderIfNotPresent(request, target.sessionTimeout)
	applyCustomHeaders(request, target.customHeaders)
}

func applyCustomHeaders(request *http.Request, headers config.CustomHeaders) {
	for name, value := range headers {
		request.Header.Set(name, value)
	}
}

func (d *VanityDirector) ServeExec(target VanityExecRequest, writer http.ResponseWriter, request *http.Request) error {
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(
		traceAttrFunctionID.String(target.FunctionID),
		traceAttrFunctionVersionID.String(target.FunctionVersionID),
	)

	if writeFunctionStatusError(writer, request, target.OfflineMessage, target.EOL, "") {
		return nil
	}

	applyExecTarget(request, d.nvcfApiScheme, d.nvcfApiHost, buildExecTarget(target.FunctionID, target.FunctionVersionID, target.PathOverride, target.UsePexec, target.SessionTimeout, target.CustomHeaders))

	// Set Deprecation header if EOL is set but not yet expired
	if !target.EOL.IsZero() {
		writer.Header().Set("Deprecation", target.EOL.Format(time.RFC3339))
	}

	var proxyErr error
	requestProxy(d.rp, request, &proxyErr).ServeHTTP(writer, request)
	return proxyErr
}

func (d *VanityDirector) ServePolling(writer http.ResponseWriter, request *http.Request) {
	requestId := path.Base(request.URL.Path)
	nvcfUrl, _ := url.Parse(d.nvcfApiScheme + "://" + d.nvcfApiHost + "/v2/nvcf/pexec/status/" + requestId)
	if nvcfUrl == nil {
		request.Body.Close()
		markGatewayRejected(writer, request)
		http.NotFound(writer, request)
		return
	}
	request.URL = nvcfUrl
	request.Host = ""
	setPollingHeaderIfNotPresent(request, 0)

	requestProxy(d.rp, request, nil).ServeHTTP(writer, request)
}

func setPollingHeaderIfNotPresent(request *http.Request, sessionTimeout config.SessionTimeoutSeconds) {
	if request.Header.Get(NVCFPollSeconds) == "" {
		if sessionTimeout <= 0 {
			sessionTimeout = defaultNVCFPollSeconds
		}
		request.Header.Set(NVCFPollSeconds, strconv.Itoa(int(sessionTimeout)))
	}
}

func modifyTooManyRequestsResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	message, ok := resp.Request.Context().Value(tooManyRequestsKey).(string)
	if !ok || message == "" {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}

	body = appendTooManyRequestsMessage(body, message)

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func appendTooManyRequestsMessage(body []byte, message string) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}

	if appendOpenAIErrorMessage(parsed, message) ||
		appendProblemDetailsMessage(parsed, message) ||
		appendPlainMessage(parsed, message) {
		if b, err := json.Marshal(parsed); err == nil {
			return b
		}
	}
	return body
}

func appendOpenAIErrorMessage(parsed map[string]any, message string) bool {
	errorObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return false
	}
	msg, ok := errorObj["message"].(string)
	if !ok {
		return false
	}
	errorObj["message"] = msg + " " + message
	return true
}

// appendPlainMessage handles a bare {"message": "..."} body, which is what echo
// renders for the LLM Gateway's errors. Kept last so the two specific shapes win.
func appendPlainMessage(parsed map[string]any, message string) bool {
	msg, ok := parsed["message"].(string)
	if !ok {
		return false
	}
	parsed["message"] = msg + " " + message
	return true
}

func appendProblemDetailsMessage(parsed map[string]any, message string) bool {
	if _, hasProblemTitle := parsed["title"]; !hasProblemTitle {
		return false
	}
	detail, _ := parsed["detail"].(string)
	if detail != "" {
		parsed["detail"] = detail + " " + message
	} else {
		parsed["detail"] = message
	}
	return true
}
