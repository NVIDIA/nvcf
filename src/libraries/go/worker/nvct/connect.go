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

package nvct

import (
	"context"
	"fmt"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

// ------------------------------------------------------------------------

// Connect request to NVCT server.
func (c *Client) Connect(ctx context.Context) error {
	zap.L().Info("Connecting to NVCT")

	if ctx.Err() != nil {
		return ctx.Err()
	}

	var connectedRegion string
	err := backoff.Retry(func() error {
		if ctx.Err() != nil {
			return nil
		}

		connectRespnse, connectErr := c.Client.Connect(ctx, &pb.ConnectRequest{
			InstanceId: c.instanceId,
			TaskId:     c.taskId,
		}, auth.GrpcTokenFromSource(c.NvctTokenProvider))
		if connectErr != nil {
			zap.L().Error("failed to connect to NVCT", zap.Error(connectErr))
			return connectErr
		}

		connectedRegion = connectRespnse.GetConnectedRegion()
		if connectedRegion == "" {
			return fmt.Errorf("nvct did not respond with connected to connect request")
		}

		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 360), ctx))

	if err != nil {
		zap.L().Error("failed to reconnect to NVCT", zap.Error(err))
		return err
	}

	zap.L().Info("Connected to NVCT", zap.String("region", connectedRegion))
	return nil
}
