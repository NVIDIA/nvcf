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

package startup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/service"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
)

// TestRegisterUnauthenticatedRoutes_Info verifies GET /info returns the stamped
// service/version/commit as JSON. It wires /info onto the router before auth
// middleware, so an empty *service.Server is safe as long as /health is not
// exercised.
func TestRegisterUnauthenticatedRoutes_Info(t *testing.T) {
	prevService, prevVersion, prevHash := golibversion.Service, golibversion.Version, golibversion.GitHash
	golibversion.Service = "nvcf-event-ledger"
	golibversion.Version = "test-1.0.0"
	golibversion.GitHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Cleanup(func() {
		golibversion.Service = prevService
		golibversion.Version = prevVersion
		golibversion.GitHash = prevHash
	})

	// A recording middleware confirms registerUnauthenticatedRoutes actually
	// wraps /info with the injected middleware (tracing + logging in production).
	mwApplied := false
	infoMiddleware := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mwApplied = true
			w.Header().Set("X-Info-Middleware", "applied")
			h.ServeHTTP(w, r)
		})
	}

	router := mux.NewRouter()
	registerUnauthenticatedRoutes(router, &service.Server{}, infoMiddleware)

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/info", nil)
	router.ServeHTTP(w, r)

	assert.True(t, mwApplied, "/info should be wrapped with the injected middleware")
	assert.Equal(t, "applied", w.Header().Get("X-Info-Middleware"))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "nvcf-event-ledger", info["service"])
	assert.Equal(t, "test-1.0.0", info["version"])
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", info["commit"])
}

// TestRegisterUnauthenticatedRoutes_Info_RejectsNonGET verifies non-GET methods on
// /info return 405 with an Allow: GET header, as enforced by the go-lib handler.
func TestRegisterUnauthenticatedRoutes_Info_RejectsNonGET(t *testing.T) {
	router := mux.NewRouter()
	registerUnauthenticatedRoutes(router, &service.Server{}, func(h http.Handler) http.Handler { return h })

	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), method, "/info", nil)
			router.ServeHTTP(w, r)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
			assert.Empty(t, w.Body.String())
		})
	}
}

// TestRegisterUnauthenticatedRoutes_Info_RejectsNonGET_WithCORSMiddleware
// wires /info through middleware.EnableCORS the same way runService does, to
// guard against regressions where the CORS preflight short-circuit swallows
// OPTIONS /info before it reaches the go-lib handler's 405 enforcement
// (see NVBug 6664046: OPTIONS /info returned 204 instead of 405 in staging
// because the plain-router test above doesn't apply the production
// middleware chain).
func TestRegisterUnauthenticatedRoutes_Info_RejectsNonGET_WithCORSMiddleware(t *testing.T) {
	router := mux.NewRouter()
	router.Use(middleware.EnableCORS)
	registerUnauthenticatedRoutes(router, &service.Server{}, func(h http.Handler) http.Handler { return h })

	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), method, "/info", nil)
			router.ServeHTTP(w, r)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
			assert.Empty(t, w.Body.String())
		})
	}
}
