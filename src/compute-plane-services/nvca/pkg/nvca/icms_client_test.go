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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelpropagation "go.opentelemetry.io/otel/propagation"
	otelsdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type mockTransport struct {
	mu   sync.RWMutex
	req  *http.Request
	err  error
	code int32
	body string
}

func newMockTransport() *mockTransport {
	m := &mockTransport{}
	m.mu.Lock()
	m.code = http.StatusOK
	m.mu.Unlock()
	return m
}

func (m *mockTransport) setError(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
}

func (m *mockTransport) setBody(body string) {
	m.mu.Lock()
	m.body = body
	m.mu.Unlock()
}

func (m *mockTransport) setCode(code int) {
	m.mu.Lock()
	m.code = int32(code)
	m.mu.Unlock()
}

func (m *mockTransport) setRequest(req *http.Request) {
	m.mu.Lock()
	m.req = req
	m.mu.Unlock()
}

func (m *mockTransport) getCode() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.code
}

func (m *mockTransport) getBody() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.body
}

func (m *mockTransport) getError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.err
}

func (m *mockTransport) getRequest() *http.Request {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.req
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.req = req
	code := m.code
	body := m.body
	err := m.err
	m.mu.Unlock()

	resp := &http.Response{
		StatusCode: int(code),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	return resp, err
}

func setupTestICMSClient() (*ICMSClient, *mockTransport) {
	// Expires at 2020-04-18 00:05:11 -0700 PDT
	token := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MTkzNTExLCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg3MTUwMzExLCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.icoV7ZDRCs7PAnDVQmuH5ZqeRBZMbExdN1ztCtsv7dwij0c6LpygdDMta7VkEuYfqijuFscHbgkMMicxkdTbgIKYxWNi4vMuBXKSbDO50Z4IkqHnzxrVJ4vI_hcGKdTCt_yOgTQkQ97HvKrzTG-eOhYgGQhXyk5mDhT7bv4VGprGGYql-D8ijeG7-gq_IvKT6XWl8Mvl3JZeyt8W4BGCbUHut-34pQwLN1_qs03EHFnUfIZY9S0XD7Wm2cCVgYJAOpPeHunYROhXFSe44Oq0wCWDoRKzXaGLXJP8vkpyJWcPqfGnYJ8Sr0SFKxmIdJPWQR4IggRlZbXRZ-3jtIIEYA`
	tc, mock := setJWTCacheTest()

	tc = tc.
		WithNowFunc(func() time.Time { return timeFromString("2020-04-17T10:00:00-07:00").Time })

	mock.token, mock.tokenErr = token, nil

	s := NewICMSClient(context.Background(), uuid.NewString(), "http://localhost/icms", tc, nil)
	m := newMockTransport()
	m.setCode(http.StatusOK)
	m.setBody(`{
  "token": "token",
  "expires_in": 12284,
  "error": ""}`)
	s.client.Transport = m
	return s, m
}

func TestICMSClientHostHeader(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantHost string
	}{
		{
			name:     "uses URL host by default",
			wantHost: "bare-elb.example.invalid",
		},
		{
			name:     "uses configured host override",
			host:     "sis.bare-elb.example.invalid",
			wantHost: "sis.bare-elb.example.invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewICMSClientWithHostHeaderOverride(
				t.Context(),
				uuid.NewString(),
				"http://bare-elb.example.invalid",
				tt.host,
				&mockTokenFetcher{token: "token"},
				nil,
			)
			transport := newMockTransport()
			transport.setBody(`{"clusterId":"test-cluster"}`)
			client.client.Transport = transport

			_, err := client.Register(t.Context(), &types.ICMSRegistrationRequest{})
			require.NoError(t, err)
			req := transport.getRequest()
			require.NotNil(t, req)
			gotHost := req.Host
			if gotHost == "" {
				gotHost = req.URL.Host
			}
			assert.Equal(t, tt.wantHost, gotHost)
		})
	}
}

func TestICMSRegister(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `{
    "clusterId": "testClusterID",
    "clusterGroupId": "13e2b598-96cf-41b5-b419-8ea7f700d5d2",
    "credentials": {
        "creationQueue": {
			"` + string(testGPUNameDefault) + `": {
				"url": "http://192.168.65.2:4566/000000000000/q_gdn_icms_byoc_13e2b598-96cf-41b5-b419-8ea7f700d5d2.fifo",
				"queueType": "FifoQueue",
				"accessKeyId": "dummy-access-key-id",
				"secretAccessKey": "dummy-secret-access-key",
				"sessionToken": "dummy-session-token",
				"expiresAt": "2023-05-31T05:26:04.433Z"
			}
		},
        "terminationQueue": {
            "url": "http://192.168.65.2:4566/000000000000/q_gdn_icms_byoc_oauth-stg-0CBtUXmR8i1HFNvm7t6I1M9VD2NBqpgETUolWrlSv68.fifo",
            "queueType": "FifoQueue",
            "accessKeyId": "dummy-access-key-id",
            "secretAccessKey": "dummy-secret-access-key",
            "sessionToken": "dummy-session-token",
            "expiresAt": "2023-05-31T05:26:04.461Z"
        }
    }
}`

	res, err := s.Register(ctx, &types.ICMSRegistrationRequest{
		K8sVersion: "1.27.8",
	})
	if assert.NoError(t, err) {
		assert.Equal(t, res.ClusterID, "testClusterID")
		assert.Len(t, res.Credentials.CreationQueues, 1)
		if assert.Contains(t, res.Credentials.CreationQueues, testGPUNameDefault) {
			assert.Equal(t, res.Credentials.CreationQueues[testGPUNameDefault].GPU, string(testGPUNameDefault))
		}
	}
}

func TestICMSGetCreds(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = queueCreds

	res, err := s.GetCreds(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, res)
	assert.Len(t, res.CreationQueues, 1)
	if assert.Contains(t, res.CreationQueues, testGPUNameDefault) {
		assert.Equal(t, res.CreationQueues[testGPUNameDefault].AccessKey, "dummy-access-key-id")
	}
	assert.Equal(t, res.TerminationQueue.AccessKey, "dummy-access-key-id")

	m.body = `{
    "credentials": {
 000/q_gdn_icms_byoc_oauth-stg-0CBtUXmR8i1HFNvm7t6I1M9VD2NBqpgETUolWrlSv68.fifo",
            "queueType": "FifoQueue",
            "accessKeyId": "dummy-access-key-id",
            "secretAccessKey": "dummy-secret-access-key",
            "sessionToken": "dummy-session-token",
            "expiresAt": "2023-05-31T05:26:04.461Z"
        }
    }
}`
	res, err = s.GetCreds(ctx)
	assert.NotNil(t, err)
	assert.Nil(t, res)
}

func TestICMSPutAck(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Acknowledgement accepted`

	err := s.PutRequestAcknowledgement(ctx, "randomRequestID", "randomBatchRequestID", 1, nvcav2beta1.ICMSRequestTraceContextConfig{})
	assert.NoError(t, err)

	m.code = http.StatusMethodNotAllowed
	m.body = `Acknowledgement rejected`

	err = s.PutRequestAcknowledgement(ctx, "randomRequestID", "randomBatchRequestID", 1, nvcav2beta1.ICMSRequestTraceContextConfig{})
	assert.NotNil(t, err)

	m.code = http.StatusPreconditionFailed
	m.body = `Acknowledgement rejected, request already closed by client`

	err = s.PutRequestAcknowledgement(ctx, "randomRequestID", "randomBatchRequestID", 1, nvcav2beta1.ICMSRequestTraceContextConfig{})
	assert.NotNil(t, err)
}

