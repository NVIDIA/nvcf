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

func TestAllowReadMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		wantCalled bool
		wantStatus int
	}{
		{name: "GET is allowed", method: http.MethodGet, wantCalled: true, wantStatus: http.StatusOK},
		{name: "HEAD is allowed", method: http.MethodHead, wantCalled: true, wantStatus: http.StatusOK},
		{name: "POST is rejected", method: http.MethodPost, wantCalled: false, wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT is rejected", method: http.MethodPut, wantCalled: false, wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE is rejected", method: http.MethodDelete, wantCalled: false, wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &recordingHandler{}
			h := AllowReadMethods(next)

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tt.method, "/v1/foo", nil))

			if next.called != tt.wantCalled {
				t.Errorf("next handler called = %v, want %v", next.called, tt.wantCalled)
			}
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
			if !tt.wantCalled {
				if allow := rr.Header().Get("Allow"); allow != "GET, HEAD" {
					t.Errorf("Allow header = %q, want %q", allow, "GET, HEAD")
				}
			}
		})
	}
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

func TestAddRequestId(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		wantReqID bool
	}{
		{name: "GET has no request id", method: http.MethodGet, wantReqID: false},
		{name: "POST gets a request id", method: http.MethodPost, wantReqID: true},
		{name: "PUT gets a request id", method: http.MethodPut, wantReqID: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &recordingHandler{}
			h := AddRequestId(next)

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tt.method, "/v1/foo", nil))

			if !next.called {
				t.Fatal("next handler was not called")
			}
			gotReqID := rr.Header().Get("X-Request-Id") != ""
			if gotReqID != tt.wantReqID {
				t.Errorf("X-Request-Id present = %v, want %v", gotReqID, tt.wantReqID)
			}
		})
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
}
