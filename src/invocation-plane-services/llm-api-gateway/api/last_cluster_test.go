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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// lastClusterAPI serves the gateway routes against a fake Stargate that sets
// x-stargate-cluster-id and records each outbound request.
type lastClusterAPI struct {
	e        *echo.Echo
	tracker  *lastcluster.Tracker
	store    *lastcluster.LocalStore
	mu       sync.Mutex
	requests []upstreamRequest
}

type upstreamRequest struct {
	method, uri, body string
	header            http.Header
}

func newLastClusterAPI(t *testing.T, enabled bool) *lastClusterAPI {
	t.Helper()
	gw := &lastClusterAPI{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		gw.mu.Lock()
		gw.requests = append(gw.requests, upstreamRequest{method: r.Method, uri: r.URL.RequestURI(), body: string(body), header: r.Header.Clone()})
		gw.mu.Unlock()
		w.Header().Set("X-Stargate-Cluster-Id", "cluster-b")
		w.Header().Set(echo.HeaderContentType, "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/v1/messages":
			_, _ = io.WriteString(w, messagesTestSSE)
		case "/v1/responses":
			_, _ = io.WriteString(w, "event: response.completed\n"+
				`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"completed","created_at":1,"model":"company-name/model-name","output":[]}}`+"\n\n")
		default:
			_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Default()
	cfg.Stargate.LastCluster.Enabled = enabled
	p, err := provider.NewStargateProvider(config.StargateConfig{URL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	gw.store = lastcluster.NewLocalStore(100)
	gw.tracker = lastcluster.NewTracker(gw.store, lastcluster.Options{TTL: time.Minute, LookupTimeout: 20 * time.Millisecond})
	if enabled {
		p.SetLastClusterTracker(gw.tracker)
	}
	gw.e = echo.New()
	gw.e.Use(NewContextMiddleware(cfg))
	RegisterRoutes(gw.e, NewHandlers(cfg, p, nil, nil))
	return gw
}

func (a *lastClusterAPI) post(t *testing.T, path, payload string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	rec := httptest.NewRecorder()
	a.e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d: %s", path, rec.Code, rec.Body.String())
	}
	a.tracker.Drain(context.Background())
	return rec
}

func (a *lastClusterAPI) lastRequest() upstreamRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests[len(a.requests)-1]
}

func (a *lastClusterAPI) lastHint() string {
	return a.lastRequest().header.Get("X-Stargate-Last-Cluster-Id")
}

func TestLastClusterSessionFlows(t *testing.T) {
	sessionHeader := http.Header{HeaderMultiTurnSessionID: []string{"client-session-1"}}
	for _, tc := range []struct {
		name    string
		path    string
		payload string
		header  http.Header
	}{
		{
			name:    "chat/prompt_cache_key",
			path:    "/v1/chat/completions",
			payload: `{"model":"fn-alpha/company-name/model-name","prompt_cache_key":"pck-1","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:    "chat/header",
			path:    "/v1/chat/completions",
			payload: `{"model":"fn-alpha/company-name/model-name","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			header:  sessionHeader,
		},
		{
			name:    "responses/prompt_cache_key",
			path:    "/v1/responses",
			payload: `{"model":"fn-alpha/company-name/model-name","stream":true,"prompt_cache_key":"pck-1","input":"hello"}`,
		},
		{
			name:    "responses/conversation_id",
			path:    "/v1/responses",
			payload: `{"model":"fn-alpha/company-name/model-name","stream":true,"conversation":{"id":"conv-1"},"input":"hello"}`,
		},
		{
			name:    "responses/header",
			path:    "/v1/responses",
			payload: `{"model":"fn-alpha/company-name/model-name","stream":true,"input":"hello"}`,
			header:  sessionHeader,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newLastClusterAPI(t, true)

			gw.post(t, tc.path, tc.payload, tc.header)
			if hint := gw.lastHint(); hint != "" {
				t.Fatalf("first request hint = %q, want none", hint)
			}
			rec := gw.post(t, tc.path, tc.payload, tc.header)
			if hint := gw.lastHint(); hint != "cluster-b" {
				t.Fatalf("second request hint = %q, want cluster-b", hint)
			}
			if got := rec.Header().Get("X-Stargate-Cluster-Id"); got != "" {
				t.Fatalf("x-stargate-cluster-id leaked to the client: %q", got)
			}
		})
	}
}

func TestLastClusterPayloadSessionChains(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		for _, stream := range []bool{false, true} {
			name := fixture.name
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				gw := newLastClusterAPI(t, true)
				path := lastClusterFixturePath(fixture.endpoint)
				for i, body := range fixture.turns {
					if stream {
						body = strings.Replace(body, "{", `{"stream":true,`, 1)
					}
					gw.post(t, path, body, nil)
					wantHint := "cluster-b"
					if i == 0 {
						wantHint = ""
					}
					if hint := gw.lastHint(); hint != wantHint {
						t.Fatalf("turn %d hint = %q, want %q", i+1, hint, wantHint)
					}
				}
				edited := strings.Replace(fixture.turns[1], `"hello"`, `"edited"`, 1)
				if stream {
					edited = strings.Replace(edited, "{", `{"stream":true,`, 1)
				}
				gw.post(t, path, edited, nil)
				if hint := gw.lastHint(); hint != "" {
					t.Fatalf("edited history hint = %q, want none", hint)
				}
				if n := gw.store.Len(); n != 4 {
					t.Fatalf("store entries = %d, want 4 full-history writes", n)
				}
			})
		}
	}
}