func TestICMSHealthStatus(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	response, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status: types.HealthStatusHealthy,
	})
	assert.NoError(t, err)
	assert.Equal(t, types.HealthActionAccepted, response.Action, "Response should contain ACCEPTED action")

	m.code = http.StatusMethodNotAllowed
	m.body = `Request rejected`

	response, err = s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status: types.HealthStatusHealthy,
	})
	assert.NotNil(t, err)
	assert.Nil(t, response)
}

func TestICMSHealthStatusV1NVCA(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status: types.HealthStatusHealthy,
		GPUUsage: map[types.GPUName]types.GPUResource{
			"A100": {
				Capacity:  20,
				Allocated: 10,
			},
		},
	})
	assert.NoError(t, err)

	m.code = http.StatusMethodNotAllowed
	m.body = `Request rejected`

	_, err = s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status: types.HealthStatusHealthy,
		GPUUsage: map[types.GPUName]types.GPUResource{
			"A100": {
				Capacity:  20,
				Allocated: 10,
			},
		},
	})
	assert.NotNil(t, err)
}

func TestICMSHealthStatus_WithNVCAOperatorVersion(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		ClusterOwnerNCAID:   "test-nca-id",
		NVCAAgentVersion:    "v1.0.1",
		NVCAOperatorVersion: "v1.2.3",
		ClusterName:         "test-cluster",
		GPUUsage: map[types.GPUName]types.GPUResource{
			"A100": {
				Capacity:  20,
				Allocated: 10,
			},
		},
	})
	assert.NoError(t, err)

	// Verify the request was sent with the correct data
	assert.NotNil(t, m.req)
	assert.Equal(t, "POST", m.req.Method)
	assert.Contains(t, m.req.URL.Path, "/v1/nvca/clusters")
}

