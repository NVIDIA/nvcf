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

package worker

import (
	"errors"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

// Cause set on the request context when the IS broadcasts a cancel.
var ErrUpstreamCancel = errors.New("upstream cancel via NATS")

// Handler for nvcf.cancel.<fvid>. Fires the per-request cancel if we own
// the id; siblings drop on a map miss.
func (w *NVCFWorker) handleCancelMessage(msg *nats.Msg) {
	requestId := string(msg.Data)
	w.cancelSubMu.Lock()
	cancel, owned := w.inFlightCancels[requestId]
	w.cancelSubMu.Unlock()
	if !owned {
		return
	}
	cancel(ErrUpstreamCancel)
	zap.L().Info("upstream cancel received, tearing down request",
		utils.PublicLogMarker,
		zap.String("req id", requestId))
}

// logWorkRequestResult reports the outcome of a completed work request. An
// upstream cancel aborts the in-flight send by design, and handleCancelMessage
// already records it at info, so it is kept out of the error stream instead of
// being reported a second time as a request failure.
func logWorkRequestResult(err error, requestId string) {
	switch {
	case err == nil:
	case errors.Is(err, ErrUpstreamCancel):
		zap.L().Debug("request aborted by upstream cancel", zap.String("req id", requestId))
	default:
		// failure for a single request, keep trying to consume other requests
		zap.L().Error("failed to handle request", zap.Error(err), zap.String("req id", requestId))
	}
}
