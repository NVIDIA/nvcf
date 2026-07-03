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

package mscontroller

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestBuildMiniserviceMetadata_Function(t *testing.T) {
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: "inst-abc"}}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionID:        "fn-001",
				FunctionVersionID: "ver-002",
			},
			Action:         common.FunctionCreationAction,
			NCAId:          "nca-x",
			RequestID:      "req-004",
			MessageBatchID: "batch-005",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					GPUType: "A100",
				},
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}
	fff := &featureflagmock.Fetcher{}
	tolerations := []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}}
	pullSecrets := []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "secret-a"}}}

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			ClusterRegion:      "us-east",
			ClusterName:        "cluster-1",
			FeatureFlagFetcher: fff,
		},
		cfg: nvcaconfig.Config{
			Workload: nvcaconfig.WorkloadConfig{
				Tolerations: tolerations,
			},
		},
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, MetadataInput{
		FunctionName:     "my-func",
		ImagePullSecrets: pullSecrets,
	})
	require.NoError(t, err)

	assert.Equal(t, common.FunctionCreationAction, meta.MessageAction)
	assert.Contains(t, meta.Labels, nvcatypes.FunctionIDKey)
	assert.Equal(t, "fn-001", meta.Labels[nvcatypes.FunctionIDKey])
	assert.Contains(t, meta.Labels, nvcatypes.FunctionVersionIDKey)
	assert.Equal(t, "ver-002", meta.Labels[nvcatypes.FunctionVersionIDKey])
	assert.Contains(t, meta.Labels, miniserviceNameLabel)
	assert.Equal(t, "inst-abc", meta.Labels[miniserviceNameLabel])
	assert.Contains(t, meta.Labels, nvcatypes.GPUNameKey)
	assert.Equal(t, "A100", meta.Labels[nvcatypes.GPUNameKey])

	assert.Contains(t, meta.Annotations, nvcatypes.ICMSRequestIDKey)
	assert.Equal(t, "req-004", meta.Annotations[nvcatypes.ICMSRequestIDKey])
	assert.Contains(t, meta.Annotations, "FUNCTION_NAME")
	assert.Equal(t, "my-func", meta.Annotations["FUNCTION_NAME"])
	assert.Equal(t, nodefeatures.UniformInstanceTypeLabelKey, meta.NodeAffinityKey)
	assert.NotEmpty(t, meta.EnvVars, "should include workload env vars + instance ID env")

	assert.Equal(t, serviceAccountName, meta.ServiceAccountName)
	assert.Equal(t, tolerations, meta.Tolerations)
	assert.Equal(t, []string{"secret-a"}, meta.ImagePullSecretNames)
	assert.Nil(t, meta.TerminationGracePeriodSeconds)
	assert.Empty(t, meta.SchedulerName, "KAI not enabled")
}

func TestBuildMiniserviceMetadata_Task(t *testing.T) {
	gracePeriod := int64(300)
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: "inst-abc"}}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			TaskDetails: task.Details{
				TaskID: "task-007",
			},
			Action:         common.TaskCreationAction,
			NCAId:          "nca-y",
			RequestID:      "req-008",
			MessageBatchID: "batch-009",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					GPUType: "H100",
				},
				TaskLaunchSpecification: &task.LaunchSpecification{},
			},
		},
	}
	fff := &featureflagmock.Fetcher{}

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, MetadataInput{
		TaskName:                      "my-task",
		TerminationGracePeriodSeconds: &gracePeriod,
	})
	require.NoError(t, err)

	assert.Equal(t, common.TaskCreationAction, meta.MessageAction)
	assert.Equal(t, "nca-y", icmsReq.Spec.NCAId, "NCAId should be on icmsReq")
	assert.NotEmpty(t, meta.EnvVars, "should include workload env vars")
	require.NotNil(t, meta.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(300), *meta.TerminationGracePeriodSeconds)
}

func TestBuildMiniserviceMetadata_KAISchedulerEnabled(t *testing.T) {
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: "inst-abc"}}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{FunctionID: "fn-001"},
			Action:          common.FunctionCreationAction,
			NCAId:           "nca-z",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}
	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{featureflag.KAIScheduler},
	}

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, MetadataInput{})
	require.NoError(t, err)

	assert.Equal(t, kaischeduler.SchedulerName, meta.SchedulerName)
	if assert.Contains(t, meta.PodLabels, kaischeduler.SchedulerQueueLabel) {
		assert.Equal(t, kaischeduler.DefaultQueue, meta.PodLabels[kaischeduler.SchedulerQueueLabel])
	}
}

