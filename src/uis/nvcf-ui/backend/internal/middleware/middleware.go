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
	"net/http"
	"runtime"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

// AllowReadMethods rejects any request whose method is not GET or HEAD with a
// 405, before it reaches the wrapped handler. The Allow header advertises the
// permitted methods as required by the HTTP spec.
func AllowReadMethods(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func EntryAudit(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			hlog.FromRequest(r).Info().Msg("Entry Audit")
		}
		h.ServeHTTP(w, r)
	})
}

func AddRequestId(h http.Handler) http.Handler {
	withRequestID := hlog.RequestIDHandler("request_id", "X-Request-Id")(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			h.ServeHTTP(w, r)
			return
		}
		withRequestID.ServeHTTP(w, r)
	})
}

func PanicRecovery(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := make([]byte, 4096)
				stack = stack[:runtime.Stack(stack, false)]
				hlog.FromRequest(r).Error().
					Interface(zerolog.ErrorFieldName, err).
					Str("stack_trace", string(stack)).
					Msg("Recovered from panic")
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	})
}
