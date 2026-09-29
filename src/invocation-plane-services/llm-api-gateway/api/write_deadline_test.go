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

func serveWithWriteDeadline(
	t *testing.T,
	timeout time.Duration,
	recorder *deadlineRecorder,
	logs *bytes.Buffer,
	handler echo.HandlerFunc,
	configure ...func(*echo.Echo),
) {
	t.Helper()

	e := echo.New()
	for _, fn := range configure {
		fn(e)
	}
	withRequestContext := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			gc := NewGatewayContext(c)
			gc.store.Set(contextKeyRequestContext, &requestctx.RequestContext{
				RequestID:  "request-a",
				RoutingKey: "fn-alpha",
				OrgID:      "org-alpha",
			})
			return next(gc)
		}
	}
	e.POST("/v1/responses", handler, withRequestContext, newInferenceWriteDeadlineMiddleware(timeout))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req = req.WithContext(zerolog.New(logs).WithContext(req.Context()))
	WithFinalWriteDeadline(e, timeout).ServeHTTP(recorder, req)
}

func TestInferenceWriteDeadlineArmsOnlyDuringWrites(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	serveWithWriteDeadline(t, time.Second, recorder, &bytes.Buffer{}, func(c echo.Context) error {
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
		// Armed once more when Echo is done, for net/http's final flush.
		"arm",
	}
	if got := strings.Join(recorder.events, " "); got != strings.Join(want, " ") {
		t.Fatalf("deadline events = %s\nwant %s", got, strings.Join(want, " "))
	}
}

func TestInferenceWriteDeadlineBoundsErrorResponse(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	serveWithWriteDeadline(t, time.Second, recorder, &bytes.Buffer{}, func(echo.Context) error {
		return echo.NewHTTPError(http.StatusBadGateway, "upstream failed")
	})

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	// No deadline may run between the handler returning and Echo's error
	// handler writing; the final arm comes only after the error response.
	if got, want := strings.Join(recorder.events, " "), "clear arm write clear arm"; got != want {
		t.Fatalf("deadline events = %s, want %s", got, want)
	}
}

func TestFinalWriteDeadlineArmsAfterAllEchoWork(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		handler   echo.HandlerFunc
		configure func(*echo.Echo, *deadlineRecorder)
		want      string
	}{
		{
			name: "pre middleware works after the handler",
			handler: func(c echo.Context) error {
				return c.String(http.StatusOK, "ok")
			},
			configure: func(e *echo.Echo, recorder *deadlineRecorder) {
				e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
					return func(c echo.Context) error {
						err := next(c)
						recorder.events = append(recorder.events, "pre-done")
						return err
					}
				})
			},
			want: "clear arm write clear pre-done arm",
		},
		{
			name: "custom error handler",
			handler: func(echo.Context) error {
				return echo.NewHTTPError(http.StatusBadGateway, "upstream failed")
			},
			configure: func(e *echo.Echo, recorder *deadlineRecorder) {
				e.HTTPErrorHandler = func(err error, c echo.Context) {
					_ = c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
					recorder.events = append(recorder.events, "error-handled")
				}
			},
			want: "clear arm write clear error-handled arm",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			serveWithWriteDeadline(t, time.Second, recorder, &bytes.Buffer{}, tc.handler, func(e *echo.Echo) {
				tc.configure(e, recorder)
			})
			if got := strings.Join(recorder.events, " "); got != tc.want {
				t.Fatalf("deadline events = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInferenceWriteDeadlineDisabled(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	serveWithWriteDeadline(t, 0, recorder, &bytes.Buffer{}, func(c echo.Context) error {
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
		timeout time.Duration
		err     error
		wantLog bool
	}{
		{name: "deadline exceeded", timeout: time.Minute, err: fmt.Errorf("write tcp: %w", os.ErrDeadlineExceeded), wantLog: true},
		{name: "client closed", timeout: time.Minute, err: fmt.Errorf("write tcp: broken pipe")},
		// A disconnect noticed after the armed deadline passed is still a
		// disconnect, not a write timeout.
		{name: "client closed after deadline", timeout: time.Nanosecond, err: fmt.Errorf("http2: stream closed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), writeErr: tc.err}
			var logs bytes.Buffer
			serveWithWriteDeadline(t, tc.timeout, recorder, &logs, func(c echo.Context) error {
				for range 3 {
					_, _ = c.Response().Write([]byte("data: x\n\n"))
				}
				return nil
			})

			if !tc.wantLog {
				if logs.Len() != 0 {
					t.Fatalf("unexpected log: %s", logs.String())
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
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
