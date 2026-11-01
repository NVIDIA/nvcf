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

package utils

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
)

const (
	testRequestID = "test-request"
	testAuditID   = "test-audit"
	testETag      = "test-etag"
)

func TestHeaderProvider(t *testing.T) {
	etagProviderFn := HeaderProvider()
	testCtx := etagProviderFn(context.Background(),
		metadata.MD{
			HeaderKeyRequestID: []string{testRequestID},
			HeaderETag:         []string{testETag},
			HeaderKeyAuditID:   []string{testAuditID},
		})
	assert.NotEqual(t, testCtx, context.Background())
	hdrsFromCtx := testCtx.Value(headerKey{})
	hdrs, ok := hdrsFromCtx.(headers)
	assert.True(t, ok)
	assert.NotEmpty(t, hdrs)
	for _, h := range StandardHeaders {
		assert.NotEmpty(t, hdrs[HeaderNVPrefix+h], "Empty for "+h)
	}
}

func TestEtagHeaderAppender(t *testing.T) {
	testCtx := context.Background()
	headerMD := metadata.MD{}
	trailerMD := metadata.MD{}

	// If no etag is present in context, this should be a noop
	testCaseDesc := "headers missing in context"
	hdrAppenderFn := HeaderAppender()
	testCtx = hdrAppenderFn(testCtx, &headerMD, &trailerMD)
	assert.Equal(t, testCtx, context.Background(), testCaseDesc)
	assert.Len(t, headerMD, 0, testCaseDesc)
	assert.Len(t, trailerMD, 0, testCaseDesc)

	// Context with new etag should propagate it in the headerMD
	testCaseDesc = "headers present in context"
	testCtx = context.WithValue(testCtx, headerKey{}, headers{"k1": "v1", HeaderNVPrefix + HeaderNewETag: "new-etag"})
	newTestCtx := hdrAppenderFn(testCtx, &headerMD, &trailerMD)
	assert.Equal(t, newTestCtx, testCtx, testCaseDesc)
	assert.Len(t, headerMD, 2, testCaseDesc)
	assert.Len(t, trailerMD, 0, testCaseDesc)
	etag := headerMD.Get(HeaderNVPrefix + HeaderETag)
	assert.NotEmpty(t, etag, testCaseDesc)
}
