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
			nb := &nvidiaiov1.NVCFBackend{}
			nb.Spec.AgentConfig.DeploymentConfig.OverrideEnvironmentVars = tt.agentEnv

			err := bc.validateInfraOverheadForActiveInstances(ctx, nb, overheadTestAgentConfigCM(t, tt.cfg))
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

			err := bc.validateInfraOverheadForActiveInstances(ctx, &nvidiaiov1.NVCFBackend{}, overheadTestAgentConfigCM(t, cfg))
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
	clients := mockKubeClients()
	fakeK8s := clients.K8s.(*fakek8sclient.Clientset)
	_, err := fakeK8s.CoreV1().Nodes().Create(ctx, overheadTestGPUNode("gpu-node-1"), metav1.CreateOptions{})
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
	recorder := record.NewFakeRecorder(200)
	bc := &BackendK8sCache{
		clients:                 clients,
		operatorNamespace:       NVCAOperatorNamespace,
		nvcfBackendLister:       nvcabelister.NewNVCFBackendLister(indexer),
		eventRecorder:           recorder,
		tracer:                  nvcaopotel.NewTracer(),
		ngcServiceKeyFetcher:    &mockTokenFetcher{token: "randomkey"},
		now:                     time.Now,
		clusterSource:           nvcaoptypes.ClusterSourceSelfHosted,
		generateImagePullSecret: false,
	}
	systemNamespace := getSystemNamespace(backend)

	mergeCM, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data:       map[string]string{agentConfigFile: "agent:\n  additionalResourceOverhead:\n    cpu: 100m\n"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))

	icmsRequests := clients.DynamicClient.Resource(icmsRequestGVR).Namespace("nvcf-backend")
	activeRequest := overheadTestICMSRequest("sr-1", overheadTestInstanceType,
		nvcav2beta1.ICMSRequestStatusCompleted, string(nvcatypes.ICMSInstanceRunning))
	created, err := icmsRequests.Create(ctx,
		overheadTestObjects(t, activeRequest)[0].(*unstructured.Unstructured), metav1.CreateOptions{})
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
