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
	"context"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"go.uber.org/zap"
)

func (w *NVCFWorker) handleStatefulWorkRequest(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, workTrackingRegion string) error {
	zap.L().Info("handling stateful work request", zap.String("req id", work.RequestId))
	err := w.httpProxy.Proxy(ctx, work, workTrackingRegion)
	if err != nil {
		zap.L().Error("failed to handle stateful work request", zap.Error(err), zap.String("req id", work.RequestId))
	}
	return err
}
