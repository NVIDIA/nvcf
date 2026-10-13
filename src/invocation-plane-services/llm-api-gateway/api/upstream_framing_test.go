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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// Stargate checks its size limit against the declared length, so every
// buffered body must reach it with a matching Content-Length.
func TestGatewayUpstreamRequest_BufferedBody_SendsMatchingContentLength(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		path    string
		payload string
	}{
		{"chat", "/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":false}`},
		{"streaming chat", "/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":true}`},
		{"responses", "/v1/responses", `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":false}`},
		{"streaming responses", "/v1/responses", `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`},
		{"embeddings", "/v1/embeddings", `{"model":"fn-alpha/company-name/model-name","input":"hello"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotContentLength int64
			var gotTransferEncoding []string
			var gotBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotContentLength = r.ContentLength
				gotTransferEncoding = r.TransferEncoding
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer upstream.Close()

			cfg := config.Default()
			proxyProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
			require.NoError(t, err)
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			RegisterRoutes(e, NewHandlers(cfg, proxyProvider, nil, nil))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.payload))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(HeaderRequestID, "request-a")
			e.ServeHTTP(httptest.NewRecorder(), req)

			require.NotEmpty(t, gotBody, "request did not reach the upstream")
			require.Empty(t, gotTransferEncoding)
			require.Equal(t, int64(len(gotBody)), gotContentLength)
		})
	}
}
