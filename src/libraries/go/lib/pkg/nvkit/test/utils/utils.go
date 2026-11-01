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

package utils

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jarcoal/httpmock"
)

func MockAuthCall(t *testing.T, clientID string, clientSecret string, testAccessToken string) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		expectedAuthHeader := fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))
		authHeader := req.Header.Get("Authorization")
		if authHeader != expectedAuthHeader {
			t.Errorf("Unexpected authorization header, %v is found.", authHeader)
		}
		header := http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}}
		buf := bytes.NewBuffer([]byte(fmt.Sprintf("access_token=%s&token_type=bearer", testAccessToken)))
		resp := &http.Response{
			Status:        http.StatusText(http.StatusOK),
			StatusCode:    http.StatusOK,
			Header:        header,
			Body:          &readCloserWrapper{bufio.NewReader(buf), func() error { return nil }},
			ContentLength: 0,
		}

		return resp, nil
	}
}

// MockAuthCallWithValidation creates a mock responder that validates the OAuth2 client credentials
// request format according to RFC 6749 section 4.4.
// It validates:
// - Authorization header (Basic auth with client_id:client_secret)
// - Content-Type header (application/x-www-form-urlencoded)
// - Request body contains grant_type=client_credentials
// - Request body contains expected scopes (if provided)
func MockAuthCallWithValidation(t *testing.T, clientID, clientSecret, testAccessToken string, expectedScopes []string, expiresIn int) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		// Validate Authorization header
		expectedAuthHeader := fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))
		authHeader := req.Header.Get("Authorization")
		if authHeader != expectedAuthHeader {
			t.Errorf("Unexpected authorization header: got %v, want %v", authHeader, expectedAuthHeader)
		}

		// Validate Content-Type header
		contentType := req.Header.Get("Content-Type")
		if !strings.Contains(contentType, "application/x-www-form-urlencoded") {
			t.Errorf("Unexpected Content-Type: got %v, want application/x-www-form-urlencoded", contentType)
		}

		// Parse and validate request body
		if err := req.ParseForm(); err != nil {
			t.Errorf("Failed to parse form: %v", err)
		}

		// Validate grant_type=client_credentials
		grantType := req.FormValue("grant_type")
		if grantType != "client_credentials" {
			t.Errorf("Unexpected grant_type: got %v, want client_credentials", grantType)
		}

		// Validate scopes if expected
		if len(expectedScopes) > 0 {
			scopeValue := req.FormValue("scope")
			for _, expectedScope := range expectedScopes {
				if !strings.Contains(scopeValue, expectedScope) {
					t.Errorf("Expected scope %q not found in request scope %q", expectedScope, scopeValue)
				}
			}
		}

		// Build response matching authz provider token response format
		respBody := fmt.Sprintf(`{"access_token":"%s","token_type":"bearer","expires_in":%d`, testAccessToken, expiresIn)
		if len(expectedScopes) > 0 {
			respBody += fmt.Sprintf(`,"scope":"%s"`, strings.Join(expectedScopes, " "))
		}
		respBody += "}"

		header := http.Header{"Content-Type": []string{"application/json"}}
		buf := bytes.NewBuffer([]byte(respBody))
		resp := &http.Response{
			Status:        http.StatusText(http.StatusOK),
			StatusCode:    http.StatusOK,
			Header:        header,
			Body:          &readCloserWrapper{bufio.NewReader(buf), func() error { return nil }},
			ContentLength: int64(len(respBody)),
		}

		return resp, nil
	}
}

// MockAuthErrorResponse creates a mock responder that returns an OAuth2 error response.
// This is used to test error handling for authz provider error responses (400, 401, 429, 5xx).
func MockAuthErrorResponse(statusCode int, errorCode, errorDescription string) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		respBody := fmt.Sprintf(`{"error":"%s","error_description":"%s"}`, errorCode, errorDescription)
		header := http.Header{"Content-Type": []string{"application/json"}}
		buf := bytes.NewBuffer([]byte(respBody))
		resp := &http.Response{
			Status:        http.StatusText(statusCode),
			StatusCode:    statusCode,
			Header:        header,
			Body:          &readCloserWrapper{bufio.NewReader(buf), func() error { return nil }},
			ContentLength: int64(len(respBody)),
		}
		return resp, nil
	}
}

// MockAuthCallCounter creates a mock responder that counts how many times the token endpoint is called.
// This is useful for testing token caching behavior.
func MockAuthCallCounter(t *testing.T, clientID, clientSecret, testAccessToken string, expiresIn int, callCount *int) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		*callCount++

		// Validate Authorization header
		expectedAuthHeader := fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))
		authHeader := req.Header.Get("Authorization")
		if authHeader != expectedAuthHeader {
			t.Errorf("Unexpected authorization header: got %v, want %v", authHeader, expectedAuthHeader)
		}

		// Build response
		respBody := fmt.Sprintf(`{"access_token":"%s","token_type":"bearer","expires_in":%d}`, testAccessToken, expiresIn)
		header := http.Header{"Content-Type": []string{"application/json"}}
		buf := bytes.NewBuffer([]byte(respBody))
		resp := &http.Response{
			Status:        http.StatusText(http.StatusOK),
			StatusCode:    http.StatusOK,
			Header:        header,
			Body:          &readCloserWrapper{bufio.NewReader(buf), func() error { return nil }},
			ContentLength: int64(len(respBody)),
		}

		return resp, nil
	}
}

// readCloserWrapper wraps an io.Reader, and implements an io.ReadCloser
// It calls the given callback function when closed.
type readCloserWrapper struct {
	io.Reader
	closer func() error
}

// Close calls back the passed closer function
func (r *readCloserWrapper) Close() error {
	return r.closer()
}
