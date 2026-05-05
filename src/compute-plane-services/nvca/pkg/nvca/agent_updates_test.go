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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	mockicmsservice "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/icmsservice"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestICMSRegistrationSync(t *testing.T) {
	oldSyncInterval := syncICMSRegistrationInterval
	syncICMSRegistrationInterval = 100 * time.Millisecond
	t.Cleanup(func() {
		syncICMSRegistrationInterval = oldSyncInterval
	})
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	var err error

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			SSATokenScope:      "byoc_registration",
			SSAClientID:        "client-id-test",
			SSAClientSecretKey: "client-secret",
		},
		NCAId:                          "nca-id-1",
		ClusterName:                    "cluster-1",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group-1",
		K8sVersion:                     "1.25.8",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		RolloverServiceUpdateInterval:  DefaultRolloverServicesUpdateInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		DynamicGPUDiscoveryEnabled:     true,
		MultipleGPUTypesAllowed:        true,
		UniformInstanceLabelsEnabled:   true,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
	}

	agent := newMockAgent(t, ctx, agentOpts)
	fff := &featureflagmock.Fetcher{}
	agent.FeatureFlagFetcher = fff

	require.NoError(t, agent.Start(ctx))

	nodeIface := agent.backendk8scache.clients.K8s.CoreV1().Nodes()
	icmsSvcEndpoint := agent.icmsClient.Endpoint()

	// Check if the cluster was registered with existing GPUs.
	var gotClusterInfoBefore mockicmsservice.ClusterInfo
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotClusterInfoBefore, err = mockicmsservice.GetRegisteredNVCACluster(ctx, icmsSvcEndpoint, agent.ClusterID)
		assert.NoError(ct, err)
		assert.Equal(ct, []types.RegistrationGPU{{
			Name: "A100",
			InstanceTypes: []types.RegistrationInstanceType{{
				Name:          "ON-PREM.GPU.A100_1x",
				Value:         "ON-PREM.GPU.A100",
				Description:   "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
				Default:       true,
				CPUCores:      6,
				CPU:           "6",
				SystemMemory:  "32Gi",
				GPUCount:      1,
				GPUMemory:     "40Gi",
				Storage:       "512Gi",
				CPUArch:       "unknown",
				OS:            "unknown",
				DriverVersion: "unknown",
				NodeType:      types.RegistrationInstanceTypeNodeTypeSingle,
				MaxInstances:  1,
			}},
		}}, gotClusterInfoBefore.BackendGPUs)
	}, 10*time.Second, 100*time.Millisecond)

	// Create a new GPU of the same instance type, and ensure the update loop
	// does re-register this cluster with one additional MaxInstance
	// and constructs the instance type label.
	node2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-2",
			Labels: map[string]string{
				"nvidia.com/gpu.present": "true",
				"nvidia.com/gpu.family":  "ampere",
				"nvidia.com/gpu.machine": "Google-Compute-Engine",
				"nvidia.com/gpu.memory":  "40960",
				"nvidia.com/gpu.product": "A100-SXM4-40GB",
			},
		},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{{
				Type:   v1.NodeReady,
				Status: v1.ConditionTrue,
			}},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
				v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
				v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
			},
		},
	}
	_, err = nodeIface.Create(ctx, node2, metav1.CreateOptions{})
	require.NoError(t, err)

	agent.backendk8scache.ForceSync(ctx)
	var gotClusterInfoAfter1 mockicmsservice.ClusterInfo
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotClusterInfoAfter1, err = mockicmsservice.GetRegisteredNVCACluster(ctx, icmsSvcEndpoint, agent.ClusterID)
		if !assert.NoError(ct, err) {
			return
		}
		assert.Equal(ct, []types.RegistrationGPU{{
			Name: "A100",
			InstanceTypes: []types.RegistrationInstanceType{{
				Name:          "ON-PREM.GPU.A100_1x",
				Value:         "ON-PREM.GPU.A100",
				Description:   "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
				Default:       true,
				CPUCores:      6,
				CPU:           "6",
				SystemMemory:  "32Gi",
				GPUCount:      1,
				GPUMemory:     "40Gi",
				Storage:       "512Gi",
				CPUArch:       "unknown",
				OS:            "unknown",
				DriverVersion: "unknown",
				NodeType:      types.RegistrationInstanceTypeNodeTypeSingle,
				MaxInstances:  2,
			}},
		}}, gotClusterInfoAfter1.BackendGPUs)
		assert.True(ct, gotClusterInfoBefore.Updated.Before(gotClusterInfoAfter1.Updated))
	}, 10*time.Second, 100*time.Millisecond)

	// Create a new GPU of a different instance type, and ensure the update loop
	// picked up the change and updated registration info.
	node3 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-3",
			Labels: map[string]string{
				"nvidia.com/gpu.present":          "true",
				"nvidia.com/gpu.family":           "volta",
				"nvidia.com/gpu.machine":          "Google-Compute-Engine",
				"nvidia.com/gpu.memory":           "32768",
				"nvidia.com/gpu.product":          "V100-SXM2-32GB",
				"nvca.nvcf.nvidia.io/gpu.product": "AD102GL",
			},
		},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{{
				Type:   v1.NodeReady,
				Status: v1.ConditionTrue,
			}},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
				v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
				v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
			},
		},
	}
	_, err = nodeIface.Create(ctx, node3, metav1.CreateOptions{})
	require.NoError(t, err)

	agent.backendk8scache.ForceSync(ctx)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		node, err := nodeIface.Get(ctx, node3.Name, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Contains(ct, node.Labels, nodefeatures.UniformInstanceTypeLabelKey)
		assert.Equal(ct, "ON-PREM.GPU.AD102GL", node.Labels[nodefeatures.UniformInstanceTypeLabelKey])
	}, 10*time.Second, 100*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotClusterInfoAfter2, err := mockicmsservice.GetRegisteredNVCACluster(ctx, icmsSvcEndpoint, agent.ClusterID)
		if !assert.NoError(ct, err) {
			return
		}
		assert.Equal(ct, []types.RegistrationGPU{
			{
				Name: "A100",
				InstanceTypes: []types.RegistrationInstanceType{
					{
						Name:          "ON-PREM.GPU.A100_1x",
						Value:         "ON-PREM.GPU.A100",
						Description:   "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
						Default:       true,
						CPUCores:      6,
						CPU:           "6",
						SystemMemory:  "32Gi",
						Storage:       "512Gi",
						GPUCount:      1,
						GPUMemory:     "40Gi",
						CPUArch:       "unknown",
						OS:            "unknown",
						DriverVersion: "unknown",
						NodeType:      types.RegistrationInstanceTypeNodeTypeSingle,
						MaxInstances:  2,
					},
				},
			},
			{
				Name: "AD102GL",
				InstanceTypes: []types.RegistrationInstanceType{
					{
						Name:          "ON-PREM.GPU.AD102GL_1x",
						Value:         "ON-PREM.GPU.AD102GL",
						Description:   "AD102GL (volta family) on a Google-Compute-Engine machine",
						Default:       true,
						CPUCores:      6,
						CPU:           "6",
						SystemMemory:  "32Gi",
						Storage:       "512Gi",
						GPUCount:      1,
						GPUMemory:     "32Gi",
						CPUArch:       "unknown",
						OS:            "unknown",
						DriverVersion: "unknown",
						NodeType:      types.RegistrationInstanceTypeNodeTypeSingle,
						MaxInstances:  1,
					},
				},
			},
		}, gotClusterInfoAfter2.BackendGPUs)
		assert.True(ct, gotClusterInfoAfter2.Updated.After(gotClusterInfoAfter1.Updated))
	}, 10*time.Second, 100*time.Millisecond)
}
