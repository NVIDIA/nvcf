/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package ratelimit_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit/ratelimitmock"
)

func TestPubSubConsumerError(t *testing.T) {
	var (
		ctrl        = gomock.NewController(t)
		rateLimiter = ratelimitmock.NewMockRateLimiter(ctrl)
	)
	rateLimiter.EXPECT().
		CheckLimit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("test error"))

	consumer := ratelimit.PubSubConsumer(rateLimiter, "cluster-test2", true)

	event := &ratelimit.RateLimitEventWireFormat{
		Key:         "foo1",
		Units:       200,
		Rate:        300,
		Period:      1 * time.Minute,
		ClusterName: "cluster-test1",
		RequestID:   "req_00aaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt:   time.Now().Unix(),
	}

	err := consumer(t.Context(), event, nil)
	require.Error(t, err)
}

func TestPubSubConsumerEmptyClusterName(t *testing.T) {
	ctrl := gomock.NewController(t)
	rateLimiter := ratelimitmock.NewMockRateLimiter(ctrl)

	consumer := ratelimit.PubSubConsumer(rateLimiter, "", true)

	event := &ratelimit.RateLimitEventWireFormat{
		Key:         "foo1",
		Units:       200,
		Rate:        300,
		Period:      1 * time.Minute,
		ClusterName: "cluster-test1",
		RequestID:   "req_00aaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt:   time.Now().Unix(),
	}

	err := consumer(t.Context(), event, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cluster name must be configured")
}
