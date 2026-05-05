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
	"net/http"
	"testing"

	"github.com/openzipkin/zipkin-go/propagation/b3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	NotSupportedHeader = "custom-handler"
)

func TestGetMetadataFromRequest(t *testing.T) {

	supportedHeaders := []string{HeaderIfMatch, HeaderETag, HeaderKeyAuditID,
		HeaderKeyRequestID, b3.Sampled, b3.Flags, b3.ParentSpanID, b3.SpanID,
	}

	for _, header := range supportedHeaders {
		req := &http.Request{Header: map[string][]string{}}
		testValue := header + "value"
		req.Header.Add(header, testValue)
		md := GetMetadataFromRequest(req)
		require.Len(t, md.Get(header), 1)
		assert.Equal(t, testValue, md.Get(header)[0])
	}

	testEtagInvalid := "test-not-supported"
	req := &http.Request{Header: map[string][]string{}}
	req.Header.Add(NotSupportedHeader, testEtagInvalid)
	md := GetMetadataFromRequest(req)
	require.Len(t, md.Get(NotSupportedHeader), 0)
}
