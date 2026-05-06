/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package logging

import (
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

func NewZapLoggerMiddleware(logger *zap.Logger) func(next http.Handler) http.Handler {
	if logger == nil {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			wrappedWriter := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				reqLogger := logger.With(
					zap.String("path", r.URL.Path),
					zap.Duration("duration", time.Since(start)),
					zap.Int("status", wrappedWriter.Status()),
					zap.Int("size", wrappedWriter.BytesWritten()),
				)

				userAgent := r.Header.Get("User-Agent")
				if userAgent != "" {
					reqLogger = reqLogger.With(zap.String("user-agent", userAgent))
				}

				zapotelspan.ContextLogger(r.Context(), reqLogger).Info("HTTP Request")
			}()
			next.ServeHTTP(wrappedWriter, r)
		}
		return http.HandlerFunc(fn)
	}
}
