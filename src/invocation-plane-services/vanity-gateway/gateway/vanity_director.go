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
	"ai-api-gateway-service/pool"
	"bytes"
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

// writeFunctionStatusError writes a 503 or 410 response if the function is offline or expired.
// name is used in the EOL detail message; pass empty string for vanity/path-based endpoints.
// Returns true if an error response was written and the caller should return early.
func writeFunctionStatusError(writer http.ResponseWriter, offlineMessage string, eol time.Time, name string) bool {
	if offlineMessage != "" {
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

func writeBadGatewayProblem(writer http.ResponseWriter, _ *http.Request, err error) {
	zap.L().Warn("proxy request failed", zap.Error(err))
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(writer).Encode(ProblemDetails{
		Type:   "about:blank",
		Title:  "Bad Gateway",
		Status: http.StatusBadGateway,
		Detail: "Upstream request failed.",
	})
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
}

type VanityExecRequest struct {
	FunctionID        string
	FunctionVersionID string
	PathOverride      *string
	UsePexec          bool
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

func NewVanityDirector(nvcfApiHost string, transport http.RoundTripper) (*VanityDirector, error) {
	rp := &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			// already directed, needed to be able to error
		},
		FlushInterval:  -1,
		BufferPool:     pool.ByteSlice,
		Transport:      transport,
		ModifyResponse: modifyTooManyRequestsResponse,
		ErrorHandler:   writeBadGatewayProblem,
	}
	nvcfApiUrl, err := url.Parse(nvcfApiHost)
	if err != nil || nvcfApiUrl.Scheme == "" || nvcfApiUrl.Host == "" {
		return nil, fmt.Errorf("invalid NVCF API host: %s", nvcfApiHost)
	}
	return &VanityDirector{rp: rp, nvcfApiHost: nvcfApiUrl.Host, nvcfApiScheme: nvcfApiUrl.Scheme}, nil
}

func buildExecTarget(functionID string, functionVersionID string, pathOverride *string, usePexec bool) execTarget {
	target := execTarget{
		path:              pathOverride,
		functionID:        functionID,
		functionVersionID: functionVersionID,
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
	setPollingHeaderIfNotPresent(request)
}

func (d *VanityDirector) ServeExec(target VanityExecRequest, writer http.ResponseWriter, request *http.Request) error {
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(
		traceAttrFunctionID.String(target.FunctionID),
		traceAttrFunctionVersionID.String(target.FunctionVersionID),
	)

	if writeFunctionStatusError(writer, target.OfflineMessage, target.EOL, "") {
		return nil
	}

	applyExecTarget(request, d.nvcfApiScheme, d.nvcfApiHost, buildExecTarget(target.FunctionID, target.FunctionVersionID, target.PathOverride, target.UsePexec))

	// Set Deprecation header if EOL is set but not yet expired
	if !target.EOL.IsZero() {
		writer.Header().Set("Deprecation", target.EOL.Format(time.RFC3339))
	}

	var proxyErr error
	rp := *d.rp
	rp.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		proxyErr = err
		writeBadGatewayProblem(writer, request, err)
	}
	rp.ServeHTTP(writer, request)
	return proxyErr
}

func (d *VanityDirector) ServePolling(writer http.ResponseWriter, request *http.Request) {
	requestId := path.Base(request.URL.Path)
	nvcfUrl, _ := url.Parse(d.nvcfApiScheme + "://" + d.nvcfApiHost + "/v2/nvcf/pexec/status/" + requestId)
	if nvcfUrl == nil {
		request.Body.Close()
		http.NotFound(writer, request)
		return
	}
	request.URL = nvcfUrl
	request.Host = ""
	setPollingHeaderIfNotPresent(request)

	d.rp.ServeHTTP(writer, request)
}

func setPollingHeaderIfNotPresent(request *http.Request) {
	if request.Header.Get(NVCFPollSeconds) == "" {
		request.Header.Set(NVCFPollSeconds, "300") // 5 minutes
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

	if appendOpenAIErrorMessage(parsed, message) || appendProblemDetailsMessage(parsed, message) {
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
