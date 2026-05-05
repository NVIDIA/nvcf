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

package queue

import (
	"context"
)

const (
	MaximumQueueMessages          = 1
	MaximumQueueWaitTimeInSeconds = 15
	MaxCreationSkip               = uint64(50)
)

var (
	DefaultVisibilityTimeoutSeconds       = int64(30)
	CreationQueueVisibilityTimeoutSeconds = int64(360)
)

type Client interface {
	ReceiveMessage(context.Context, ReceiveMessageInput) ([]ReceiveMessageOutput, error)
	DeleteMessage(context.Context, DeleteMessageInput) error
	ChangeMessageVisibility(context.Context, ChangeMessageVisibilityInput) error
	IsMessageNotFoundError(error) bool
}

type ReceiveMessageInput struct {
	QueueInfo                MessageQueueInfo
	MaxNumberOfMessages      int64
	WaitTimeSeconds          int64
	VisibilityTimeoutSeconds int64
}

type ReceiveMessageOutput struct {
	MessageID     string
	ReceiptHandle string
	Body          []byte
}

type DeleteMessageInput struct {
	QueueInfo     MessageQueueInfo
	ReceiptHandle string
}

type ChangeMessageVisibilityInput struct {
	QueueInfo                MessageQueueInfo
	ReceiptHandle            string
	VisibilityTimeoutSeconds *int64
}

//nolint:revive
type QueueType string

const (
	CreationQueue    QueueType = "CreationQueue"
	TerminationQueue QueueType = "TerminationQueue"
)

type MessageQueueInfo struct {
	GPU          string    `json:"gpu,omitempty"`
	QueueURL     string    `json:"url,omitempty"`
	QueueType    QueueType `json:"queueType,omitempty"`
	AccessKey    string    `json:"accessKeyId,omitempty"`
	SecretKey    string    `json:"secretAccessKey,omitempty"`
	SessionToken string    `json:"sessionToken,omitempty"`
	Expiration   string    `json:"expiresAt,omitempty"`
}
