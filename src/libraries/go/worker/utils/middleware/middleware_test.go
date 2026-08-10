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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestApplyMiddleware(t *testing.T) {
	called := false
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	})

	wrapped := ApplyMiddleware(base, "test-router")
	require.NotNil(t, wrapped)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	wrapped.ServeHTTP(rec, req)

	require.True(t, called, "inner handler must be invoked through logger+tracer chain")
	require.Equal(t, http.StatusTeapot, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

func TestTracingMiddleware(t *testing.T) {
	mw := TracingMiddleware()
	require.NotNil(t, mw)

	served := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/traced", nil))
	require.True(t, served)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestLoggingMiddleware(t *testing.T) {
	mw := LoggingMiddleware("router-name")
	require.NotNil(t, mw)

	served := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logged", nil))
	require.True(t, served)
	require.Equal(t, http.StatusAccepted, rec.Code)
}

func TestTracedRoundTripper(t *testing.T) {
	var seen *http.Request
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})

	rt := TracedRoundTripper(base)
	require.NotNil(t, rt)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/rt", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.NotNil(t, seen, "wrapped transport must be invoked")
}
