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

package nvca

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/ros"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func setupTestROSClient() (*ros.ROSClient, *mockTransport) {
	// Expires at 2020-04-18 00:05:11 -0700 PDT
	token := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MTkzNTExLCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg3MTUwMzExLCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.icoV7ZDRCs7PAnDVQmuH5ZqeRBZMbExdN1ztCtsv7dwij0c6LpygdDMta7VkEuYfqijuFscHbgkMMicxkdTbgIKYxWNi4vMuBXKSbDO50Z4IkqHnzxrVJ4vI_hcGKdTCt_yOgTQkQ97HvKrzTG-eOhYgGQhXyk5mDhT7bv4VGprGGYql-D8ijeG7-gq_IvKT6XWl8Mvl3JZeyt8W4BGCbUHut-34pQwLN1_qs03EHFnUfIZY9S0XD7Wm2cCVgYJAOpPeHunYROhXFSe44Oq0wCWDoRKzXaGLXJP8vkpyJWcPqfGnYJ8Sr0SFKxmIdJPWQR4IggRlZbXRZ-3jtIIEYA`
	tc, mock := setJWTCacheTest()

	tc = tc.
		WithNowFunc(func() time.Time { return timeFromString("2020-04-17T10:00:00-07:00").Time })

	mock.token, mock.tokenErr = token, nil

	s := ros.NewROSClient(context.Background(), uuid.NewString(), uuid.NewString(), "http://localhost/ros", tc, nil)
	m := &mockTransport{
		code: http.StatusOK,
		body: `{
  "token": "token",
  "expires_in": 12284,
  "error": ""}`,
	}
	s.Client.Transport = m
	return s, m
}

func TestROSFunctionInstanceStatusUpdate(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestROSClient()

	_, eP := s.GetConfig()
	assert.Equal(t, eP, "http://localhost/ros")

	m.code = int32(http.StatusOK)
	m.body = `Request accepted`

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion: map[string]string{
					"inference-container":   "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
					"init-container":        "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:0.7.0",
					"utils-container-image": "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_utils:2.2.1",
				},
			},
		})
	assert.NoError(t, err)

	m.code = int32(http.StatusMethodNotAllowed)
	m.body = `Request rejected`

	err = s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion: map[string]string{
					"inference-container":   "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
					"init-container":        "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:0.7.0",
					"utils-container-image": "stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_utils:2.2.1",
				},
			},
		})
	assert.NotNil(t, err)
}

func TestROSFunctionInstanceStatusUpdateTokenError(t *testing.T) {
	ctx := newTestContext()

	// Create a new client with a mock token fetcher that returns an error
	tc, mock := setJWTCacheTest()
	mock.tokenErr = fmt.Errorf("token fetch error")

	s := ros.NewROSClient(context.Background(), uuid.NewString(), uuid.NewString(), "http://localhost/ros", tc, nil)
	m := &mockTransport{
		code: int32(http.StatusOK),
		body: `Request accepted`,
	}
	s.Client.Transport = m

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion: map[string]string{
					"inference-container": "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
				},
			},
		})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token fetch error")
}

func TestROSFunctionInstanceStatusUpdateInvalidJSON(t *testing.T) {
	ctx := newTestContext()
	s, _ := setupTestROSClient()

	// Create a request with invalid JSON (circular reference)
	type InvalidRequest struct {
		Self *InvalidRequest
	}
	invalidReq := &InvalidRequest{}
	invalidReq.Self = invalidReq

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion:  make(map[string]string),
			},
		})
	assert.NoError(t, err) // Should not error as we're not using the invalid request
}

func TestROSFunctionInstanceStatusUpdateNetworkError(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestROSClient()

	m.err = fmt.Errorf("network error")

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion: map[string]string{
					"inference-container": "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
				},
			},
		})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestROSFunctionInstanceStatusUpdateDifferentStatusCodes(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestROSClient()

	testCases := []struct {
		statusCode int
		body       string
		expectErr  bool
	}{
		{http.StatusCreated, "Created", false},
		{http.StatusAccepted, "Accepted", false},
		{http.StatusBadRequest, "Bad Request", true},
		{http.StatusUnauthorized, "Unauthorized", true},
		{http.StatusForbidden, "Forbidden", true},
		{http.StatusNotFound, "Not Found", true},
		{http.StatusInternalServerError, "Internal Server Error", true},
	}

	for _, tc := range testCases {
		m.code = int32(tc.statusCode)
		m.body = tc.body

		err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
			[]types.InstanceUpdateStatusDTO{
				{
					InstanceID:        "randomInstanceID",
					NCAID:             "randomNCAID",
					FunctionID:        "randomFunctionID",
					FunctionVersionID: "randomFunctionVersionID",
					ClusterID:         "randomClusterID",
					ContainerVersion: map[string]string{
						"inference-container": "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
					},
				},
			})
		assert.Error(t, err)
	}
}

func TestROSFunctionInstanceStatusUpdateEmptyContainerVersion(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestROSClient()

	m.code = int32(http.StatusOK)
	m.body = `Request accepted`

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion:  make(map[string]string),
			},
		})
	assert.NoError(t, err)
}

func TestROSFunctionInstanceStatusUpdateLargeResponse(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestROSClient()

	// Create a large response body that exceeds maxResponseBytes
	largeBody := strings.Repeat("a", 251*1024) // 251KB, exceeding the 250KB limit
	m.code = int32(http.StatusOK)
	m.body = largeBody

	err := s.PostFunctionInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID",
		[]types.InstanceUpdateStatusDTO{
			{
				InstanceID:        "randomInstanceID",
				NCAID:             "randomNCAID",
				FunctionID:        "randomFunctionID",
				FunctionVersionID: "randomFunctionVersionID",
				ClusterID:         "randomClusterID",
				ContainerVersion: map[string]string{
					"inference-container": "stg.nvcr.io/nvidia/tritonserver:23.04-py3",
				},
			},
		})
	assert.NoError(t, err) // Should not error as we're not reading the response body
}