func TestICMSHealthStatus_WithoutNVCAOperatorVersion(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status:            types.HealthStatusHealthy,
		ClusterOwnerNCAID: "test-nca-id",
		NVCAAgentVersion:  "v1.0.1",
		ClusterName:       "test-cluster",
		// NVCAOperatorVersion not set - should be empty string
		GPUUsage: map[types.GPUName]types.GPUResource{
			"A100": {
				Capacity:  20,
				Allocated: 10,
			},
		},
	})
	assert.NoError(t, err)

	// Verify the request was sent with the correct data
	assert.NotNil(t, m.req)
	assert.Equal(t, "POST", m.req.Method)
	assert.Contains(t, m.req.URL.Path, "/v1/nvca/clusters")
}

func TestICMSInstanceStatusUpdate(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	err := s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", &types.ICMSInstanceStatusUpdateRequest{
		Status:        types.ICMSRequestFulfilled,
		InstanceState: types.ICMSInstanceRunning,
	})
	assert.NoError(t, err)

	m.code = http.StatusMethodNotAllowed
	m.body = `Request rejected`

	err = s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", &types.ICMSInstanceStatusUpdateRequest{
		Status:        types.ICMSRequestFulfilled,
		InstanceState: types.ICMSInstanceRunning,
	})
	assert.NotNil(t, err)

	m.code = http.StatusPreconditionFailed
	m.body = `Instance Status rejected, request already closed by client`

	err = s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", &types.ICMSInstanceStatusUpdateRequest{
		Status:        types.ICMSRequestFulfilled,
		InstanceState: types.ICMSInstanceRunning,
	})
	assert.NotNil(t, err)

	m.code = http.StatusPreconditionFailed
	m.body = `Termination Instance Status rejected, request already closed by client`

	err = s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", &types.ICMSInstanceStatusUpdateRequest{
		Status:        types.ICMSRequestInstanceTerminatedByService,
		InstanceState: types.ICMSInstanceTerminated,
		SystemFailure: string(types.ICMSInstanceFailedInitContainerStuck),
	})
	assert.NoError(t, err)

	m.code = http.StatusNotFound
	m.body = `Instance Not Found, may have been already terminated`
	err = s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", &types.ICMSInstanceStatusUpdateRequest{
		Status:        types.ICMSRequestInstanceTerminatedNoCapacity,
		InstanceState: types.ICMSInstanceTerminated,
		SystemFailure: string(types.ICMSInstanceTerminatedPreconditionFailure),
	})
	assert.NoError(t, err)
}

func TestICMSInstanceStatusUpdate_NormalizesICMSActionsOnWire(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body = `Request accepted`

	testCases := []struct {
		name         string
		action       common.MessageAction
		legacyAction string
	}{
		{
			name:         "function action",
			action:       common.RequestICMSInstances,
			legacyAction: string(legacyFunctionCreationAction),
		},
		{
			name:         "task action",
			action:       common.RequestICMSInstancesForTask,
			legacyAction: string(legacyTaskCreationAction),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			payload := &types.ICMSInstanceStatusUpdateRequest{
				Status:        types.ICMSRequestFulfilled,
				InstanceState: types.ICMSInstanceRunning,
				Action:        tc.action,
			}

			err := s.PostInstanceStatusUpdate(ctx, "randomRequestID", "randomInstanceID", payload)
			require.NoError(t, err)

			req := m.getRequest()
			require.NotNil(t, req)

			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			var posted map[string]any
			require.NoError(t, json.Unmarshal(body, &posted))
			assert.Equal(t, tc.legacyAction, posted["action"])
			assert.Equal(t, string(types.ICMSRequestFulfilled), posted["status"])
			assert.Equal(t, string(types.ICMSInstanceRunning), posted["instanceState"])

			assert.Equal(t, tc.action, payload.Action)
		})
	}
}

