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
// it cuts off streams and long generations that outlive it. Here the deadline
// is pushed out before every write instead, so timeout only bounds how long a
// single write may stall on a client that stopped reading. A timeout <= 0
// removes the write deadline entirely.
func newInferenceWriteDeadlineMiddleware(timeout time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec echo.Context) error {
			response := ec.Response()
			w := &deadlineExtendingWriter{
				ResponseWriter: response.Writer,
				controller:     http.NewResponseController(response.Writer),
				timeout:        timeout,
				ctx:            ec.Request().Context(),
			}
			// Nothing is written before the handler's first write, so clear
			// the server-wide deadline now and extend it per write below.
			w.setDeadline(time.Time{})
			if timeout > 0 {
				response.Writer = w
			}
			// Bound the final flush net/http performs after the handler
			// returns, which bypasses the wrapper (for example a status-only
			// response written after a long upstream call).
			defer w.extendDeadline()
			return next(ec)
		}
	}
}

func (h *Handlers) inferenceWriteTimeout() time.Duration {
	if h == nil || h.config == nil {
		return config.Default().Server.InferenceWriteTimeout
	}
	return h.config.Server.InferenceWriteTimeout
}

type deadlineExtendingWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	ctx        context.Context
	logged     bool
}

func (w *deadlineExtendingWriter) Write(b []byte) (int, error) {
	w.extendDeadline()
	n, err := w.ResponseWriter.Write(b)
	w.logTimeout(err)
	return n, err
}

func (w *deadlineExtendingWriter) Flush() {
	w.extendDeadline()
	if err := w.controller.Flush(); err != nil {
		w.logTimeout(err)
	}
}

func (w *deadlineExtendingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *deadlineExtendingWriter) extendDeadline() {
	if w.timeout <= 0 {
		return
	}
	w.setDeadline(time.Now().Add(w.timeout))
}

func (w *deadlineExtendingWriter) setDeadline(deadline time.Time) {
	// ErrNotSupported covers recorders and writers without a connection;
	// other errors mean the connection is already gone and the next write
	// reports it.
	_ = w.controller.SetWriteDeadline(deadline)
}

func (w *deadlineExtendingWriter) logTimeout(err error) {
	if w.logged || !errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	w.logged = true
	telemetry.Logger(w.ctx).
		Warn().
		Err(err).
		Dur("write_timeout", w.timeout).
		Msg("inference response write timed out; client stopped reading")
}
