// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// noGateway points the forwarding routes at a host that never answers, for
// tests about other routes.
var noGateway = gatewayConfig{
	url:       &url.URL{Scheme: "https", Host: "gateway.invalid"},
	transport: http.DefaultTransport,
}

// syncBuffer is a log sink that is safe to read while a live server writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// keyFile writes key where GATEWAY_API_KEY_PATH can point, as the mounted
// Secret does in the cluster.
func keyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeGateway serves h over TLS and returns the BFF settings for it. The fake's
// certificate is trusted through GATEWAY_CA_PATH, as the stack CA is in the
// cluster.
func fakeGateway(t *testing.T, h http.Handler, key string) gatewayConfig {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.crt")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, block, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gatewayURL, srv.URL)
	t.Setenv(gatewayCAPath, ca)
	t.Setenv(gatewayAPIKeyPath, keyFile(t, key))
	gw, err := gatewayFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

// startBFF runs the BFF on a real listener, which flushing and connection
// deadlines need. timeout sets the server's read and write timeouts.
func startBFF(t *testing.T, gw gatewayConfig, timeout time.Duration) (*httptest.Server, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	srv := httptest.NewUnstartedServer(newHandler(zerolog.New(logs), writeBuild(t), gw, uiConfig{}, noRecipes))
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = timeout, timeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, logs
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGatewayReadRoutesAreForwardedUnchanged(t *testing.T) {
	type upstream struct{ method, uri, host, auth, cookie, forwardedFor string }
	seen := make(chan upstream, 1)
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- upstream{r.Method, r.RequestURI, r.Host, r.Header.Get("Authorization"),
			r.Header.Get("Cookie"), r.Header.Get("X-Forwarded-For")}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "gw-123")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}), "test-key")
	h := newHandler(zerolog.Nop(), writeBuild(t), gw, uiConfig{}, noRecipes)

	for _, target := range []string{
		"/v1/registry",
		"/v1/registry?model=GLM-5.3-UD-IQ2_M",
		"/v1/models",
		"/v1/models/GLM-5.3-UD-IQ2_M",
		"/v1/models/meta/llama-3.1-8b-instruct",
		"/v1/models/meta%2Fllama-3.1-8b-instruct",
	} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Authorization", "Bearer browser-token")
			req.Header.Set("Cookie", "session=browser")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Body.String() != `{"object":"list","data":[]}` {
				t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
			}
			if id := rec.Header().Get("X-Request-Id"); id != "gw-123" {
				t.Errorf("X-Request-Id = %q, want the gateway's", id)
			}
			got := receive(t, seen, "the forwarded request")
			if got.method != http.MethodGet || got.uri != target {
				t.Errorf("gateway got %s %s, want GET %s", got.method, got.uri, target)
			}
			if got.auth != "Bearer test-key" {
				t.Errorf("Authorization = %q, want the BFF's key", got.auth)
			}
			if got.cookie != "" {
				t.Errorf("Cookie = %q, want none", got.cookie)
			}
			if got.host != gw.url.Host || got.forwardedFor == "" {
				t.Errorf("Host = %q and X-Forwarded-For = %q, want the gateway host and the client", got.host, got.forwardedFor)
			}
		})
	}
}

func TestGatewayErrorsPassThrough(t *testing.T) {
	body := `{"error":{"code":"overloaded_error","message":"Inference capacity is temporarily unavailable.","param":"","type":"overloaded_error"}}`
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = io.WriteString(w, body)
	}), "test-key")

	rec := serve(newHandler(zerolog.Nop(), writeBuild(t), gw, uiConfig{}, noRecipes), http.MethodPost, "/v1/chat/completions")
	if rec.Code != 529 || rec.Body.String() != body {
		t.Errorf("response = %d %q, want the gateway's 529 unchanged", rec.Code, rec.Body.String())
	}
}

func TestOnlyTheUsedGatewayRoutesAreForwarded(t *testing.T) {
	var hits atomic.Int32
	gw := fakeGateway(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }), "test-key")
	h := newHandler(zerolog.Nop(), writeBuild(t), gw, uiConfig{}, noRecipes)

	for _, tt := range []struct{ method, target string }{
		{http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/v1/embeddings"},
		{http.MethodPost, "/v1/images/generations"},
		{http.MethodPost, "/v1/registry"},
		{http.MethodGet, "/v1/chat/completions"},
		{http.MethodDelete, "/v1/models/GLM-5.3-UD-IQ2_M"},
	} {
		if rec := serve(h, tt.method, tt.target); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.target, rec.Code, http.StatusNotFound)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("gateway received %d requests, want none", n)
	}
}