func TestNormalizeInstanceStatusUpdateRequestForLegacyAPI(t *testing.T) {
	testCases := []struct {
		name         string
		input        *types.ICMSInstanceStatusUpdateRequest
		expected     common.MessageAction
		expectSame   bool
		expectNilOut bool
	}{
		{
			name:         "nil input",
			input:        nil,
			expectNilOut: true,
		},
		{
			name: "function action normalizes to legacy wire action",
			input: &types.ICMSInstanceStatusUpdateRequest{
				Action:        common.RequestICMSInstances,
				Status:        types.ICMSRequestFulfilled,
				InstanceState: types.ICMSInstanceRunning,
			},
			expected:   legacyFunctionCreationAction,
			expectSame: false,
		},
		{
			name: "task action normalizes to legacy wire action",
			input: &types.ICMSInstanceStatusUpdateRequest{
				Action:        common.RequestICMSInstancesForTask,
				Status:        types.ICMSRequestFulfilled,
				InstanceState: types.ICMSInstanceRunning,
			},
			expected:   legacyTaskCreationAction,
			expectSame: false,
		},
		{
			name: "non-legacy action passes through",
			input: &types.ICMSInstanceStatusUpdateRequest{
				Action:        common.TerminationAction,
				Status:        types.ICMSRequestFulfilled,
				InstanceState: types.ICMSInstanceRunning,
			},
			expected:   common.TerminationAction,
			expectSame: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			normalized := normalizeInstanceStatusUpdateRequestForLegacyAPI(tc.input)
			if tc.expectNilOut {
				assert.Nil(t, normalized)
				return
			}

			require.NotNil(t, normalized)
			assert.Equal(t, tc.expected, normalized.Action)
			assert.Equal(t, tc.input.Status, normalized.Status)
			assert.Equal(t, tc.input.InstanceState, normalized.InstanceState)
			if tc.expectSame {
				assert.Same(t, tc.input, normalized)
			} else {
				assert.NotSame(t, tc.input, normalized)
			}
		})
	}
}

func TestSyncPeriodicInstanceStatus(t *testing.T) {

	ctx := newTestContext()
	s, m := setupTestICMSClient()

	m.code = http.StatusOK
	m.body =
		`{"instances":[{"instanceId":"p1","requestId":"38fc59fe","instanceState":"starting"}]}`

	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10)).
		WithRequestsNamespace("nvcf-backend").
		WithWorkerDegradationHandler(true)
	assert.NotNil(t, b)

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "req1",
				Namespace: "nvcf-backend",
				Labels: map[string]string{
					nvcatypes.ICMSRequestIDKey: "38fc59fe",
				},
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "starting"},
				},
			},
		},
	}

	p1Pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "nvcf-backend"},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodInitialized,
					Status: v1.ConditionTrue,
				},
				{
					Type:   v1.ContainersReady,
					Status: v1.ConditionFalse,
				},
				{
					Type:   v1.PodReady,
					Status: v1.ConditionFalse,
				},
			},
		},
	}

	clients := bartClientWithPresetSR(srs, p1Pod)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		ClusterID:          "random-id-123",
		ClusterGroupID:     "random-cgid",
		ICMSURL:            "https://icms.nvcf.nvidia.com",
		CloudProvider:      "on-prem",
		KubeConfigPath:     "test/kubeconfig.yaml",
		NamespaceLabels:    labels.Set{"foo": "bar"},
		FeatureFlagFetcher: featureflag.DefaultFetcher,
		MetricsRegisterer:  prometheus.NewRegistry(),
	}
	ag, err := NewAgent(ctx, &agentOpts)
	assert.Nil(t, err)

	ag.backendk8scache = bc
	ag.icmsClient = s

	err = ag.SyncPeriodicInstanceStatuses(ctx)
	assert.Nil(t, err)

	srs = []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "req1",
				Namespace: "nvcf-backend",
				Labels: map[string]string{
					nvcatypes.ICMSRequestIDKey: "38fc59f",
				},
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "starting"},
				},
			},
		},
	}

	p1Pod = &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "nvcf-backend"},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodInitialized,
					Status: v1.ConditionTrue,
				},
				{
					Type:   v1.ContainersReady,
					Status: v1.ConditionFalse,
				},
				{
					Type:   v1.PodReady,
					Status: v1.ConditionFalse,
				},
			},
		},
	}

	clients = bartClientWithPresetSR(srs, p1Pod)
	bc, _, err = b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	ag.backendk8scache = bc

	m.code = http.StatusOK
	m.body =
		`{"instances":[{"instanceId":"p1","requestId":"38fc59f","instanceState":"running"}]}`

	err = ag.SyncPeriodicInstanceStatuses(ctx)
	assert.NoError(t, err)

	is := types.ICMSServerInstanceState{InstanceID: "p1", RequestID: "38fc59f", InstanceState: types.ICMSInstanceRunning}

	_, _, ra, _ := bc.ReconcileInstanceStatus(ctx, is)
	assert.Equal(t, ra, ICMSInstanceReconcileUpdateOnly)

	is = types.ICMSServerInstanceState{InstanceID: "p1", RequestID: "38fc59f", InstanceState: types.ICMSInstanceStarted}
	_, _, ra, _ = bc.ReconcileInstanceStatus(ctx, is)
	assert.Equal(t, ra, ICMSInstanceReconcileNoAction)

	is = types.ICMSServerInstanceState{InstanceID: "p1", RequestID: "38fc59f", InstanceState: types.ICMSInstanceShuttingDown}
	_, _, ra, _ = bc.ReconcileInstanceStatus(ctx, is)
	assert.Equal(t, ra, ICMSInstanceReconcileTerminateAndUpdate)

	// Instance it not found should be handled.
	m.code = http.StatusOK
	m.body =
		`{"instances":[{"instanceId":"p2","requestId":"38fc59f","instanceState":"running"}]}`

	err = ag.SyncPeriodicInstanceStatuses(ctx)
	assert.NoError(t, err)

	is = types.ICMSServerInstanceState{InstanceID: "p2", RequestID: "38fc59f", InstanceState: types.ICMSInstanceShuttingDown}
	_, _, ra, _ = bc.ReconcileInstanceStatus(ctx, is)
	assert.Equal(t, ra, ICMSInstanceReconcileTerminateAndUpdate)
}

