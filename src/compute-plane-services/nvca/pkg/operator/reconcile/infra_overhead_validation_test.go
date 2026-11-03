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

package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcabelister "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	overheadTestInstanceType = "NCP.GPU.A100_1x"
	// overheadTestNodeCPU is the CPU of the single-GPU test node, so all of it
	// belongs to overheadTestInstanceType.
	overheadTestNodeCPU = "7910m"
)

var icmsRequestGVR = nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests")

func overheadTestGPUNode(name string) *corev1.Node {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse(overheadTestNodeCPU),
		corev1.ResourceMemory:           resource.MustParse("30Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
		"nvidia.com/gpu":                resource.MustParse("1"),
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				nvcatypes.InstanceTypeLabel:    "NCP.GPU.A100",
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
			},
		},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Capacity:    resources,
			Allocatable: resources,
		},
	}
}

// overheadTestICMSRequest returns a function request for instanceType. Each
// value of lastReported is the last status NVCA reported for one instance.
func overheadTestICMSRequest(
	name, instanceType string, status nvcav2beta1.RequestStatus, lastReported ...string,
) *nvcav2beta1.ICMSRequest {
	req := &nvcav2beta1.ICMSRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: icmsGVK.GroupVersion().String(), Kind: icmsGVK.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "nvcf-backend"},
	}
	req.Spec.RequestID = name
	req.Spec.CreationMsgInfo.InstanceTypeName = instanceType
	req.Spec.CreationMsgInfo.InstanceCount = uint64(max(1, len(lastReported)))
	req.Spec.FunctionDetails.FunctionID = "fn-" + name
	req.Spec.FunctionDetails.FunctionVersionID = "v1"
	req.Status.RequestStatus = status
	for i, reported := range lastReported {
		if req.Status.Instances == nil {
			req.Status.Instances = map[string]nvcav2beta1.InstanceStatus{}
		}
		id := fmt.Sprintf("%d-%s", i, name)
		req.Status.Instances[id] = nvcav2beta1.InstanceStatus{ID: id, LastReportedStatus: reported}
	}
	return req
}

// overheadTestObjects converts requests through JSON, since the fake dynamic
// client cannot copy the unsigned integers of the typed objects.
func overheadTestObjects(t *testing.T, requests ...*nvcav2beta1.ICMSRequest) []runtime.Object {
	t.Helper()
	objs := make([]runtime.Object, 0, len(requests))
	for _, req := range requests {
		data, err := json.Marshal(req)
		require.NoError(t, err)
		obj := &unstructured.Unstructured{}
		require.NoError(t, obj.UnmarshalJSON(data))
		objs = append(objs, obj)
	}
	return objs
}

func overheadTestAgentConfigCM(t *testing.T, cfg nvcaconfig.Config) *corev1.ConfigMap {
	t.Helper()
	data, err := nvcaconfig.EncodeConfig(cfg)
	require.NoError(t, err)
	return &corev1.ConfigMap{Data: map[string]string{agentConfigFile: string(data)}}
}

func overheadTestAgentConfig(additionalCPU string, featureFlags ...string) nvcaconfig.Config {
	cfg := nvcaconfig.Config{
		Cluster: nvcaconfig.NVCFClusterConfig{CloudProvider: "NCP"},
		Agent:   nvcaconfig.AgentConfig{FeatureFlags: featureFlags},
	}
	if additionalCPU != "" {
		cfg.Agent.AdditionalResourceOverhead = nvcaconfig.ResourceList{
			corev1.ResourceCPU: resource.MustParse(additionalCPU),
		}
	}
	return cfg
}

