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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/olrictest"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

// hintingStargate answers chat completions with x-stargate-cluster-id:
// cluster-b and reports the x-stargate-last-cluster-id of the latest request.
func hintingStargate(t *testing.T) (url string, lastHint func() string) {
	t.Helper()
	var (
		mu   sync.Mutex
		hint string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hint = r.Header.Get("X-Stargate-Last-Cluster-Id")
		mu.Unlock()
		w.Header().Set("X-Stargate-Cluster-Id", "cluster-b")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server.URL, func() string {
		mu.Lock()
		defer mu.Unlock()
		return hint
	}
}

func TestLastClusterStoreWiring(t *testing.T) {
	for _, tc := range []struct {
		name  string
		olric bool
	}{
		{name: "local store"},
		{name: "olric without rate limiter", olric: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.olric && testing.Short() {
				t.Skip("skipping embedded Olric test in -short mode")
			}
			t.Setenv("POD_NAMESPACE", "")
			url, lastHint := hintingStargate(t)
			cfg := testConfig(url)
			if tc.olric {
				cfg.Olric = olrictest.NodeConfig(t, nil)
			}
			cfg.Stargate.LastCluster.Enabled = true
			gateway := startGatewayWithConfig(t, cfg, protocols[0])

			// The write is asynchronous, so repeat the session's request until
			// a follow-up carries the hint.
			deadline := time.Now().Add(5 * time.Second)
			for lastHint() != "cluster-b" {
				if time.Now().After(deadline) {
					t.Fatalf("hint = %q, want cluster-b", lastHint())
				}
				status, body, err := gateway.post("/v1/chat/completions",
					`{"model":"fn-alpha/company-name/model-name","prompt_cache_key":"session-1","messages":[{"role":"user","content":"hello"}]}`)
				if err != nil || status != http.StatusOK {
					t.Fatalf("status=%d err=%v body=%s", status, err, body)
				}
			}
		})
	}
}

func TestLastClusterDisabledHasNoTracker(t *testing.T) {
	tracker, err := newLastClusterTracker(context.Background(), config.Default(), nil)
	if tracker != nil || err != nil {
		t.Fatalf("newLastClusterTracker() = %v, %v; want nil, nil", tracker, err)
	}
}

func TestGatewayShutdownReleasesStateOnceInOrder(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Stargate.LastCluster.Enabled = true
	inferenceProvider, err := provider.NewStargateProvider(cfg.Stargate)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(cfg, inferenceProvider, nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	stopLimiter, drain, stopOlric := g.state.stopLimiter, g.state.drain, g.state.stopOlric
	g.state.stopLimiter = func() { order = append(order, "limiter"); stopLimiter() }
	g.state.drain = func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("drain context has no deadline")
		}
		order = append(order, "drain")
		drain(ctx)
	}
	g.state.stopOlric = func(ctx context.Context) { order = append(order, "olric"); stopOlric(ctx) }

	for range 2 {
		if err := g.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	}
	if got := strings.Join(order, ","); got != "limiter,drain,olric" {
		t.Fatalf("shutdown steps = %s, want limiter,drain,olric once", got)
	}
}
