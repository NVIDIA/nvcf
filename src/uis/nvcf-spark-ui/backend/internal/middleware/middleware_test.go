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

package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

// recordingHandler records whether it was invoked and writes a fixed response.
type recordingHandler struct {
	called bool
	status int
	body   string
}

func (rh *recordingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	rh.called = true
	if rh.status != 0 {
		w.WriteHeader(rh.status)
	}
	if rh.body != "" {
		_, _ = w.Write([]byte(rh.body))
	}
}

// withLogger injects a zerolog logger into the request context so hlog.FromRequest
// resolves to it, capturing emitted logs into buf.
func withLogger(buf *bytes.Buffer, h http.Handler) http.Handler {
	logger := zerolog.New(buf)
	return hlog.NewHandler(logger)(h)
}

func TestEntryAudit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantLog bool
	}{
		{name: "GET is not audited", method: http.MethodGet, wantLog: false},
		{name: "POST is audited", method: http.MethodPost, wantLog: true},
		{name: "DELETE is audited", method: http.MethodDelete, wantLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &recordingHandler{}
			var buf bytes.Buffer
			h := withLogger(&buf, EntryAudit(next))

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tt.method, "/v1/foo", nil))

			if !next.called {
				t.Fatal("next handler was not called")
			}
			gotLog := strings.Contains(buf.String(), "Entry Audit")
			if gotLog != tt.wantLog {
				t.Errorf("audit logged = %v, want %v (log: %q)", gotLog, tt.wantLog, buf.String())
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	next := &recordingHandler{status: http.StatusTeapot, body: "ok"}
	rr := httptest.NewRecorder()
	SecurityHeaders(next).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if !next.called || rr.Code != http.StatusTeapot {
		t.Fatalf("next handler not run as is: called=%v status=%d", next.called, rr.Code)
	}
	for header, want := range map[string]string{
		"Content-Security-Policy":      ContentSecurityPolicy,
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := rr.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if rr.Header().Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy not set")
	}
}

// TestContentSecurityPolicyIsSameOrigin guards the offline requirement: the
// policy may name no other origin, and no source may be widened to a scheme
// or wildcard that admits one.
func TestContentSecurityPolicyIsSameOrigin(t *testing.T) {
	t.Parallel()

	for directive := range strings.SplitSeq(ContentSecurityPolicy, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			t.Fatalf("empty directive in %q", ContentSecurityPolicy)
		}
		for _, source := range fields[1:] {
			switch source {
			case "'self'", "'none'", "'unsafe-inline'":
			case "data:":
				if fields[0] != "img-src" {
					t.Errorf("%s allows data:, which only img-src needs", fields[0])
				}
			default:
				t.Errorf("%s allows %s; only same-origin sources are allowed", fields[0], source)
			}
		}
		if fields[0] == "script-src" && strings.Contains(directive, "unsafe") {
			t.Errorf("script-src must not allow unsafe sources: %q", directive)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	t.Parallel()

	t.Run("recovers from panic and returns 500", func(t *testing.T) {
		panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})
		var buf bytes.Buffer
		h := withLogger(&buf, PanicRecovery(panicking))

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/foo", nil))

		if rr.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
		log := buf.String()
		if !strings.Contains(log, "Recovered from panic") {
			t.Errorf("expected panic log, got %q", log)
		}
		if !strings.Contains(log, "stack_trace") {
			t.Errorf("expected stack_trace field in log, got %q", log)
		}
	})

	t.Run("passes through when no panic", func(t *testing.T) {
		next := &recordingHandler{status: http.StatusAccepted, body: "ok"}
		h := PanicRecovery(next)

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/foo", nil))

		if !next.called {
			t.Fatal("next handler was not called")
		}
		if rr.Code != http.StatusAccepted {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusAccepted)
		}
		if rr.Body.String() != "ok" {
			t.Errorf("body = %q, want %q", rr.Body.String(), "ok")
		}
	})

	// The server must still see http.ErrAbortHandler, so it drops the
	// connection instead of ending a broken stream cleanly.
	t.Run("passes http.ErrAbortHandler through to the server", func(t *testing.T) {
		aborting := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		})
		var buf bytes.Buffer
		h := withLogger(&buf, PanicRecovery(aborting))
		rr := httptest.NewRecorder()

		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("recovered %v, want http.ErrAbortHandler", got)
			}
			if rr.Code == http.StatusInternalServerError {
				t.Error("PanicRecovery wrote a 500 for an aborted response")
			}
			if strings.Contains(buf.String(), "Recovered from panic") {
				t.Errorf("aborted response logged as a panic: %q", buf.String())
			}
		}()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	})
}
