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
package policy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pdpv1 "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/credentials"
)

func newTestTokenReader(t *testing.T, token string) *credentials.BearerTokenReader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	data, err := json.Marshal(map[string]any{"token": token})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	r, err := credentials.NewBearerTokenReader(path, "token")
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })
	return r
}

func TestStaticBearerClient_PolicyConfig(t *testing.T) {
	cfg := &PolicyConfig{Namespace: "event-ledger", PolicyFQDN: "apikey.allow"}
	client := NewStaticBearerClient("http://example.com", cfg, newTestTokenReader(t, "tok"), &http.Client{})
	assert.Equal(t, cfg, client.PolicyConfig())
}

func TestStaticBearerClient_Evaluate(t *testing.T) {
	allowResponse := map[string]any{
		"result": map[string]any{"allow": true},
	}

	tests := []struct {
		name           string
		tokenReader    *credentials.BearerTokenReader
		serverResponse any
		serverStatus   int
		wantErr        bool
		wantAuthHeader string
		checkURL       bool
	}{
		{
			name:           "with token reader sends Authorization header and correct URL",
			serverStatus:   http.StatusOK,
			serverResponse: allowResponse,
			wantAuthHeader: "Bearer test-bearer-token",
			checkURL:       true,
		},
		{
			name:           "without token reader sends no Authorization header",
			tokenReader:    nil,
			serverStatus:   http.StatusOK,
			serverResponse: allowResponse,
			wantAuthHeader: "",
		},
		{
			name:           "with token reader returns error on non-200 status",
			serverStatus:   http.StatusForbidden,
			wantAuthHeader: "Bearer test-bearer-token",
			wantErr:        true,
		},
		{
			name:         "without token reader returns error on non-200 status",
			tokenReader:  nil,
			serverStatus: http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokenReader := tc.tokenReader
			if tc.wantAuthHeader != "" {
				tokenReader = newTestTokenReader(t, "test-bearer-token")
			}

			var capturedAuth, capturedURL string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedAuth = r.Header.Get("Authorization")
				capturedURL = r.URL.Path
				w.WriteHeader(tc.serverStatus)
				if tc.serverResponse != nil {
					json.NewEncoder(w).Encode(tc.serverResponse)
				}
			}))
			defer srv.Close()

			cfg := &PolicyConfig{Namespace: "event-ledger", PolicyFQDN: "apikey.allow"}
			client := NewStaticBearerClient(srv.URL, cfg, tokenReader, &http.Client{})

			req := &pdpv1.RuleRequest{
				Namespace: "event-ledger",
				RuleName:  "apikey.allow",
			}

			_, err := client.Evaluate(context.Background(), req)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tc.wantAuthHeader, capturedAuth)
			if tc.checkURL {
				assert.Equal(t, "/v1/namespaces/event-ledger/evaluations/apikey.allow", capturedURL)
			}
		})
	}
}
