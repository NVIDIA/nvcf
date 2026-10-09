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

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/olrictest"
)

// hintingStargate answers like Stargate with x-stargate-cluster-id set and
// records the x-stargate-last-cluster-id each request carried.
type hintingStargate struct {
	*httptest.Server
	mu    sync.Mutex
	hints []string
}

func newHintingStargate(t *testing.T, clusterID string) *hintingStargate {
	t.Helper()
	s := &hintingStargate{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.hints = append(s.hints, r.Header.Get("X-Stargate-Last-Cluster-Id"))
		s.mu.Unlock()
		w.Header().Set("X-Stargate-Cluster-Id", clusterID)
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"company-name/model-name","usage":{"prompt_tokens":1,"total_tokens":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *hintingStargate) lastHint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hints) == 0 {
		return ""
	}
	return s.hints[len(s.hints)-1]
}

const lastClusterChatPayload = `{"model":"fn-alpha/company-name/model-name","prompt_cache_key":"session-1","messages":[{"role":"user","content":"hello"}]}`

// assertSecondRequestCarriesHint sends a first request, then repeats the
// session's request until the background write is visible.
func assertSecondRequestCarriesHint(t *testing.T, gateway *testGateway, upstream *hintingStargate) {
	t.Helper()
	status, body, err := gateway.post("/v1/chat/completions", lastClusterChatPayload)
	if err != nil || status != http.StatusOK {
		t.Fatalf("first request status=%d err=%v body=%s", status, err, body)
	}
	if hint := upstream.lastHint(); hint != "" {
		t.Fatalf("first request hint = %q, want none", hint)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body, err := gateway.post("/v1/chat/completions", lastClusterChatPayload)
		if err != nil || status != http.StatusOK {
			t.Fatalf("follow-up request status=%d err=%v body=%s", status, err, body)
		}
		if upstream.lastHint() == "cluster-b" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-up hint = %q, want cluster-b", upstream.lastHint())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOlricWithoutRateLimiterServesLastCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded Olric test in -short mode")
	}
	t.Setenv("POD_NAMESPACE", "")
	upstream := newHintingStargate(t, "cluster-b")
	cfg := testConfig(upstream.URL)
	cfg.RateLimiter.Enabled = false
	cfg.Olric = olrictest.NodeConfig(t, nil)
	cfg.Stargate.LastCluster.Enabled = true

	assertSecondRequestCarriesHint(t, startGatewayWithConfig(t, cfg, protocols[0]), upstream)
}

func TestLocalStoreServesLastClusterWhenOlricOff(t *testing.T) {
	upstream := newHintingStargate(t, "cluster-b")
	cfg := testConfig(upstream.URL)
	cfg.Olric.Enabled = false
	cfg.Stargate.LastCluster.Enabled = true

	assertSecondRequestCarriesHint(t, startGatewayWithConfig(t, cfg, protocols[0]), upstream)
}

func TestLastClusterDisabledSendsNoHint(t *testing.T) {
	upstream := newHintingStargate(t, "cluster-b")
	cfg := testConfig(upstream.URL)
	gateway := startGatewayWithConfig(t, cfg, protocols[0])

	for i := 0; i < 3; i++ {
		status, body, err := gateway.post("/v1/chat/completions", lastClusterChatPayload)
		if err != nil || status != http.StatusOK {
			t.Fatalf("request status=%d err=%v body=%s", status, err, body)
		}
		if hint := upstream.lastHint(); hint != "" {
			t.Fatalf("request %d hint = %q, want none while disabled", i, hint)
		}
	}
}

func TestStargateClusterIDStrippedFromClientResponses(t *testing.T) {
	upstream := newHintingStargate(t, "cluster-b")
	cfg := testConfig(upstream.URL)
	cfg.Stargate.LastCluster.Enabled = true
	gateway := startGatewayWithConfig(t, cfg, protocols[0])

	for _, tc := range []struct{ path, payload string }{
		{"/v1/chat/completions", lastClusterChatPayload},
		{"/v1/chat/completions", `{"model":"fn-alpha/company-name/model-name","prompt_cache_key":"s","stream":true,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/embeddings", `{"model":"fn-alpha/company-name/model-name","input":"hello"}`},
	} {
		req, err := http.NewRequest(http.MethodPost, gateway.url+tc.path, strings.NewReader(tc.payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Stargate-Last-Cluster-Id", "client-forged")
		resp, err := gateway.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", tc.path, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Stargate-Cluster-Id"); got != "" {
			t.Fatalf("%s leaked x-stargate-cluster-id %q to the client", tc.path, got)
		}
		if hint := upstream.lastHint(); hint == "client-forged" {
			t.Fatalf("%s forwarded the client-supplied last-cluster hint", tc.path)
		}
	}
}
