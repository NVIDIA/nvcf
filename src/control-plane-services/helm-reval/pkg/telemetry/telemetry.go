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

package telemetry

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// GetWrappedWriter is a helper function to get a wrapped response writer
//
// The wrapped response wrapper give us access to extra information about the response
// such as the status code and the size of the response. This is used for all kinds of
// http telemetry.
func GetWrappedWriter(w http.ResponseWriter, r *http.Request) middleware.WrapResponseWriter {
	if mw, ok := w.(middleware.WrapResponseWriter); ok {
		return mw
	}
	if r == nil {
		return middleware.NewWrapResponseWriter(w, 1)
	}
	return middleware.NewWrapResponseWriter(w, r.ProtoMajor)
}
