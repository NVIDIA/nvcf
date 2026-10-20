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
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// The BYOO collector defaults come from the shared icms-translate library, but
// NVCA compiles its vendored copy. These literals pin the values NVCA must ship
// so a stale vendor tree fails here instead of reaching a cluster.
const (
	wantBYOOCollectorCPU                 = "1"
	wantBYOOCollectorMemory              = "2Gi"
	wantBYOOCollectorProbeInitialDelay   = int32(5)
	wantBYOOCollectorProbePeriod         = int32(5)
	wantBYOOCollectorProbeFailureThresh  = int32(3)
	wantBYOOCollectorProbeTimeoutSeconds = int32(1)
)

func Test_translateFunctionLaunchSpecification_BYOOCollectorDefaults(t *testing.T) {
	ctx := newTestContext()

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.UseFunctionTranslator,
			featureflag.BYOObservability,
		},
	}
	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []types.BackendGPU{{
			Name: types.GPUName("L40"),
			InstanceTypes: []types.InstanceType{{
				Name:         types.InstanceName("DGX-CLOUD.GPU.L40"),
				FullName:     "NVIDIA-L40",
				CPU:          resource.MustParse("24"),
				GPUCount:     2,
				SystemMemory: resource.MustParse("256Gi"),
				Storage:      resource.MustParse("128Gi"),
			}},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(types.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	mockClients := makeMockKubeClients()
	kc := K8sComputeBackend{
		scheme:  runtime.NewScheme(),
		clients: mockClients,
		bk8s: &BackendK8sCache{
			requestsNamespace:   RequestsNamespace,
			nfClient:            nfClient,
			clients:             mockClients,
			featureFlagFetcher:  fff,
			regITCache:          regITCache,
			infraOverheadGetter: enforce.NewInfraOverheadGetter(fff, nvcaconfig.Config{}, nil),
			eventRecorder:       record.NewFakeRecorder(100),
		},
	}
	require.NoError(t, corev1.AddToScheme(kc.scheme))
	require.NoError(t, batchv1.AddToScheme(kc.scheme))

	tests := []struct {
		name string
		msg  string
	}{
		{name: "container function", msg: creationgMsgFunctionContainerLaunchSpecBYOO},
		{name: "helm function", msg: creationgMsgFunctionHelmLaunchSpecBYOO},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := decodeCM(t, tt.msg)
			require.NotNil(t, cm.LaunchSpecification.Telemetries)

			meta := cm.CreationQueueMessageMetadata
			req := &nvcav2beta1.ICMSRequest{
				ObjectMeta: getICMSRequestObjectMeta(types.DeploymentInfo{
					RequestID:      meta.RequestID,
					MessageID:      "msgid-byoo",
					MessageBatchID: meta.MessageBatchID,
					NCAID:          meta.NCAID,
					GPUType:        meta.GPUType,
				}),
				Spec: nvcav2beta1.ICMSRequestSpec{
					RequestID:       meta.RequestID,
					Action:          meta.Action,
					MessageReceipt:  "receipt-byoo",
					NCAId:           meta.NCAID,
					MessageBatchID:  meta.MessageBatchID,
					FunctionDetails: cm.Details,
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						CreationQueueMessageMetadata: meta,
						GPUName:                      meta.GPUType,
						QueueURL:                     "fooqueue",
						FunctionLaunchSpecification:  cm.LaunchSpecification,
					},
				},
			}
			req.Name = "sr-byoo"
			req.Namespace = "foo"

			arts, err := kc.translateFunctionLaunchSpecification(ctx, req)
			require.NoError(t, err)

			collector := findBYOOCollectorContainer(t, arts)
			require.NotNil(t, collector, "no %s container in the translated pods", common.ByooOTelCollectorPodNameBase)

			def := common.GetDefaultContainerResourcesBYOO()
			assert.Zero(t, def.Requests.Cpu().Cmp(*collector.Resources.Requests.Cpu()), "CPU request")
			assert.Zero(t, def.Limits.Cpu().Cmp(*collector.Resources.Limits.Cpu()), "CPU limit")
			assert.Zero(t, def.Requests.Memory().Cmp(*collector.Resources.Requests.Memory()), "memory request")
			assert.Zero(t, def.Limits.Memory().Cmp(*collector.Resources.Limits.Memory()), "memory limit")
			assert.Equal(t, wantBYOOCollectorCPU, collector.Resources.Requests.Cpu().String())
			assert.Equal(t, wantBYOOCollectorCPU, collector.Resources.Limits.Cpu().String())
			assert.Equal(t, wantBYOOCollectorMemory, collector.Resources.Requests.Memory().String())
			assert.Equal(t, wantBYOOCollectorMemory, collector.Resources.Limits.Memory().String())

			probe := collector.ReadinessProbe
			require.NotNil(t, probe)
			assert.Equal(t, wantBYOOCollectorProbeInitialDelay, probe.InitialDelaySeconds)
			assert.Equal(t, wantBYOOCollectorProbePeriod, probe.PeriodSeconds)
			assert.Equal(t, wantBYOOCollectorProbeFailureThresh, probe.FailureThreshold)
			assert.Equal(t, wantBYOOCollectorProbeTimeoutSeconds, probe.TimeoutSeconds)
		})
	}
}

func findBYOOCollectorContainer(t *testing.T, arts function.LaunchArtifacts) *corev1.Container {
	t.Helper()
	for _, art := range arts {
		if art.Type != function.LaunchArtifactTypePod {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(art.Specification)
		require.NoError(t, err)
		pod := &corev1.Pod{}
		require.NoError(t, json.Unmarshal(b, pod))
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == common.ByooOTelCollectorPodNameBase {
				return &pod.Spec.Containers[i]
			}
		}
	}
	return nil
}
