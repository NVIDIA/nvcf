/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	statusOverloaded  = 529
	overloadErrorCode = "overloaded_error"
)

type openAIErrorResponse struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

type anthropicErrorResponse struct {
	Type      string         `json:"type"`
	Error     anthropicError `json:"error"`
	RequestID string         `json:"request_id"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// newHTTPErrorHandler renders every error the gateway or the request router
// produces in the endpoint's native envelope: Anthropic on /v1/messages and
// OpenAI everywhere else. Model server errors already in that envelope are
// relayed so their type and code reach the client; Anthropic ones get the
// NVCF request_id.
func newHTTPErrorHandler(exposeRouterErrorCodes bool) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		status, message := http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)
		var he *echo.HTTPError
		if errors.As(err, &he) {
			status = he.Code
			switch public := he.Message.(type) {
			case string:
				message = public
			case error:
				message = public.Error()
			}
		}
		if c.Request().Method == http.MethodHead {
			_ = c.NoContent(status)
			return
		}

		// The handler always writes an uncompressed body; drop any encoding
		// copied from the upstream response.
		c.Response().Header().Del(echo.HeaderContentEncoding)
		anthropic := isMessagesRequest(c.Request())
		var upstream *provider.UpstreamError
		if errors.As(err, &upstream) && upstream.RouterCode == "" {
			native := isOpenAIErrorBody
			if anthropic {
				native = isAnthropicErrorBody
			}
			if native(upstream.Body) {
				body := upstream.Body
				if anthropic {
					body = withAnthropicRequestID(body, errorRequestID(c))
				}
				c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				_ = c.JSONBlob(status, body)
				return
			}
		}

		c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if anthropic {
			_ = c.JSON(status, anthropicErrorResponse{
				Type:      "error",
				Error:     anthropicError{Type: anthropicErrorType(status), Message: message},
				RequestID: errorRequestID(c),
			})
			return
		}

		var code *string
		switch {
		case status == statusOverloaded:
			overloaded := overloadErrorCode
			code = &overloaded
		case exposeRouterErrorCodes && upstream != nil && upstream.RouterCode != "":
			code = &upstream.RouterCode
		}
		_ = c.JSON(status, openAIErrorResponse{Error: openAIError{
			Message: message,
			Type:    openAIErrorType(status),
			Code:    code,
		}})
	}
}

func openAIErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == statusOverloaded:
		return "overloaded_error"
	case status >= http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case statusOverloaded:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// isOpenAIErrorBody reports whether body is {"error":{"message":"..."}}.
func isOpenAIErrorBody(body []byte) bool {
	var envelope struct {
		Error *struct {
			Message *string `json:"message"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &envelope) == nil &&
		envelope.Error != nil && envelope.Error.Message != nil
}

// anthropicErrorTypePattern matches the lowercase snake_case error types the
// Anthropic API uses, such as invalid_request_error. A pattern rather than a
// fixed list keeps new Anthropic types working while still rejecting model
// server names such as BadRequestError.
var anthropicErrorTypePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// isAnthropicErrorBody reports whether body is
// {"type":"error","error":{"type":"...","message":"..."}} with a lowercase
// snake_case error type. Model servers that copy the shape with their own type
// names, such as "BadRequestError", get the gateway envelope instead.
func isAnthropicErrorBody(body []byte) bool {
	var envelope struct {
		Type  string `json:"type"`
		Error *struct {
			Type    *string `json:"type"`
			Message *string `json:"message"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Type == "error" &&
		envelope.Error != nil && envelope.Error.Type != nil && envelope.Error.Message != nil &&
		anthropicErrorTypePattern.MatchString(*envelope.Error.Type)
}

// withAnthropicRequestID sets the top-level request_id of a relayed Anthropic
// error body to the NVCF request ID, replacing any backend ID, so the error
// can be traced in gateway logs. Other fields are kept.
func withAnthropicRequestID(body []byte, requestID string) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body
	}
	id, err := json.Marshal(requestID)
	if err != nil {
		return body
	}
	fields["request_id"] = id
	out, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return out
}

// proxyUpstreamError returns the error for a non-2xx proxy response so the
// HTTP error handler renders it like any other gateway error. It returns nil
// for successful responses.
func proxyUpstreamError(resp *provider.ProxyResponse) error {
	if resp.StatusCode < http.StatusBadRequest {
		return nil
	}
	var body []byte
	if resp.Body != nil {
		body = provider.ReadUpstreamErrorBody(resp.Body, resp.Header.Get(echo.HeaderContentEncoding))
		_ = resp.Body.Close()
	}
	return provider.NewUpstreamHTTPError(resp.StatusCode, resp.Header, body)
}

// errorRequestID returns the NVCF request ID. A relayed backend response can
// overwrite the X-Request-Id response header, so the context value wins.
func errorRequestID(c echo.Context) string {
	if id, ok := c.Get(contextKeyRequestID).(string); ok && id != "" {
		return id
	}
	return c.Response().Header().Get(HeaderRequestID)
}
