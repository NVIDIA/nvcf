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

package gateway

import (
	config "ai-api-gateway-service/gateway_config"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newInfoTestMux builds the top-level chi router with a minimal, valid config.
// The /info handler does not depend on any mapping or upstream, so an empty
// GatewayConfig pointed at a throwaway backend is enough to exercise it.
func newInfoTestMux(t *testing.T) http.Handler {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	mux, err := buildChiMux(&config.GatewayConfig{}, Config{
		NvcfApiEndpoint:              backend.URL,
		PrivateModelNameRegexPattern: "^$",
	})
	require.NoError(t, err)
	return mux
}

func TestBuildChiMux_Info(t *testing.T) {
	golibversion.Service = "nvcf-ai-api-gateway-service"
	golibversion.Version = "test-1.0.0"
	golibversion.GitHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Cleanup(func() {
		golibversion.Service = ""
		golibversion.Version = ""
		golibversion.GitHash = ""
	})

	mux := newInfoTestMux(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	mux.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "nvcf-ai-api-gateway-service", info["service"])
	assert.Equal(t, "test-1.0.0", info["version"])
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", info["commit"])
}

func TestBuildChiMux_Info_RejectsNonGET(t *testing.T) {
	mux := newInfoTestMux(t)

	for _, method := range []string{
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/info", nil)
			mux.ServeHTTP(w, r)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
			assert.Empty(t, w.Body.String())
		})
	}
}
