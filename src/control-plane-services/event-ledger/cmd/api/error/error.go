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

package error

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"go.uber.org/zap"

	eventledgertypes "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

func writeErrorResponse(w http.ResponseWriter, status int, problem eventledgertypes.ProblemDetails) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	err := json.NewEncoder(w).Encode(problem)
	if err != nil {
		return err
	}
	return nil
}

func newProblemDetails(errType string, title string, path string, status int, err error) eventledgertypes.ProblemDetails {
	safeTitle := title
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if status >= http.StatusInternalServerError {
		safeTitle = http.StatusText(status)
		detail = http.StatusText(status)
	}
	return eventledgertypes.ProblemDetails{
		Type:     errType,
		Title:    safeTitle,
		Status:   status,
		Detail:   detail,
		Instance: path,
	}
}

func GenerateErrorResponse(traceCtx context.Context, errType string, title string, path string, status int, err error, w http.ResponseWriter) {
	logger := logging.GetLogger(traceCtx)
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(traceCtx, title, zap.Error(err))
	} else {
		logger.WarnContext(traceCtx, title, zap.Error(err))
	}
	response := newProblemDetails(errType, title, path, status, err)
	err = writeErrorResponse(w, status, response)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