func TestValidateInfraOverheadForActiveInstances(t *testing.T) {
	enforce := featureflag.EnforceContainerFunctionResourceLimits.Key
	running := string(nvcatypes.ICMSInstanceRunning)
	terminated := string(nvcatypes.ICMSInstanceTerminated)

	tests := []struct {
		name      string
		cfg       nvcaconfig.Config
		agentEnv  map[string]string
		requests  []*nvcav2beta1.ICMSRequest
		wantError []string
	}{
		{
			name:     "overhead fits the instance type",
			cfg:      overheadTestAgentConfig("100m", enforce),
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running)},
		},
		{
			name: "overhead removes an instance type without instances",
			cfg:  overheadTestAgentConfig(overheadTestNodeCPU, enforce),
		},
		{
			name:     "overhead removes an instance type with active instances",
			cfg:      overheadTestAgentConfig("6360m", enforce),
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running, running)},
			wantError: []string{
				"NCP.GPU.A100_1x (cpu 7910m before overhead; active instances: function fn-sr-1 version v1 (2))",
				"lower byoo.additionalResourceOverhead.cpu (6360m) or another infrastructure container resource",
				"NVCA would drop them from its ICMS registration",
			},
		},
		{
			name: "requested instances count before NVCA records them",
			cfg:  overheadTestAgentConfig(overheadTestNodeCPU, enforce),
			requests: []*nvcav2beta1.ICMSRequest{
				overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusPending),
			},
			wantError: []string{"active instances: function fn-sr-1 version v1 (1)"},
		},
		{
			name: "instances whose termination was reported are not active",
			cfg:  overheadTestAgentConfig(overheadTestNodeCPU, enforce),
			requests: []*nvcav2beta1.ICMSRequest{
				overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, terminated),
			},
		},
		{
			name: "failed requests without instances are not active",
			cfg:  overheadTestAgentConfig(overheadTestNodeCPU, enforce),
			requests: []*nvcav2beta1.ICMSRequest{
				overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusFailureAcknowledged),
			},
		},
		{
			name: "instances of another instance type do not conflict",
			cfg:  overheadTestAgentConfig(overheadTestNodeCPU, enforce),
			requests: []*nvcav2beta1.ICMSRequest{
				overheadTestICMSRequest("sr-1", "NCP.GPU.H100_1x", nvcav2beta1.ICMSRequestStatusCompleted, running),
			},
		},
		{
			name:     "overhead is not subtracted without InfraResourceOverhead",
			cfg:      overheadTestAgentConfig(overheadTestNodeCPU),
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running)},
		},
		{
			name: "static instance types are registered without overhead",
			cfg: func() nvcaconfig.Config {
				cfg := overheadTestAgentConfig(overheadTestNodeCPU, enforce)
				cfg.Agent.StaticGPUCapacity = 1
				return cfg
			}(),
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running)},
		},
		{
			name:     "dynamic GPU discovery disabled",
			cfg:      overheadTestAgentConfig(overheadTestNodeCPU, enforce, "-"+featureflag.DynamicGPUDiscovery.Key),
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running)},
		},
		{
			name:     "agent environment overrides the overhead",
			cfg:      overheadTestAgentConfig(overheadTestNodeCPU, enforce),
			agentEnv: map[string]string{"NVCF_UTILS_CPU_LIMIT": "100m"},
			requests: []*nvcav2beta1.ICMSRequest{overheadTestICMSRequest("sr-1", overheadTestInstanceType, nvcav2beta1.ICMSRequestStatusCompleted, running)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			bc := &BackendK8sCache{clients: &kubeclients.KubeClients{
				K8s:           fakek8sclient.NewSimpleClientset(overheadTestGPUNode("gpu-node-1")),
				DynamicClient: fakedynamic.NewSimpleDynamicClient(newTestScheme(), overheadTestObjects(t, tt.requests...)...),
			}}

			err := bc.validateInfraOverheadForActiveInstances(ctx, tt.agentEnv, overheadTestAgentConfigCM(t, tt.cfg))
			if len(tt.wantError) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, isInvalidAgentConfigError(err))
			assert.False(t, nvcaoperatorerrors.IsFatal(err))
			for _, want := range tt.wantError {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestValidateInfraOverheadForActiveInstances_ListErrors(t *testing.T) {
	cfg := overheadTestAgentConfig(overheadTestNodeCPU, featureflag.EnforceContainerFunctionResourceLimits.Key)

	tests := []struct {
		name      string
		listErr   error
		wantRetry bool
	}{
		{
			name:      "transient errors retry the check",
			listErr:   k8serrors.NewServerTimeout(icmsRequestGVR.GroupResource(), "list", 1),
			wantRetry: true,
		},
		{
			name:    "other errors allow the rollout",
			listErr: k8serrors.NewForbidden(icmsRequestGVR.GroupResource(), "", fmt.Errorf("denied")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			dynamicClient := fakedynamic.NewSimpleDynamicClient(newTestScheme())
			dynamicClient.PrependReactor("list", "icmsrequests",
				func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tt.listErr })
			bc := &BackendK8sCache{clients: &kubeclients.KubeClients{
				K8s:           fakek8sclient.NewSimpleClientset(overheadTestGPUNode("gpu-node-1")),
				DynamicClient: dynamicClient,
			}}

			err := bc.validateInfraOverheadForActiveInstances(ctx, nil, overheadTestAgentConfigCM(t, cfg))
			if !tt.wantRetry {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.False(t, isInvalidAgentConfigError(err))
			assert.True(t, k8serrors.IsServerTimeout(err))
		})
	}
}

func TestFormatWorkloads(t *testing.T) {
	assert.Equal(t, "function a version 1 (2), function b version 1 (1), function c version 1 (1), 1 more",
		formatWorkloads(map[string]int{
			"function a version 1": 2,
			"function b version 1": 1,
			"function c version 1": 1,
			"function d version 1": 1,
		}))
}

// TestSyncNVCFBackend_OverheadRemovingActiveInstanceTypeDoesNotRollOutAgent
// covers the reported lifecycle: a working install, an overhead change that
// would remove an instance type with active instances, repeated syncs that must
// keep the running agent and its configuration, and the rollout that follows
// once those instances are gone.
func TestSyncNVCFBackend_OverheadRemovingActiveInstanceTypeDoesNotRollOutAgent(t *testing.T) {
	ctx := newTestContext()
	bc, backend, mergeCM := newOverheadSyncTest(t, "100m")
	clients := bc.clients
	fakeK8s := clients.K8s.(*fakek8sclient.Clientset)
	recorder := bc.eventRecorder.(*record.FakeRecorder)
	systemNamespace := getSystemNamespace(backend)
	require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))

	icmsRequests := clients.DynamicClient.Resource(icmsRequestGVR).Namespace("nvcf-backend")
	created, err := icmsRequests.Create(ctx, overheadTestActiveRequest(t), metav1.CreateOptions{})
	require.NoError(t, err)

	lastGood, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)

	exhausting := mergeCM.DeepCopy()
	exhausting.Data[agentConfigFile] = "agent:\n  additionalResourceOverhead:\n    cpu: 6360m\n"
	exhausting, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, exhausting, metav1.UpdateOptions{})
	require.NoError(t, err)
	fakeK8s.ClearActions()

	err = bc.handleConfigMapUpdate(ctx, mergeCM, exhausting)
	require.Error(t, err)
	assert.False(t, nvcaoperatorerrors.IsFatal(err))
	for range 5 {
		err := bc.SyncNVCFBackend(ctx, backend, false)
		require.Error(t, err)
		assert.False(t, nvcaoperatorerrors.IsFatal(err))
		assert.True(t, isInvalidAgentConfigError(err))
		assert.ErrorContains(t, err, overheadTestInstanceType)
		assert.ErrorContains(t, err, "byoo.additionalResourceOverhead.cpu (6360m)")
	}

	assert.Empty(t, agentWriteActions(fakeK8s.Actions(), systemNamespace),
		"a configuration that would remove an instance type with active instances must not reach the agent")
	current, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGood.Data, current.Data)

	gotBackend, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvidiaiov1.AgentStatusUnhealthy, gotBackend.Status.AgentStatus)
	assert.True(t, hasEventContaining(recorder, "byoo.additionalResourceOverhead.cpu"),
		"the NVCFBackend should carry a warning event naming the overhead value")

	// Once NVCA reports the instance terminated, the held change rolls out.
	require.NoError(t, unstructured.SetNestedField(created.Object, string(nvcatypes.ICMSInstanceTerminated),
		"status", "instances", "0-sr-1", "lastReportedStatus"))
	_, err = icmsRequests.Update(ctx, created, metav1.UpdateOptions{})
	require.NoError(t, err)
	fakeK8s.ClearActions()
	require.NoError(t, bc.SyncNVCFBackend(ctx, backend, false))

	assert.Equal(t, 1, countDeploymentUpdates(fakeK8s.Actions(), systemNamespace))
	managed, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	managedConfig, err := nvcaconfig.DecodeConfig([]byte(managed.Data[agentConfigFile]))
	require.NoError(t, err)
	additionalCPU := managedConfig.Agent.AdditionalResourceOverhead[corev1.ResourceCPU]
	assert.Equal(t, "6360m", additionalCPU.String())
}

