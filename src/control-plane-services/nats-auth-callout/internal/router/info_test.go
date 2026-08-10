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

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestInfoEndpoint_GET(t *testing.T) {
	previousService := golibversion.Service
	previousVersion := golibversion.Version
	previousGitHash := golibversion.GitHash
	golibversion.Service = "nvcf-nats-auth-callout-service"
	golibversion.Version = "test-1.0.0"
	golibversion.GitHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Cleanup(func() {
		golibversion.Service = previousService
		golibversion.Version = previousVersion
		golibversion.GitHash = previousGitHash
	})

	gin.SetMode(gin.TestMode)
	r := New(zap.NewNop(), &Config{ServiceName: "test-service"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	r.engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "nvcf-nats-auth-callout-service", info["service"])
	assert.Equal(t, "test-1.0.0", info["version"])
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", info["commit"])
}

func TestInfoEndpoint_GET_UnstampedFallback(t *testing.T) {
	previousService := golibversion.Service
	previousVersion := golibversion.Version
	previousGitHash := golibversion.GitHash
	golibversion.Service = ""
	golibversion.Version = ""
	golibversion.GitHash = ""
	t.Cleanup(func() {
		golibversion.Service = previousService
		golibversion.Version = previousVersion
		golibversion.GitHash = previousGitHash
	})

	gin.SetMode(gin.TestMode)
	r := New(zap.NewNop(), &Config{ServiceName: "test-service"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	r.engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "unknown", info["service"])
	assert.Equal(t, "unknown", info["version"])
	assert.NotEmpty(t, info["commit"])
}

func TestInfoEndpoint_RejectsNonGET(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New(zap.NewNop(), &Config{ServiceName: "test-service"})

	for _, method := range []string{
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodConnect,
		http.MethodTrace,
	} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/info", nil)
			r.engine.ServeHTTP(w, req)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
		})
	}
}
