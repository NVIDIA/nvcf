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
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/uis/nvcf-spark-ui/backend/internal/middleware"
)

// TestDefaultTimeoutsAreSet guards the shipped defaults: a zero value means the
// phase has no deadline, which is the resource exhaustion these guard against.
func TestDefaultTimeoutsAreSet(t *testing.T) {
	defaults := map[string]time.Duration{
		readHeaderTimeoutSeconds: defaultReadHeaderTimeoutSeconds * time.Second,
		readTimeoutSeconds:       defaultReadTimeoutSeconds * time.Second,
		writeTimeoutSeconds:      defaultWriteTimeoutSeconds * time.Second,
		idleTimeoutSeconds:       defaultIdleTimeoutSeconds * time.Second,
	}
	for env := range defaults {
		t.Setenv(env, "")
	}

	timeouts, err := timeoutsFromEnv()
	if err != nil {
		t.Fatalf("timeoutsFromEnv() error = %v", err)
	}

	got := map[string]time.Duration{
		readHeaderTimeoutSeconds: timeouts.readHeader,
		readTimeoutSeconds:       timeouts.read,
		writeTimeoutSeconds:      timeouts.write,
		idleTimeoutSeconds:       timeouts.idle,
	}
	for env, want := range defaults {
		if got[env] <= 0 {
			t.Errorf("default for %s = %v, want a positive deadline", env, got[env])
		}
		if got[env] != want {
			t.Errorf("%s = %v, want %v", env, got[env], want)
		}
	}

	if timeouts.readHeader > timeouts.read {
		t.Errorf("read header timeout (%v) exceeds read timeout (%v); headers would "+
			"never be the limiting deadline", timeouts.readHeader, timeouts.read)
	}
}

