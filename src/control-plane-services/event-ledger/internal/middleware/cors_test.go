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

	"github.com/stretchr/testify/assert"
)

// TestEnableCORS_Preflight verifies OPTIONS requests to ordinary routes are
// short-circuited with a 204 CORS preflight response.
func TestEnableCORS_Preflight(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/ledger/versions/abc/instances/def", nil)
	EnableCORS(next).ServeHTTP(w, r)

	assert.False(t, nextCalled, "preflight OPTIONS should not reach the wrapped handler")
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "GET, POST, DELETE, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
}

// TestEnableCORS_InfoBypassesPreflight verifies /info is excluded from CORS
// preflight handling so its own GET-only contract (405 for every other
// method, including OPTIONS) is enforced by the wrapped handler.
func TestEnableCORS_InfoBypassesPreflight(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
			})

			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), method, "/info", nil)
			EnableCORS(next).ServeHTTP(w, r)

			assert.True(t, nextCalled, "/info requests must reach the wrapped handler regardless of method")
			assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
		})
	}
}