func TestUnreachableGatewayIsAJSON502(t *testing.T) {
	gw := fakeGateway(t, http.NotFoundHandler(), "test-key")
	gw.url = &url.URL{Scheme: "https", Host: "127.0.0.1:1"}
	var logs bytes.Buffer

	rec := serve(newHandler(zerolog.New(&logs), writeBuild(t), gw, uiConfig{}, noRecipes), http.MethodGet, "/v1/registry")
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = %d %q, want a JSON 502", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"LLM API Gateway unavailable"}` {
		t.Errorf("body = %s", got)
	}
	if !strings.Contains(logs.String(), "upstream 127.0.0.1:1 error") {
		t.Errorf("the failure was not logged:\n%s", logs.String())
	}
}

func TestUntrustedGatewayCertificateIsRejected(t *testing.T) {
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "test-key")
	transport, err := gatewayTransport("")
	if err != nil {
		t.Fatal(err)
	}
	gw.transport = transport

	if rec := serve(newHandler(zerolog.Nop(), writeBuild(t), gw, uiConfig{}, noRecipes), http.MethodGet, "/v1/registry"); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d without the fake's CA", rec.Code, http.StatusBadGateway)
	}
}

func TestSlowGatewayReadsTimeOut(t *testing.T) {
	gw := fakeGateway(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}), "test-key")
	gw.timeout = 50 * time.Millisecond

	rec := serve(newHandler(zerolog.Nop(), writeBuild(t), gw, uiConfig{}, noRecipes), http.MethodGet, "/v1/registry")
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"LLM API Gateway timed out"}` {
		t.Errorf("body = %s", got)
	}
}

func TestChatStreamsEachEventAsItArrives(t *testing.T) {
	release := make(chan struct{})
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "gw-chat-1")
		_, _ = io.WriteString(w, "data: {\"n\":1}\n\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}), "test-key")
	bff, logs := startBFF(t, gw, time.Minute)

	resp, err := http.Post(bff.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); line != "" {
				lines <- line
			}
		}
		if err := scanner.Err(); err != nil {
			lines <- "read error: " + err.Error()
		}
	}()

	// The gateway holds [DONE] back until the first event arrives, so this
	// only passes if the BFF flushed that event on its own.
	if line := receive(t, lines, "the first event"); line != `data: {"n":1}` {
		t.Fatalf("first line = %q", line)
	}
	close(release)
	if line := receive(t, lines, "[DONE]"); line != "data: [DONE]" {
		t.Errorf("second line = %q", line)
	}
	eventually(t, func() bool { return strings.Contains(logs.String(), `"gateway_request_id":"gw-chat-1"`) },
		"the gateway request id in the access log")
}

func TestChatStreamsOutliveTheServerTimeouts(t *testing.T) {
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/registry" {
			time.Sleep(600 * time.Millisecond)
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 6 {
			_, _ = fmt.Fprintf(w, "data: {\"n\":%d}\n\n", i)
			_ = http.NewResponseController(w).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}), "test-key")
	bff, _ := startBFF(t, gw, 300*time.Millisecond)

	t.Run("a 600 ms chat stream completes", func(t *testing.T) {
		resp, err := http.Post(bff.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("stream cut off after %q: %v", body, err)
		}
		if strings.Count(string(body), `data: {"n"`) != 6 || !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
			t.Errorf("body = %q, want 6 events and [DONE]", body)
		}
	})

	// The same server cuts an ordinary response that outlives its timeouts,
	// which shows the chat route's own deadlines are what kept it alive.
	t.Run("an ordinary 600 ms response is cut", func(t *testing.T) {
		resp, err := http.Get(bff.URL + "/v1/registry")
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Error("the registry response outlived the 300 ms server timeouts")
		}
	})
}

func TestMidStreamGatewayFailureBreaksTheBrowserStream(t *testing.T) {
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"n\":1}\n\n")
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	}), "test-key")
	bff, logs := startBFF(t, gw, time.Minute)

	resp, err := http.Post(bff.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, err := io.ReadAll(resp.Body); err == nil {
		t.Errorf("stream ended cleanly after %q, want a broken connection", body)
	}
	if strings.Contains(logs.String(), "Recovered from panic") {
		t.Errorf("the aborted stream was logged as a panic:\n%s", logs.String())
	}
}

func TestClosingTheBrowserStreamCancelsTheGatewayRequest(t *testing.T) {
	cancelled := make(chan struct{})
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"n\":1}\n\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	}), "test-key")
	bff, _ := startBFF(t, gw, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bff.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	receive(t, cancelled, "the gateway request to be cancelled")
}

func TestGatewayFromEnv(t *testing.T) {
	validKey := keyFile(t, "test-key")
	setEnv := func(t *testing.T, overrides map[string]string) {
		for _, name := range []string{gatewayURL, gatewayCAPath, gatewayTimeoutSeconds, chatTimeoutSeconds} {
			t.Setenv(name, overrides[name])
		}
		key, ok := overrides[gatewayAPIKeyPath]
		if !ok {
			key = validKey
		}
		t.Setenv(gatewayAPIKeyPath, key)
	}

	t.Run("defaults", func(t *testing.T) {
		setEnv(t, nil)
		gw, err := gatewayFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if gw.url.String() != defaultGatewayURL || gw.timeout != 15*time.Second || gw.chatTimeout != 10*time.Minute || gw.key != "test-key" {
			t.Errorf("got %s, %s, %s and key %q", gw.url, gw.timeout, gw.chatTimeout, gw.key)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		setEnv(t, map[string]string{gatewayURL: "http://127.0.0.1:18080/", gatewayTimeoutSeconds: "5", chatTimeoutSeconds: "0"})
		gw, err := gatewayFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if gw.url.String() != "http://127.0.0.1:18080" || gw.timeout != 5*time.Second || gw.chatTimeout != 0 {
			t.Errorf("got %s, %s, %s", gw.url, gw.timeout, gw.chatTimeout)
		}
	})

	notPEM := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, overrides := range map[string]map[string]string{
		"no scheme":       {gatewayURL: "llm-api-gateway:8080"},
		"other scheme":    {gatewayURL: "ftp://llm-api-gateway"},
		"no host":         {gatewayURL: "https://"},
		"path":            {gatewayURL: "https://llm-api-gateway:8080/v1"},
		"query":           {gatewayURL: "https://llm-api-gateway:8080?x=1"},
		"unparsable":      {gatewayURL: "https://[::1"},
		"missing CA":      {gatewayCAPath: filepath.Join(t.TempDir(), "missing.crt")},
		"CA not PEM":      {gatewayCAPath: notPEM},
		"bad timeout":     {gatewayTimeoutSeconds: "soon"},
		"negative chat":   {chatTimeoutSeconds: "-1"},
		"fractional read": {gatewayTimeoutSeconds: "1.5"},
		"missing key":     {gatewayAPIKeyPath: filepath.Join(t.TempDir(), "missing-key")},
		"empty key":       {gatewayAPIKeyPath: keyFile(t, "  ")},
		"two-token key":   {gatewayAPIKeyPath: keyFile(t, "demo key")},
	} {
		t.Run(name, func(t *testing.T) {
			setEnv(t, overrides)
			if _, err := gatewayFromEnv(); err == nil {
				t.Error("gatewayFromEnv accepted an invalid setting")
			}
		})
	}
}

// TestOversizedChatRequestsAreRejected checks the cap on chat bodies, with and
// without a Content-Length, in the gateway's own 413 shape.
func TestOversizedChatRequestsAreRejected(t *testing.T) {
	var forwarded atomic.Int64
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		forwarded.Add(n)
		w.WriteHeader(http.StatusOK)
	}), "key")
	bff, _ := startBFF(t, gw, 10*time.Second)
	oversized := strings.Repeat("x", maxChatBody+1)

	tests := []struct {
		name string
		body io.Reader
	}{
		{"declared length", strings.NewReader(oversized)},
		// io.MultiReader hides the length, so the request is chunked.
		{"chunked", io.MultiReader(strings.NewReader(oversized))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := http.Post(bff.URL+"/v1/chat/completions", "application/json", tt.body)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)

			if res.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d (body %s)", res.StatusCode, http.StatusRequestEntityTooLarge, body)
			}
			if got := strings.TrimSpace(string(body)); got != `{"message":"Request Entity Too Large"}` {
				t.Errorf("body = %s, want the gateway's 413 shape", got)
			}
		})
	}
	if n := forwarded.Load(); n > maxChatBody {
		t.Errorf("gateway received %d bytes, want at most %d", n, maxChatBody)
	}
}

func TestChatRequestsUpToTheCapAreForwarded(t *testing.T) {
	received := make(chan int, 1)
	gw := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received <- int(n)
		w.WriteHeader(http.StatusOK)
	}), "key")
	bff, _ := startBFF(t, gw, 10*time.Second)

	res, err := http.Post(bff.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(strings.Repeat("x", maxChatBody)))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if n := receive(t, received, "the forwarded body"); n != maxChatBody {
		t.Errorf("gateway received %d bytes, want %d", n, maxChatBody)
	}
}
