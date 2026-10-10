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

func EntryAudit(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			hlog.FromRequest(r).Info().Msg("Entry Audit")
		}
		h.ServeHTTP(w, r)
	})
}

// ContentSecurityPolicy lets the UI load only what this server serves: the
// bundle is built in, and the demo runs with no external network, so any
// request to another origin is a bug or an injection. Two allowances:
//   - style-src 'unsafe-inline': React style props and the code highlighter's
//     token colors are inline style attributes.
//   - img-src data: KUI's icons are SVG data URIs in its CSS.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
	"frame-ancestors 'none'"

// SecurityHeaders sets the browser hardening headers on every response.
func SecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", ContentSecurityPolicy)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.ServeHTTP(w, r)
	})
}

func PanicRecovery(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// A handler panics with http.ErrAbortHandler to make the server drop
				// the connection, as the reverse proxy does when the gateway fails
				// mid-stream. Recovering it would end the response cleanly instead.
				if err == http.ErrAbortHandler {
					panic(err)
				}
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