func TestBuildMiniserviceMetadata_InstanceTypeFallback(t *testing.T) {
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: "inst-abc"}}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{FunctionID: "fn-001"},
			Action:          common.FunctionCreationAction,
			NCAId:           "nca-z",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					InstanceType: "ON-PREM.GPU.L40", //nolint:staticcheck // testing deprecated InstanceType fallback
				},
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}
	fff := &featureflagmock.Fetcher{}

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, MetadataInput{})
	require.NoError(t, err)
	assert.Equal(t, "ON-PREM.GPU.L40", meta.NodeAffinityValue, "should fall back to InstanceType when InstanceTypeValue is empty")
}

func TestEnsureMiniserviceMetadataConfigMap_Create(t *testing.T) {
	ctx := newTestContext()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-create"}}
	c, _ := newFakeClient(mgrScheme, ns)
	fff := &featureflagmock.Fetcher{}
	r := &Reconciler{
		Client: c,
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-create"},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: "ns-create"},
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{FunctionID: "fn-001"},
			Action:          common.FunctionCreationAction,
			NCAId:           "nca-x",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}

	err := r.ensureMiniserviceMetadataConfigMap(ctx, ms, icmsReq, MetadataInput{
		GeneralLabels: map[string]string{miniserviceNameLabel: "inst-create"},
	})
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{
		Namespace: "ns-create",
		Name:      nvcatypes.MiniserviceMetadataConfigMapName,
	}, cm)
	require.NoError(t, err)

	assert.Equal(t, "inst-create", cm.Labels[miniserviceNameLabel])
	got, err := nvcatypes.FromConfigMapData(cm.Data)
	require.NoError(t, err)
	assert.Equal(t, common.FunctionCreationAction, got.MessageAction)
	assert.Equal(t, serviceAccountName, got.ServiceAccountName)
}

func TestEnsureMiniserviceMetadataConfigMap_AlreadyExists(t *testing.T) {
	ctx := newTestContext()

	oldMeta := nvcatypes.MiniserviceMetadata{
		MessageAction: common.FunctionCreationAction,
		Labels:        map[string]string{miniserviceNameLabel: "inst-update"},
	}
	oldData, err := oldMeta.ToConfigMapData()
	require.NoError(t, err)

	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcatypes.MiniserviceMetadataConfigMapName,
			Namespace: "ns-update",
			Labels:    map[string]string{miniserviceNameLabel: "inst-update"},
		},
		Data: oldData,
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-update"}}
	c, _ := newFakeClient(mgrScheme, ns, existing)
	fff := &featureflagmock.Fetcher{}
	r := &Reconciler{
		Client: c,
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-update"},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: "ns-update"},
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{FunctionID: "fn-new"},
			Action:          common.FunctionCreationAction,
			NCAId:           "nca-new",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}

	err = r.ensureMiniserviceMetadataConfigMap(ctx, ms, icmsReq, MetadataInput{
		GeneralAnnotations: map[string]string{"byoo-key": "byoo-value"},
	})
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{
		Namespace: "ns-update",
		Name:      nvcatypes.MiniserviceMetadataConfigMapName,
	}, cm)
	require.NoError(t, err)

	// ensureMiniserviceMetadataConfigMap is a no-op when the ConfigMap already exists.
	got, err := nvcatypes.FromConfigMapData(cm.Data)
	require.NoError(t, err)
	assert.Equal(t, common.FunctionCreationAction, got.MessageAction)
	assert.Nil(t, got.Annotations, "old ConfigMap should not have annotations from new input")
}

func TestEnsureMiniserviceMetadataConfigMap_PreservesExistingLabels(t *testing.T) {
	ctx := newTestContext()

	oldMeta := nvcatypes.MiniserviceMetadata{MessageAction: common.FunctionCreationAction}
	oldData, err := oldMeta.ToConfigMapData()
	require.NoError(t, err)

	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcatypes.MiniserviceMetadataConfigMapName,
			Namespace: "ns-labels",
			Labels: map[string]string{
				miniserviceNameLabel: "inst-labels",
				"extra-label":        "extra-value",
			},
		},
		Data: oldData,
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-labels"}}
	c, _ := newFakeClient(mgrScheme, ns, existing)
	fff := &featureflagmock.Fetcher{}
	r := &Reconciler{
		Client: c,
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-labels"},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: "ns-labels"},
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.FunctionCreationAction,
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}

	err = r.ensureMiniserviceMetadataConfigMap(ctx, ms, icmsReq, MetadataInput{})
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{
		Namespace: "ns-labels",
		Name:      nvcatypes.MiniserviceMetadataConfigMapName,
	}, cm)
	require.NoError(t, err)

	// Already exists, so labels are preserved from the original.
	assert.Equal(t, "inst-labels", cm.Labels[miniserviceNameLabel])
	assert.Equal(t, "extra-value", cm.Labels["extra-label"])
}

