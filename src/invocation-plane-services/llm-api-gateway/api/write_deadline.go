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
	"errors"
	"net/http"
	"os"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// newInferenceWriteDeadlineMiddleware replaces the server-wide write deadline
// on inference routes. http.Server.WriteTimeout bounds the whole response, so
// it cuts off streams and long generations that outlive it. Here a deadline is
// armed only while a write is in progress, so timeout bounds how long a single
// write may stall on a client that stopped reading, and never the time spent
// waiting on the upstream between writes. A timeout <= 0 removes the write
// deadline entirely.
func newInferenceWriteDeadlineMiddleware(timeout time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			response := ec.Response()
			w := &deadlineWriter{
				ResponseWriter: response.Writer,
				controller:     http.NewResponseController(response.Writer),
				timeout:        timeout,
				ctx:            ec.Request().Context(),
			}
			if gc, ok := ec.(*GatewayContext); ok {
				w.gc = gc
			}
			w.clearDeadline()
			if timeout > 0 {
				response.Writer = w
				if final, ok := ec.Request().Context().Value(finalWriteDeadlineKey{}).(*finalWriteDeadline); ok {
					final.needed = true
				}
			}
			return next(ec)
		}
	}
}

type finalWriteDeadlineKey struct{}

// finalWriteDeadline records whether the inference middleware replaced the
// server-wide write deadline for a request.
type finalWriteDeadline struct {
	needed bool
}

// WithFinalWriteDeadline arms the write deadline once next has returned, for
// requests whose server-wide deadline the inference middleware replaced, so
// the flush net/http performs after the handler (the rest of the buffered
// body, the chunked terminator, or the HTTP/2 end of stream) cannot block
// forever on a client that stopped reading. Wrap the whole Echo instance so
// the deadline starts only after every middleware and the error handler are
// done. Other routes keep http.Server.WriteTimeout. A timeout <= 0 disables
// this, matching the inference middleware.
func WithFinalWriteDeadline(next http.Handler, timeout time.Duration) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		final := &finalWriteDeadline{}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), finalWriteDeadlineKey{}, final)))
		if final.needed {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout))
		}
	})
}

func (h *Handlers) inferenceWriteTimeout() time.Duration {
	if h == nil || h.config == nil {
		return config.Default().Server.InferenceWriteTimeout
	}
	return h.config.Server.InferenceWriteTimeout
}

// deadlineWriter arms the write deadline around each Write and Flush and
// clears it afterward. Clearing matters for HTTP/2, where an armed deadline
// resets the stream when it passes even if no write is in progress.
type deadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	ctx        context.Context
	gc         *GatewayContext
	logged     bool
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.armDeadline()
	n, err := w.ResponseWriter.Write(b)
	w.afterWrite(err)
	return n, err
}

func (w *deadlineWriter) Flush() {
	w.armDeadline()
	w.afterWrite(w.controller.Flush())
}

func (w *deadlineWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *deadlineWriter) armDeadline() {
	if w.timeout <= 0 {
		return
	}
	w.setDeadline(time.Now().Add(w.timeout))
}

func (w *deadlineWriter) clearDeadline() {
	w.setDeadline(time.Time{})
}

func (w *deadlineWriter) afterWrite(err error) {
	w.clearDeadline()
	if err != nil {
		w.logTimeout(err)
	}
}

func (w *deadlineWriter) setDeadline(deadline time.Time) {
	// ErrNotSupported covers recorders and writers without a connection;
	// other errors mean the connection is already gone and the next write
	// reports it.
	_ = w.controller.SetWriteDeadline(deadline)
}

// logTimeout logs the first write that failed because its deadline passed.
// Both HTTP/1.1 and HTTP/2 report that cause as os.ErrDeadlineExceeded.
func (w *deadlineWriter) logTimeout(err error) {
	if w.logged || !errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	w.logged = true
	event := telemetry.Logger(w.ctx).
		Warn().
		Err(err).
		Dur("write_timeout", w.timeout)
	if w.gc != nil {
		if reqCtx := w.gc.RequestContext(); reqCtx != nil {
			event = event.
				Str("function_id", reqCtx.RoutingKey).
				Str("org_id", reqCtx.OrgID)
		}
	}
	event.Msg("inference response write timed out; client stopped reading")
}