func TestNVClusterIDHeader(t *testing.T) {
	ctx := context.Background()
	clusterID := uuid.NewString()
	tokFetcher := &mockTokenFetcher{
		token: "myToken",
	}
	var counter uint32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, clusterID, r.Header.Get(types.HeaderNVClusterID))
		atomic.AddUint32(&counter, 1)
	}))
	t.Cleanup(s.Close)
	sc := NewICMSClient(ctx, clusterID, s.URL, tokFetcher, nil)
	sc.PutHealthStatus(ctx, &types.HealthStatusRequest{})
	assert.Equal(t, uint32(1), counter)
}

func TestOTelHeadersPutRequestAcknowledgement(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	t.Cleanup(func() { _ = exporter.Shutdown(ctx) })
	tp := otelsdktrace.NewTracerProvider(otelsdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(ctx) })
	otel.SetTextMapPropagator(otelpropagation.NewCompositeTextMapPropagator(
		otelpropagation.Baggage{},
		otelpropagation.TraceContext{},
	))

	traceParent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	clusterID := uuid.NewString()
	tokFetcher := &mockTokenFetcher{
		token: "myToken",
	}
	var counter uint32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, clusterID, r.Header.Get(types.HeaderNVClusterID))
		atomic.AddUint32(&counter, 1)
		assert.Regexp(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-.*$", r.Header.Get("Traceparent"))
		assert.Regexp(t, ".*key1=value1.*", r.Header.Get("Tracestate"))
		assert.Regexp(t, ".*key2=value2.*", r.Header.Get("Tracestate"))
	}))
	t.Cleanup(s.Close)
	sc := NewICMSClient(ctx, clusterID, s.URL, tokFetcher, tp.Tracer(""))
	sc.PutRequestAcknowledgement(ctx, "sr-1", "batch-id-1", 1, nvcav2beta1.ICMSRequestTraceContextConfig{
		TraceParent: traceParent,
		TraceState: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	})
	assert.Equal(t, uint32(1), counter)
}

