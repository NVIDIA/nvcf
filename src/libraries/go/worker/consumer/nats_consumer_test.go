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

package consumer

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/semaphore"
	"sync"
	"testing"
)

func TestAdjustMaxBatchIfNeeded(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectAdjusted bool
		expectError    bool
		expectedMax    int64
	}{
		{
			name:           "No adjustment needed",
			err:            errors.New("some other error"),
			expectAdjusted: false,
			expectError:    false,
			expectedMax:    10, // Should stay the same
		},
		{
			name:           "Adjustment needed",
			err:            errors.New("nats: Exceeded MaxRequestBatch of 5"),
			expectAdjusted: true,
			expectError:    false,
			expectedMax:    5, // Should be adjusted to 5
		},
		{
			name:           "Invalid adjustment format",
			err:            errors.New("nats: Exceeded MaxRequestBatch of invalid"),
			expectAdjusted: false,
			expectError:    true,
			expectedMax:    10, // Should stay the same
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumer := &GlobalNatsConsumer{
				workLimiter:         semaphore.NewWeighted(10),
				notifyWorkCompleted: sync.NewCond(&sync.Mutex{}),
			}
			consumer.maxPullBatchSize.Store(10)

			adjusted, err := consumer.adjustMaxBatchIfNeeded(tt.err)

			assert.Equal(t, tt.expectAdjusted, adjusted)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectAdjusted {
				assert.Equal(t, tt.expectedMax, consumer.maxPullBatchSize.Load())
			}
		})
	}
}

func TestReleaseWork(t *testing.T) {
	consumer := &GlobalNatsConsumer{
		workLimiter:         semaphore.NewWeighted(5),
		notifyWorkCompleted: sync.NewCond(&sync.Mutex{}),
	}

	// Acquire all slots
	acquired := consumer.workLimiter.TryAcquire(5)
	assert.True(t, acquired)
	assert.Equal(t, int64(0), consumer.workLimiter.MaxAvailable())

	// Now release one
	consumer.releaseWork()
	assert.Equal(t, int64(1), consumer.workLimiter.MaxAvailable())
}
