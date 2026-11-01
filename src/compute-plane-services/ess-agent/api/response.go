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
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
)

const (
	HttpHeaderNameEssRequestId = "X-ESS-Request-Id"
	undetermined               = "undetermined"
)

// Response is a raw response that wraps an HTTP response.
type Response struct {
	*http.Response
}

// DecodeJSON will decode the response body to a JSON structure. This
// will consume the response body, but will not close it. Close must
// still be called.
func (r *Response) DecodeJSON(out interface{}) error {
	return jsonutil.DecodeJSONFromReader(r.Body, out)
}

// Error returns an error response if there is one. If there is an error,
// this will fully consume the response body, but will not close it. The
// body must still be closed manually.
func (r *Response) Error() error {
	// 200 to 399 are okay status codes. 429 is the code for health status of
	// standby nodes, otherwise, 429 is treated as quota limit reached.
	if (r.StatusCode >= 200 && r.StatusCode < 400) || (r.StatusCode == 429 && r.Request.URL.Path == "/v1/sys/health") {
		return nil
	}

	// We have an error. Let's copy the body into our own buffer first,
	// so that if we can't decode JSON, we can at least copy it raw.
	bodyBuf := &bytes.Buffer{}
	if _, err := io.Copy(bodyBuf, r.Body); err != nil {
		return err
	}

	r.Body.Close()
	r.Body = ioutil.NopCloser(bodyBuf)
	ns := r.Header.Get(consts.NamespaceHeaderName)

	// Build up the error object
	respErr := &ResponseError{
		HTTPMethod:    r.Request.Method,
		URL:           r.Request.URL.String(),
		StatusCode:    r.StatusCode,
		NamespacePath: ns,
		// added for ess
		Headers: r.Header,
	}

	// Decode the error response if we can. Note that we wrap the bodyBuf
	// in a bytes.Reader here so that the JSON decoder doesn't move the
	// read pointer for the original buffer.
	var resp ErrorResponse
	if err := jsonutil.DecodeJSON(bodyBuf.Bytes(), &resp); err != nil {
		// Store the fact that we couldn't decode the errors
		respErr.RawError = true
		respErr.Errors = []string{bodyBuf.String()}
	} else {
		// Store the decoded errors
		respErr.Errors = resp.Errors
		// added for ess
		respErr.Detail = resp.Detail
		respErr.Title = resp.Title
	}

	return respErr
}

// ErrorResponse is the raw structure of errors when they're returned by the
// HTTP API.
type ErrorResponse struct {
	Errors []string
	Detail string
	Title  string
}

// ResponseError is the error returned when Vault responds with an error or
// non-success HTTP status code. If a request to Vault fails because of a
// network error a different error message will be returned. ResponseError gives
// access to the underlying errors and status code.
type ResponseError struct {
	// HTTPMethod is the HTTP method for the request (PUT, GET, etc).
	HTTPMethod string

	// URL is the URL of the request.
	URL string

	// StatusCode is the HTTP status code.
	StatusCode int

	// RawError marks that the underlying error messages returned by Vault were
	// not parsable. The Errors slice will contain the raw response body as the
	// first and only error string if this value is set to true.
	RawError bool

	// Errors are the underlying errors returned by Vault.
	Errors []string

	// Namespace path to be reported to the client if it is set to anything other
	// than root
	NamespacePath string

	// Detail is the ESS implemented error field named detail as per the spec https://datatracker.ietf.org/doc/html/rfc7807
	Detail string

	// Title is the ESS implemented error field named detail as per the spec https://datatracker.ietf.org/doc/html/rfc7807
	Title string

	// Headers associated with response
	Headers http.Header
}

// Error returns a human-readable error string for the response error.
func (r *ResponseError) Error() string {
	var ns string
	if r.NamespacePath != "" {
		ns = "Namespace: " + strings.TrimSuffix(r.NamespacePath, "/")
	}

	// added for ess
	// get request id from response header
	requestId := "n/a"
	if r.Headers.Get(HttpHeaderNameEssRequestId) != "" {
		requestId = r.Headers.Get(HttpHeaderNameEssRequestId)
	}

	if r.Title == "" {
		r.Title = undetermined
	}

	if r.Detail == "" {
		if r.RawError && len(r.Errors) == 1 {
			r.Detail = r.Errors[0]
		} else {
			r.Detail = strings.Join(r.Errors, ",")
		}
		if r.Detail == "" {
			r.Detail = undetermined
		}
	}

	errBodyInitMsg := fmt.Sprintf(
		"Error making API request | %s | URL: %s %s | Code: %d | Request Id: %s | Title: %s | Detail: %s",
		ns, r.HTTPMethod, r.URL, r.StatusCode, requestId, r.Title, r.Detail)

	var errBody bytes.Buffer
	errBody.WriteString(errBodyInitMsg)
	return errBody.String()
}
