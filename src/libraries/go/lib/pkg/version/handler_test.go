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
		ver         string
		commit      string
		wantVersion string
		wantCommit  string
	}{
		{
			name:        "both set",
			ver:         "1.2.3",
			commit:      "abc1234",
			wantVersion: "1.2.3",
			wantCommit:  "abc1234",
		},
		{
			name:        "empty version falls back to unknown",
			ver:         "",
			commit:      "abc1234",
			wantVersion: "unknown",
			wantCommit:  "abc1234",
		},
		{
			name:    "empty commit falls back to buildinfo or unknown",
			ver:     "1.2.3",
			commit:  "",
			wantVersion: "1.2.3",
			// commit will be whatever buildinfo provides in the test binary,
			// or "unknown" -- just assert it is non-empty string
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/version", nil)
			HandlerFor(tc.ver, tc.commit).ServeHTTP(w, r)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var got Info
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

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
		Version = ""
		GitHash = ""
	})

	Version = "2.0.0"
	GitHash = "deadbeef"

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/version", nil)
	Handler().ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var got Info
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "2.0.0", got.Version)
	assert.Equal(t, "deadbeef", got.Commit)
}
