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

// White-box unit tests for the pure helpers in large.go that do not require an
// S3 client or NVCF gRPC client.
package worker

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
)

func TestFinishedLargeResponse(t *testing.T) {
	t.Run("happy path returns 302 with location", func(t *testing.T) {
		ch := make(chan lo.Tuple2[string, error], 1)
		ch <- lo.T2("http://example/download", error(nil))
		resp, err := finishedLargeResponse(ch)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusFound, resp.StatusCode)
		assert.Equal(t, "http://example/download", resp.Header.Get("Location"))
		assert.Equal(t, http.NoBody, resp.Body)
	})

	t.Run("error from download url channel is propagated", func(t *testing.T) {
		wantErr := errors.New("download url failed")
		ch := make(chan lo.Tuple2[string, error], 1)
		ch <- lo.T2("", wantErr)
		resp, err := finishedLargeResponse(ch)
		assert.Nil(t, resp)
		assert.ErrorIs(t, err, wantErr)
	})
}

func TestRecordUploadStats(t *testing.T) {
	meteringEvent := metering.New(&metering.Config{}, "req", "sub", "nca", nil)
	meteringEvent.InferenceSize = 100
	recordUploadStats(time.Now().Add(-1*time.Second), meteringEvent, 250)
	// Stats accumulate onto the metering event's inference size.
	assert.Equal(t, int64(350), meteringEvent.InferenceSize)
}
