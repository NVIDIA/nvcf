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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestGetROSUpdatesForRequest(t *testing.T) {
	ctx := context.Background()
	backend := K8sComputeBackend{}

	// Test case 1: Function creation action with running instances
	t.Run("function creation with running instances", func(t *testing.T) {
		req := &nvcav2beta1.ICMSRequest{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					types.ICMSRequestIDKey: "test-request-id",
				},
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
						NCAID: "test-nca-id",
					},
					FunctionLaunchSpecification: &function.LaunchSpecification{
						EnvironmentB64: "VVRJTFNfQ09OVEFJTkVSPSJ0ZXN0LXV0aWxzOjEuMC4wIgpJTklUX0NPTlRBSU5FUj0idGVzdC1pbml0OjEuMC4wIg==",
					},
				},
				FunctionDetails: function.Details{
					FunctionID:        "test-func-id",
					FunctionVersionID: "test-func-version-id",
				},
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"instance-1": {
						Status: string(types.ICMSInstanceRunning),
					},
				},
			},
		}

		updates, err := backend.GetROSUpdatesForRequest(ctx, req)
		require.NoError(t, err)
		require.Len(t, updates, 1)

		update := updates[0]
		assert.Equal(t, "test-request-id", update.RequestID)
		assert.Equal(t, "instance-1", update.InstanceID)
		assert.Equal(t, "test-nca-id", update.Payload[0].NCAID)
		assert.Equal(t, "test-func-id", update.Payload[0].FunctionID)
		assert.Equal(t, "test-func-version-id", update.Payload[0].FunctionVersionID)
		assert.Equal(t, map[string]string{
			"UTILS_CONTAINER": "test-utils:1.0.0",
			"INIT_CONTAINER":  "test-init:1.0.0",
		}, update.Payload[0].ContainerVersion)
	})

	// Test case 2: Non-function creation action
	t.Run("non-function creation action", func(t *testing.T) {
		req := &nvcav2beta1.ICMSRequest{
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.TaskCreationAction,
			},
		}

		updates, err := backend.GetROSUpdatesForRequest(ctx, req)
		require.NoError(t, err)
		assert.Empty(t, updates)
	})

	// Test case 3: Invalid environment base64
	t.Run("invalid environment base64", func(t *testing.T) {
		req := &nvcav2beta1.ICMSRequest{
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					FunctionLaunchSpecification: &function.LaunchSpecification{
						EnvironmentB64: "invalid-base64",
					},
				},
			},
		}

		updates, err := backend.GetROSUpdatesForRequest(ctx, req)
		require.NoError(t, err)
		assert.Empty(t, updates)
	})
}

func TestPostRolloverServiceUpdates(t *testing.T) {
	// Create a test server to mock ROS API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/function-instance-status-update", r.URL.Path)

		var req []types.InstanceUpdateStatusDTO
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Only verify the fields we know will be present
		assert.Equal(t, "test-nca-id", req[0].NCAID)
		assert.Equal(t, "test-func-id", req[0].FunctionID)
		assert.Equal(t, "test-func-version-id", req[0].FunctionVersionID)
		assert.Equal(t, "instance-1", req[0].InstanceID)
		assert.Equal(t, map[string]string{
			"UTILS_CONTAINER": "test-utils:1.0.0",
			"INIT_CONTAINER":  "test-init:1.0.0",
		}, req[0].ContainerVersion)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create test agent with mocked dependencies
	ctx := context.Background()
	opts := &AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		ClusterID:                     "test-cluster-id",
		ICMSURL:                       "http://icms.example.com",
		RolloverServiceURL:            server.URL,
		RolloverServiceUpdateInterval: 30 * time.Minute,
		FeatureFlagFetcher:            featureflag.DefaultFetcher,
		MetricsRegisterer:             prometheus.NewRegistry(),
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Create test ICMS request
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				types.ICMSRequestIDKey: "test-request-id",
			},
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.FunctionCreationAction,
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					NCAID: "test-nca-id",
				},
				FunctionLaunchSpecification: &function.LaunchSpecification{
					EnvironmentB64: "VVRJTFNfQ09OVEFJTkVSPSJ0ZXN0LXV0aWxzOjEuMC4wIgpJTklUX0NPTlRBSU5FUj0idGVzdC1pbml0OjEuMC4wIg==",
				},
			},
			FunctionDetails: function.Details{
				FunctionID:        "test-func-id",
				FunctionVersionID: "test-func-version-id",
			},
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			RequestStatus: nvcav2beta1.ICMSRequestStatusCompletionAcknowledged,
			Instances: map[string]nvcav2beta1.InstanceStatus{
				"instance-1": {
					Status: string(types.ICMSInstanceRunning),
				},
			},
		},
	}

	// Initialize backendk8scache with required dependencies
	bc := &BackendK8sCache{
		icmsRequestLister: &mockSpotRequestLister{requests: []*nvcav2beta1.ICMSRequest{req}},
		icmsRequestHelper: K8sComputeBackend{},
	}
	agent.backendk8scache = bc

	// Test the PostRolloverServiceUpdates function
	err = agent.PostRolloverServiceUpdates(ctx)
	require.NoError(t, err)
}

// Mock implementation of icmsRequestLister
type mockSpotRequestLister struct {
	requests []*nvcav2beta1.ICMSRequest
}

func (m *mockSpotRequestLister) List(selector labels.Selector) ([]*nvcav2beta1.ICMSRequest, error) {
	return m.requests, nil
}

func (m *mockSpotRequestLister) Get(name string) (*nvcav2beta1.ICMSRequest, error) {
	for _, req := range m.requests {
		if req.Name == name {
			return req, nil
		}
	}
	return nil, fmt.Errorf("request not found")
}

func (m *mockSpotRequestLister) ICMSRequests(namespace string) nvcav2beta1listers.ICMSRequestNamespaceLister {
	return &mockICMSRequestNamespaceLister{
		requests:  m.requests,
		namespace: namespace,
	}
}

type mockICMSRequestNamespaceLister struct {
	requests  []*nvcav2beta1.ICMSRequest
	namespace string
}

func (m *mockICMSRequestNamespaceLister) List(selector labels.Selector) ([]*nvcav2beta1.ICMSRequest, error) {
	return m.requests, nil
}

func (m *mockICMSRequestNamespaceLister) Get(name string) (*nvcav2beta1.ICMSRequest, error) {
	for _, req := range m.requests {
		if req.Name == name {
			return req, nil
		}
	}
	return nil, fmt.Errorf("request not found")
}
