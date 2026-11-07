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
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cli "github.com/urfave/cli/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	nvcaerrors "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type fakeRegistrationValidator struct {
	err error
	got *types.ICMSRegistrationRequest
}

func (f *fakeRegistrationValidator) ValidateRegistration(_ context.Context, rreq *types.ICMSRegistrationRequest) error {
	f.got = rreq
	return f.err
}

func preflightTestGPUNode(name string, ready bool, extraLabels map[string]string) *corev1.Node {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse("8"),
		corev1.ResourceMemory:           resource.MustParse("30Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
		"nvidia.com/gpu":                resource.MustParse("1"),
	}
	labels := map[string]string{
		types.InstanceTypeLabel:        "NCP.GPU.A100",
		"nvidia.com/gpu.present":       "true",
		"nvidia.com/gpu.product":       "A100-SXM4-40GB",
		"nvidia.com/gpu.family":        "ampere",
		"nvidia.com/gpu.machine":       "test-machine",
		"nvidia.com/gpu.memory":        "40960",
		"nvidia.com/cuda.driver.major": "535",
		"nvidia.com/cuda.driver.minor": "154",
		"nvidia.com/cuda.driver.rev":   "05",
		"kubernetes.io/arch":           "amd64",
		"kubernetes.io/os":             "linux",
	}
	for k, v := range extraLabels {
		labels[k] = v
	}
	readyStatus := corev1.ConditionTrue
	if !ready {
		readyStatus = corev1.ConditionFalse
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: readyStatus}},
			Capacity:    resources,
			Allocatable: resources,
		},
	}
}

func preflightTestOptions() *AgentOptions {
	return &AgentOptions{
		FeatureFlagFetcher:         featureflag.DefaultFetcher,
		DynamicGPUDiscoveryEnabled: true,
		CloudProvider:              "NCP",
		K8sVersion:                 "1.30.0",
		NVCAAgentVersion:           "3.20.0",
		ClusterTargetingEnabled:    true,
		MaintenanceMode:            types.MaintenanceModeCordon,
	}
}

func preflightTestNodes() []*corev1.Node {
	cpuOnly := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cpu-1"}}
	return []*corev1.Node{
		preflightTestGPUNode("gpu-1", true, nil),
		preflightTestGPUNode("gpu-2", true, nil),
		preflightTestGPUNode("gpu-not-ready", false, nil),
		cpuOnly,
	}
}

