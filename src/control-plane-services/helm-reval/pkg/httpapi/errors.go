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

package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/tracing"
)

type ApiError struct {
	Reason string `json:"reason,omitempty"`
	// A brief identifier of the origin of this error
	Origin string `json:"origin,omitempty"`
	// Unique ID for this error. Useful for troubleshooting
	ErrorId string `json:"error_id,omitempty"`
	// Optional further information about this error that can be used for debugging/recovering from the error
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ServeAPIError writes an ApiError to the http.ResponseWriter as a JSON response.
//

// The handler calling this function is responsible for ensuring that no headers or body
// have been written to w before this function is called. If statusCode is 0 or not
// a valid HTTP status code, http.StatusInternalServerError (500) will be used.
//
// Example usage in a handler:
//
//	if err != nil {
//	    apiErr := errors.ApiError{
//	        Reason: "Failed to process request",
//	        Origin: "my-processing-service",
//	        // ErrorId: can be pre-filled or generated here
//	    }
//	    errors.ServeAPIError(w, apiErr, http.StatusBadRequest)
//	    return // Stop further processing in the handler
//	}
func ServeAPIError(r *http.Request, w http.ResponseWriter, apiErr ApiError, statusCode int) {
	if apiErr.ErrorId == "" {
		if traceId := tracing.GetTraceID(r.Context()); traceId != "" {
			apiErr.ErrorId = "trace:" + traceId
		} else {
			apiErr.ErrorId = "unknown:"
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff") // Security best practice
	w.WriteHeader(statusCode)

	if encodeErr := json.NewEncoder(w).Encode(apiErr); encodeErr != nil {
		zapotelspan.ContextLogger(r.Context(), nil).Error("Failed to encode API error", zap.Error(encodeErr))
	}
}

func ServeSimpleError(r *http.Request, w http.ResponseWriter, reason string, err error, statusCode int) {
	apiErr := ApiError{
		Reason: reason,
		Origin: "reval-service",
	}
	if err != nil {
		apiErr.Metadata = map[string]string{
			"error": err.Error(),
		}
	}
	ServeAPIError(r, w, apiErr, statusCode)
}

func ServeNotFound(w http.ResponseWriter, r *http.Request) {
	ServeAPIError(r, w, ApiError{
		Reason: "Not Found",
		Origin: "reval-service",
	}, http.StatusNotFound)
}
