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

package mockqueue

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

func TestMockQueue(t *testing.T) {
	qc := &Client{Use10MillisForWaits: true}

	queueInfo := queue.MessageQueueInfo{
		GPU:       "A100",
		QueueURL:  "url",
		QueueType: queue.CreationQueue,
	}

	out, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo:           queueInfo,
		MaxNumberOfMessages: 1,
	})
	assert.Nil(t, out)
	assert.NoError(t, err)
	out, err = qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo:                queueInfo,
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          10,
		VisibilityTimeoutSeconds: 1,
	})
	assert.Nil(t, out)
	assert.NoError(t, err)
	err = qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queueInfo,
		ReceiptHandle: "foo",
	})
	assert.Nil(t, out)
	assert.EqualError(t, err, "no message in queue url with receipt handle foo")
	err = qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:     queueInfo,
		ReceiptHandle: "foo",
	})
	assert.Nil(t, out)
	assert.EqualError(t, err, "no message in queue url with receipt handle foo")

	msg := queue.ReceiveMessageOutput{
		MessageID:     "msgid1",
		ReceiptHandle: "rhdl1",
		Body:          []byte("{}"),
	}
	qc.AddMessage(queueInfo.QueueURL, msg)

	out, err = qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo:                queueInfo,
		MaxNumberOfMessages:      2,
		WaitTimeSeconds:          10,
		VisibilityTimeoutSeconds: 1,
	})
	assert.Len(t, out, 1)
	assert.NoError(t, err)
	err = qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:     queueInfo,
		ReceiptHandle: msg.ReceiptHandle,
	})
	assert.NoError(t, err)
	err = qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queueInfo,
		ReceiptHandle: msg.ReceiptHandle,
	})
	assert.NoError(t, err)
}
