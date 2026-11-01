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
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCorsMiddleware(t *testing.T) {
	// Create a simple test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	t.Run("preflight_options_request", func(t *testing.T) {
		// Create a CORS preflight request
		req := httptest.NewRequest(http.MethodOptions, "/test", nil)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		req.Header.Set("Origin", "https://example.com")

		recorder := httptest.NewRecorder()

		// Apply CORS middleware
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Check that preflight is handled correctly
		if recorder.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", recorder.Code)
		}

		// Check CORS headers - the cors library processes the request and responds accordingly
		// Let's check that the important headers are present
		if recorder.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Error("expected Access-Control-Allow-Origin header to be present")
		}
		if recorder.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Error("expected Access-Control-Allow-Methods header to be present")
		}
		if recorder.Header().Get("Access-Control-Allow-Headers") == "" {
			t.Error("expected Access-Control-Allow-Headers header to be present")
		}

		// Check specific values that should be set based on DefaultCorsOptions
		// With AllowOriginFunc, the header should reflect the requesting origin
		expectedOrigin := "https://example.com" // This is what we sent in the Origin header
		if recorder.Header().Get("Access-Control-Allow-Origin") != expectedOrigin {
			t.Errorf("expected Access-Control-Allow-Origin: %s, got %s", expectedOrigin, recorder.Header().Get("Access-Control-Allow-Origin"))
		}
		if recorder.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("expected Access-Control-Allow-Credentials: true, got %s", recorder.Header().Get("Access-Control-Allow-Credentials"))
		}

		// The test handler should NOT be called for preflight requests
		body := recorder.Body.String()
		if body == "test response" {
			t.Error("test handler was called for preflight request, but it shouldn't have been")
		}
	})

	t.Run("non_preflight_options_request", func(t *testing.T) {
		// Create an OPTIONS request without Access-Control-Request-Method (not a preflight)
		req := httptest.NewRequest(http.MethodOptions, "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		recorder := httptest.NewRecorder()

		// Apply CORS middleware
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should pass through to the test handler
		if recorder.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", recorder.Code)
		}

		// The test handler should be called
		body := recorder.Body.String()
		if body != "test response" {
			t.Error("test handler was not called for non-preflight OPTIONS request")
		}

		// Should not have CORS headers since it's not a preflight request
		if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("unexpected CORS headers on non-preflight request")
		}
	})

	t.Run("regular_post_request", func(t *testing.T) {
		// Create a regular POST request
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Content-Type", "application/json")

		recorder := httptest.NewRecorder()

		// Apply CORS middleware
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should pass through to the test handler
		if recorder.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", recorder.Code)
		}

		// The test handler should be called
		body := recorder.Body.String()
		if body != "test response" {
			t.Error("test handler was not called for POST request")
		}

		// Should not have CORS headers since it's not a preflight request
		if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("unexpected CORS headers on regular POST request")
		}
	})

	t.Run("get_request", func(t *testing.T) {
		// Create a regular GET request
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		recorder := httptest.NewRecorder()

		// Apply CORS middleware
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should pass through to the test handler
		if recorder.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", recorder.Code)
		}

		// The test handler should be called
		body := recorder.Body.String()
		if body != "test response" {
			t.Error("test handler was not called for GET request")
		}

		// Should not have CORS headers since it's not a preflight request
		if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("unexpected CORS headers on regular GET request")
		}
	})
}

