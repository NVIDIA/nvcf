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
)

// BodyLimitMiddleware limits the size of request bodies to prevent memory exhaustion
// It returns 413 Request Entity Too Large if the body exceeds maxBytes
func BodyLimitMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Wrap the request body with http.MaxBytesReader
			// This automatically returns 413 if exceeded
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

			// Call the next handler in the chain
			next.ServeHTTP(w, r)
		})
	}
}
