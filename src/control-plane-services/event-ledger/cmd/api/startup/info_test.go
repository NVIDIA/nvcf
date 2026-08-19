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

	router := mux.NewRouter()
	registerUnauthenticatedRoutes(router, &service.Server{}, func(h http.Handler) http.Handler { return h })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	router.ServeHTTP(w, r)

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
	} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/info", nil)
			router.ServeHTTP(w, r)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
			assert.Empty(t, w.Body.String())
		})
	}
}
