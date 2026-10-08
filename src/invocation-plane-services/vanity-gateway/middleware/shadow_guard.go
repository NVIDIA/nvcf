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
	"net/http"

	"go.uber.org/zap"
)

// RejectSpoofedShadowRequests returns middleware that rejects any external
// request carrying the given header. Internal shadow replay requests bypass
// the router middleware chain entirely, so this only affects external traffic.
// The guard runs ahead of the server telemetry middleware, so the rejection is
// served through observe (when non-nil) to land on the server request metric
// with the gateway_rejected outcome.
func RejectSpoofedShadowRequests(shadowHeaderName string, observe func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	reject := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RecordGatewayProxyOutcome(r.Context(), GatewayProxyOutcomeRejected)
		w.Header().Set(ErrorSourceHeader, string(GatewayProxyOutcomeRejected))
		// The guard runs before routing and auth, so function, cluster and org
		// IDs are not known yet; the shared request log also has not run.
		zap.L().Warn("rejected request carrying reserved shadow header",
			append(TraceFields(r.Context()),
				zap.String("http.method", r.Method),
				zap.String("http.path", r.URL.Path),
				zap.String("http.remote_addr", r.RemoteAddr),
				zap.String(string(GatewayProxyOutcomeMetricAttribute), string(GatewayProxyOutcomeRejected)),
			)...,
		)
		http.Error(w, "reserved header in external request", http.StatusBadRequest)
	}))
	if observe != nil {
		reject = observe(reject)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(shadowHeaderName) != "" {
				reject.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
