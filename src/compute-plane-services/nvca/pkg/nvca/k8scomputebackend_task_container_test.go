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
	_ "embed"
	"encoding/base64"
	"sort"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var (
	//go:embed testdata/creationmsg_task_container.json
	cmsgTaskContainer []byte
	//go:embed testdata/creationmsg_task_container_byoo.json
	cmsgTaskContainerBYOO []byte

	taskTestNode = &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			Labels: map[string]string{
				nodefeatures.UniformInstanceTypeLabelKey: "DGX-CLOUD.GPU.L40",
				"nvidia.com/gpu.present":                 "true",
				"nvidia.com/gpu.family":                  "ampere",
				"nvidia.com/gpu.machine":                 "Google-Compute-Engine",
				"nvidia.com/gpu.memory":                  "40960",
				"nvidia.com/gpu.product":                 "L40-40GB",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("6000m"),
				corev1.ResourceMemory:           resource.MustParse("32Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("256Gi"),
				nodefeatures.GPUResourceKey:     resource.MustParse("4"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("6000m"),
				corev1.ResourceMemory:           resource.MustParse("32Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("256Gi"),
				nodefeatures.GPUResourceKey:     resource.MustParse("4"),
			},
		},
	}
)

func Test_applyContainerTaskCreationMessage(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	// Set additional resource overhead to ensure it does not get added
	// to container function inference containers.
	t.Setenv("NVCF_ADDITIONAL_OVERHEAD_RESOURCES_B64", base64.StdEncoding.EncodeToString([]byte(`
{
	"cpu": "1",
	"memory": "2Gi",
	"ephemeral-storage": "5Gi"
}
`)))

	k8sClients := mockKubeClients(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: RequestsNamespace,
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: SystemNamespace,
			},
		},
		taskTestNode,
	)
	nodeInfFactory := informers.NewSharedInformerFactoryWithOptions(k8sClients.K8s, 1*time.Second,
		nodefeatures.NewNodeInformerOptions(nil)...,
	)
	ni := nodeInfFactory.Core().V1().Nodes()
	pi := nodeInfFactory.Core().V1().Pods()

	srInfFactory := nvcainformers.NewSharedInformerFactoryWithOptions(k8sClients.BART, 1*time.Second)
	icmsReqGenInf, err := srInfFactory.ForResource(nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests"))
	require.NoError(t, err)

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.InfraResourceOverhead,
			featureflag.EnforceContainerTaskResourceLimits,
		},
	}
	bc := &BackendK8sCache{
		clients:              k8sClients,
		eventRecorder:        record.NewFakeRecorder(100),
		tracer:               noop.NewTracerProvider().Tracer("foo"),
		requestsNamespace:    RequestsNamespace,
		podInstanceNamespace: RequestsNamespace,
		systemNamespace:      SystemNamespace,
		podSpecLister:        pi.Lister().Pods(RequestsNamespace),
		icmsRequestLister:    nvcav2beta1listers.NewICMSRequestLister(icmsReqGenInf.Informer().GetIndexer()),
		regITCache:           icms.NewRegistrationInstanceTypeCache(),
		featureFlagFetcher:   fff,
		k8sTimeConfig: (&k8sutil.TimeConfig{
			MaxRunningTimeout: 1 * time.Minute,
		}).Complete(),
		imageCredentialHelperImage: "nvcf-image-credential-helper:latest",
		infraOverheadGetter:        enforce.NewInfraOverheadGetter(fff, nvcaconfig.Config{}, nil),
	}

	err = k8sutil.SetConfigDefaultResources(&bc.cfg)
	require.NoError(t, err)

	srHelper, _ := NewK8sComputeBackend(k8sClients, bc)
	ag := newGPUAllocationGetter(bc.icmsRequestLister, srHelper)
	bc.nfClient = nodefeatures.NewDynamicClient(ag, ni.Lister(), "DGX-CLOUD", nodefeatures.DynamicClientOptions{
		UniformInstanceLabels: true,
	})

	nodeInfFactory.Start(ctx.Done())
	srInfFactory.Start(ctx.Done())
	syncCtx, syncCancel := context.WithTimeout(ctx, 10*time.Second)
	synced := cache.WaitForCacheSync(syncCtx.Done(),
		ni.Informer().HasSynced,
		pi.Informer().HasSynced,
		icmsReqGenInf.Informer().HasSynced,
	)
	syncCancel()
	if !synced {
		t.Skip("Cache sync did not complete within 10s (fake NVCA client/informers without WatchList semantics support)")
	}

	backendGPUs, err := bc.nfClient.GetAllBackendGPUs(ctx)
	require.NoError(t, err)
	bc.regITCache.Put(types.BackendGPUs(backendGPUs).ToRegistration(false, corev1.ResourceList{}))

	// BYOO without flag enabled.
	taskMsgBYOO := decodeCMTask(t, cmsgTaskContainerBYOO)

	_, err = bc.CreateICMSCreationMessageRequest(ctx, taskMsgBYOO, "mrecpt1", "msg1", "")
	require.EqualError(t, err, "telemetries is set but required features are disabled: BYOObservability")

	taskMsg := decodeCMTask(t, cmsgTaskContainer)

	sr, err := bc.CreateICMSCreationMessageRequest(ctx, taskMsg, "mrecpt1", "msg1", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	srName := "sr-" + taskMsg.RequestID
	req, err := k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, req.Status.RequestStatus)
	req.CreationTimestamp = metav1.Now()
	req.UID = apitypes.UID(uuid.NewString())
	req, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Update(ctx, req, metav1.UpdateOptions{})
	require.NoError(t, err)

	attrs := map[string]string{
		featureflag.AttrKataRuntimeIsolation.Key: "true",
	}

	fff.EnabledAttrs = append(fff.EnabledAttrs, featureflag.AttrKataRuntimeIsolation)
	kbi, _ := NewK8sComputeBackend(k8sClients, bc)
	kb := kbi.(K8sComputeBackend)
	// Check addition of enforcement labels.
	kb.enabledAttrs = featureflag.NewAttributes(attrs)

	bc.icmsRequestHelper = kb

	expOwnerRefsForReq := getOwnerRefForRequest(req)
	expLabels := mergeMaps(types.GetLabelsForRequest(req, bc.featureFlagFetcher), map[string]string{
		"ENVIRONMENT":                     "stage",
		"GPU_COUNT":                       "2",
		"NCA_ID":                          "nca-_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq-nca",
		"TASK_ID":                         "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
		"environment":                     "stage",
		"performance_class":               taskMsg.InstanceTypeValue,
		"gpu-name":                        "L40",
		"icms-request-id":                 req.Spec.RequestID,
		"nvcf.nvidia.io/message-batch-id": "14d2ec9c-25d8-4712-9288-b682570671d6",
		"task-id":                         "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
	})
	expPodLabels := mergeMaps(expLabels, map[string]string{
		"nvca.nvcf.nvidia.io/needs-enforce": "true",
	})
	expAnnos := mergeMaps(types.GetAnnotationsForRequest(req), map[string]string{
		"TASK_NAME":                          "my-task",
		"task-name":                          "my-task",
		"nvcf.nvidia.io/backend":             "",
		"nvcf.nvidia.io/environment":         "stage",
		"nvcf.nvidia.io/instance-type-name":  "DGX-CLOUD.GPU.L40_2x",
		"nvcf.nvidia.io/instance-type-value": "DGX-CLOUD.GPU.L40",
		"nvcf.nvidia.io/region":              "",
	})
	expPodAnnos := mergeMaps(expAnnos, map[string]string{
		"nvca.nvcf.nvidia.io/enforcements": featureflag.AttrKataRuntimeIsolation.Key + "=true",
	})
	expResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			nodefeatures.PGPUResourceKey:    *resource.NewQuantity(2, resource.DecimalSI),
			corev1.ResourceCPU:              resource.MustParse("3"),
			corev1.ResourceMemory:           *resource.NewQuantity(16<<30, resource.BinarySI),
			corev1.ResourceEphemeralStorage: *resource.NewQuantity(128<<30, resource.BinarySI),
		},
		Limits: corev1.ResourceList{
			nodefeatures.PGPUResourceKey:    *resource.NewQuantity(2, resource.DecimalSI),
			corev1.ResourceCPU:              resource.MustParse("3"),
			corev1.ResourceMemory:           *resource.NewQuantity(16<<30, resource.BinarySI),
			corev1.ResourceEphemeralStorage: *resource.NewQuantity(128<<30, resource.BinarySI),
		},
	}

	waitForBackendGPUs := func(collect *assert.CollectT) {
		_, err := kb.bk8s.GetAllBackendGPUs(ctx)
		assert.NoError(t, err)
	}
	require.EventuallyWithT(t, waitForBackendGPUs, 5*time.Second, 100*time.Millisecond)

	err = kb.applyContainerTaskCreationMessage(ctx, req)
	require.NoError(t, err)

	// Ensure image cred update job requeues before instance apply.
	podList, err := k8sClients.K8s.CoreV1().Pods(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, podList.Items, 0)

	gotJob, err := k8sClients.K8s.BatchV1().Jobs(bc.systemNamespace).Get(ctx, req.Name+"-cred-init", metav1.GetOptions{})
	require.NoError(t, err)
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
			Reason: "done",
		}},
	}
	_, err = k8sClients.K8s.BatchV1().Jobs(bc.systemNamespace).UpdateStatus(ctx, gotJob, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = kb.applyContainerTaskCreationMessage(ctx, req)
	require.NoError(t, err)

	podName1 := "0-" + req.Name + "-task"
	podName2 := "1-" + req.Name + "-task"
	podList, err = k8sClients.K8s.CoreV1().Pods(bc.podInstanceNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	if assert.Len(t, podList.Items, 2) {
		assert.Equal(t, podList.Items[0].Name, podName1)
		assert.Equal(t, expOwnerRefsForReq, podList.Items[0].OwnerReferences)
		assert.Equal(t, expPodLabels, podList.Items[0].Labels)
		assert.Equal(t, expPodAnnos, podList.Items[0].Annotations)
		if assert.Len(t, podList.Items[0].Spec.Containers, 2) &&
			assert.Equal(t, "task", podList.Items[0].Spec.Containers[0].Name) {
			assert.Equal(t, expResources, podList.Items[0].Spec.Containers[0].Resources)
		}
		assert.Equal(t, podList.Items[1].Name, podName2)
		assert.Equal(t, expOwnerRefsForReq, podList.Items[1].OwnerReferences)
		assert.Equal(t, expPodLabels, podList.Items[1].Labels)
		assert.Equal(t, expPodAnnos, podList.Items[1].Annotations)
		if assert.Len(t, podList.Items[1].Spec.Containers, 2) &&
			assert.Equal(t, "task", podList.Items[1].Spec.Containers[0].Name) {
			assert.Equal(t, expResources, podList.Items[1].Spec.Containers[0].Resources)
		}
	}

	pod, err := k8sClients.K8s.CoreV1().Pods(bc.podInstanceNamespace).Get(ctx, podName1, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, featureflag.NewAttributes(map[string]string{
		"KataRuntimeIsolation": "true",
	}), enforce.GetEnforcements(pod))

	uSecretList, err := kb.dynClient.Resource(corev1.SchemeGroupVersion.WithResource("secrets")).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	secretList := corev1.SecretList{Items: make([]corev1.Secret, len(uSecretList.Items))}
	for i, u := range uSecretList.Items {
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &secretList.Items[i])
		require.NoError(t, err)
	}
	if assert.Len(t, secretList.Items, 2) {
		assert.Equal(t, "worker-"+req.Name+"-regcred-0", secretList.Items[0].Name)
		assert.Equal(t, expOwnerRefsForReq, secretList.Items[0].OwnerReferences)
		assert.Equal(t, expLabels, secretList.Items[0].Labels)
		assert.Equal(t, expAnnos, secretList.Items[0].Annotations)
		assert.Equal(t, "workload-"+req.Name+"-regcred-0", secretList.Items[1].Name)
		assert.Equal(t, expOwnerRefsForReq, secretList.Items[1].OwnerReferences)
		assert.Equal(t, expLabels, secretList.Items[1].Labels)
		assert.Equal(t, expAnnos, secretList.Items[1].Annotations)
	}

	req, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	if assert.Len(t, req.Status.Instances, int(req.Spec.CreationMsgInfo.InstanceCount)) {
		assert.Contains(t, req.Status.Instances, podName1)
		assert.Contains(t, req.Status.Instances, podName2)
	}
	assert.Equal(t, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	t.Cleanup(cancel)
	cache.WaitForCacheSync(cctx.Done(), pi.Informer().HasSynced)

	// Re-apply to make sure ICMSRequest is marked as in progress
	err = kb.applyContainerTaskCreationMessage(ctx, req)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, srName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, req.Status.RequestStatus)
		}
	}, 5*time.Second, 100*time.Millisecond)

	// Ensure failure on pending for too long.
	bc.k8sTimeConfig.MaxRunningTimeout = 0
	err = kb.applyContainerTaskCreationMessage(ctx, req)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, srName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav2beta1.ICMSRequestStatusFailed, req.Status.RequestStatus)
		}
	}, 5*time.Second, 100*time.Millisecond)
}

