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

package logging

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"
)

const redactedHeaderValue = "REDACTED"

func sanitizeHeaders(headers http.Header) http.Header {
	sanitized := headers.Clone()
	for name := range sanitized {
		if isSensitiveHeader(name) {
			sanitized[name] = []string{redactedHeaderValue}
		}
	}
	return sanitized
}

func isSensitiveHeader(name string) bool {
	lowerName := strings.ToLower(name)
	switch lowerName {
	case "authorization",
		"proxy-authorization",
		"cookie",
		"set-cookie",
		"x-api-key",
		"x-auth-token",
		"x-csrf-token",
		"x-xsrf-token":
		return true
	}

	return strings.Contains(lowerName, "api-key") ||
		strings.Contains(lowerName, "apikey") ||
		strings.Contains(lowerName, "authorization") ||
		strings.Contains(lowerName, "secret") ||
		strings.Contains(lowerName, "session") ||
		strings.Contains(lowerName, "token")
}

func requestURLForLogging(requestURL *url.URL) string {
	if requestURL == nil {
		return ""
	}

	sanitized := *requestURL
	sanitized.RawQuery = ""
	sanitized.ForceQuery = false
	sanitized.Fragment = ""
	return sanitized.String()
}

func LogHTTPRequest(traceCtx context.Context, logger *TraceLogger, req *http.Request) {
	logger.InfoContext(traceCtx,
		"http request",
		zap.String("method", req.Method),
		zap.String("url", requestURLForLogging(req.URL)),
		zap.String("host", req.Host),
		zap.String("remote_addr", req.RemoteAddr),
		zap.String("user_agent", req.UserAgent()),
		zap.String("protocol", req.Proto),
		zap.Any("headers", sanitizeHeaders(req.Header)),
	)
}

func LogHTTPResponse(traceCtx context.Context, logger *TraceLogger, statusCode int, headers http.Header) {
	logger.InfoContext(traceCtx,
		"http response",
		zap.Int("status_code", statusCode),
		zap.Any("headers", sanitizeHeaders(headers)),
	)
}
