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

package queuesqs

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

func TestGetAWSRegion(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())

	url1 := "https://sqs.us-west-1.services.fifo"
	assert.Equal(t, getAWSRegion(ctx, url1), "us-west-1")

	url1 = "http://192.168.65.2:4566/000000000000/q_gdn_icms_byoc_13e2b598-96cf-41b5-b419-8ea7f700d5d2.fifo"
	assert.Equal(t, getAWSRegion(ctx, url1), "us-east-1")
}

func TestGetSessionAndHelpers(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())

	c := &client{}

	cmqi := queue.MessageQueueInfo{
		QueueURL:     "https://foo.bar",
		QueueType:    queue.CreationQueue,
		AccessKey:    "ASIAQAAAAAAAKQ563GZD",
		SessionToken: "FQoGZXIvYXdzEBYaDWBDIgdpw+WFwbMJjzWlBUY8Tz8VkPa4m7GD6pF006Pu5J2q82CeF08FYzgBFK1KsfZbenSykRH01TifaKDnghJMtHIQMXo1cerGTbXeqyCpvsl42gRiFqmiR1Hwy5sVUhlqQ05ZnVYUGPoWGu6OpA/9jWQbKK3ITVTXMhrbTXl0AN/e05Gxk16zCnPwsO1FFFDbOkd6Y5g1raAgtGZmst/6NkBpxAjehzUFfZvhQOg1FGJUsYkg3Y11QQ39DB4Ytl1AZTqmB//jCJiTPfJXGF+7MuX3Ufb/yC66Q89ENx9jpL8/lC66hliPrQKC1BOOYLBYgopaFMHTuVcL/wA=",
		SecretKey:    "VuwiYiuZ54FpjJNoKi4+xLmItKsuSkL4JM7Gibg/",
	}
	tmqi := queue.MessageQueueInfo{
		QueueURL:     "https://foo.bar",
		QueueType:    queue.TerminationQueue,
		AccessKey:    "ASIAQAAAAAAAKQ563GZD",
		SessionToken: "FQoGZXIvYXdzEBYaDWBDIgdpw+WFwbMJjzWlBUY8Tz8VkPa4m7GD6pF006Pu5J2q82CeF08FYzgBFK1KsfZbenSykRH01TifaKDnghJMtHIQMXo1cerGTbXeqyCpvsl42gRiFqmiR1Hwy5sVUhlqQ05ZnVYUGPoWGu6OpA/9jWQbKK3ITVTXMhrbTXl0AN/e05Gxk16zCnPwsO1FFFDbOkd6Y5g1raAgtGZmst/6NkBpxAjehzUFfZvhQOg1FGJUsYkg3Y11QQ39DB4Ytl1AZTqmB//jCJiTPfJXGF+7MuX3Ufb/yC66Q89ENx9jpL8/lC66hliPrQKC1BOOYLBYgopaFMHTuVcL/wA=",
		SecretKey:    "VuwiYiuZ54FpjJNoKi4+xLmItKsuSkL4JM7Gibg/",
	}

	var v any
	var ok bool

	_, ok = c.sessions.Load(cmqi)
	require.False(t, ok)
	_, ok = c.sessions.Load(tmqi)
	require.False(t, ok)

	csess, err := c.getSession(ctx, cmqi)
	require.NoError(t, err)
	assert.NotNil(t, csess)

	v, ok = c.sessions.Load(cmqi)
	if assert.True(t, ok) {
		assert.Equal(t, v, csess)
	}

	tsess, err := c.getSession(ctx, tmqi)
	require.NoError(t, err)
	assert.NotNil(t, tsess)

	v, ok = c.sessions.Load(tmqi)
	if assert.True(t, ok) {
		assert.Equal(t, v, tsess)
	}

	_, err = c.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:                cmqi,
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          100,
		VisibilityTimeoutSeconds: 30,
	})
	assert.NotNil(t, err)

	vTO := int64(20)
	err = c.ChangeMessageVisibility(ctx, queue.ChangeMessageVisibilityInput{
		QueueInfo:                cmqi,
		ReceiptHandle:            "random",
		VisibilityTimeoutSeconds: &vTO,
	})
	assert.NotNil(t, err)

	err = c.DeleteMessage(ctx, queue.DeleteMessageInput{
		QueueInfo:     tmqi,
		ReceiptHandle: "random",
	})
	assert.NotNil(t, err)
}