func decodeCMTask(t *testing.T, b []byte) (m task.CreationQueueMessage) {
	msg, err := translate.DecodeCreationQueueMessage(b)
	require.NoError(t, err)
	require.IsType(t, task.CreationQueueMessage{}, msg)
	return msg.(task.CreationQueueMessage)
}

func Test_calculatePodInstanceResourcesForInstanceType(t *testing.T) {
	kb := K8sComputeBackend{
		bk8s: &BackendK8sCache{
			regITCache:         icms.NewRegistrationInstanceTypeCache(),
			featureFlagFetcher: featureflag.DefaultFetcher,
		},
	}

	backendGPUs := types.BackendGPUs{{
		Name: "A100",
		InstanceTypes: []types.InstanceType{
			{
				Name:            "ON-PREM.GPU.A100",
				GPUCount:        6,
				GPUMemoryPerGPU: resource.MustParse("40Gi"),
				CPU:             resource.MustParse("30"),
				SystemMemory:    resource.MustParse("256Gi"),
				Storage:         resource.MustParse("2Ti"),
			},
		},
	}}
	kb.bk8s.regITCache.Put(backendGPUs.ToRegistration(false, corev1.ResourceList{}))

	cmInfo := nvcav2beta1.ICMSCreationMessageInfo{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			InstanceCount:     2,
			InstanceType:      "ON-PREM.GPU.A100",
			InstanceTypeName:  "ON-PREM.GPU.A100_2x",
			InstanceTypeValue: "ON-PREM.GPU.A100",
			GPUType:           "A100",
			RequestedGPUCount: 2,
		},
	}

	expCPU := resource.NewQuantity(10, resource.DecimalSI) // (30/6) * 2 = 10 cores
	_ = expCPU.String()
	expMem := resource.NewQuantity(91268055040, resource.BinarySI) // (256Gi/6) * 2 = ~85Gi
	_ = expMem.String()
	expStorage := resource.NewQuantity(732291923968, resource.BinarySI) // (2Ti/6) * 2 = ~682Gi
	_ = expStorage.String()
	expGPU := resource.NewQuantity(2, resource.DecimalSI)

	gotReqs, gotLims, err := kb.calculatePodInstanceResourcesForInstanceType(cmInfo, nil)
	require.NoError(t, err)
	if assert.Len(t, gotReqs, 4) {
		assert.Equal(t, *expGPU, gotReqs[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPU, gotReqs[corev1.ResourceCPU])
		assert.Equal(t, *expMem, gotReqs[corev1.ResourceMemory])
		assert.Equal(t, *expStorage, gotReqs[corev1.ResourceEphemeralStorage])
	}
	if assert.Len(t, gotLims, 4) {
		assert.Equal(t, *expGPU, gotLims[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPU, gotLims[corev1.ResourceCPU])
		assert.Equal(t, *expMem, gotLims[corev1.ResourceMemory])
		assert.Equal(t, *expStorage, gotLims[corev1.ResourceEphemeralStorage])
	}

	// Check overhead.
	expCPUWithOverhead := resource.NewQuantity(int64(expCPU.AsApproximateFloat64())+1, resource.DecimalSI)
	expMemWithOverhead := resource.NewQuantity(int64(expMem.AsApproximateFloat64())+2<<30, resource.BinarySI)
	expStorageWithOverhead := resource.NewQuantity(int64(expStorage.AsApproximateFloat64())+2<<30, resource.BinarySI)
	gotReqs, gotLims, err = kb.calculatePodInstanceResourcesForInstanceType(cmInfo, corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse("1"),
		corev1.ResourceMemory:           resource.MustParse("2Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
	})
	require.NoError(t, err)
	if assert.Len(t, gotReqs, 4) {
		assert.Equal(t, *expGPU, gotReqs[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPUWithOverhead, gotReqs[corev1.ResourceCPU])
		assert.Equal(t, *expMemWithOverhead, gotReqs[corev1.ResourceMemory])
		assert.Equal(t, *expStorageWithOverhead, gotReqs[corev1.ResourceEphemeralStorage])
	}
	if assert.Len(t, gotLims, 4) {
		assert.Equal(t, *expGPU, gotLims[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPUWithOverhead, gotLims[corev1.ResourceCPU])
		assert.Equal(t, *expMemWithOverhead, gotLims[corev1.ResourceMemory])
		assert.Equal(t, *expStorageWithOverhead, gotLims[corev1.ResourceEphemeralStorage])
	}

	// Check milli CPU
	backendGPUs[0].InstanceTypes[0].CPU = resource.MustParse("8")
	kb.bk8s.regITCache.Put(backendGPUs.ToRegistration(false, corev1.ResourceList{}))

	expCPU = resource.NewMilliQuantity(2666, resource.DecimalSI) // (8/6) * 2 = 2666m
	_ = expCPU.String()
	gotReqs, gotLims, err = kb.calculatePodInstanceResourcesForInstanceType(cmInfo, nil)
	require.NoError(t, err)
	if assert.Len(t, gotReqs, 4) {
		assert.Equal(t, *expGPU, gotReqs[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPU, gotReqs[corev1.ResourceCPU])
		assert.Equal(t, *expMem, gotReqs[corev1.ResourceMemory])
		assert.Equal(t, *expStorage, gotReqs[corev1.ResourceEphemeralStorage])
	}
	if assert.Len(t, gotLims, 4) {
		assert.Equal(t, *expGPU, gotLims[nodefeatures.GPUResourceKey])
		assert.Equal(t, *expCPU, gotLims[corev1.ResourceCPU])
		assert.Equal(t, *expMem, gotLims[corev1.ResourceMemory])
		assert.Equal(t, *expStorage, gotLims[corev1.ResourceEphemeralStorage])
	}
}

func Test_reconcileContainerTaskPodState(t *testing.T) {
	type spec struct {
		name               string
		pod                *corev1.Pod
		state              types.ICMSInstanceState
		maxRuntimeDuration time.Duration
		maxQueuedDuration  time.Duration
		fff                featureflag.Fetcher
		now                time.Time
		expGPS             *int64
		expDeleted         bool
	}

	baseTime := time.Now()

	cases := []spec{
		{
			name: "task:running utils:running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			expDeleted: false,
		},
		{
			name: "bad state",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			state:      types.ICMSInstanceFailedContainerRestartLoop,
			expDeleted: true,
		},
		{
			name: "bad state utils terminated",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{},
							},
						},
					},
				},
			},
			state:      types.ICMSInstanceFailedContainerRestartLoop,
			expDeleted: true,
			expGPS:     new(int64),
		},
		{
			name: "degraded without autopurge",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			state:      types.ICMSInstanceDegradedWorker,
			fff:        &featureflagmock.Fetcher{},
			expDeleted: false,
		},
		{
			name: "containers not found",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{},
				},
			},
			expDeleted: false,
		},
		// NVCT would handle this case.
		{
			name: "task:0 utils:0",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			expDeleted: false,
		},
		// NVCT would handle this case.
		{
			name: "task:running utils:0",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "task",
							State: corev1.ContainerState{},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			expDeleted: false,
		},
		// NVCT would handle this case.
		{
			name: "task:1 utils:0",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					},
				},
			},
			expDeleted: false,
		},
		// NVCT may NOT handle this case.
		{
			name: "task:0 utils:1",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
					},
				},
			},
			expDeleted: true,
			expGPS:     new(int64),
		},
		// NVCT would handle this case.
		{
			name: "task:1 utils:running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
						{
							Name:  common.UtilsContainerName,
							State: corev1.ContainerState{},
						},
					},
				},
			},
			expDeleted: false,
		},
		// NVCT may NOT handle this case.
		{
			name: "exceeded max runtime duration",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
						{
							Name:  common.UtilsContainerName,
							State: corev1.ContainerState{},
						},
					},
				},
			},
			now:        baseTime.Add(16 * time.Minute),
			expDeleted: true,
			expGPS:     new(int64),
		},
		// NVCT would handle this case.
		{
			name: "task:0 utils:running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "task",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
						{
							Name:  common.UtilsContainerName,
							State: corev1.ContainerState{},
						},
					},
				},
			},
			expDeleted: false,
		},
		// NVCT may NOT handle this case.
		{
			name: "task:running utils:1",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(baseTime)},
				Status: corev1.PodStatus{
					StartTime: &metav1.Time{Time: baseTime},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
						},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "task",
							State: corev1.ContainerState{},
						},
						{
							Name: common.UtilsContainerName,
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 1,
								},
							},
						},
					},
				},
			},
			expDeleted: true,
			expGPS:     new(int64),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pod := tt.pod.DeepCopy()
			pod.Name = "testpod"
			pod.Namespace = "default"
			pod.Spec.Containers = []corev1.Container{{Name: "task"}, {Name: "utils"}}

			if tt.fff == nil {
				tt.fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.AutoPurgeDegradedWorkers,
					},
				}
			}

			kb := K8sComputeBackend{
				bk8s: &BackendK8sCache{
					logPostingEnabled:  true,
					featureFlagFetcher: tt.fff,
				},
			}

			if tt.state == "" {
				tt.state = types.ICMSInstanceRunning
			}
			if tt.maxQueuedDuration == 0 {
				tt.maxQueuedDuration = 10 * time.Minute
			}
			if tt.maxRuntimeDuration == 0 {
				tt.maxRuntimeDuration = 10 * time.Minute
			}
			if tt.now.IsZero() {
				tt.now = baseTime
			}

			gotGPS, gotShouldTerminate := kb.reconcileContainerTaskPodState(ctx, pod, tt.state, tt.maxQueuedDuration, tt.maxRuntimeDuration, tt.now)
			assert.Equal(t, tt.expDeleted, gotShouldTerminate)
			assert.Equal(t, tt.expGPS, gotGPS)
		})
	}
}