func lastClusterFixturePath(endpoint string) string {
	switch endpoint {
	case lastcluster.EndpointChatCompletions:
		return "/v1/chat/completions"
	case lastcluster.EndpointResponses:
		return "/v1/responses"
	default:
		return "/v1/messages"
	}
}

// TestLastClusterPayloadFeatureOff checks that enabling the feature changes
// nothing on the wire except the last-cluster hint. Only X-Request-Id, a
// per-request UUID, is excluded from the comparison.
func TestLastClusterPayloadFeatureOff(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		for _, stream := range []bool{false, true} {
			name := fixture.name
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				enabled, disabled := newLastClusterAPI(t, true), newLastClusterAPI(t, false)
				path := lastClusterFixturePath(fixture.endpoint)
				for i, body := range fixture.turns {
					if stream {
						body = strings.Replace(body, "{", `{"stream":true,`, 1)
					}
					on := enabled.post(t, path, body, nil)
					off := disabled.post(t, path, body, nil)
					onReq, offReq := enabled.lastRequest(), disabled.lastRequest()
					for _, req := range []upstreamRequest{onReq, offReq} {
						if req.header.Get(HeaderRequestID) == "" {
							t.Fatalf("turn %d missing %s", i+1, HeaderRequestID)
						}
						req.header.Del(HeaderRequestID)
					}
					wantHint := "cluster-b"
					if i == 0 {
						wantHint = ""
					}
					if hint := onReq.header.Get("X-Stargate-Last-Cluster-Id"); hint != wantHint {
						t.Fatalf("turn %d enabled hint = %q, want %q", i+1, hint, wantHint)
					}
					if hint := offReq.header.Get("X-Stargate-Last-Cluster-Id"); hint != "" {
						t.Fatalf("turn %d disabled hint = %q, want none", i+1, hint)
					}
					onReq.header.Del("X-Stargate-Last-Cluster-Id")
					if !reflect.DeepEqual(onReq, offReq) {
						t.Fatalf("turn %d feature flag changed outbound request:\non:  %+v\noff: %+v", i+1, onReq, offReq)
					}
					expectedAffinity := lastClusterFixtureAffinity(t, fixture.endpoint, body)
					if got := offReq.header.Get("X-Cache-Affinity-Key"); got != expectedAffinity {
						t.Fatalf("turn %d affinity = %q, want %q", i+1, got, expectedAffinity)
					}
					if on.Header().Get(HeaderMultiTurnSessionID) != expectedAffinity || off.Header().Get(HeaderMultiTurnSessionID) != expectedAffinity {
						t.Fatalf("turn %d changed session response header", i+1)
					}
				}
				if n := disabled.store.Len(); n != 0 {
					t.Fatalf("disabled store entries = %d, want 0", n)
				}
			})
		}
	}
}

func lastClusterFixtureAffinity(t *testing.T, endpoint, body string) string {
	t.Helper()
	var payload []byte
	switch endpoint {
	case lastcluster.EndpointChatCompletions:
		var req models.ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		var err error
		payload, err = json.Marshal(req.Messages)
		if err != nil {
			t.Fatal(err)
		}
	case lastcluster.EndpointResponses:
		var req openairesponses.CreateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		var err error
		payload, err = json.Marshal(req.Input)
		if err != nil {
			t.Fatal(err)
		}
	default:
		var req messagesRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		payload = req.Messages
	}
	return affinityKey(sessionAffinitySourcePayload, payload)
}

func TestClientLastClusterHeaderNeverForwarded(t *testing.T) {
	forged := http.Header{"X-Stargate-Last-Cluster-Id": []string{"client-forged"}}
	for _, tc := range []struct{ path, payload string }{
		{"/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","stream":true,"prompt_cache_key":"pck-2","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/responses", `{"model":"fn-alpha/company-name/model-name","stream":true,"input":"hello"}`},
		{"/v1/responses", `{"model":"fn-alpha/company-name/model-name","stream":true,"prompt_cache_key":"pck-2","input":"hello"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			gw := newLastClusterAPI(t, true)
			gw.post(t, tc.path, tc.payload, forged)
			if hint := gw.lastHint(); hint != "" {
				t.Fatalf("first request hint = %q, want the forged value dropped", hint)
			}
			gw.post(t, tc.path, tc.payload, forged)
			if hint := gw.lastHint(); hint == "client-forged" {
				t.Fatal("client-supplied hint reached Stargate")
			}
		})
	}
}
