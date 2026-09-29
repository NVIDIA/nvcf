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
			}
			return next(ec)
		}
	}
}

// installWriteDeadlineFinalizer arms the write deadline one last time when
// Echo is done with a request, so the flush net/http performs after the
// handler returns is bounded. On success that is when the outermost (pre)
// middleware returns; on error it is after HTTPErrorHandler has written the
// error response. Arming any earlier would let the deadline run while outer
// middleware or the error handler is still working.
func installWriteDeadlineFinalizer(e *echo.Echo) {
	e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err == nil {
				finishWriteDeadline(c)
			}
			return err
		}
	})
	handleError := e.HTTPErrorHandler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		handleError(err, c)
		finishWriteDeadline(c)
	}
}

func finishWriteDeadline(c echo.Context) {
	if w, ok := c.Response().Writer.(*deadlineWriter); ok {
		w.finish()
	}
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
	finished   bool
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

func (w *deadlineWriter) finish() {
	w.finished = true
	w.armDeadline()
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
	if err != nil {
		w.logTimeout(err)
		return
	}
	if !w.finished {
		w.clearDeadline()
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