func Test_taskContainerCache(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	skipVolumeDetachCheck = true
	t.Cleanup(func() { skipVolumeDetachCheck = false })

	clients := makeMockKubeClients()

	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []types.BackendGPU{{
			Name: types.GPUName("L40"),
			InstanceTypes: []types.InstanceType{{
				Name:         types.InstanceName("DGX-CLOUD.GPU.L40"),
				FullName:     "NVIDIA-L40",
				CPU:          *resource.NewQuantity(24, resource.DecimalSI),
				GPUCount:     2,
				SystemMemory: resource.MustParse("256Gi"),
			}},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(types.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	bc := &BackendK8sCache{
		requestsNamespace:       RequestsNamespace,
		podInstanceNamespace:    RequestsNamespace,
		featureFlagFetcher:      &featureflagmock.Fetcher{},
		clients:                 clients,
		eventRecorder:           record.NewFakeRecorder(10),
		cachingSupportEnabled:   true,
		nvmeshEncryptionEnabled: true,
		nfClient:                nfClient,
		regITCache:              regITCache,
		k8sTimeConfig: (&k8sutil.TimeConfig{
			ModelCacheVolumeDetachmentTimeout: 2 * time.Second,
		}).Complete(),
		infraOverheadGetter: enforce.NoOpInfraOverheadGetter,
	}
	kbIface, _ := NewK8sComputeBackend(clients, bc)
	kb := kbIface.(K8sComputeBackend)

	cmsgLS := task.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:         "randomRequestID1",
			NCAID:             "randomID",
			MessageBatchID:    "randomMessageBatchID",
			Action:            common.TaskCreationAction,
			InstanceCount:     2,
			RequestedGPUCount: 1,
			InstanceType:      "DGX-CLOUD.GPU.L40",
			InstanceTypeName:  "DGX-CLOUD.GPU.L40_1x",
			InstanceTypeValue: "DGX-CLOUD.GPU.L40",
			GPUType:           "L40",
		},
		Details: task.Details{
			TaskID: "taskID1",
		},
	}
	req := &nvcav2beta1.ICMSRequest{}
	req.ObjectMeta = getICMSRequestObjectMeta(types.DeploymentInfo{
		RequestID:      cmsgLS.RequestID,
		MessageID:      "randomId1",
		MessageBatchID: cmsgLS.MessageBatchID,
		NCAID:          cmsgLS.NCAID,
		GPUType:        cmsgLS.GPUType,
		TaskID:         cmsgLS.Details.TaskID,
	})
	req.Namespace = bc.requestsNamespace
	req.Spec = nvcav2beta1.ICMSRequestSpec{
		RequestID:      cmsgLS.RequestID,
		Action:         cmsgLS.Action,
		MessageReceipt: "randomMsgReceipt1",
		NCAId:          cmsgLS.NCAID,
		MessageBatchID: cmsgLS.MessageBatchID,
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: cmsgLS.CreationQueueMessageMetadata,
			GPUName:                      cmsgLS.GPUType,
			QueueURL:                     "queueurl",
			TaskLaunchSpecification: &task.LaunchSpecification{
				CloudProvider:                  "DGXCLOUD",
				ICMSEnvironment:                "stage",
				ResultHandlingStrategy:         "UPLOAD",
				TerminationGracePeriodDuration: "PT10M",
				MaxRuntimeDuration:             "PT2H",
				MaxQueuedDuration:              "PT2M",
				EnvironmentB64:                 `SU5JVF9DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL252Y2Zfd29ya2VyX2luaXQ6MC4yNC4xMApNQVhfUkVRVUVTVF9DT05DVVJSRU5DWT0iMSIKTkNBX0lEPV9sSUxYQi0xTmZObUJuUVNrX3NwcVZXT3RDQVhRbTUwVUVNd2ozVFJneW1KSjJBeXV3Y2d4cQpOVkNGX0ZRRE49aHR0cHM6Ly91cy13ZXN0LTIuYXBpLm52Y2YubnZpZGlhLmNvbQpOVkNGX0ZRRE5fR1JQQz1odHRwczovL2dycGMuYXBpLm52Y2YubnZpZGlhLmNvbQpOVkNGX0ZRRE5fTkFUUz10bHM6Ly91cy13ZXN0LTIuYXdzLmNsb3VkLm5hdHMubnZjZi5udmlkaWEuY29tOjQyMjIKTlZDRl9XT1JLRVJfVE9LRU49dG9rCk9URUxfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9vcGVudGVsZW1ldHJ5LWNvbGxlY3RvcjowLjc0LjAKT1RFTF9FWFBPUlRFUl9PVExQX0VORFBPSU5UPWh0dHBzOi8vcHJvZC5vdGVsLmthaXplbi5udmlkaWEuY29tOjgyODIKU0lERUNBUl9DUkVERU5USUFMPWNvbnRhaW5lci1jcmVkLTEKVEFTS19DT05UQUlORVI9bnZjci5pby9teW9yZy9ncHQtMy41LXR1cmJvLWZpbmUtdHVuZToxLjAuMApUQVNLX0NPTlRBSU5FUl9BUkdTPS1hcmcxPXRlc3QxIGFyZzI9dGVzdDIgLS1hcmczIHRlc3QzIC0tICJibGFoIGJsYWgiClRBU0tfQ09OVEFJTkVSX0NSRURFTlRJQUw9Y29udGFpbmVyLWNyZWQtMQpUQVNLX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pU1U1R1JWSkZUa05GWDBWT1ZsOUxSVmtpTENKMllXeDFaU0k2SW1sdVptVnlaVzVqWlY5MllXeDFaU0o5WFE9PQpUQVNLX0hFQUxUSF9FTkRQT0lOVD0vdjIvaGVhbHRoL3JlYWR5ClRBU0tfSEVBTFRIX0VYUEVDVEVEX1JFU1BPTlNFX0NPREU9IjIwMCIKVEFTS19IRUFMVEhfUE9SVD0iNTAwNTEiClRBU0tfSUQ9MTNlMmI1OTktOTZjYS00MmI1LWE0MTktOGZhN2Y3MDFkNWQyClRBU0tfTkFNRT1teS10YXNrClRBU0tfUE9SVD0iNTAwNTEiClRBU0tfUFJPVE9DT0w9R1JQQwpUQVNLX1VSTD0vZ3JwYwpURVJNSU5BVElPTl9HUkFDRV9QRVJJT0Q9UFQySApUUkFDSU5HX0FDQ0VTU19UT0tFTj10cmFjZS10b2stMQpVVElMU19DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL252Y2Zfd29ya2VyX3V0aWxzOjIuMjEuNAo=`,
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    "abc123handle",
					CacheSize:      200000000,
				},
			},
		},
	}
	var err error
	req, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Create(ctx, req, metav1.CreateOptions{})
	require.NoError(t, err)

	err = kb.ApplyCreationMessage(ctx, req)
	require.EqualError(t, err, "model caching is still in progress")

	expInitJobName := "writer-job-abc123handle"
	initJob, err := clients.K8s.BatchV1().Jobs(bc.requestsNamespace).Get(ctx, expInitJobName, metav1.GetOptions{})
	require.NoError(t, err)
	initJob.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	initJob.Status.Succeeded++
	_, err = clients.K8s.BatchV1().Jobs(bc.requestsNamespace).UpdateStatus(ctx, initJob, metav1.UpdateOptions{})
	require.NoError(t, err)

	rwPVCName := "rw-pvc-abc123handle"
	pvName := "pvc-abc123handle"
	pv := &corev1.PersistentVolume{}
	pv.Name = pvName
	pv.Labels = map[string]string{
		taskIDLabelString: cmsgLS.Details.TaskID,
	}
	pv.Spec = corev1.PersistentVolumeSpec{
		ClaimRef: &corev1.ObjectReference{
			Name:      rwPVCName,
			Namespace: bc.podInstanceNamespace,
		},
	}
	pv, err = clients.K8s.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
	require.NoError(t, err)
	pv.Status.Phase = corev1.VolumeBound
	_, err = clients.K8s.CoreV1().PersistentVolumes().UpdateStatus(ctx, pv, metav1.UpdateOptions{})
	require.NoError(t, err)

	rwPVC, err := clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, rwPVCName, metav1.GetOptions{})
	require.NoError(t, err)
	rwPVC.Spec.VolumeName = pv.Name
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Update(ctx, rwPVC, metav1.UpdateOptions{})
	require.NoError(t, err)
	rwPVC.Status.Phase = corev1.ClaimBound
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).UpdateStatus(ctx, rwPVC, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = kb.ApplyCreationMessage(ctx, req)
	require.EqualError(t, err, "model caching is still in progress")

	roPVCName := "ro-pvc-abc123handle"
	roPVC, err := clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, roPVCName, metav1.GetOptions{})
	require.NoError(t, err)
	roPVC.Spec.VolumeName = pv.Name
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Update(ctx, roPVC, metav1.UpdateOptions{})
	require.NoError(t, err)
	roPVC.Status.Phase = corev1.ClaimBound
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).UpdateStatus(ctx, roPVC, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, rwPVCName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))

	err = kb.ApplyCreationMessage(ctx, req)
	require.NoError(t, err)

	podList, err := clients.K8s.CoreV1().Pods(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, podList.Items, 2)
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].Name < podList.Items[j].Name
	})
	assert.Equal(t, "0-sr-randomRequestID1-task", podList.Items[0].Name)
	assert.Equal(t, "1-sr-randomRequestID1-task", podList.Items[1].Name)
	for _, pod := range podList.Items {
		assert.Equal(t, corev1.Volume{
			Name: "model-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: roPVCName,
					ReadOnly:  true,
				},
			},
		}, pod.Spec.Volumes[0])
	}
	req, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, req.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, roPVC.Name, req.Status.CacheReferenceName)
}
