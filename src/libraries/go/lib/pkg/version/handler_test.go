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

package version

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerFor(t *testing.T) {
	tests := []struct {
		name        string
		service     string
		ver         string
		commit      string
		wantService string
		wantVersion string
		wantCommit  string
	}{
		{
			name:        "all set",
			service:     "nvcf-my-service",
			ver:         "1.2.3",
			commit:      "abc1234def5678901234567890123456789012ab",
			wantService: "nvcf-my-service",
			wantVersion: "1.2.3",
			wantCommit:  "abc1234def5678901234567890123456789012ab",
		},
		{
			name:        "empty service falls back to unknown",
			service:     "",
			ver:         "1.2.3",
			commit:      "abc1234def5678901234567890123456789012ab",
			wantService: "unknown",
			wantVersion: "1.2.3",
			wantCommit:  "abc1234def5678901234567890123456789012ab",
		},
		{
			name:        "empty version falls back to unknown",
			service:     "nvcf-my-service",
			ver:         "",
			commit:      "abc1234def5678901234567890123456789012ab",
			wantService: "nvcf-my-service",
			wantVersion: "unknown",
			wantCommit:  "abc1234def5678901234567890123456789012ab",
		},
		{
			name:    "empty commit falls back to buildinfo or unknown",
			service: "nvcf-my-service",
			ver:     "1.2.3",
			commit:  "",
			// commit will be whatever buildinfo provides in the test binary,
			// or "unknown" -- just assert it is non-empty string
			wantService: "nvcf-my-service",
			wantVersion: "1.2.3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/info", nil)
			HandlerFor(tc.service, tc.ver, tc.commit).ServeHTTP(w, r)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var got Info
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

			if tc.wantService != "" {
				assert.Equal(t, tc.wantService, got.Service)
			}
			if tc.wantVersion != "" {
				assert.Equal(t, tc.wantVersion, got.Version)
			}
			if tc.wantCommit != "" {
				assert.Equal(t, tc.wantCommit, got.Commit)
			}
			assert.NotEmpty(t, got.Commit)
		})
	}
}

func TestHandler(t *testing.T) {
	t.Cleanup(func() {
		Service = ""
		Version = ""
		GitHash = ""
	})

	Service = "nvcf-my-service"
	Version = "2.0.0"
	GitHash = "deadbeef1234567890abcdef1234567890abcdef"

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	Handler().ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var got Info
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "nvcf-my-service", got.Service)
	assert.Equal(t, "2.0.0", got.Version)
	assert.Equal(t, "deadbeef1234567890abcdef1234567890abcdef", got.Commit)
}