// TestSyncAllNVCFBackends_HeldOverheadChangeStaysUnhealthy checks that a held
// overhead change keeps the NVCFBackend Unhealthy across periodic syncs while the
// previous agent still reports healthy. SyncAllNVCFBackends runs the health sync
// only after SyncNVCFBackend succeeds, so the previous agent's health cannot reset
// the status while the change is held back.
func TestSyncAllNVCFBackends_HeldOverheadChangeStaysUnhealthy(t *testing.T) {
	ctx := newTestContext()
	bc, backend, mergeCM := newOverheadSyncTest(t, "100m")
	clients := bc.clients
	recorder := bc.eventRecorder.(*record.FakeRecorder)
	require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))

	// The previous agent keeps serving: /healthz reports healthy and the
	// Deployment has a ready replica.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(nvcfBackendHealthResponse{
			Status: nvidiaiov1.AgentStatus(nvcatypes.HealthStatusHealthy),
		}))
	}))
	t.Cleanup(server.Close)
	previousHealthzURL := makeNVCAHealthzURL
	makeNVCAHealthzURL = func(*nvidiaiov1.NVCFBackend) (string, error) { return server.URL, nil }
	t.Cleanup(func() { makeNVCAHealthzURL = previousHealthzURL })
	bc.httpClient = server.Client()
	deployments := clients.K8s.AppsV1().Deployments(getSystemNamespace(backend))
	dep, err := deployments.Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	dep.Status.Replicas, dep.Status.ReadyReplicas = 1, 1
	_, err = deployments.UpdateStatus(ctx, dep, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = clients.DynamicClient.Resource(icmsRequestGVR).Namespace("nvcf-backend").
		Create(ctx, overheadTestActiveRequest(t), metav1.CreateOptions{})
	require.NoError(t, err)
	exhausting := mergeCM.DeepCopy()
	exhausting.Data[agentConfigFile] = "agent:\n  additionalResourceOverhead:\n    cpu: 6360m\n"
	_, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, exhausting, metav1.UpdateOptions{})
	require.NoError(t, err)
	for len(recorder.Events) > 0 {
		<-recorder.Events
	}

	for range 3 {
		require.NoError(t, bc.SyncAllNVCFBackends(ctx))
		got, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, nvidiaiov1.AgentStatusUnhealthy, got.Status.AgentStatus)
	}
	var healthEvents []string
	for len(recorder.Events) > 0 {
		if event := <-recorder.Events; strings.Contains(event, "health changed") {
			healthEvents = append(healthEvents, event)
		}
	}
	require.Len(t, healthEvents, 1, "the status should change once, when the change is first held back")
	assert.Contains(t, healthEvents[0], "Unhealthy")
}

