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

package authz_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/authz"
)

func TestServeUnauthorized_StatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	authz.ServeUnauthorized(r, w, errors.New("bad token"))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServeUnauthorized_ContentType(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	authz.ServeUnauthorized(r, w, errors.New("bad token"))
	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestServeUnauthorized_ReasonIsUnauthorized(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	authz.ServeUnauthorized(r, w, errors.New("bad token"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "Unauthorized", body["reason"])
}

func TestServeUnauthorized_ErrorInMetadata(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	authz.ServeUnauthorized(r, w, errors.New("token expired"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	meta, _ := body["metadata"].(map[string]any)
	assert.Equal(t, "token expired", meta["error"])
}

func TestServeUnauthorized_NilError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/validate", nil)
	authz.ServeUnauthorized(r, w, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
