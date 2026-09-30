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
	"fmt"
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

const bareModelName = "meta/llama-3.1-8b-instruct"

const bareModelResponsesUpstreamSSE = "event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"completed","created_at":1,"model":"meta/llama-3.1-8b-instruct","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` +
	"\n\n"

var bareModelEndpoints = []struct {
	name                string
	path                string
	body                string
	upstreamContentType string
	upstreamBody        string
}{
	{
		name:                "chat completions",
		path:                "/v1/chat/completions",
		body:                `{"model":%q,"messages":[{"role":"user","content":"hi"}]}`,
		upstreamContentType: "text/event-stream",
		upstreamBody:        passthroughChatUpstreamSSE,
	},
	{
		name:                "responses",
		path:                "/v1/responses",
		body:                `{"model":%q,"input":"hi"}`,
		upstreamContentType: "text/event-stream",
		upstreamBody:        bareModelResponsesUpstreamSSE,
	},
	{
		name:                "embeddings",
		path:                "/v1/embeddings",
		body:                `{"model":%q,"input":"hi"}`,
		upstreamContentType: echo.MIMEApplicationJSON,
		upstreamBody:        `{"object":"list","data":[],"model":"meta/llama-3.1-8b-instruct"}`,
	},
}

// newBareModelAPI wires the gateway the way server.go does, in front of a stub
// router that records the headers of every request it receives.
func newBareModelAPI(
	t *testing.T,
	bareModelNamesEnabled bool,
	upstreamContentType string,
	upstreamBody string,
) (*echo.Echo, chan http.Header) {
	t.Helper()

	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received <- r.Header.Clone()
		w.Header().Set(echo.HeaderContentType, upstreamContentType)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(upstream.Close)

	stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	require.NoError(t, err)

	cfg := config.Default()
	cfg.BareModelNamesEnabled = bareModelNamesEnabled
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewNVCFAuthMiddleware(echoingInvocationAuthClient{}))
	RegisterRoutes(e, NewHandlers(cfg, stargate, nil, nil))
	return e, received
}

// TestBareModelNames_RoutedRequest_RouterGetsOnlyGatewayResolvedHeaders sends
// caller-supplied X-Routing-Key and Authorization on every request. Only the
// values the gateway resolved may reach the router.
func TestBareModelNames_RoutedRequest_RouterGetsOnlyGatewayResolvedHeaders(t *testing.T) {
	t.Parallel()

	settings := []struct {
		name               string
		isEnabled          bool
		model              string
		wantRoutingKey     []string
		wantAuthorizations []string
	}{
		{"setting on", true, bareModelName, nil, nil},
		{"setting off", false, "fn-alpha/" + bareModelName, []string{"fn-alpha"}, []string{"Bearer caller-secret"}},
	}

	for _, setting := range settings {
		for _, endpoint := range bareModelEndpoints {
			t.Run(setting.name+"/"+endpoint.name, func(t *testing.T) {
				t.Parallel()

				e, received := newBareModelAPI(t, setting.isEnabled, endpoint.upstreamContentType, endpoint.upstreamBody)
				req := httptest.NewRequest(
					http.MethodPost,
					endpoint.path,
					strings.NewReader(fmt.Sprintf(endpoint.body, setting.model)),
				)
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				req.Header.Set(echo.HeaderAuthorization, "Bearer caller-secret")
				req.Header.Set("X-Routing-Key", "caller-routing-key")
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				routerHeaders := <-received
				require.Equal(t, bareModelName, routerHeaders.Get("X-Model"))
				require.Equal(t, setting.wantRoutingKey, routerHeaders.Values("X-Routing-Key"))
				require.Equal(t, setting.wantAuthorizations, routerHeaders.Values(echo.HeaderAuthorization))
			})
		}
	}
}

func TestBareModelNames_SettingOff_RejectsModelWithoutRoutingKey(t *testing.T) {
	t.Parallel()

	for _, tc := range bareModelEndpoints {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e, received := newBareModelAPI(t, false, tc.upstreamContentType, tc.upstreamBody)
			req := httptest.NewRequest(
				http.MethodPost,
				tc.path,
				strings.NewReader(fmt.Sprintf(tc.body, "llama-3.1-8b-instruct")),
			)
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "model prefix is required")
			require.Empty(t, received, "request must not reach the router")
		})
	}
}