// TestSyncNVCFBackend_OverheadCheckFollowsAgentEnvOverrides checks that the
// overhead check sees the environment overrides the agent Deployment applies.
// The operator's overrides, set from the chart's
// agent.overrideEnvironmentVariables, replace those of the NVCFBackend.
func TestSyncNVCFBackend_OverheadCheckFollowsAgentEnvOverrides(t *testing.T) {
	overheadEnv := map[string]string{"NVCF_UTILS_CPU_LIMIT": "100m"}
	tests := []struct {
		name        string
		backendEnv  map[string]string
		operatorEnv map[string]string
		wantRollout bool
	}{
		{name: "no overrides"},
		{name: "NVCFBackend overrides", backendEnv: overheadEnv, wantRollout: true},
		{name: "operator overrides", operatorEnv: overheadEnv, wantRollout: true},
		{
			name:        "operator overrides replace NVCFBackend overrides",
			backendEnv:  overheadEnv,
			operatorEnv: map[string]string{"LOG_LEVEL": "debug"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			bc, backend, _ := newOverheadSyncTest(t, "6360m")
			backend.Spec.AgentConfig.OverrideEnvironmentVars = tt.backendEnv
			backend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).
				Update(ctx, backend, metav1.UpdateOptions{})
			require.NoError(t, err)
			bc.deploymentConfig.OverrideEnvironmentVars = tt.operatorEnv
			_, err = bc.clients.DynamicClient.Resource(icmsRequestGVR).Namespace("nvcf-backend").
				Create(ctx, overheadTestActiveRequest(t), metav1.CreateOptions{})
			require.NoError(t, err)

			err = bc.syncNVCFBackend(ctx, backend, false)
			if !tt.wantRollout {
				require.Error(t, err)
				assert.True(t, isInvalidAgentConfigError(err))
				return
			}
			require.NoError(t, err)
			_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(backend)).
				Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
			require.NoError(t, err)
		})
	}
}