func runTestRegistrationPreflight(
	t *testing.T,
	nodes []*corev1.Node,
	validator *fakeRegistrationValidator,
) types.RegistrationPreflightResult {
	t.Helper()
	k8s := k8sfake.NewSimpleClientset()
	for _, n := range nodes {
		_, err := k8s.CoreV1().Nodes().Create(t.Context(), n, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	p := &registrationPreflight{
		opts:      preflightTestOptions(),
		clients:   &kubeclients.KubeClients{K8s: k8s},
		validator: validator,
	}
	return p.run(newTestContext())
}

func TestRegistrationPreflightValidatesTheAgentRegistration(t *testing.T) {
	validator := &fakeRegistrationValidator{}
	result := runTestRegistrationPreflight(t, preflightTestNodes(), validator)

	assert.Equal(t, types.RegistrationPreflightAccepted, result.Outcome)
	require.NotNil(t, validator.got)
	assert.Equal(t, "CORDON", validator.got.ClusterStatus)
	assert.Equal(t, "1.30.0", validator.got.K8sVersion)
	assert.True(t, validator.got.ClusterTargeting)
	assert.True(t, validator.got.TaskClusterCreationQueues)
	assert.Equal(t, "3.20.0", validator.got.NVCAVersion)
	require.Len(t, validator.got.BackendGPUs, 1)
	gpu := validator.got.BackendGPUs[0]
	assert.Equal(t, "A100", gpu.Name)
	require.NotEmpty(t, gpu.InstanceTypes)
	it := gpu.InstanceTypes[0]
	assert.Equal(t, "NCP.GPU.A100_1x", it.Name)
	assert.Equal(t, uint64(2), it.MaxInstances, "the two ready GPU nodes")
	assert.Equal(t, uint64(1), it.UnschedulableCapacity, "the GPU node that is not ready")
}

func TestRegistrationPreflightAppliesSharedClusterMode(t *testing.T) {
	nodes := preflightTestNodes()
	nodes[0].Labels[sharedcluster.ScheduleLabelKey] = "true"
	validator := &fakeRegistrationValidator{}

	result := runTestRegistrationPreflight(t, nodes, validator)

	assert.Equal(t, types.RegistrationPreflightAccepted, result.Outcome)
	require.NotNil(t, validator.got)
	require.Len(t, validator.got.BackendGPUs, 1)
	require.NotEmpty(t, validator.got.BackendGPUs[0].InstanceTypes)
	it := validator.got.BackendGPUs[0].InstanceTypes[0]
	assert.Equal(t, uint64(1), it.MaxInstances, "only the labeled node is in the shared pool")
	assert.Equal(t, uint64(2), it.UnschedulableCapacity, "the unlabeled node and the node that is not ready")
}

func TestRegistrationPreflightWithoutGPUs(t *testing.T) {
	validator := &fakeRegistrationValidator{}
	result := runTestRegistrationPreflight(t, nil, validator)

	assert.Equal(t, types.RegistrationPreflightError, result.Outcome)
	assert.Contains(t, result.Message, "discover GPUs")
	assert.Nil(t, validator.got, "nothing is sent to ICMS")
}

func TestRegistrationPreflightFallsBackWithoutValidationRoute(t *testing.T) {
	validator := &fakeRegistrationValidator{
		err: nvcaerrors.HTTPStatusError(http.StatusNotFound,
			fmt.Errorf("%w: 404 Not Found", ErrRegistrationValidationUnsupported)),
	}
	result := runTestRegistrationPreflight(t, preflightTestNodes(), validator)

	assert.Equal(t, types.RegistrationPreflightUnsupported, result.Outcome)
	assert.Equal(t, http.StatusNotFound, result.HTTPStatus)
	assert.Equal(t, 3, result.Outcome.ExitCode())
}

func TestRegistrationPreflightResult(t *testing.T) {
	icmsErr := func(code int, body types.ICMSRegistrationError) error {
		return nvcaerrors.HTTPStatusError(code, &ICMSErrorResponse{
			StatusCode: code,
			Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
			Body:       body,
		})
	}
	conflictBody := types.ICMSRegistrationError{
		Message:           "cluster registration failed",
		Reason:            types.ICMSReasonActiveInstancesOnRemovedInstanceTypes,
		InstanceTypes:     []string{"NCP.GPU.A100_1x"},
		ActiveInstanceIDs: []string{"i-1"},
	}
	hint := func(*types.ICMSRegistrationError) string { return "the overhead leaves no room" }

	tests := []struct {
		name     string
		err      error
		want     types.RegistrationPreflightResult
		wantExit int
	}{
		{
			name:     "accepted",
			want:     types.RegistrationPreflightResult{Outcome: types.RegistrationPreflightAccepted, Message: "ICMS accepts the registration"},
			wantExit: 0,
		},
		{
			name: "active instances on removed instance types",
			err:  icmsErr(http.StatusConflict, conflictBody),
			want: types.RegistrationPreflightResult{
				Outcome:           types.RegistrationPreflightRejected,
				HTTPStatus:        http.StatusConflict,
				Reason:            types.ICMSReasonActiveInstancesOnRemovedInstanceTypes,
				InstanceTypes:     []string{"NCP.GPU.A100_1x"},
				ActiveInstanceIDs: []string{"i-1"},
				Message:           "ICMS rejects the registration: cluster registration failed; the overhead leaves no room",
			},
			wantExit: 2,
		},
		{
			name: "unknown cluster",
			err:  icmsErr(http.StatusNotFound, types.ICMSRegistrationError{Message: "Cluster with clusterId c1 doesn't exists"}),
			want: types.RegistrationPreflightResult{
				Outcome:    types.RegistrationPreflightRejected,
				HTTPStatus: http.StatusNotFound,
				Message:    "ICMS rejects the registration: Cluster with clusterId c1 doesn't exists",
			},
			wantExit: 2,
		},
		{
			name: "invalid request",
			err:  icmsErr(http.StatusBadRequest, types.ICMSRegistrationError{Message: "k8sVersion must be provided"}),
			want: types.RegistrationPreflightResult{
				Outcome:    types.RegistrationPreflightRejected,
				HTTPStatus: http.StatusBadRequest,
				Message:    "ICMS rejects the registration: k8sVersion must be provided",
			},
			wantExit: 2,
		},
		{
			name: "no validation route",
			err: nvcaerrors.HTTPStatusError(http.StatusNotFound,
				fmt.Errorf("%w: 404 Not Found", ErrRegistrationValidationUnsupported)),
			want: types.RegistrationPreflightResult{
				Outcome:    types.RegistrationPreflightUnsupported,
				HTTPStatus: http.StatusNotFound,
				Message:    "HTTP 404: ICMS does not support registration validation: 404 Not Found",
			},
			wantExit: 3,
		},
		{
			name: "ICMS failure",
			err:  icmsErr(http.StatusInternalServerError, types.ICMSRegistrationError{Message: "internal error"}),
			want: types.RegistrationPreflightResult{
				Outcome:    types.RegistrationPreflightError,
				HTTPStatus: http.StatusInternalServerError,
				Message:    "HTTP 500: error response from ICMS: 500 Internal Server Error: internal error",
			},
			wantExit: 1,
		},
		{
			name: "unauthorized",
			err:  nvcaerrors.HTTPStatusError(http.StatusUnauthorized, errors.New("error response from ICMS: 401 Unauthorized")),
			want: types.RegistrationPreflightResult{
				Outcome:    types.RegistrationPreflightError,
				HTTPStatus: http.StatusUnauthorized,
				Message:    "HTTP 401: error response from ICMS: 401 Unauthorized",
			},
			wantExit: 1,
		},
		{
			name: "ICMS unreachable",
			err:  errors.New("dial tcp: connection refused"),
			want: types.RegistrationPreflightResult{
				Outcome: types.RegistrationPreflightError,
				Message: "dial tcp: connection refused",
			},
			wantExit: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preflightResult(tt.err, hint)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantExit, got.Outcome.ExitCode())
		})
	}
}

