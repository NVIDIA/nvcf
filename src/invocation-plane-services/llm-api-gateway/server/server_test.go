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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
)

const (
	testWriteTimeout = 200 * time.Millisecond
	// testUpstreamDuration keeps every slow upstream response well past
	// testWriteTimeout so a server-wide write deadline would truncate it.
	testUpstreamDuration = 4 * testWriteTimeout
	testUpstreamEvents   = 8
)

func TestInferenceResponsesOutliveServerWriteTimeout(t *testing.T) {
	t.Parallel()

	gatewayURL := startGateway(t, slowStargate(t))

	for _, tc := range []struct {
		name     string
		path     string
		payload  string
		wantBody []string
	}{
		{
			name:     "streaming responses",
			path:     "/v1/responses",
			payload:  `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`,
			wantBody: []string{"event: response.completed", "delta-7"},
		},
		{
			name:     "non-streaming responses",
			path:     "/v1/responses",
			payload:  `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":false}`,
			wantBody: []string{`"status":"completed"`},
		},
		{
			name:     "streaming chat",
			path:     "/v1/chat/completions",
			payload:  `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			wantBody: []string{"delta-7", "data: [DONE]"},
		},
		{
			name:     "non-streaming chat",
			path:     "/v1/chat/completions",
			payload:  `{"model":"fn-alpha/company-name/model-name","messages":[{"role":"user","content":"hello"}],"stream":false}`,
			wantBody: []string{"delta-0delta-1", `"finish_reason":"stop"`},
		},
		{
			name:     "embeddings proxy",
			path:     "/v1/embeddings",
			payload:  `{"model":"fn-alpha/company-name/model-name","input":"hello"}`,
			wantBody: []string{`"embedding"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			status, body, err := post(gatewayURL+tc.path, tc.payload)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("request failed after %s: %v (partial body %q)", elapsed, err, body)
			}
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", status, body)
			}
			if elapsed < testUpstreamDuration {
				t.Fatalf("response took %s, want at least %s; upstream was not slow", elapsed, testUpstreamDuration)
			}
			for _, want := range tc.wantBody {
				if !strings.Contains(body, want) {
					t.Fatalf("body missing %q after %s: %s", want, elapsed, body)
				}
			}
		})
	}
}

func TestInferenceWriteTimeoutDisabled(t *testing.T) {
	t.Parallel()

	cfg := testConfig(slowStargate(t).URL)
	cfg.Server.InferenceWriteTimeout = 0
	gatewayURL := startGatewayWithConfig(t, cfg)

	status, body, err := post(
		gatewayURL+"/v1/responses",
		`{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`,
	)
	if err != nil {
		t.Fatalf("request failed: %v (partial body %q)", err, body)
	}
	if status != http.StatusOK || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("status = %d, body = %s; want completed stream", status, body)
	}
}

// TestInferenceWriteTimeoutStopsStalledClient checks that removing the
// whole-response deadline still leaves a bound on a client that stops
// reading: the gateway gives up and releases the upstream stream, including
// when the upstream goes quiet right after the client stalls.
func TestInferenceWriteTimeoutStopsStalledClient(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// events is sized well past kernel socket buffers so the gateway
		// blocks writing to the stalled client.
		events    int
		thenQuiet bool
	}{
		{name: "upstream keeps streaming", events: 16 * 1024},
		{name: "upstream goes quiet", events: 2 * 1024, thenQuiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstreamDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				event := "event: response.output_text.delta\n" +
					`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg","output_index":0,"content_index":0,"delta":"` +
					strings.Repeat("x", 16*1024) + `","logprobs":[]}` + "\n\n"
				for range tc.events {
					if _, err := io.WriteString(w, event); err != nil {
						return
					}
				}
				if tc.thenQuiet {
					w.(http.Flusher).Flush()
					// Only the gateway canceling its upstream request ends this.
					<-r.Context().Done()
				}
			}))
			t.Cleanup(upstream.Close)

			cfg := testConfig(upstream.URL)
			cfg.Server.WriteTimeout = time.Hour
			cfg.Server.InferenceWriteTimeout = testWriteTimeout
			gatewayURL := startGatewayWithConfig(t, cfg)

			conn, err := net.Dial("tcp", strings.TrimPrefix(gatewayURL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetReadBuffer(4 * 1024)
			}
			payload := `{"model":"fn-alpha/company-name/model-name","input":"hello","stream":true}`
			if _, err := fmt.Fprintf(conn,
				"POST /v1/responses HTTP/1.1\r\nHost: gateway\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
				len(payload), payload,
			); err != nil {
				t.Fatal(err)
			}

			select {
			case <-upstreamDone:
			case <-time.After(20 * time.Second):
				t.Fatal("gateway kept its upstream stream open for a client that stopped reading")
			}
		})
	}
}

func startGateway(t *testing.T, upstream *httptest.Server) string {
	t.Helper()
	return startGatewayWithConfig(t, testConfig(upstream.URL))
}

func testConfig(upstreamURL string) *config.Config {
	cfg := config.Default()
	cfg.Server.WriteTimeout = testWriteTimeout
	cfg.Stargate.URL = upstreamURL
	cfg.RateLimiter.Enabled = false
	return cfg
}

func startGatewayWithConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()

	inferenceProvider, err := provider.NewStargateProvider(cfg.Stargate)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, inferenceProvider, nil)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		served <- e.Server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(ctx)
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return "http://" + listener.Addr().String()
}

// slowStargate emits upstream responses that take testUpstreamDuration to
// finish, spread across testUpstreamEvents flushes.
func slowStargate(t *testing.T) *httptest.Server {
	t.Helper()

	gap := testUpstreamDuration / testUpstreamEvents
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		flusher, _ := w.(http.Flusher)
		writeEvent := func(event string) {
			_, _ = io.WriteString(w, event)
			if flusher != nil {
				flusher.Flush()
			}
		}

		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeEvent("event: response.created\n" +
				`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_slow","object":"response","status":"in_progress","created_at":1,"model":"company-name/model-name","output":[]}}` +
				"\n\n")
			for i := range testUpstreamEvents {
				time.Sleep(gap)
				writeEvent("event: response.output_text.delta\n" +
					fmt.Sprintf(`data: {"type":"response.output_text.delta","sequence_number":%d,"item_id":"msg_slow","output_index":0,"content_index":0,"delta":"delta-%d","logprobs":[]}`, i+1, i) +
					"\n\n")
			}
			writeEvent("event: response.completed\n" +
				fmt.Sprintf(`data: {"type":"response.completed","sequence_number":%d,"response":{"id":"resp_slow","object":"response","status":"completed","created_at":1,"model":"company-name/model-name","output":[],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":%d}}}`, testUpstreamEvents+1, testUpstreamEvents, testUpstreamEvents+1) +
				"\n\n")
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for i := range testUpstreamEvents {
				time.Sleep(gap)
				writeEvent(fmt.Sprintf(`data: {"id":"chatcmpl-slow","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{"content":"delta-%d"}}]}`, i) + "\n\n")
			}
			writeEvent(`data: {"id":"chatcmpl-slow","object":"chat.completion.chunk","created":1,"model":"company-name/model-name","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":8,"total_tokens":9}}` + "\n\n")
			writeEvent("data: [DONE]\n\n")
		case "/v1/embeddings":
			time.Sleep(testUpstreamDuration)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"company-name/model-name","usage":{"prompt_tokens":1,"total_tokens":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func post(url string, payload string) (int, string, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}
