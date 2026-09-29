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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// echoingInvocationAuthClient mimics the invocation auth RPC, which parses the
// routing key as a UUID (accepting either hex case) and echoes the
// caller-supplied routing key back verbatim.
type echoingInvocationAuthClient struct{}

func (echoingInvocationAuthClient) AuthorizeInvocation(
	_ context.Context,
	_ string,
	routingKey string,
) (*nvcf.InvocationAuthResponse, error) {
	return &nvcf.InvocationAuthResponse{
		RoutingKey:   routingKey,
		ClientAuthID: "subject-123",
		RateLimitKey: "nca-456",
	}, nil
}

// TestChatCompletionsRoutingKeyUUIDCaseInsensitive covers a request whose model
// prefix is a UUID written in uppercase. RFC 9562 section 4 makes UUID hex
// digits case-insensitive on input, but routing targets register under the
// lowercase form, so the gateway must canonicalize the routing key before
// routing. Without that, the uppercase spelling is rejected upstream while the
// lowercase spelling succeeds.
func TestChatCompletionsRoutingKeyUUIDCaseInsensitive(t *testing.T) {
	t.Parallel()

	const canonicalKey = "3f2b1c4d-5e6a-4b7c-8d9e-0a1b2c3d4e5f"

	// Stand-in router: only the registered lowercase routing key has a target.
	// Any other key gets a 400 with an empty body, which the gateway surfaces
	// to the caller as the bare message "Bad Request".
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Routing-Key") != canonicalKey {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"id":"chatcmpl-case","object":"chat.completion.chunk","created":123,"model":"company-name/model-name","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	stargateProvider, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	if err != nil {
		t.Fatalf("new stargate provider: %v", err)
	}

	cfg := config.Default()
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(echoingInvocationAuthClient{}))
	RegisterRoutes(e, NewHandlers(cfg, stargateProvider, nil))

	for _, tc := range []struct {
		name  string
		model string
	}{
		{"lowercase uuid", canonicalKey + "/company-name/model-name"},
		{"uppercase uuid", strings.ToUpper(canonicalKey) + "/company-name/model-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`,
				tc.model,
			)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.Header.Set(echo.HeaderAuthorization, "Bearer sk-live")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
		})
	}
}
