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

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddingsTokenInputValidationAndAccounting(t *testing.T) {
	for _, tt := range []struct {
		name, input, tokens string
		status              int
	}{
		{"single token array", `[0,123,456]`, "3", http.StatusOK},
		{"token batch", `[[0,123],[456]]`, "3", http.StatusOK},
		{"token count is not batch size", `[` + strings.Repeat("1,", MaxEmbeddingInputs) + `1]`, "2049", http.StatusOK},
		{"too many sequences", `[` + strings.Repeat("[1],", MaxEmbeddingInputs) + `[1]]`, "", http.StatusBadRequest},
		{"empty batch", `[]`, "", http.StatusBadRequest},
		// The backend validates elements. Invalid ones count by raw size.
		{"empty sequence", `[[],[1]]`, "1", http.StatusOK},
		{"null sequence", `[null,[1]]`, "8", http.StatusOK},
		{"null token", `[[1,null]]`, "7", http.StatusOK},
		{"fractional token", `[1.5]`, "7", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newPassthroughUpstream(t, "application/json", `{"object":"list","data":[]}`)
			gateway := newPassthroughAPI(t, upstream.server.URL)
			response := servePassthroughRequest(t, gateway, "/v1/embeddings", `{"model":"fn-alpha/company/model","input":`+tt.input+`}`)
			require.Equal(t, tt.status, response.Code, response.Body.String())
			if tt.status != http.StatusOK {
				require.Empty(t, upstream.requests, "invalid input must not reach upstream")
				return
			}
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tt.tokens, <-upstream.inputTokens, "tokenized input must use its exact token count")
		})
	}
}
