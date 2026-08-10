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

package response

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

func TestCreateErrorResponse(t *testing.T) {
	resp := CreateErrorResponse("nvcf", http.StatusBadRequest, "missing field")
	require.NotNil(t, resp)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, int64(len(body)), resp.ContentLength)

	var details pb.ErrorDetails
	require.NoError(t, protojson.Unmarshal(body, &details))

	// http.StatusText(400) == "Bad Request" -> lower-cased and hyphenated.
	assert.Equal(t, "urn:nvcf:problem-details:bad-request", details.GetType())
	assert.Equal(t, "Bad Request", details.GetTitle())
	assert.Equal(t, uint32(http.StatusBadRequest), details.GetStatus())
	assert.Equal(t, "missing field", details.GetDetail())
}

func TestCreateErrorResponseUnknownStatusCode(t *testing.T) {
	// http.StatusText returns "" for an unknown code; the helper still produces
	// a well-formed response with an empty title/type suffix.
	resp := CreateErrorResponse("worker", 799, "weird")
	require.NotNil(t, resp)
	assert.Equal(t, 799, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var details pb.ErrorDetails
	require.NoError(t, protojson.Unmarshal(body, &details))
	assert.Equal(t, uint32(799), details.GetStatus())
	assert.Equal(t, "weird", details.GetDetail())
	assert.Equal(t, "urn:worker:problem-details:", details.GetType())
}

func TestCreateErrorResponseMultiWordStatus(t *testing.T) {
	resp := CreateErrorResponse("nvcf", http.StatusInternalServerError, "boom")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var details pb.ErrorDetails
	require.NoError(t, protojson.Unmarshal(body, &details))
	assert.Equal(t, "urn:nvcf:problem-details:internal-server-error", details.GetType())
	assert.Equal(t, "Internal Server Error", details.GetTitle())
}
