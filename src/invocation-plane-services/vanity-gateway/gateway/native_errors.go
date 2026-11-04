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

// Error kinds a caller passes to writeNativeError. Only a 400 needs one: the
// status alone cannot tell a JSON syntax error from any other bad request.
// nativeErrorCode derives every other code from the status.
const (
	errKindInvalidJSON    = "invalid_json"
	errKindInvalidRequest = "invalid_request"
)

const nativeProtocolKey contextKey = tooManyRequestsKey + 1

// withNativeProtocol records the protocol of the route the client called.
// applyExecTarget rewrites URL.Path when usePexec is set, so the path alone
// cannot name the protocol once a request is being proxied.
func withNativeProtocol(request *http.Request, protocol nativeProtocol) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), nativeProtocolKey, protocol))
}

// requestProtocol returns the protocol stored at route entry. Generic vanity
// path entries never store one, so they keep the problem+json shape even when
// their path matches a native route.
func requestProtocol(request *http.Request) nativeProtocol {
	protocol, _ := request.Context().Value(nativeProtocolKey).(nativeProtocol)
	return protocol
}

// proxyErrorProtocol is the protocol for errors written by the reverse proxy
// error handler. Vanity path entries at /v1/messages used the Anthropic shape
// for these errors before the protocol was stored, and keep it.
func proxyErrorProtocol(request *http.Request) nativeProtocol {
	if protocol := requestProtocol(request); protocol != nativeProtocolNone {
		return protocol
	}
	if request.URL.Path == messagesPath {
		return nativeProtocolMessages
	}
	return nativeProtocolNone
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
	return writeProtocolError(writer, requestProtocol(request), status, kind, message)
}

func writeProtocolError(writer http.ResponseWriter, protocol nativeProtocol, status int, kind, message string) bool {
	switch protocol {
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

// nativeErrorCode is the error code for a gateway-written error on the
// decision routes. The LLM Gateway uses the same table, so a client sees the
// same code whichever service rejected the request.
func nativeErrorCode(protocol nativeProtocol, status int, kind string) string {
	switch status {
	case http.StatusBadRequest:
		if kind == errKindInvalidJSON {
			return errKindInvalidJSON
		}
		return errKindInvalidRequest
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		if protocol == nativeProtocolDecisions {
			return "model_not_found"
		}
		return "not_found"
	case http.StatusGone:
		if protocol == nativeProtocolDecisions {
			return "model_expired"
		}
		return "not_found"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case statusClientClosedRequest:
		return "request_cancelled"
	case http.StatusBadGateway, http.StatusServiceUnavailable, statusOverloaded:
		return "provider_unavailable"
	case http.StatusGatewayTimeout:
		return "provider_timeout"
	}
	if status >= http.StatusInternalServerError {
		return "internal_error"
	}
	return errKindInvalidRequest
}

// statusOverloaded is the non-standard 529 some model servers return.
const statusOverloaded = 529

// openAIErrorType is the OpenAI error type for a status on /v1/decisions.
func openAIErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case statusOverloaded:
		return "overloaded_error"
	}
	if status >= http.StatusInternalServerError {
		return "server_error"
	}
	return "invalid_request_error"
}

func writeDecisionsError(writer http.ResponseWriter, status int, kind, message string) {
	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(OpenAIError{Error: CustomOpenAIError{
		Message: message,
		Type:    openAIErrorType(status),
		Code:    nativeErrorCode(nativeProtocolDecisions, status, kind),
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
		Error:     systemOneErrorDetail{Code: nativeErrorCode(nativeProtocolSystemOne, status, kind), Message: message, RequestID: requestID},
		RequestID: requestID,
	})
}