func TestICMSHealthStatus_WithMaintenanceMode(t *testing.T) {
	ctx := newTestContext()

	testCases := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
		expectedMode    types.MaintenanceMode
	}{
		{
			name:            "Normal mode",
			maintenanceMode: types.MaintenanceModeNone,
			expectedMode:    types.MaintenanceModeNone,
		},
		{
			name:            "Cordon mode",
			maintenanceMode: types.MaintenanceModeCordon,
			expectedMode:    types.MaintenanceModeCordon,
		},
		{
			name:            "Cordon and drain mode",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
			expectedMode:    types.MaintenanceModeCordonAndDrain,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := setupTestICMSClient()
			m.code = http.StatusOK
			m.body = `Request accepted`

			_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
				Status:              types.HealthStatusHealthy,
				ClusterOwnerNCAID:   "test-nca-id",
				NVCAAgentVersion:    "v1.0.1",
				NVCAOperatorVersion: "v1.2.3",
				ClusterName:         "test-cluster",
				MaintenanceMode:     tc.maintenanceMode,
				GPUUsage: map[types.GPUName]types.GPUResource{
					"A100": {
						Capacity:  20,
						Allocated: 10,
					},
				},
			})
			assert.NoError(t, err)

			// Verify the request was sent with the correct data
			assert.NotNil(t, m.req)
			assert.Equal(t, "POST", m.req.Method)
			assert.Contains(t, m.req.URL.Path, "/v1/nvca/clusters")

			// Parse the request body to verify maintenance mode is included
			var requestBody types.HealthStatusRequest
			bodyBytes, err := io.ReadAll(m.req.Body)
			assert.NoError(t, err)
			err = json.Unmarshal(bodyBytes, &requestBody)
			assert.NoError(t, err)

			assert.Equal(t, tc.expectedMode, requestBody.MaintenanceMode)
			assert.Equal(t, tc.expectedMode.String(), requestBody.MaintenanceMode.String())
		})
	}
}

func TestICMSHealthStatus_MaintenanceModeStringMethod(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()
	m.code = http.StatusOK
	m.body = `Request accepted`

	// Test that MaintenanceMode's String() method works correctly
	maintenanceMode := types.MaintenanceModeCordonAndDrain

	_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		ClusterOwnerNCAID:   "test-nca-id",
		NVCAAgentVersion:    "v1.0.1",
		NVCAOperatorVersion: "v1.2.3",
		ClusterName:         "test-cluster",
		MaintenanceMode:     maintenanceMode,
		GPUUsage: map[types.GPUName]types.GPUResource{
			"A100": {
				Capacity:  20,
				Allocated: 10,
			},
		},
	})
	assert.NoError(t, err)

	// Verify the String() method returns the correct value
	assert.Equal(t, "CordonAndDrain", maintenanceMode.String())

	// Parse the request body to verify maintenance mode string is serialized correctly
	var requestBody types.HealthStatusRequest
	bodyBytes, err := io.ReadAll(m.req.Body)
	assert.NoError(t, err)
	err = json.Unmarshal(bodyBytes, &requestBody)
	assert.NoError(t, err)

	assert.Equal(t, types.MaintenanceModeCordonAndDrain, requestBody.MaintenanceMode)
}

