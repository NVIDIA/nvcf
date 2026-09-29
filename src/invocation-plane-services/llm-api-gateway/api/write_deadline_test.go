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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	echo "github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

// deadlineRecorder records every write deadline the middleware sets and the
// order of writes around them.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	events   []string
	writeErr error
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		r.events = append(r.events, "clear")
	} else {
		r.events = append(r.events, "arm")
	}
	return nil
}

func (r *deadlineRecorder) Write(b []byte) (int, error) {
	r.events = append(r.events, "write")
	if r.writeErr != nil {
		return 0, r.writeErr
	}
	return r.ResponseRecorder.Write(b)
}

func (r *deadlineRecorder) Flush() {
	r.events = append(r.events, "flush")
}

func runDeadlineMiddleware(
	t *testing.T,
	timeout time.Duration,
	recorder *deadlineRecorder,
	logs *bytes.Buffer,
	handler echo.HandlerFunc,
) {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req = req.WithContext(zerolog.New(logs).WithContext(req.Context()))
	gc := NewGatewayContext(e.NewContext(req, recorder))
	gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
		RequestID:  "request-a",
		RoutingKey: "fn-alpha",
		OrgID:      "org-alpha",
	})
	if err := newInferenceWriteDeadlineMiddleware(timeout)(handler)(gc); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestInferenceWriteDeadlineArmsOnlyDuringWrites(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	runDeadlineMiddleware(t, time.Second, recorder, &bytes.Buffer{}, func(c echo.Context) error {
		c.Response().WriteHeader(http.StatusOK)
		for range 2 {
			if _, err := c.Response().Write([]byte("data: x\n\n")); err != nil {
				return err
			}
			c.Response().Flush()
		}
		return nil
	})

	want := []string{
		"clear",
		"arm", "write", "clear", "arm", "flush", "clear",
		"arm", "write", "clear", "arm", "flush", "clear",
		"arm",
	}
	if got := strings.Join(recorder.events, " "); got != strings.Join(want, " ") {
		t.Fatalf("deadline events = %s\nwant %s", got, strings.Join(want, " "))
	}
}

func TestInferenceWriteDeadlineStaysArmedAfterHandlerReturns(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	var w http.ResponseWriter
	runDeadlineMiddleware(t, time.Second, recorder, &bytes.Buffer{}, func(c echo.Context) error {
		w = c.Response().Writer
		return nil
	})
	recorder.events = nil

	// Echo's error handler writes through the wrapper after the middleware
	// returns; the deadline must stay armed for net/http's final flush.
	if _, err := w.Write([]byte(`{"error":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(recorder.events, " "), "arm write"; got != want {
		t.Fatalf("deadline events = %s, want %s", got, want)
	}
}

func TestInferenceWriteDeadlineDisabled(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	runDeadlineMiddleware(t, 0, recorder, &bytes.Buffer{}, func(c echo.Context) error {
		_, err := c.Response().Write([]byte("ok"))
		return err
	})

	if got, want := strings.Join(recorder.events, " "), "clear write"; got != want {
		t.Fatalf("deadline events = %s, want %s", got, want)
	}
}

func TestInferenceWriteTimeoutLogsOncePerRequest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		err     error
		wantLog bool
	}{
		{name: "deadline exceeded", err: fmt.Errorf("write tcp: %w", os.ErrDeadlineExceeded), wantLog: true},
		{name: "client closed", err: fmt.Errorf("write tcp: broken pipe"), wantLog: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), writeErr: tc.err}
			var logs bytes.Buffer
			runDeadlineMiddleware(t, time.Minute, recorder, &logs, func(c echo.Context) error {
				for range 3 {
					_, _ = c.Response().Write([]byte("data: x\n\n"))
				}
				return nil
			})

			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if !tc.wantLog {
				if logs.Len() != 0 {
					t.Fatalf("unexpected log: %s", logs.String())
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("log lines = %d, want 1: %s", len(lines), logs.String())
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{
				"level":       "warn",
				"function_id": "fn-alpha",
				"org_id":      "org-alpha",
			} {
				if entry[key] != want {
					t.Errorf("%s = %v, want %v", key, entry[key], want)
				}
			}
			if entry["write_timeout"] == nil {
				t.Error("write_timeout missing")
			}
		})
	}
}
