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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/callerkeys"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	listedCallerKey = "demo-ui-key"
	// SHA-256 of listedCallerKey.
	listedCallerKeyHash = "276932c4694447817ad43a6afceb8f8a64657038679602b46ce8dc254b18bbcd"
)

// callerKeyEndpoints are the six endpoints that require a caller key.
var callerKeyEndpoints = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"chat completions", http.MethodPost, "/v1/chat/completions", `{"model":%q,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", http.MethodPost, "/v1/responses", `{"model":%q,"input":"hi"}`},
	{"embeddings", http.MethodPost, "/v1/embeddings", `{"model":%q,"input":"hi"}`},
	{"list models", http.MethodGet, "/v1/models", ""},
	{"retrieve model", http.MethodGet, "/v1/models/" + bareModelName, ""},
	{"registry", http.MethodGet, "/v1/registry", ""},
}

// newCallerKeyAPI wires the gateway with caller keys the way server.go does,
// in front of a stub router that serves the model listing and records the
// headers of every inference request it receives.
func newCallerKeyAPI(t *testing.T, bareModelNamesEnabled bool) (*echo.Echo, chan http.Header) {
	t.Helper()

	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			_ = json.NewEncoder(w).Encode(routableListing([]string{bareModelName}))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		received <- r.Header.Clone()
		for _, endpoint := range bareModelEndpoints {
			if endpoint.path == r.URL.Path {
				w.Header().Set(echo.HeaderContentType, endpoint.upstreamContentType)
				_, _ = io.WriteString(w, endpoint.upstreamBody)
				return
			}
		}
		t.Errorf("router got unexpected %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	keyFile := filepath.Join(t.TempDir(), "caller-keys.yaml")
	require.NoError(t, os.WriteFile(
		keyFile,
		[]byte("keys:\n  - id: demo-ui\n    sha256: "+listedCallerKeyHash+"\n"),
		0o600,
	))
	keys, err := callerkeys.Load(context.Background(), callerkeys.NewFileStore(keyFile))
	require.NoError(t, err)

	stargate, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	require.NoError(t, err)

	cfg := config.Default()
	cfg.BareModelNamesEnabled = bareModelNamesEnabled
	e := echo.New()
	e.Use(NewContextMiddleware(cfg))
	e.Use(NewCallerKeyAuthMiddleware(keys))
	RegisterRoutes(e, NewHandlers(cfg, stargate, nil, nil))
	return e, received
}

func newCallerKeyRequest(method, path, body, model, authorization string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(fmt.Sprintf(body, model))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if authorization != "" {
		req.Header.Set(echo.HeaderAuthorization, authorization)
	}
	return req
}

func TestCallerKeyAuth_Endpoint_RequiresListedKey(t *testing.T) {
	t.Parallel()

	credentials := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{"missing key", "", http.StatusUnauthorized},
		{"unknown key", "Bearer other-key", http.StatusUnauthorized},
		{"listed key with another scheme", "Basic " + listedCallerKey, http.StatusUnauthorized},
		{"listed key", "Bearer " + listedCallerKey, http.StatusOK},
	}

	for _, credential := range credentials {
		for _, endpoint := range callerKeyEndpoints {
			t.Run(credential.name+"/"+endpoint.name, func(t *testing.T) {
				t.Parallel()

				e, received := newCallerKeyAPI(t, true)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, newCallerKeyRequest(
					endpoint.method, endpoint.path, endpoint.body, bareModelName, credential.authorization,
				))

				require.Equal(t, credential.wantStatus, rec.Code, rec.Body.String())
				if credential.wantStatus != http.StatusUnauthorized {
					return
				}
				var got models.ErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
				require.Equal(t, models.Error{
					Code:    "invalid_api_key",
					Message: "Missing or invalid API key",
					Type:    "invalid_request_error",
				}, got.Error)
				require.Empty(t, received, "request must not reach the router")
			})
		}
	}
}

// TestCallerKeyAuth_ModelNameForms_RouteWithModelRoutingKey sends a listed key
// with caller-supplied X-Routing-Key and X-Priority. The authorizer adds no
// routing key or priority, and the caller's key never reaches the router.
func TestCallerKeyAuth_ModelNameForms_RouteWithModelRoutingKey(t *testing.T) {
	t.Parallel()

	settings := []struct {
		name           string
		isBareEnabled  bool
		model          string
		wantRoutingKey []string
	}{
		{"bare model name", true, bareModelName, nil},
		{"routing-key/model name", false, "fn-alpha/" + bareModelName, []string{"fn-alpha"}},
	}

	for _, setting := range settings {
		for _, endpoint := range callerKeyEndpoints[:3] {
			t.Run(setting.name+"/"+endpoint.name, func(t *testing.T) {
				t.Parallel()

				e, received := newCallerKeyAPI(t, setting.isBareEnabled)
				req := newCallerKeyRequest(
					endpoint.method, endpoint.path, endpoint.body, setting.model, "Bearer "+listedCallerKey,
				)
				req.Header.Set("X-Routing-Key", "caller-routing-key")
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				routerHeaders := <-received
				require.Equal(t, bareModelName, routerHeaders.Get("X-Model"))
				require.Equal(t, setting.wantRoutingKey, routerHeaders.Values("X-Routing-Key"))
				require.Empty(t, routerHeaders.Values(echo.HeaderAuthorization))
				require.Empty(t, routerHeaders.Values("X-Priority"))
			})
		}
	}
}

func TestCallerKeyAuth_ProbeAndInfoRoutes_NeedNoKey(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/healthz", "/readyz", "/info"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			e, _ := newCallerKeyAPI(t, true)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// Not parallel: it swaps the global logger.
func TestCallerKeyAuth_AuthenticatedRequest_LogsKeyIDNotKey(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := zlog.Logger
	zlog.Logger = zerolog.New(&logs)
	t.Cleanup(func() { zlog.Logger = oldLogger })

	e, received := newCallerKeyAPI(t, true)
	rec := httptest.NewRecorder()
	endpoint := callerKeyEndpoints[0]
	e.ServeHTTP(rec, newCallerKeyRequest(
		endpoint.method, endpoint.path, endpoint.body, bareModelName, "Bearer "+listedCallerKey,
	))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	<-received
	require.Contains(t, logs.String(), `"subject":"api-key:demo-ui"`)
	require.NotContains(t, logs.String(), listedCallerKey)
}