// newOverheadSyncTest returns a BackendK8sCache for a self-hosted NVCFBackend on
// a cluster with one overheadTestInstanceType GPU node, and the agent
// configuration merge ConfigMap, which sets the additional CPU overhead.
func newOverheadSyncTest(t *testing.T, additionalCPU string) (*BackendK8sCache, *nvidiaiov1.NVCFBackend, *corev1.ConfigMap) {
	t.Helper()
	ctx := newTestContext()
	clients := mockKubeClients()
	_, err := clients.K8s.CoreV1().Nodes().Create(ctx, overheadTestGPUNode("gpu-node-1"), metav1.CreateOptions{})
	require.NoError(t, err)

	backend := getTestNVCFBackendAllFeatures()
	backend.Spec.Overrides = nil
	backend.Spec.ClusterSource = nvcaoptypes.ClusterSourceSelfHosted
	backend.Spec.ClusterConfig.ClusterID = "test-cluster-id"
	backend.Spec.ClusterConfig.ClusterGroupID = "test-cluster-group-id"
	backend.Spec.ClusterConfig.CloudProvider = "NCP"
	backend.Spec.FeatureGate.Values = append(backend.Spec.FeatureGate.Values,
		featureflag.EnforceContainerFunctionResourceLimits.Key)
	_, err = clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Create(ctx, backend, metav1.CreateOptions{})
	require.NoError(t, err)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(backend))
	bc := &BackendK8sCache{
		clients:                 clients,
		operatorNamespace:       NVCAOperatorNamespace,
		nvcfBackendLister:       nvcabelister.NewNVCFBackendLister(indexer),
		eventRecorder:           record.NewFakeRecorder(200),
		tracer:                  nvcaopotel.NewTracer(),
		ngcServiceKeyFetcher:    &mockTokenFetcher{token: "randomkey"},
		now:                     time.Now,
		clusterSource:           nvcaoptypes.ClusterSourceSelfHosted,
		generateImagePullSecret: false,
	}

	mergeCM, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data:       map[string]string{agentConfigFile: "agent:\n  additionalResourceOverhead:\n    cpu: " + additionalCPU + "\n"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return bc, backend, mergeCM
}

// overheadTestActiveRequest returns an ICMSRequest with one running
// overheadTestInstanceType instance.
func overheadTestActiveRequest(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	req := overheadTestICMSRequest("sr-1", overheadTestInstanceType,
		nvcav2beta1.ICMSRequestStatusCompleted, string(nvcatypes.ICMSInstanceRunning))
	return overheadTestObjects(t, req)[0].(*unstructured.Unstructured)
}