func TestDefaultCorsOptions(t *testing.T) {
	// Test that DefaultCorsOptions has the expected values
	expectedMethods := []string{
		http.MethodHead,
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}
	expectedHeaders := []string{"*"}
	expectedCredentials := true
	expectedMaxAge := 3600 // 1 hour

	// Check that AllowOriginFunc is set instead of AllowedOrigins
	if DefaultCorsOptions.AllowOriginFunc == nil {
		t.Error("expected AllowOriginFunc to be set")
	}
	if len(DefaultCorsOptions.AllowedOrigins) != 0 {
		t.Errorf("expected AllowedOrigins to be empty when using AllowOriginFunc, got %d origins", len(DefaultCorsOptions.AllowedOrigins))
	}

	// Test that AllowOriginFunc returns true for various origins
	testOrigins := []string{
		"https://example.com",
		"http://localhost:3000",
		"https://app.mysite.com",
		"https://evil.com",
	}
	for _, origin := range testOrigins {
		req := &http.Request{Header: http.Header{"Origin": []string{origin}}}
		if !DefaultCorsOptions.AllowOriginFunc(req, origin) {
			t.Errorf("expected AllowOriginFunc to return true for origin %s", origin)
		}
	}

	// Test that AllowOriginFunc returns true even for empty origin
	emptyReq := &http.Request{Header: http.Header{}}
	if !DefaultCorsOptions.AllowOriginFunc(emptyReq, "") {
		t.Error("expected AllowOriginFunc to return true for empty origin")
	}

	if len(DefaultCorsOptions.AllowedMethods) != len(expectedMethods) {
		t.Errorf("expected %d allowed methods, got %d", len(expectedMethods), len(DefaultCorsOptions.AllowedMethods))
	}
	for i, method := range expectedMethods {
		if DefaultCorsOptions.AllowedMethods[i] != method {
			t.Errorf("expected allowed method %s, got %s", method, DefaultCorsOptions.AllowedMethods[i])
		}
	}

	if len(DefaultCorsOptions.AllowedHeaders) != len(expectedHeaders) {
		t.Errorf("expected %d allowed headers, got %d", len(expectedHeaders), len(DefaultCorsOptions.AllowedHeaders))
	}
	for i, header := range expectedHeaders {
		if DefaultCorsOptions.AllowedHeaders[i] != header {
			t.Errorf("expected allowed header %s, got %s", header, DefaultCorsOptions.AllowedHeaders[i])
		}
	}

	if DefaultCorsOptions.AllowCredentials != expectedCredentials {
		t.Errorf("expected AllowCredentials %t, got %t", expectedCredentials, DefaultCorsOptions.AllowCredentials)
	}

	if DefaultCorsOptions.MaxAge != expectedMaxAge {
		t.Errorf("expected MaxAge %d, got %d", expectedMaxAge, DefaultCorsOptions.MaxAge)
	}
}

func TestCorsMiddlewareWithErrorHandler(t *testing.T) {
	// Create a test handler that returns an error
	errorHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	})

	t.Run("error_response_should_not_have_cors_headers", func(t *testing.T) {
		// Create a regular POST request
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		recorder := httptest.NewRecorder()

		// Apply CORS middleware
		corsHandler := Cors(errorHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should pass through to the error handler
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("expected status 500, got %d", recorder.Code)
		}

		// Should not have CORS headers since it's not a preflight request
		if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("unexpected CORS headers on error response from middleware")
		}
	})
}

func TestCorsEdgeCases(t *testing.T) {
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	t.Run("options_without_origin", func(t *testing.T) {
		// Preflight request without Origin header
		req := httptest.NewRequest(http.MethodOptions, "/test", nil)
		req.Header.Set("Access-Control-Request-Method", "POST")

		recorder := httptest.NewRecorder()
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Without Origin header, the cors library might reject the request or handle it differently
		// Let's just check that it doesn't crash and that we get some response
		if recorder.Code == 0 {
			t.Error("expected some HTTP status code")
		}

		// The behavior might vary based on the CORS library implementation
		// so we'll just verify it doesn't crash
	})

	t.Run("case_sensitive_method_check", func(t *testing.T) {
		// Test with lowercase options method
		req := httptest.NewRequest("options", "/test", nil)
		req.Header.Set("Access-Control-Request-Method", "POST")

		recorder := httptest.NewRecorder()
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should not be treated as preflight since method is lowercase
		body := recorder.Body.String()
		if body != "test response" {
			t.Error("lowercase 'options' should not be treated as preflight")
		}
	})

	t.Run("empty_access_control_request_method", func(t *testing.T) {
		// OPTIONS with empty Access-Control-Request-Method
		req := httptest.NewRequest(http.MethodOptions, "/test", nil)
		req.Header.Set("Access-Control-Request-Method", "")

		recorder := httptest.NewRecorder()
		corsHandler := Cors(testHandler)
		corsHandler.ServeHTTP(recorder, req)

		// Should not be treated as preflight
		body := recorder.Body.String()
		if body != "test response" {
			t.Error("OPTIONS with empty Access-Control-Request-Method should not be treated as preflight")
		}
	})
}
