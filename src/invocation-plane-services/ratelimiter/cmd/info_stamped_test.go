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

// Package main - stamped metadata tests.
// This file is compiled by the cmd_info_stamped_test Bazel target, which
// injects known x_defs for Service, Version, and GitHash so the assertions
// can verify the exact values rather than the "unknown" fallback.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHealthServeMux_Info_StampedValues(t *testing.T) {
	if golibversion.Version == "" {
		t.Skip("x_defs not injected; run via bazel test :cmd_info_stamped_test")
	}

	mux := newHealthServeMux(nil)
	require.NotNil(t, mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	mux.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var info map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "nvcf-ratelimiter", info["service"])
	assert.Equal(t, "test-1.0.0", info["version"])
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", info["commit"])
}
