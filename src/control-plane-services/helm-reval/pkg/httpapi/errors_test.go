// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/httpapi"
)

func decodeAPIError(t *testing.T, body string) httpapi.ApiError {
	t.Helper()
	var ae httpapi.ApiError
	require.NoError(t, json.Unmarshal([]byte(body), &ae))
	return ae
}

// ── ServeAPIError ─────────────────────────────────────────────────────────────

func TestServeAPIError_StatusCode(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			httpapi.ServeAPIError(r, w, httpapi.ApiError{Reason: "test"}, code)
			assert.Equal(t, code, w.Code)
		})
	}
}

func TestServeAPIError_ContentType(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeAPIError(r, w, httpapi.ApiError{Reason: "oops"}, http.StatusBadRequest)
	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

func TestServeAPIError_ReasonAndOrigin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeAPIError(r, w, httpapi.ApiError{
		Reason: "bad request",
		Origin: "test-origin",
	}, http.StatusBadRequest)

	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "bad request", ae.Reason)
	assert.Equal(t, "test-origin", ae.Origin)
}

func TestServeAPIError_ErrorIdFilledWhenEmpty(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeAPIError(r, w, httpapi.ApiError{Reason: "oops"}, http.StatusInternalServerError)

	ae := decodeAPIError(t, w.Body.String())
	// No trace in plain context → falls back to "unknown:" prefix.
	assert.NotEmpty(t, ae.ErrorId)
}

func TestServeAPIError_ErrorIdPreservedWhenSet(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeAPIError(r, w, httpapi.ApiError{
		Reason:  "oops",
		ErrorId: "custom-id-123",
	}, http.StatusBadRequest)

	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "custom-id-123", ae.ErrorId)
}

func TestServeAPIError_MetadataRoundTrips(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeAPIError(r, w, httpapi.ApiError{
		Reason:   "something failed",
		Metadata: map[string]string{"key": "value", "foo": "bar"},
	}, http.StatusInternalServerError)

	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "value", ae.Metadata["key"])
	assert.Equal(t, "bar", ae.Metadata["foo"])
}

// ── ServeSimpleError ──────────────────────────────────────────────────────────

func TestServeSimpleError_WithError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeSimpleError(r, w, "something went wrong", errors.New("root cause"), http.StatusBadRequest)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "something went wrong", ae.Reason)
	assert.Equal(t, "reval-service", ae.Origin)
	assert.Equal(t, "root cause", ae.Metadata["error"])
}

func TestServeSimpleError_NilError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeSimpleError(r, w, "not found", nil, http.StatusNotFound)

	assert.Equal(t, http.StatusNotFound, w.Code)
	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "not found", ae.Reason)
	assert.Nil(t, ae.Metadata)
}

func TestServeSimpleError_Origin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpapi.ServeSimpleError(r, w, "reason", nil, http.StatusInternalServerError)

	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "reval-service", ae.Origin)
}

// ── ServeNotFound ─────────────────────────────────────────────────────────────

func TestServeNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/missing", nil)
	httpapi.ServeNotFound(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code)
	ae := decodeAPIError(t, w.Body.String())
	assert.Equal(t, "Not Found", ae.Reason)
	assert.Equal(t, "reval-service", ae.Origin)
}
