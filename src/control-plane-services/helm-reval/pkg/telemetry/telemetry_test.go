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

package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry"
)

func TestGetWrappedWriter_PlainResponseWriter(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped := telemetry.GetWrappedWriter(w, r)
	require.NotNil(t, wrapped)
	wrapped.WriteHeader(http.StatusTeapot)
	assert.Equal(t, http.StatusTeapot, wrapped.Status())
}

func TestGetWrappedWriter_AlreadyWrapped_ReturnsItself(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	already := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	got := telemetry.GetWrappedWriter(already, r)
	// Should return the same wrapper, not double-wrap.
	assert.Equal(t, already, got)
}

func TestGetWrappedWriter_NilRequest_UsesHTTP1(t *testing.T) {
	w := httptest.NewRecorder()
	wrapped := telemetry.GetWrappedWriter(w, nil)
	require.NotNil(t, wrapped)
	wrapped.WriteHeader(http.StatusOK)
	assert.Equal(t, http.StatusOK, wrapped.Status())
}
