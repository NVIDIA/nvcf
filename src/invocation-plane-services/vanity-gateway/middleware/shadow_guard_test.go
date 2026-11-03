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
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRejectSpoofedShadowRequests_RejectsHeaderPresent(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RejectSpoofedShadowRequests("NVCF-Shadow", nil)(inner)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("NVCF-Shadow", "true")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "reserved header")
}

func TestRejectSpoofedShadowRequests_AllowsNormalRequests(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := RejectSpoofedShadowRequests("NVCF-Shadow", nil)(inner)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestRejectSpoofedShadowRequests_RejectsAnyHeaderValue(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RejectSpoofedShadowRequests("NVCF-Shadow", nil)(inner)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("NVCF-Shadow", "false")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRejectSpoofedShadowRequests_LogsRejection(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)

	handler := RejectSpoofedShadowRequests("NVCF-Shadow", nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("NVCF-Shadow", "secret-value")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.All()
	if assert.Len(t, entries, 1) {
		fields := entries[0].ContextMap()
		assert.Equal(t, "/v1/chat/completions", fields["http.path"])
		assert.Equal(t, string(GatewayProxyOutcomeRejected), fields[string(GatewayProxyOutcomeMetricAttribute)])
		assert.NotContains(t, entries[0].Message+toString(fields), "secret-value")
	}
}

func toString(fields map[string]any) string {
	out := ""
	for _, value := range fields {
		if s, ok := value.(string); ok {
			out += s
		}
	}
	return out
}
