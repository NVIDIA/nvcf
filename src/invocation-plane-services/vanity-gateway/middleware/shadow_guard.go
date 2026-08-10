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

import "net/http"

// RejectSpoofedShadowRequests returns middleware that rejects any external
// request carrying the given header. Internal shadow replay requests bypass
// the router middleware chain entirely, so this only affects external traffic.
func RejectSpoofedShadowRequests(shadowHeaderName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(shadowHeaderName) != "" {
				http.Error(w, "reserved header in external request", http.StatusBadRequest)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