func TestICMSHealthStatus_MaintenanceModeJSONSerialization(t *testing.T) {
	// Test that MaintenanceMode is properly serialized to JSON
	testCases := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
		expectedJSON    string
	}{
		{
			name:            "Normal mode",
			maintenanceMode: types.MaintenanceModeNone,
			expectedJSON:    `"None"`,
		},
		{
			name:            "Cordon mode",
			maintenanceMode: types.MaintenanceModeCordon,
			expectedJSON:    `"CordonOnly"`,
		},
		{
			name:            "Cordon and drain mode",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
			expectedJSON:    `"CordonAndDrain"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &types.HealthStatusRequest{
				Status:              types.HealthStatusHealthy,
				ClusterOwnerNCAID:   "test-nca-id",
				NVCAAgentVersion:    "v1.0.1",
				NVCAOperatorVersion: "v1.2.3",
				ClusterName:         "test-cluster",
				MaintenanceMode:     tc.maintenanceMode,
				GPUUsage: map[types.GPUName]types.GPUResource{
					"A100": {
						Capacity:  20,
						Allocated: 10,
					},
				},
			}

			// Serialize to JSON
			jsonData, err := json.Marshal(req)
			assert.NoError(t, err)

			// Check that the maintenance mode is properly serialized
			assert.Contains(t, string(jsonData), fmt.Sprintf(`"maintenanceMode":%s`, tc.expectedJSON))

			// Deserialize back to verify round-trip
			var deserializedReq types.HealthStatusRequest
			err = json.Unmarshal(jsonData, &deserializedReq)
			assert.NoError(t, err)
			assert.Equal(t, tc.maintenanceMode, deserializedReq.MaintenanceMode)
		})
	}
}

type mockTokenFetcher struct {
	token string
	err   error
}

func (m *mockTokenFetcher) FetchToken(ctx context.Context) (string, error) {
	return m.token, m.err
}

func TestICMSClient_checkResponse(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responseBody   []byte
		expectedError  error
		expectedIsHTTP bool
	}{
		{
			name:           "200 OK",
			statusCode:     http.StatusOK,
			responseBody:   []byte("success"),
			expectedError:  nil,
			expectedIsHTTP: false,
		},
		{
			name:           "404 Not Found",
			statusCode:     http.StatusNotFound,
			responseBody:   []byte("not found"),
			expectedError:  nvcaerrors.HTTPStatusError(http.StatusNotFound, nil),
			expectedIsHTTP: true,
		},
		{
			name:           "412 Precondition Failed",
			statusCode:     http.StatusPreconditionFailed,
			responseBody:   []byte("precondition failed"),
			expectedError:  nvcaerrors.HTTPStatusError(http.StatusPreconditionFailed, nil),
			expectedIsHTTP: true,
		},
		{
			name:           "500 Internal Server Error",
			statusCode:     http.StatusInternalServerError,
			responseBody:   []byte("server error"),
			expectedError:  nvcaerrors.HTTPStatusError(http.StatusInternalServerError, nil),
			expectedIsHTTP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &ICMSClient{}
			log := logrus.New().WithField("test", tt.name)

			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(tt.responseBody)), 1024),
			}

			err := client.checkResponse(log, resp, tt.responseBody)

			if tt.expectedError == nil {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				if tt.expectedIsHTTP {
					assert.True(t, nvcaerrors.IsHTTPStatus(err))
					assert.Equal(t, tt.statusCode, nvcaerrors.GetHTTPStatusCode(err))
				}
				if tt.statusCode == http.StatusPreconditionFailed {
					assert.True(t, nvcaerrors.GetHTTPStatusCode(err) == http.StatusPreconditionFailed)
				}
			}
		})
	}
}

func TestICMSPathConfig_DefaultPaths(t *testing.T) {
	ctx := newTestContext()
	s, m := setupTestICMSClient()
	clusterID := s.clusterID

	// Instance status
	m.setCode(http.StatusOK)
	m.setBody(`{"instances":[]}`)
	_, err := s.GetICMSServerInstanceStatuses(ctx)
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/si/clusters/")
	assert.Contains(t, m.getRequest().URL.Path, clusterID)
	assert.Contains(t, m.getRequest().URL.Path, "/instances")

	// Register
	m.setCode(http.StatusOK)
	m.setBody(`{"clusterId":"c","credentials":{}}`)
	_, err = s.Register(ctx, &types.ICMSRegistrationRequest{K8sVersion: "1.27"})
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/nvca/clusters/")
	assert.Contains(t, m.getRequest().URL.Path, "/register")

	// Credentials
	m.setCode(http.StatusOK)
	m.setBody(`{}`)
	_, err = s.GetCreds(ctx)
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/nvca/clusters/")
	assert.Contains(t, m.getRequest().URL.Path, "/credentials")

	// Heartbeat
	m.setCode(http.StatusOK)
	m.setBody(`{"action":"ACCEPTED"}`)
	_, err = s.PutHealthStatus(ctx, &types.HealthStatusRequest{Status: types.HealthStatusHealthy, ClusterOwnerNCAID: "nca", NVCAAgentVersion: "1.0", ClusterName: "c"})
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/nvca/clusters/")
	assert.Contains(t, m.getRequest().URL.Path, "/heartbeat")

	// SIR ack
	m.setCode(http.StatusOK)
	m.setBody(``)
	err = s.PutRequestAcknowledgement(ctx, "req-1", "batch-1", 1, nvcav2beta1.ICMSRequestTraceContextConfig{})
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/sirs/")
	assert.Contains(t, m.getRequest().URL.Path, "req-1")

	// Instance status update
	m.setCode(http.StatusOK)
	m.setBody(``)
	err = s.PostInstanceStatusUpdate(ctx, "req-1", "inst-1", &types.ICMSInstanceStatusUpdateRequest{Status: types.ICMSRequestFulfilled, InstanceState: types.ICMSInstanceRunning})
	require.NoError(t, err)
	require.NotNil(t, m.getRequest())
	assert.Contains(t, m.getRequest().URL.Path, "/v1/sirs/req-1/inst-1")
}

func TestICMSPathConfig_CustomPathsFromEnv(t *testing.T) {
	ctx := newTestContext()
	tc, mockToken := setJWTCacheTest()
	token := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MTkzNTExLCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg3MTUwMzExLCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.icoV7ZDRCs7PAnDVQmuH5ZqeRBZMbExdN1ztCtsv7dwij0c6LpygdDMta7VkEuYfqijuFscHbgkMMicxkdTbgIKYxWNi4vMuBXKSbDO50Z4IkqHnzxrVJ4vI_hcGKdTCt_yOgTQkQ97HvKrzTG-eOhYgGQhXyk5mDhT7bv4VGprGGYql-D8ijeG7-gq_IvKT6XWl8Mvl3JZeyt8W4BGCbUHut-34pQwLN1_qs03EHFnUfIZY9S0XD7Wm2cCVgYJAOpPeHunYROhXFSe44Oq0wCWDoRKzXaGLXJP8vkpyJWcPqfGnYJ8Sr0SFKxmIdJPWQR4IggRlZbXRZ-3jtIIEYA`
	mockToken.token, mockToken.tokenErr = token, nil
	tc = tc.WithNowFunc(func() time.Time { return timeFromString("2020-04-17T10:00:00-07:00").Time })

	clusterID := "test-cluster-id"
	base := "http://localhost/icms"
	mt := newMockTransport()
	mt.setCode(http.StatusOK)

	t.Run("NVCA_ICMS_PATH_INSTANCE_STATUS", func(t *testing.T) {
		t.Setenv(envICMSPathInstanceStatus, "custom/si/clusters/%s/instances")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(`{"instances":[]}`)
		_, err := s.GetICMSServerInstanceStatuses(ctx)
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "custom/si/clusters/")
		assert.Contains(t, req.URL.Path, clusterID)
		assert.Contains(t, req.URL.Path, "/instances")
	})

	t.Run("NVCA_ICMS_PATH_NVCA_PREFIX", func(t *testing.T) {
		t.Setenv(envICMSPathNVCAPrefix, "custom/v1/nvca")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(`{"clusterId":"c","credentials":{}}`)
		_, err := s.Register(ctx, &types.ICMSRegistrationRequest{K8sVersion: "1.27"})
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "custom/v1/nvca/clusters/")
		assert.Contains(t, req.URL.Path, "/register")
	})

	t.Run("NVCA_ICMS_PATH_REGISTER", func(t *testing.T) {
		t.Setenv(envICMSPathRegister, "api/custom/register/%s")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(`{"clusterId":"c","credentials":{}}`)
		_, err := s.Register(ctx, &types.ICMSRegistrationRequest{K8sVersion: "1.27"})
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "api/custom/register/")
		assert.Contains(t, req.URL.Path, clusterID)
	})

	t.Run("NVCA_ICMS_PATH_CREDENTIALS", func(t *testing.T) {
		t.Setenv(envICMSPathCredentials, "api/creds/%s")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(`{}`)
		_, err := s.GetCreds(ctx)
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "api/creds/")
		assert.Contains(t, req.URL.Path, clusterID)
	})

	t.Run("NVCA_ICMS_PATH_HEARTBEAT", func(t *testing.T) {
		t.Setenv(envICMSPathHeartbeat, "api/hb/%s")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(`{"action":"ACCEPTED"}`)
		_, err := s.PutHealthStatus(ctx, &types.HealthStatusRequest{Status: types.HealthStatusHealthy, ClusterOwnerNCAID: "nca", NVCAAgentVersion: "1.0", ClusterName: "c"})
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "api/hb/")
		assert.Contains(t, req.URL.Path, clusterID)
	})

	t.Run("NVCA_ICMS_PATH_SIRS_PREFIX", func(t *testing.T) {
		t.Setenv(envICMSPathSIRsPrefix, "custom/sirs")
		s := NewICMSClient(context.Background(), clusterID, base, tc, nil)
		s.client.Transport = mt
		mt.setBody(``)
		err := s.PutRequestAcknowledgement(ctx, "req-99", "batch-1", 1, nvcav2beta1.ICMSRequestTraceContextConfig{})
		require.NoError(t, err)
		req := mt.getRequest()
		require.NotNil(t, req)
		assert.Contains(t, req.URL.Path, "custom/sirs/req-99")
	})
}
