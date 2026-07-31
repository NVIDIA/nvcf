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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestInfoEndpoint_GET(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := New(zap.NewNop(), &Config{ServiceName: "test-service"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	r.engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// x_defs are not injected under `go test`; resolve() falls back to
	// "unknown" for any empty field, so all three values are non-empty.
	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	for _, field := range []string{"service", "version", "commit"} {
		assert.Contains(t, info, field)
		assert.NotEmpty(t, info[field], field+" must be populated")
	}
}

func TestInfoEndpoint_RejectsNonGET(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := New(zap.NewNop(), &Config{ServiceName: "test-service"})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/info", nil)
			r.engine.ServeHTTP(w, req)

			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
			assert.Equal(t, http.MethodGet, w.Header().Get("Allow"))
			assert.Empty(t, w.Body.String())
		})
	}
}