func TestTimeoutsFromEnvOverrides(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want serverTimeouts
	}{
		{
			name: "each timeout is overridden in seconds",
			env: map[string]string{
				readHeaderTimeoutSeconds: "5",
				readTimeoutSeconds:       "15",
				writeTimeoutSeconds:      "45",
				idleTimeoutSeconds:       "90",
			},
			want: serverTimeouts{
				readHeader: 5 * time.Second,
				read:       15 * time.Second,
				write:      45 * time.Second,
				idle:       90 * time.Second,
			},
		},
		{
			name: "unset variables keep their defaults",
			env:  map[string]string{writeTimeoutSeconds: "600"},
			want: serverTimeouts{
				readHeader: defaultReadHeaderTimeoutSeconds * time.Second,
				read:       defaultReadTimeoutSeconds * time.Second,
				write:      600 * time.Second,
				idle:       defaultIdleTimeoutSeconds * time.Second,
			},
		},
		{
			name: "zero disables that deadline",
			env:  map[string]string{writeTimeoutSeconds: "0"},
			want: serverTimeouts{
				readHeader: defaultReadHeaderTimeoutSeconds * time.Second,
				read:       defaultReadTimeoutSeconds * time.Second,
				write:      0,
				idle:       defaultIdleTimeoutSeconds * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, env := range []string{
				readHeaderTimeoutSeconds, readTimeoutSeconds,
				writeTimeoutSeconds, idleTimeoutSeconds,
			} {
				t.Setenv(env, tt.env[env])
			}

			got, err := timeoutsFromEnv()
			if err != nil {
				t.Fatalf("timeoutsFromEnv() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("timeoutsFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestTimeoutsFromEnvRejectsBadValues covers the chart-typo case: an unusable
// value must fail startup rather than silently fall back to the default.
func TestTimeoutsFromEnvRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "not a number", value: "30s"},
		{name: "fractional", value: "1.5"},
		{name: "negative", value: "-1"},
		{name: "whitespace", value: " 30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(readHeaderTimeoutSeconds, tt.value)

			if _, err := timeoutsFromEnv(); err == nil {
				t.Errorf("timeoutsFromEnv() with %s=%q returned no error, want one",
					readHeaderTimeoutSeconds, tt.value)
			}
		})
	}
}

// writeBuild lays out a minimal UI build the way Vite emits it: index.html, a
// content-hashed asset under assets/, and a root-level file.
func writeBuild(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html":        "<!doctype html><title>app</title>",
		"assets/app-abc.js": "console.log('app')",
		"favicon.ico":       "icon",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestStatus(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	if rec := serve(h, http.MethodGet, "/status"); rec.Code != http.StatusOK {
		t.Errorf("GET /status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestStaticRoutes(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	tests := []struct {
		name      string
		target    string
		wantCache string
		wantBody  string
	}{
		{"hashed asset is cached forever", "/assets/app-abc.js", "public, max-age=31536000, immutable", "console.log('app')"},
		{"root file is revalidated", "/favicon.ico", "no-cache", "icon"},
		{"root serves index.html", "/", "no-store", "<title>app</title>"},
		{"client-side route serves index.html", "/registry", "no-store", "<title>app</title>"},
		{"nested client-side route serves index.html", "/recipes/glm-5-3", "no-store", "<title>app</title>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodGet, tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", tt.target, rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestStaticHeadServesHeadersOnly(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	rec := serve(h, http.MethodHead, "/favicon.ico")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /favicon.ico = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body has %d bytes, want none", rec.Body.Len())
	}
}

func TestStaticRejectsWrites(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	for _, target := range []string{"/", "/registry", "/favicon.ico", "/status"} {
		t.Run(target, func(t *testing.T) {
			rec := serve(h, http.MethodPost, target)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s = %d, want %d", target, rec.Code, http.StatusMethodNotAllowed)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
		})
	}
}

// TestAPIPathsNeverFallBackToIndex guards against unknown API paths getting
// index.html with 200, which the UI would fail to parse as JSON.
func TestAPIPathsNeverFallBackToIndex(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	tests := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/v1/typo"},
		{http.MethodGet, "/api/"},
		{http.MethodGet, "/v1/unknown"},
		{http.MethodPost, "/v1/images/generations"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			rec := serve(h, tt.method, tt.target)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want %d", tt.method, tt.target, rec.Code, http.StatusNotFound)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"Not Found"}` {
				t.Errorf("body = %q, want the gateway's {\"message\"} shape", got)
			}
		})
	}
}

func TestMiddlewareChainIsApplied(t *testing.T) {
	var logs bytes.Buffer
	h := newHandler(zerolog.New(&logs), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	rec := serve(h, http.MethodPost, "/v1/unknown")

	out := logs.String()
	for _, want := range []string{`"message":"Entry Audit"`, `"message":"Exit Audit"`, `"status":404`, `"url":"POST /v1/unknown"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit logs missing %s; the middleware chain is not wired:\n%s", want, out)
		}
	}

	// The gateway assigns request ids to forwarded responses; the BFF must not
	// add its own, or forwarded responses would carry two X-Request-Id values.
	if got := rec.Header().Values("X-Request-Id"); len(got) != 0 {
		t.Errorf("BFF set X-Request-Id %v, want none", got)
	}
}

// TestMissingBuildLeavesTheAPIUp shows a server without a UI build still serves
// its API; pages get the plain 404 nvcf-ui gives.
func TestMissingBuildLeavesTheAPIUp(t *testing.T) {
	h := newHandler(zerolog.Nop(), filepath.Join(t.TempDir(), "missing"), noGateway, uiConfig{}, noRecipes)
	for target, want := range map[string]int{
		"/status":      http.StatusOK,
		"/registry":    http.StatusNotFound,
		"/api/v1/typo": http.StatusNotFound,
	} {
		if rec := serve(h, http.MethodGet, target); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
	}
}

// writeCompressedBuild is writeBuild plus the compressed copies the UI build
// writes next to text assets: brotli and gzip for the script, gzip only for
// index.html. The copies hold marker text, so a test can tell which was sent.
func writeCompressedBuild(t *testing.T) string {
	t.Helper()
	root := writeBuild(t)
	for name, body := range map[string]string{
		"assets/app-abc.js.br": "br-bytes",
		"assets/app-abc.js.gz": "gzip-bytes",
		"index.html.gz":        "gzip-index",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestStaticServesPrecompressedCopies(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeCompressedBuild(t), noGateway, uiConfig{}, noRecipes)
	tests := []struct {
		name           string
		target         string
		acceptEncoding string
		wantEncoding   string
		wantBody       string
		wantType       string
	}{
		{"brotli preferred", "/assets/app-abc.js", "gzip, deflate, br", "br", "br-bytes", "text/javascript; charset=utf-8"},
		{"gzip when brotli isn't accepted", "/assets/app-abc.js", "gzip", "gzip", "gzip-bytes", "text/javascript; charset=utf-8"},
		{"brotli refused with q=0", "/assets/app-abc.js", "br;q=0, gzip", "gzip", "gzip-bytes", "text/javascript; charset=utf-8"},
		{"wildcard accepts brotli", "/assets/app-abc.js", "*", "br", "br-bytes", "text/javascript; charset=utf-8"},
		{"explicit refusal beats the wildcard", "/assets/app-abc.js", "br;q=0, *", "gzip", "gzip-bytes", "text/javascript; charset=utf-8"},
		{"original without Accept-Encoding", "/assets/app-abc.js", "", "", "console.log('app')", "text/javascript; charset=utf-8"},
		{"identity only", "/assets/app-abc.js", "identity", "", "console.log('app')", "text/javascript; charset=utf-8"},
		{"index.html through the SPA fallback", "/registry", "br, gzip", "gzip", "gzip-index", "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", tt.target, rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}
			if got := rec.Body.String(); !strings.Contains(got, tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", got)
			}
		})
	}
}

func TestStaticWithoutCompressedCopies(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeCompressedBuild(t), noGateway, uiConfig{}, noRecipes)
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want none for a file without copies", got)
	}
}

// TestCompressedCopiesAreNotRoutes checks that the copies are only served in
// place of the file they compress: their own paths fall through to the SPA.
func TestCompressedCopiesAreNotRoutes(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeCompressedBuild(t), noGateway, uiConfig{}, noRecipes)
	rec := serve(h, http.MethodGet, "/assets/app-abc.js.br")
	if !strings.Contains(rec.Body.String(), "<title>app</title>") {
		t.Errorf("GET /assets/app-abc.js.br = %q, want the SPA fallback", rec.Body.String())
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newHandler(zerolog.Nop(), writeBuild(t), noGateway, uiConfig{}, noRecipes)
	for _, target := range []string{"/", "/registry", "/assets/app-abc.js", "/api/v1/config", "/api/v1/typo", "/status"} {
		t.Run(target, func(t *testing.T) {
			rec := serve(h, http.MethodGet, target)
			if got := rec.Header().Get("Content-Security-Policy"); got != middleware.ContentSecurityPolicy {
				t.Errorf("Content-Security-Policy = %q, want the BFF's policy", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}
