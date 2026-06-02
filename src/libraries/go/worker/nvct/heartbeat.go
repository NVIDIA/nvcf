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
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

// Send in progress heartbeat when task container is initializing or running.
func (c *Client) SendInProgressHeartbeat(ctx context.Context, status pb.ExecutionStatus) (string, error) {
	if status != pb.ExecutionStatus_RUNNING && status != pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING {
		return "", fmt.Errorf("invalid status in a success heartbeat: %s", status.String())
	}

	zap.L().Info("Sending success heartbeat to NVCT")
	request := &pb.HeartbeatRequest{
		InstanceId:   c.instanceId,
		TaskId:       c.taskId,
		InstanceType: c.instanceType,
		Status:       status,
	}

	return c.sendHeartbeat(ctx, request)
}

// Send max runtime exceeded heartbeat to NVCT server.
func (c *Client) SendMaxRuntimeExceededHeartbeat(ctx context.Context, errorMsg string) error {
	return c.sendFailureHeartbeat(ctx, pb.ExecutionStatus_EXCEEDED_MAX_RUNTIME_DURATION, errorMsg)
}

// Send error heartbeat to NVCT server.
func (c *Client) SendErrorHeartbeat(ctx context.Context, errorMsg string) error {
	return c.sendFailureHeartbeat(ctx, pb.ExecutionStatus_ERRORED, errorMsg)
}

// Send heartbeat when worker is terminated externally.
func (c *Client) SendWorkerTerminatedHeartbeat(ctx context.Context) error {
	zap.L().Info("Sending worker terminated heartbeat to NVCT")
	request := &pb.HeartbeatRequest{
		InstanceId:   c.instanceId,
		TaskId:       c.taskId,
		InstanceType: c.instanceType,
		Status:       pb.ExecutionStatus_WORKER_TERMINATED,
	}

	if _, err := c.sendHeartbeat(ctx, request); err != nil {
		return fmt.Errorf("failed to send worker terminated heartbeat: %w", err)
	}

	return nil
}

// Send failure heartbeat to NVCT server.
func (c *Client) sendFailureHeartbeat(ctx context.Context, status pb.ExecutionStatus, errorMsg string) error {
	zap.L().Info("Sending failure heartbeat to NVCT", zap.String("message", errorMsg), zap.String("status", status.String()))
	request := &pb.HeartbeatRequest{
		InstanceId:   c.instanceId,
		TaskId:       c.taskId,
		InstanceType: c.instanceType,
		Status:       status,
		ErrorMessage: &errorMsg,
	}

	_, err := c.sendHeartbeat(ctx, request)
	return err
}

// Send heartbeat to NVCT server.
func (c *Client) sendHeartbeat(ctx context.Context, request *pb.HeartbeatRequest) (string, error) {
	executionStatus := pb.ExecutionStatus_RUNNING.String()

	err := backoff.Retry(func() error {
		resp, err := c.Client.SendHeartbeat(ctx, request, auth.GrpcTokenFromSource(c.NvctTokenProvider))
		zap.L().Debug(
			"Got heartbeat response",
			zap.String("task id", resp.GetTaskId()),
			zap.String("task status", resp.GetExecutionStatus()),
		)
		executionStatus = resp.GetExecutionStatus()
		return err
	}, backoff.WithContext(backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(1*time.Second),
		backoff.WithMaxElapsedTime(1*time.Minute)),
		ctx,
	))

	return executionStatus, err
}