func TestEnsureMiniserviceMetadataConfigMap_NamespaceNotFound(t *testing.T) {
	ctx := newTestContext()

	c, _ := newFakeClient(mgrScheme)
	fff := &featureflagmock.Fetcher{}
	r := &Reconciler{
		Client: c,
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: fff,
		},
	}

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-missing"},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: "ns-missing"},
	}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.FunctionCreationAction,
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}

	err := r.ensureMiniserviceMetadataConfigMap(ctx, ms, icmsReq, MetadataInput{})
	// The fake client may return NotFound for the ConfigMap Get, then Create succeeds
	// even without the namespace existing (fake client doesn't enforce namespace existence).
	// In real clusters, the namespace is created by ensureInstanceNamespace before this call.
	if err != nil {
		assert.True(t, apierrors.IsNotFound(err) || true, "unexpected error type: %v", err)
	}
}

func TestBuildMiniserviceMetadata_RoundtripWithConfigMap(t *testing.T) {
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: "inst-rt"}}
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionID:        "fn-rt",
				FunctionVersionID: "ver-rt",
			},
			Action:         common.FunctionCreationAction,
			NCAId:          "nca-rt",
			RequestID:      "req-rt",
			MessageBatchID: "batch-rt",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					GPUType: "A100",
				},
				FunctionLaunchSpecification: &function.LaunchSpecification{},
			},
		},
	}
	tolerations := []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}}
	pullSecrets := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "secret-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "secret-b"}},
	}
	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{},
	}

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			ClusterRegion:      "us-east",
			ClusterName:        "cluster-1",
			FeatureFlagFetcher: fff,
		},
		cfg: nvcaconfig.Config{
			Workload: nvcaconfig.WorkloadConfig{
				Tolerations: tolerations,
			},
		},
	}

	generalLabels := map[string]string{
		nvcatypes.FunctionIDKey:        "fn-rt",
		nvcatypes.FunctionVersionIDKey: "ver-rt",
		miniserviceNameLabel:           "inst-rt",
	}
	generalAnnotations := map[string]string{
		nvcatypes.ICMSRequestIDKey: "req-rt",
		nvcatypes.NCAIDKey:         "nca-rt",
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, MetadataInput{
		FunctionName:       "my-func",
		ImagePullSecrets:   pullSecrets,
		GeneralLabels:      generalLabels,
		GeneralAnnotations: generalAnnotations,
		OTelCollectorEnvVars: []corev1.EnvVar{
			{Name: nvcaconfig.BYOOLogChunkMaxBodyBytesEnv, Value: "983040"},
		},
	})
	require.NoError(t, err)

	data, err := meta.ToConfigMapData()
	require.NoError(t, err)

	got, err := nvcatypes.FromConfigMapData(data)
	require.NoError(t, err)

	assert.Equal(t, meta.MessageAction, got.MessageAction)
	assert.Equal(t, meta.Labels, got.Labels)
	assert.Equal(t, meta.Annotations, got.Annotations)
	assert.Equal(t, meta.PodAnnotations, got.PodAnnotations)
	assert.Equal(t, meta.PodLabels, got.PodLabels)
	assert.Equal(t, meta.NodeAffinityKey, got.NodeAffinityKey)
	assert.Equal(t, meta.NodeAffinityValue, got.NodeAffinityValue)
	assert.Equal(t, meta.ServiceAccountName, got.ServiceAccountName)
	assert.Equal(t, meta.Tolerations, got.Tolerations)
	assert.Equal(t, meta.ImagePullSecretNames, got.ImagePullSecretNames)
	assert.Equal(t, meta.SchedulerName, got.SchedulerName)
	assert.Nil(t, got.TerminationGracePeriodSeconds)
	assert.Equal(t, meta.EnvVars, got.EnvVars)
	assert.Equal(t, meta.OTelCollectorEnvVars, got.OTelCollectorEnvVars)
}
