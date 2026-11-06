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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcaerrors "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// icmsNoValidationRouteBody is what an ICMS without the registration validation route,
// such as ICMS 0.7.2, answers to POST .../register/validate.
const icmsNoValidationRouteBody = `{"detail":"No static resource v1/nvca/clusters/c1/register/validate.",` +
	`"instance":"/v1/nvca/clusters/c1/register/validate","status":404,"title":"Not Found"}`

const icmsActiveInstancesConflictBody = `{"error":"cluster registration failed, active instances exists for ` +
	`removed [NCP.GPU.H100_32x] instanceTypes","reason":"ACTIVE_INSTANCES_ON_REMOVED_INSTANCE_TYPES",` +
	`"instanceTypes":["NCP.GPU.H100_32x"],"activeInstanceIds":["i-1","i-2"]}`

func TestICMSValidateRegistration(t *testing.T) {
	tests := []struct {
		name            string
		code            int
		body            string
		wantErr         bool
		wantUnsupported bool
		wantICMSErr     *types.ICMSRegistrationError
	}{
		{
			name: "ICMS accepts the registration",
			code: http.StatusNoContent,
		},
		{
			name:    "ICMS rejects removing instance types with active instances",
			code:    http.StatusConflict,
			body:    icmsActiveInstancesConflictBody,
			wantErr: true,
			wantICMSErr: &types.ICMSRegistrationError{
				Message:           "cluster registration failed, active instances exists for removed [NCP.GPU.H100_32x] instanceTypes",
				Reason:            types.ICMSReasonActiveInstancesOnRemovedInstanceTypes,
				InstanceTypes:     []string{"NCP.GPU.H100_32x"},
				ActiveInstanceIDs: []string{"i-1", "i-2"},
			},
		},
		{
			name:        "ICMS rejects the request",
			code:        http.StatusBadRequest,
			body:        `{"error":"k8sVersion must be provided"}`,
			wantErr:     true,
			wantICMSErr: &types.ICMSRegistrationError{Message: "k8sVersion must be provided"},
		},
		{
			name:        "ICMS does not know the cluster",
			code:        http.StatusNotFound,
			body:        `{"error":"Cluster with clusterId c1 doesn't exists"}`,
			wantErr:     true,
			wantICMSErr: &types.ICMSRegistrationError{Message: "Cluster with clusterId c1 doesn't exists"},
		},
		{
			name:            "ICMS has no validation route",
			code:            http.StatusNotFound,
			body:            icmsNoValidationRouteBody,
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "a proxy in front of ICMS has no validation route",
			code:            http.StatusNotFound,
			body:            "<html><body>404 Not Found</body></html>",
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:        "ICMS fails",
			code:        http.StatusInternalServerError,
			body:        `{"error":"internal error"}`,
			wantErr:     true,
			wantICMSErr: &types.ICMSRegistrationError{Message: "internal error"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, m := setupTestICMSClient()
			m.setCode(tt.code)
			m.setBody(tt.body)

			err := s.ValidateRegistration(newTestContext(), &types.ICMSRegistrationRequest{K8sVersion: "1.30.0"})

			req := m.getRequest()
			require.NotNil(t, req)
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, "/icms/v1/nvca/clusters/"+s.clusterID+"/register/validate", req.URL.Path)
			sent, readErr := io.ReadAll(req.Body)
			require.NoError(t, readErr)
			var sentReq types.ICMSRegistrationRequest
			require.NoError(t, json.Unmarshal(sent, &sentReq))
			assert.Equal(t, "1.30.0", sentReq.K8sVersion)

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.code, nvcaerrors.GetHTTPStatusCode(err), "the HTTP status must stay readable")
			assert.Equal(t, tt.wantUnsupported, errors.Is(err, ErrRegistrationValidationUnsupported))
			var icmsErr *ICMSErrorResponse
			if tt.wantICMSErr == nil {
				assert.False(t, errors.As(err, &icmsErr))
				return
			}
			require.True(t, errors.As(err, &icmsErr))
			assert.Equal(t, *tt.wantICMSErr, icmsErr.Body)
		})
	}
}

func TestICMSValidateRegistrationFollowsCustomRegisterPath(t *testing.T) {
	t.Setenv(envICMSPathRegister, "api/custom/register/%s")
	s, m := setupTestICMSClient()
	m.setCode(http.StatusNoContent)

	require.NoError(t, s.ValidateRegistration(newTestContext(), &types.ICMSRegistrationRequest{}))
	assert.Equal(t, "/icms/api/custom/register/"+s.clusterID+"/validate", m.getRequest().URL.Path)
}

func TestICMSRegisterKeepsICMSErrorBody(t *testing.T) {
	s, m := setupTestICMSClient()
	m.setCode(http.StatusConflict)
	m.setBody(icmsActiveInstancesConflictBody)

	_, err := s.Register(newTestContext(), &types.ICMSRegistrationRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, nvcaerrors.GetHTTPStatusCode(err))
	assert.ErrorContains(t, err, "error response from ICMS: 409 Conflict: cluster registration failed")
	icmsErr := icmsRegistrationError(err)
	require.NotNil(t, icmsErr)
	assert.Equal(t, []string{"NCP.GPU.H100_32x"}, icmsErr.InstanceTypes)
	assert.Equal(t, []string{"i-1", "i-2"}, icmsErr.ActiveInstanceIDs)

	// A response without an ICMS error body keeps the plain status error.
	m.setBody("")
	_, err = s.Register(newTestContext(), &types.ICMSRegistrationRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, nvcaerrors.GetHTTPStatusCode(err))
	assert.EqualError(t, err, "HTTP 409: error response from ICMS: 409 Conflict")
	assert.Nil(t, icmsRegistrationError(err))
}
