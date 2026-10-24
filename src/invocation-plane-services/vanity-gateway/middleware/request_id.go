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

package middleware

import (
	"context"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

const (
	// RequestIDHeader carries the NVCF request ID. The LLM Gateway reuses an
	// inbound value, so one ID spans both services.
	RequestIDHeader = "X-Request-Id"
	// messagesRequestIDHeader is the header Anthropic SDKs report as the request ID.
	messagesRequestIDHeader = "Request-Id"
	messagesPath            = "/v1/messages"
)

type requestIDContextKey struct{}

// RequestID keeps the caller's X-Request-Id or creates one, forwards it
// upstream, and sets it on every response. Messages responses also carry it as
// request-id. Headers are set before the handler runs so responses the gateway
// writes itself carry them too.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = NewRequestID()
		}
		r.Header.Set(RequestIDHeader, id)

		owned := []string{RequestIDHeader}
		if r.URL.Path == messagesPath {
			owned = append(owned, messagesRequestIDHeader)
		}
		for _, name := range owned {
			w.Header().Set(name, id)
		}

		ctx := context.WithValue(r.Context(), requestIDContextKey{}, owned)
		// The chi request logger prints this value on each request line.
		ctx = context.WithValue(ctx, chimiddleware.RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// NewRequestID returns a new random request ID.
func NewRequestID() string {
	return uuid.NewString()
}

// DropUpstreamRequestIDHeaders removes the request ID headers that RequestID
// already set on the response, so a proxied response carries one value each.
func DropUpstreamRequestIDHeaders(ctx context.Context, upstream http.Header) {
	owned, _ := ctx.Value(requestIDContextKey{}).([]string)
	for _, name := range owned {
		upstream.Del(name)
	}
}
