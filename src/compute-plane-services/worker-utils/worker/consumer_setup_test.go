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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/consumer"
)

func TestNewNatsConsumerWithRetry(t *testing.T) {
	errNatsDown := errors.New("context deadline exceeded")

	t.Run("recovers after transient failures", func(t *testing.T) {
		want := &consumer.GlobalNatsConsumer{}
		attempts := 0
		got, err := newNatsConsumerWithRetry(context.Background(), time.Minute, func() (*consumer.GlobalNatsConsumer, error) {
			attempts++
			if attempts < 3 {
				return nil, errNatsDown
			}
			return want, nil
		})
		require.NoError(t, err)
		assert.Same(t, want, got)
		assert.Equal(t, 3, attempts)
	})

	t.Run("gives up after max elapsed time", func(t *testing.T) {
		start := time.Now()
		_, err := newNatsConsumerWithRetry(context.Background(), 500*time.Millisecond, func() (*consumer.GlobalNatsConsumer, error) {
			return nil, errNatsDown
		})
		require.ErrorIs(t, err, errNatsDown)
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("stops when context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		_, err := newNatsConsumerWithRetry(ctx, time.Minute, func() (*consumer.GlobalNatsConsumer, error) {
			attempts++
			cancel()
			return nil, errNatsDown
		})
		require.Error(t, err)
		assert.Equal(t, 1, attempts)
	})

	t.Run("does not retry errors that retrying cannot fix", func(t *testing.T) {
		for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument} {
			t.Run(code.String(), func(t *testing.T) {
				rejected := fmt.Errorf("failed to create consumer for primary region ncp: %w", status.Error(code, "rejected"))
				attempts := 0
				_, err := newNatsConsumerWithRetry(context.Background(), time.Minute, func() (*consumer.GlobalNatsConsumer, error) {
					attempts++
					return nil, rejected
				})
				require.ErrorIs(t, err, rejected)
				assert.Equal(t, 1, attempts)
			})
		}
	})

	t.Run("retry warning includes the caller's log fields", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		t.Cleanup(zap.ReplaceGlobals(zap.New(core)))
		attempts := 0
		_, err := newNatsConsumerWithRetry(context.Background(), time.Minute, func() (*consumer.GlobalNatsConsumer, error) {
			attempts++
			if attempts < 2 {
				return nil, errNatsDown
			}
			return &consumer.GlobalNatsConsumer{}, nil
		}, zap.String("function_id", "fn-1"))
		require.NoError(t, err)
		entries := logs.FilterMessage("failed to set up nats consumer, retrying").All()
		require.Len(t, entries, 1)
		assert.Equal(t, "fn-1", entries[0].ContextMap()["function_id"])
	})

	t.Run("retries unavailable provisioning", func(t *testing.T) {
		attempts := 0
		_, err := newNatsConsumerWithRetry(context.Background(), time.Minute, func() (*consumer.GlobalNatsConsumer, error) {
			attempts++
			if attempts < 2 {
				return nil, status.Error(codes.Unavailable, "connection refused")
			}
			return &consumer.GlobalNatsConsumer{}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})
}
