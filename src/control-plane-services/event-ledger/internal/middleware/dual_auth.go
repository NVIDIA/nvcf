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
	"strings"

	"github.com/gorilla/mux"
)

const pdpAuthorizedContextKey contextKey = "pdp_authorized"

func BearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(authHeader, "Bearer ")
}

func MarkPDPAuthorized(ctx context.Context) context.Context {
	return context.WithValue(ctx, pdpAuthorizedContextKey, true)
}

func IsPDPAuthorized(ctx context.Context) bool {
	authorized, ok := ctx.Value(pdpAuthorizedContextKey).(bool)
	return ok && authorized
}

func ChainMiddleware(first, second mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return first(second(next))
	}
}

// Routes by token shape: JWT-shaped tokens take jwtPath, the rest apiKeyPath.
func NewDualAuthMiddleware(jwtPath, apiKeyPath mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		if jwtPath == nil {
			return apiKeyPath(next)
		}

		jwtChain := jwtPath(next)
		policyChain := apiKeyPath(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isJWTShapedToken(BearerToken(r)) {
				jwtChain.ServeHTTP(w, r)
				return
			}
			policyChain.ServeHTTP(w, r)
		})
	}
}
