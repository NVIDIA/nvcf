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

package polling

import (
	"context"
	"errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/cenkalti/backoff/v4"
	"github.com/nats-io/nats.go"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"time"
)

func HandlePollingRequests(ctx context.Context, nc *nats.Conn, originalRequestId string, timeoutPerMessage time.Duration, pollingRequestCallback func(work *pb.WorkerInvokeFunctionRequest)) (<-chan error, error) {
	zap.L().Info("subscribing to polling subject", zap.String("req id", originalRequestId))
	subscription, err := nc.SubscribeSync("rq_polling." + originalRequestId)
	if err != nil {
		return nil, err
	}
	return lo.Async(func() error {
		defer subscription.Unsubscribe()
		for ctx.Err() == nil {
			var msg *nats.Msg
			err = backoff.Retry(func() error {
				ctx, cancel := context.WithTimeout(ctx, timeoutPerMessage)
				defer cancel()
				nextMsg, err := subscription.NextMsgWithContext(ctx)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						zap.L().Warn("failed to get next rq_polling message", zap.String("req id", originalRequestId), zap.Error(err))
					}
					if errors.Is(err, context.DeadlineExceeded) {
						return backoff.Permanent(err)
					}
					return err
				}
				msg = nextMsg
				return nil
			}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(0)), ctx))
			if err != nil {
				return err
			}
			var work pb.WorkerInvokeFunctionRequest
			err = proto.Unmarshal(msg.Data, &work)
			if err != nil {
				zap.L().Warn("malformed rq_polling message", zap.String("req id", originalRequestId), zap.Error(err))
				continue
			}
			zap.L().Debug("got polling request", zap.String("req id", originalRequestId))
			// ack receipt of polling request
			err := msg.Respond(nil)
			if err != nil {
				zap.L().Warn("failed to ack rq_polling request", zap.String("req id", originalRequestId), zap.Error(err))
				continue
			}
			pollingRequestCallback(&work)
		}
		return nil
	}), nil
}
