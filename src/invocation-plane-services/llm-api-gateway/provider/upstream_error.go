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

package provider

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	echo "github.com/labstack/echo/v4"
)

// MaxUpstreamErrorBodyBytes bounds how much of a non-2xx upstream body the
// gateway reads to render an error response. Validation errors can echo the
// request input, so the bound is well above a typical error body.
const MaxUpstreamErrorBodyBytes = 1 << 20

// maxUpstreamErrorMessageBytes bounds the upstream text copied into a
// gateway-generated error message.
const maxUpstreamErrorMessageBytes = 32 * 1024

// truncateMessage cuts message to maxUpstreamErrorMessageBytes on a UTF-8
// boundary.
func truncateMessage(message string) string {
	if len(message) <= maxUpstreamErrorMessageBytes {
		return message
	}
	cut := maxUpstreamErrorMessageBytes
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
}

// ReadUpstreamErrorBody reads a non-2xx upstream body for error rendering and
// decodes gzip. It returns nil when the body is larger than
// MaxUpstreamErrorBodyBytes, uses another encoding, or fails to read, so a
// partial body is never parsed or relayed.
func ReadUpstreamErrorBody(body io.Reader, contentEncoding string) []byte {
	if body == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
	case "gzip", "x-gzip":
		decoded, err := gzip.NewReader(body)
		if err != nil {
			return nil
		}
		defer decoded.Close()
		body = decoded
	default:
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxUpstreamErrorBodyBytes+1))
	if err != nil || len(data) > MaxUpstreamErrorBodyBytes {
		return nil
	}
	return data
}

// UpstreamError is a non-2xx response from the request router or the model
// server. The API layer renders it in the endpoint's error envelope, or relays
// Body unchanged when the model server already used that envelope.
type UpstreamError struct {
	Status int
	// RouterCode is the X-Stargate-Error-Code value. It is set only when the
	// request router rejected the request itself.
	RouterCode string
	Body       []byte
}

// NewUpstreamHTTPError wraps an upstream error response for the gateway error
// handler. The public message is decoded from the body; the router code stays
// in the internal error for the request log.
func NewUpstreamHTTPError(status int, header http.Header, body []byte) *echo.HTTPError {
	upstream := &UpstreamError{
		Status:     status,
		RouterCode: strings.Join(header.Values(headerStargateErrorCode), ","),
		Body:       body,
	}
	return echo.NewHTTPError(status, upstream.Message()).SetInternal(upstream)
}

func (e *UpstreamError) Error() string {
	if e.RouterCode != "" {
		return "stargate error code " + e.RouterCode
	}
	return fmt.Sprintf("upstream status %d", e.Status)
}

// Message returns the human-readable reason from the upstream body, capped at
// maxUpstreamErrorMessageBytes, falling back to the HTTP status text. JSON
// bodies in an unknown shape are never returned verbatim.
func (e *UpstreamError) Message() string {
	if message := upstreamErrorMessage(e.Body); message != "" {
		return truncateMessage(message)
	}
	trimmed := bytes.TrimSpace(e.Body)
	if len(trimmed) > 0 && !json.Valid(trimmed) && utf8.Valid(trimmed) {
		return truncateMessage(string(trimmed))
	}
	if text := http.StatusText(e.Status); text != "" {
		return text
	}
	if e.Status == statusOverloaded {
		return "Overloaded"
	}
	return "upstream request failed"
}

// upstreamErrorMessage returns the message from the error body shapes the
// router, pylon, and model servers produce:
//
//   - OpenAI and Anthropic: {"error":{"message":"..."}}
//   - Stargate rejections: {"error":"no eligible candidates","code":"..."}
//   - older vLLM: {"object":"error","message":"..."}
//   - pylon problem details: {"title":"...","detail":"..."}
//   - FastAPI validation: {"detail":[{"loc":[...],"msg":"..."}]}
//
// It returns "" for any other body.
func upstreamErrorMessage(body []byte) string {
	var fields struct {
		Error   json.RawMessage `json:"error"`
		Detail  json.RawMessage `json:"detail"`
		Title   json.RawMessage `json:"title"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return ""
	}

	var nested struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(fields.Error, &nested); err == nil && nested.Message != "" {
		return nested.Message
	}
	for _, field := range []json.RawMessage{fields.Error, fields.Message, fields.Detail, fields.Title} {
		var message string
		if err := json.Unmarshal(field, &message); err == nil && message != "" {
			return message
		}
	}
	return validationErrorMessage(fields.Detail)
}

// validationErrorMessage joins the FastAPI validation error list into one
// message, for example "body.messages: Field required".
func validationErrorMessage(detail json.RawMessage) string {
	var items []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(detail, &items); err != nil {
		return ""
	}
	messages := make([]string, 0, len(items))
	for _, item := range items {
		if item.Msg == "" {
			continue
		}
		location := make([]string, 0, len(item.Loc))
		for _, part := range item.Loc {
			location = append(location, fmt.Sprint(part))
		}
		if len(location) == 0 {
			messages = append(messages, item.Msg)
			continue
		}
		messages = append(messages, strings.Join(location, ".")+": "+item.Msg)
	}
	return strings.Join(messages, "; ")
}