func TestRegistrationPreflightTerminationMessage(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("0-sr-%036d", i)
	}
	result := types.RegistrationPreflightResult{
		Outcome:           types.RegistrationPreflightRejected,
		HTTPStatus:        http.StatusConflict,
		Reason:            types.ICMSReasonActiveInstancesOnRemovedInstanceTypes,
		InstanceTypes:     []string{"NCP.GPU.A100_1x"},
		ActiveInstanceIDs: ids,
		Message:           strings.Repeat("é", 3000),
	}

	msg := registrationPreflightTerminationMessage(result)

	assert.LessOrEqual(t, len(msg), 4096, "Kubernetes keeps at most 4096 bytes")
	var got types.RegistrationPreflightResult
	require.NoError(t, json.Unmarshal(msg, &got))
	assert.Equal(t, types.RegistrationPreflightRejected, got.Outcome)
	assert.Equal(t, ids[:20], got.ActiveInstanceIDs)
	assert.True(t, strings.HasSuffix(got.Message, "..."))
	assert.LessOrEqual(t, len(got.Message), 1024)
}

func TestRegistrationPreflightCommand(t *testing.T) {
	cfgFilePath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFilePath, []byte(`
agent:
  icmsURL: https://test.example.com
`), 0600))

	for _, outcome := range []types.RegistrationPreflightOutcome{
		types.RegistrationPreflightAccepted,
		types.RegistrationPreflightRejected,
		types.RegistrationPreflightUnsupported,
		types.RegistrationPreflightError,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			var gotOpts *AgentOptions
			orig := runRegistrationPreflightFunc
			t.Cleanup(func() { runRegistrationPreflightFunc = orig })
			runRegistrationPreflightFunc = func(_ context.Context, opts *AgentOptions) types.RegistrationPreflightResult {
				gotOpts = opts
				return types.RegistrationPreflightResult{Outcome: outcome, Message: "result " + string(outcome)}
			}
			cmd := newCobraCommand(
				func(context.Context, *AgentOptions) (cliAgent, error) {
					t.Fatal("the preflight must not start the agent")
					return nil, nil
				},
				func(cli.Generic, string) error { return nil },
				func(*logrus.Entry) logr.Logger { return logr.Discard() },
			)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"preflight-registration", "--config=" + cfgFilePath, "--timeout=30s"})

			err := cmd.ExecuteContext(t.Context())

			require.NotNil(t, gotOpts)
			assert.Equal(t, "https://test.example.com", gotOpts.ICMSURL)
			var printed types.RegistrationPreflightResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &printed))
			assert.Equal(t, outcome, printed.Outcome)
			if outcome == types.RegistrationPreflightAccepted {
				require.NoError(t, err)
				return
			}
			var exitErr *nvcaerrors.ExitCodeError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, outcome.ExitCode(), exitErr.Code)
			assert.EqualError(t, err, "result "+string(outcome))
		})
	}
}

func TestRegistrationPreflightWithin(t *testing.T) {
	t.Run("returns the result of a run that finishes in time", func(t *testing.T) {
		result := runRegistrationPreflightWithin(t.Context(), time.Minute,
			func(context.Context) types.RegistrationPreflightResult {
				return types.RegistrationPreflightResult{Outcome: types.RegistrationPreflightAccepted}
			})
		assert.Equal(t, types.RegistrationPreflightAccepted, result.Outcome)
	})

	t.Run("reports Error at the deadline when the run ignores its context", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		runCtxCh := make(chan context.Context, 1)

		result := runRegistrationPreflightWithin(t.Context(), 50*time.Millisecond,
			func(ctx context.Context) types.RegistrationPreflightResult {
				runCtxCh <- ctx
				<-release
				return types.RegistrationPreflightResult{Outcome: types.RegistrationPreflightAccepted}
			})

		assert.Equal(t, types.RegistrationPreflightError, result.Outcome)
		assert.Equal(t, "the preflight did not finish within 50ms: context deadline exceeded", result.Message)
		assert.Equal(t, 1, result.Outcome.ExitCode())
		assert.ErrorIs(t, (<-runCtxCh).Err(), context.DeadlineExceeded, "the run's context ends at the deadline")
	})
}
