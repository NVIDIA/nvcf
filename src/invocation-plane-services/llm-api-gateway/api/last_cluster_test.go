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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// TestLastClusterSessionFlows sends two requests per session through the
// handlers and checks the hint the second one carries to Stargate.
func TestLastClusterSessionFlows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		payload  string
		session  string
		wantHint string
	}{
		{
			name:     "chat/prompt_cache_key",
			path:     "/v1/chat/completions",
			payload:  `{"model":"fn-alpha/company-name/model-name","prompt_cache_key":"pck-1","messages":[{"role":"user","content":"hello"}]}`,
			wantHint: "cluster-b",
		},
		{
			name:     "chat/header",
			path:     "/v1/chat/completions",
			payload:  `{"model":"fn-alpha/company-name/model-name","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			session:  "client-session-1",
			wantHint: "cluster-b",
		},
		{
			name:     "responses/conversation_id",
			path:     "/v1/responses",
			payload:  `{"model":"fn-alpha/company-name/model-name","stream":true,"conversation":{"id":"conv-1"},"input":"hello"}`,
			wantHint: "cluster-b",
		},
		{
			name:    "chat/payload",
			path:    "/v1/chat/completions",
			payload: `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:    "responses/payload",
			path:    "/v1/responses",
			payload: `{"model":"fn-alpha/company-name/model-name","stream":true,"input":"hello"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				hints []string
			)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				mu.Lock()
				hints = append(hints, r.Header.Get("X-Stargate-Last-Cluster-Id"))
				mu.Unlock()
				w.Header().Set("X-Stargate-Cluster-Id", "cluster-b")
				w.Header().Set(echo.HeaderContentType, "text/event-stream")
				if r.URL.Path == "/v1/responses" {
					_, _ = io.WriteString(w, "event: response.completed\n"+
						`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"completed","created_at":1,"model":"company-name/model-name","output":[]}}`+"\n\n")
					return
				}
				_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()

			cfg := config.Default()
			p, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
			if err != nil {
				t.Fatal(err)
			}
			store := lastcluster.NewLocalStore(100)
			tracker := lastcluster.NewTracker(store, lastcluster.Options{TTL: time.Minute, LookupTimeout: time.Second})
			p.SetLastClusterTracker(tracker)
			e := echo.New()
			e.Use(NewContextMiddleware(cfg))
			RegisterRoutes(e, NewHandlers(cfg, p, nil, nil))

			for range 2 {
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.payload))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				if tc.session != "" {
					req.Header.Set(HeaderMultiTurnSessionID, tc.session)
				}
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				tracker.Drain(context.Background())
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("X-Stargate-Cluster-Id"); got != "" {
					t.Fatalf("x-stargate-cluster-id leaked to the client: %q", got)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if want := []string{"", tc.wantHint}; strings.Join(hints, ",") != strings.Join(want, ",") {
				t.Fatalf("hints = %q, want %q", hints, want)
			}
			if tc.wantHint == "" && store.Len() != 0 {
				t.Fatalf("store entries = %d, want 0 for payload-derived sessions", store.Len())
			}
		})
	}
}
