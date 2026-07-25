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
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// fakePartUploader fails the part whose number equals failPart and succeeds for
// every other part, returning a per-part ETag.
type fakePartUploader struct {
	failPart int32
	err      error
}

func (f *fakePartUploader) UploadPart(_ context.Context, params *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if f.err != nil && params.PartNumber != nil && *params.PartNumber == f.failPart {
		return nil, f.err
	}
	return &s3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("etag-%d", aws.ToInt32(params.PartNumber)))}, nil
}

func TestUploadPart(t *testing.T) {
	t.Run("success returns the completed part", func(t *testing.T) {
		pn := int32(3)
		part, err := uploadPart(context.Background(), &fakePartUploader{}, &s3.UploadPartInput{PartNumber: &pn})
		require.NoError(t, err)
		assert.Equal(t, "etag-3", aws.ToString(part.ETag))
		assert.Equal(t, int32(3), aws.ToInt32(part.PartNumber))
	})

	t.Run("failure returns the error without a nil dereference", func(t *testing.T) {
		wantErr := errors.New("upload failed")
		pn := int32(1)
		part, err := uploadPart(context.Background(), &fakePartUploader{failPart: 1, err: wantErr}, &s3.UploadPartInput{PartNumber: &pn})
		assert.ErrorIs(t, err, wantErr)
		assert.Zero(t, part)
	})

	// Run under -race. One part fails while the rest succeed concurrently; each
	// call must report only its own outcome. The previous shared function-scope
	// err raced here and could make a successful part inherit the failure (or
	// dereference a nil result).
	t.Run("concurrent parts do not clobber each other", func(t *testing.T) {
		const parts = 16
		const failPart = int32(7)
		up := &fakePartUploader{failPart: failPart, err: errors.New("part failed")}

		results := make([]error, parts)
		var wg sync.WaitGroup
		for i := int32(0); i < parts; i++ {
			wg.Add(1)
			go func(pn int32) {
				defer wg.Done()
				_, err := uploadPart(context.Background(), up, &s3.UploadPartInput{PartNumber: &pn})
				results[pn] = err
			}(i)
		}
		wg.Wait()

		for i := int32(0); i < parts; i++ {
			if i == failPart {
				assert.Error(t, results[i], "the failing part must report its own error")
			} else {
				assert.NoError(t, results[i], "a successful part must not inherit another part's error")
			}
		}
	})
}
