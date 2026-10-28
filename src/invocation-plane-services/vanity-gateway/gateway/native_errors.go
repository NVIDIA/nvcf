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
	"ai-api-gateway-service/middleware"
	"context"
	"net/http"

	"github.com/goccy/go-json"
)

// nativeProtocol names the API whose error shape a response must use.
type nativeProtocol string

const (
	nativeProtocolNone      nativeProtocol = ""
	nativeProtocolMessages  nativeProtocol = "messages"
	nativeProtocolDecisions nativeProtocol = "decisions"
	nativeProtocolSystemOne nativeProtocol = "systemone"
)

const (
	decisionsPath = "/v1/decisions"
	systemOnePath = "/v1/systemone"
	messagesPath  = "/v1/messages"
)

// System One error codes. The gateway also uses them as the code on the
// OpenAI-shaped decisions errors, except where openAIErrorCode says otherwise.
const (
	errKindInvalidJSON         = "invalid_json"
	errKindInvalidRequest      = "invalid_request"
	errKindNotFound            = "not_found"
	errKindRequestTooLarge     = "request_too_large"
	errKindInternal            = "internal_error"
	errKindProviderUnavailable = "provider_unavailable"
)

const nativeProtocolKey contextKey = tooManyRequestsKey + 1

func protocolForPath(urlPath string) nativeProtocol {
	switch urlPath {
	case messagesPath:
		return nativeProtocolMessages
	case decisionsPath:
		return nativeProtocolDecisions
	case systemOnePath:
		return nativeProtocolSystemOne
	}
	return nativeProtocolNone
}

// withNativeProtocol records the protocol of the route the client called.
// applyExecTarget rewrites URL.Path when usePexec is set, so the path alone
// cannot name the protocol once a request is being proxied.
func withNativeProtocol(request *http.Request, protocol nativeProtocol) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), nativeProtocolKey, protocol))
}

// requestProtocol returns the protocol stored at route entry, falling back to
// the request path for requests that never went through a route entry.
func requestProtocol(request *http.Request) nativeProtocol {
	if protocol, ok := request.Context().Value(nativeProtocolKey).(nativeProtocol); ok {
		return protocol
	}
	return protocolForPath(request.URL.Path)
}

// endpointTypeForPath is the endpoint.type span value for a request path.
// fallback applies to the OpenAI routes and to the LLM Gateway hop.
func endpointTypeForPath(urlPath, fallback string) string {
	switch urlPath {
	case messagesPath:
		return traceAttrValueEndpointAnthropic
	case decisionsPath:
		return traceAttrValueEndpointDecisions
	case systemOnePath:
		return traceAttrValueEndpointSystemOne
	}
	return fallback
}

// writeNativeError writes a gateway-generated error in the shape of the route
// the client called and reports whether it did. It writes nothing for the
// OpenAI chat-style routes, which keep their own error handling.
func writeNativeError(writer http.ResponseWriter, request *http.Request, status int, kind, message string) bool {
	switch requestProtocol(request) {
	case nativeProtocolMessages:
		writeAnthropicError(writer, status, message)
	case nativeProtocolDecisions:
		writeDecisionsError(writer, status, kind, message)
	case nativeProtocolSystemOne:
		writeSystemOneError(writer, status, kind, message)
	default:
		return false
	}
	return true
}

func openAIErrorCode(status int, kind string) string {
	switch status {
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusGone:
		return "model_expired"
	}
	return kind
}

func writeDecisionsError(writer http.ResponseWriter, status int, kind, message string) {
	errorType := "invalid_request_error"
	if status >= http.StatusInternalServerError {
		errorType = "server_error"
	}
	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(OpenAIError{Error: CustomOpenAIError{
		Message: message,
		Type:    errorType,
		Code:    openAIErrorCode(status, kind),
	}})
}

type systemOneErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type systemOneError struct {
	Error     systemOneErrorDetail `json:"error"`
	RequestID string               `json:"request_id"`
}

// writeSystemOneError writes the System One error body. Gateway-written errors
// carry no X-System-One-* headers: those mark errors the model server relayed.
func writeSystemOneError(writer http.ResponseWriter, status int, kind, message string) {
	requestID := writer.Header().Get(middleware.RequestIDHeader)
	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(systemOneError{
		Error:     systemOneErrorDetail{Code: kind, Message: message, RequestID: requestID},
		RequestID: requestID,
	})
}
