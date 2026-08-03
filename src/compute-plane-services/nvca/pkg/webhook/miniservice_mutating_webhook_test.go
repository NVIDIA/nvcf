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

package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	evanphxpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	nvcfdra "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/dra"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	cmnnvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

const (
	testNamespace  = "ms-abc123"
	testFunctionID = "fn-001"
	testVersionID  = "ver-002"
	testInstanceID = "inst-003"
	testTaskID     = "task-007"
	testNcaID      = "nca-x"
	testReqID      = "req-004"
)

// testConfigMap returns a populated nvcf-miniservice-metadata ConfigMap for a function workload.
func testConfigMap(t *testing.T, ns string) *corev1.ConfigMap {
	t.Helper()
	meta := nvcatypes.MiniserviceMetadata{
		MessageAction: common.FunctionCreationAction,
		Labels: map[string]string{
			nvcatypes.FunctionIDKey:        testFunctionID,
			nvcatypes.FunctionVersionIDKey: testVersionID,
			nvcatypes.MiniserviceNameLabel: testInstanceID,
		},
		Annotations: map[string]string{
			nvcatypes.ICMSRequestIDKey: testReqID,
			nvcatypes.NCAIDKey:         testNcaID,
			cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey:     "kns-pvc-ro",
			cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey: "secrets-pvc-ro",
		},
		EnvVars: []corev1.EnvVar{
			{Name: "NVCF_NCA_ID", Value: testNcaID},
			{Name: "NVCF_FUNCTION_ID", Value: testFunctionID},
			{Name: "NVCF_FUNCTION_VERSION_ID", Value: testVersionID},
			{Name: nvcatypes.NVCFInstIDEnvKey, ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.labels['" + nvcatypes.MiniserviceNameLabel + "']",
				},
			}},
		},
		ServiceAccountName: "miniservice-instance-permissions",
		NodeAffinityKey:    "nvca.nvcf.nvidia.io/instance-type",
		NodeAffinityValue:  "ON-PREM.GPU.A100",
	}
	return createConfigMapFromMeta(t, ns, meta)
}

// testTaskConfigMap returns a populated ConfigMap for a task MiniService.
func testTaskConfigMap(t *testing.T, ns string) *corev1.ConfigMap {
	t.Helper()
	gracePeriod := int64(300)
	meta := nvcatypes.MiniserviceMetadata{
		MessageAction: common.TaskCreationAction,
		Labels: map[string]string{
			nvcatypes.TaskIDKey:            testTaskID,
			nvcatypes.MiniserviceNameLabel: testInstanceID,
		},
		Annotations: map[string]string{
			nvcatypes.ICMSRequestIDKey: testReqID,
			nvcatypes.NCAIDKey:         testNcaID,
			cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey:       "kns-pvc-ro",
			cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey:   "secrets-pvc-ro",
			cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: "task-pvc-rw",
		},
		EnvVars: []corev1.EnvVar{
			{Name: "NVCT_NCA_ID", Value: testNcaID},
			{Name: "NVCT_TASK_ID", Value: testTaskID},
			{Name: nvcatypes.NVCTInstIDEnvKey, ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.labels['" + nvcatypes.MiniserviceNameLabel + "']",
				},
			}},
		},
		TerminationGracePeriodSeconds: &gracePeriod,
	}
	return createConfigMapFromMeta(t, ns, meta)
}

func createConfigMapFromMeta(t *testing.T, ns string, meta nvcatypes.MiniserviceMetadata) *corev1.ConfigMap {
	t.Helper()
	data, err := meta.ToConfigMapData()
	require.NoError(t, err)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcatypes.MiniserviceMetadataConfigMapName,
			Namespace: ns,
			Labels: map[string]string{
				nvcatypes.MiniserviceNameLabel: testInstanceID,
			},
		},
		Data: data,
	}
}

// makeWebhook returns a miniserviceMutatingWebhook backed by a fake k8s client that
// has the given ConfigMaps pre-populated.
func makeWebhook(t *testing.T, cms ...*corev1.ConfigMap) *miniserviceMutatingWebhook {
	t.Helper()
	objs := make([]runtime.Object, len(cms))
	for i, cm := range cms {
		objs[i] = cm
	}
	k8sClient := k8sfake.NewSimpleClientset(objs...)
	handler, err := newMiniserviceMutatingWebhook(t.Context(), k8sClient)
	require.NoError(t, err)
	return handler.(*miniserviceMutatingWebhook)
}

// miniserviceNameLabels returns the minimum labels required for an object to pass webhook validation.
func miniserviceNameLabels() map[string]string {
	return map[string]string{
		nvcatypes.MiniserviceNameLabel: testInstanceID,
	}
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestMiniserviceOperatorWebhook_ConfigMapMissing(t *testing.T) {
	wh := makeWebhook(t) // no ConfigMap
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: testNamespace, Labels: miniserviceNameLabels()},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	assert.False(t, resp.Allowed)
	assert.Equal(t, http.StatusForbidden, int(resp.Result.Code), "Denied should use 403, not 500 (Errored)")
}

func TestMiniserviceOperatorWebhook_ClusterScopedObject(t *testing.T) {
	wh := makeWebhook(t) // no ConfigMap needed
	ctx := core.WithDefaultLogger(context.Background())

	raw, _ := json.Marshal(map[string]any{"kind": "ClusterRole"})
	req := admission.Request{}
	req.Namespace = "" // cluster-scoped
	req.Kind = metav1.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestMiniserviceOperatorWebhook_InfraPodObject(t *testing.T) {
	wh := makeWebhook(t) // no ConfigMap needed
	ctx := core.WithDefaultLogger(context.Background())

	raw, _ := json.Marshal(map[string]any{"kind": "Pod", "metadata": map[string]any{"name": common.UtilsPodName}})
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

// ─── Pod ─────────────────────────────────────────────────────────────────────

func TestMiniserviceOperatorWebhook_Pod_MetadataAndEnvInjected(t *testing.T) {
	wh := makeWebhook(t, testConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: testNamespace, Labels: miniserviceNameLabels()},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "app:latest"}},
		},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed, "expected Allowed, got: %v", resp.Result)
	require.NotEmpty(t, resp.Patches)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	assertFunctionLabels(t, got.Labels)
	assertFunctionAnnotations(t, got.Annotations)
	assertFunctionEnvVars(t, got.Spec.Containers[0].Env)
	assert.Equal(t, "miniservice-instance-permissions", got.Spec.ServiceAccountName)
}

func TestMiniserviceOperatorWebhook_UtilsPod_Function(t *testing.T) {
	wh := makeWebhook(t, testConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "utils", Namespace: testNamespace,
			Annotations: map[string]string{
				cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:     "kns-pvc-rw",
				cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "secrets-pvc-rw",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "utils", Image: "app:latest"}},
		},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed, "expected Allowed, got: %v", resp.Result)
	require.NotEmpty(t, resp.Patches)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	assert.Empty(t, got.Labels)
	assert.Equal(t, map[string]string{
		cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:     "kns-pvc-rw",
		cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "secrets-pvc-rw",
	}, got.Annotations)
	assert.Empty(t, got.Spec.Containers[0].Env)
	assert.Equal(t, []corev1.Volume{
		{
			Name: "kns-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "kns-pvc-rw",
				},
			},
		},
		{
			Name: "secrets-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "secrets-pvc-rw",
				},
			},
		},
	}, got.Spec.Volumes)
}

func TestMiniserviceOperatorWebhook_Pod_TaskEnvInjected(t *testing.T) {
	wh := makeWebhook(t, testTaskConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "task-pod", Namespace: testNamespace, Labels: miniserviceNameLabels()},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	assertTaskLabels(t, got.Labels)
	assertTaskAnnotations(t, got.Annotations)
	assertTaskEnvVars(t, got.Spec.Containers[0].Env)
	require.NotNil(t, got.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(300), *got.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, []corev1.Volume{
		{
			Name: "kns-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "kns-pvc-ro",
				},
			},
		},
		{
			Name: "secrets-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "secrets-pvc-ro",
				},
			},
		},
		{
			Name: "task-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "task-pvc-rw",
				},
			},
		},
	}, got.Spec.Volumes)
}

func TestMiniserviceOperatorWebhook_UtilsPod_Task(t *testing.T) {
	wh := makeWebhook(t, testTaskConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "utils", Namespace: testNamespace,
			Annotations: map[string]string{
				cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:      "kns-pvc-rw",
				cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey:  "secrets-pvc-rw",
				cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: "task-pvc-rw",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "utils", Image: "app:latest"}},
		},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed, "expected Allowed, got: %v", resp.Result)
	require.NotEmpty(t, resp.Patches)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	assert.Empty(t, got.Labels)
	assert.Equal(t, map[string]string{
		cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:      "kns-pvc-rw",
		cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey:  "secrets-pvc-rw",
		cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: "task-pvc-rw",
	}, got.Annotations)
	assert.Empty(t, got.Spec.Containers[0].Env)
	assert.Equal(t, []corev1.Volume{
		{
			Name: "kns-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "kns-pvc-rw",
				},
			},
		},
		{
			Name: "secrets-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "secrets-pvc-rw",
				},
			},
		},
		{
			Name: "task-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "task-pvc-rw",
				},
			},
		},
	}, got.Spec.Volumes)
}

func TestMiniserviceOperatorWebhook_Pod_ExistingEnvNotDuplicated(t *testing.T) {
	wh := makeWebhook(t, testConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: testNamespace, Labels: miniserviceNameLabels()},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app",
				Env:  []corev1.EnvVar{{Name: "NVCF_NCA_ID", Value: "existing"}},
			}},
		},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	count := 0
	for _, e := range got.Spec.Containers[0].Env {
		if e.Name == "NVCF_NCA_ID" {
			count++
		}
	}
	assert.Equal(t, 1, count, "NVCF_NCA_ID should not be duplicated")
}

func TestMiniserviceOperatorWebhook_PodSpecCreateThenUpdate_IsIdempotentOrderPreserved(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())

	newPod := func() *corev1.Pod {
		return &corev1.Pod{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "idempotent-pod",
				Namespace: testNamespace,
				Labels:    miniserviceNameLabels(),
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "app:latest"}},
				Affinity: &corev1.Affinity{
					PodAffinity: &corev1.PodAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
							{
								Weight: 100,
								PodAffinityTerm: corev1.PodAffinityTerm{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{Key: "foo", Operator: metav1.LabelSelectorOpIn, Values: []string{"bar"}},
										},
									},
									TopologyKey: "topology.kubernetes.io/zone",
								},
							},
						},
					},
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{Key: "baz", Operator: corev1.NodeSelectorOpIn, Values: []string{"buf"}},
									},
								},
							},
						},
					},
				},
			},
		}
	}
	expAffinity := &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{Key: "foo", Operator: metav1.LabelSelectorOpIn, Values: []string{"bar"}},
							},
						},
						TopologyKey: "topology.kubernetes.io/zone",
					},
				},
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      nvcfdra.NVLinkDomainPartitionLabel,
									Operator: metav1.LabelSelectorOpExists,
								},
								{
									Key:      nvcfdra.NVLinkDomainPartitionLabel,
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"x16989cf0cc8364fa9ex"},
								},
							},
						},
						TopologyKey: nvcfdra.GPUCliqueNodeLabel,
					},
				},
			},
		},
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "baz", Operator: corev1.NodeSelectorOpIn, Values: []string{"buf"}},
							{
								Key:      "nvca.nvcf.nvidia.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"ON-PREM.GPU.A100"},
							},
						},
					},
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      nvcfdra.GPUCliqueNodeLabel,
								Operator: corev1.NodeSelectorOpExists,
							},
							{
								Key:      "nvca.nvcf.nvidia.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"ON-PREM.GPU.A100"},
							},
						},
					},
				},
			},
		},
	}

	meta := nvcatypes.MiniserviceMetadata{
		MessageAction: common.FunctionCreationAction,
		Labels: map[string]string{
			nvcatypes.FunctionIDKey:        testFunctionID,
			nvcatypes.FunctionVersionIDKey: testVersionID,
			nvcatypes.MiniserviceNameLabel: testInstanceID,
		},
		Annotations: map[string]string{
			nvcatypes.ICMSRequestIDKey: testReqID,
			nvcatypes.NCAIDKey:         testNcaID,
		},
		PodAnnotations: map[string]string{
			cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey:       "kns-pvc-ro",
			cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey:   "secrets-pvc-ro",
			cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: "task-pvc-rw",
			cmnnvcastorage.WebhookModelCachePVCNameAnnotationKey:                         "model-cache-pvc",
		},
		EnvVars: []corev1.EnvVar{
			{Name: "NVCF_NCA_ID", Value: testNcaID},
			{Name: "NVCF_FUNCTION_ID", Value: testFunctionID},
			{Name: "NVCF_FUNCTION_VERSION_ID", Value: testVersionID},
			{Name: nvcatypes.NVCFInstIDEnvKey, ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.labels['" + nvcatypes.MiniserviceNameLabel + "']",
				},
			}},
		},
		ServiceAccountName: "miniservice-instance-permissions",
		NodeAffinityKey:    "nvca.nvcf.nvidia.io/instance-type",
		NodeAffinityValue:  "ON-PREM.GPU.A100",
	}
	cm := createConfigMapFromMeta(t, testNamespace, meta)

	t.Run("Update with other mounts applied after", func(t *testing.T) {
		wh := makeWebhook(t, cm)
		wh.fff = &featureflagmock.Fetcher{EnabledAttrs: []*featureflag.Attribute{featureflag.AttrNVLinkOptimized}}
		createPod := newPod()

		createRaw, _ := json.Marshal(createPod)
		createReq := admission.Request{}
		createReq.Namespace = testNamespace
		createReq.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
		createReq.Operation = admissionv1.Create
		createReq.Object = runtime.RawExtension{Raw: createRaw}

		createResp := wh.Handle(ctx, createReq)
		require.True(t, createResp.Allowed, "expected create Allowed, got: %v", createResp.Result)
		require.NotEmpty(t, createResp.Patches)

		createMutatedRaw := applyPatches(t, createRaw, createResp.Patches)
		var createdPod corev1.Pod
		require.NoError(t, json.Unmarshal(createMutatedRaw, &createdPod))
		require.Equal(t, []string{
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName,
			cmnnvcastorage.SharedStorageSecretsVolumeName,
			cmnnvcastorage.SharedStorageTaskDataVolumeName,
			cmnnvcastorage.ModelCachePodVolumeName,
		}, volumeNames(createdPod.Spec.Volumes))
		require.Equal(t, []string{
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeKNSTokenMountPath,
			cmnnvcastorage.SharedStorageSecretsVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeSecretsMountPath,
			cmnnvcastorage.SharedStorageTaskDataVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeTaskDataMountPath,
			cmnnvcastorage.ModelCachePodVolumeName + "@" + cmnnvcastorage.ModelCachePodModelMountPath,
			cmnnvcastorage.ModelCachePodVolumeName + "@" + cmnnvcastorage.ModelCachePodResourcesMountPath,
		}, volumeMountKeys(createdPod.Spec.Containers[0].VolumeMounts))
		require.Equal(t, expAffinity, createdPod.Spec.Affinity)

		// Simulate k8s service-account admission adding kube-api-access after create.
		createdPod.Spec.Volumes = append(createdPod.Spec.Volumes, corev1.Volume{
			Name: "kube-api-access-rmxfs",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{},
			},
		})
		createdPod.Spec.Containers[0].VolumeMounts = append(createdPod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "kube-api-access-rmxfs",
			MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
			ReadOnly:  true,
		})

		updateRaw, _ := json.Marshal(&createdPod)
		updateReq := admission.Request{}
		updateReq.Namespace = testNamespace
		updateReq.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
		updateReq.Operation = admissionv1.Update
		updateReq.Object = runtime.RawExtension{Raw: updateRaw}

		updateResp := wh.Handle(ctx, updateReq)
		require.True(t, updateResp.Allowed, "expected update Allowed, got: %v", updateResp.Result)

		updateMutatedRaw := applyPatches(t, updateRaw, updateResp.Patches)
		var gotUpdatedPod corev1.Pod
		require.NoError(t, json.Unmarshal(updateMutatedRaw, &gotUpdatedPod))

		assert.Equal(t, createdPod.Spec.Volumes, gotUpdatedPod.Spec.Volumes, "volume order must remain stable on update")
		assert.Equal(
			t,
			createdPod.Spec.Containers[0].VolumeMounts,
			gotUpdatedPod.Spec.Containers[0].VolumeMounts,
			"volumeMount order must remain stable on update",
		)
		require.Equal(t, expAffinity, gotUpdatedPod.Spec.Affinity)
	})

	t.Run("Update with other mounts applied before", func(t *testing.T) {
		wh := makeWebhook(t, cm)
		wh.fff = &featureflagmock.Fetcher{EnabledAttrs: []*featureflag.Attribute{featureflag.AttrNVLinkOptimized}}
		createPod := newPod()
		// Simulate k8s service-account admission adding kube-api-access before this webhook receives the create request.
		createPod.Spec.Volumes = append(createPod.Spec.Volumes, corev1.Volume{
			Name: "kube-api-access-rmxfs",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{},
			},
		})
		createPod.Spec.Containers[0].VolumeMounts = append(createPod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "kube-api-access-rmxfs",
			MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
			ReadOnly:  true,
		})

		createRaw, _ := json.Marshal(createPod)
		createReq := admission.Request{}
		createReq.Namespace = testNamespace
		createReq.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
		createReq.Operation = admissionv1.Create
		createReq.Object = runtime.RawExtension{Raw: createRaw}

		createResp := wh.Handle(ctx, createReq)
		require.True(t, createResp.Allowed, "expected create Allowed, got: %v", createResp.Result)
		require.NotEmpty(t, createResp.Patches)

		createMutatedRaw := applyPatches(t, createRaw, createResp.Patches)
		var createdPod corev1.Pod
		require.NoError(t, json.Unmarshal(createMutatedRaw, &createdPod))
		require.Equal(t, []string{
			"kube-api-access-rmxfs",
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName,
			cmnnvcastorage.SharedStorageSecretsVolumeName,
			cmnnvcastorage.SharedStorageTaskDataVolumeName,
			cmnnvcastorage.ModelCachePodVolumeName,
		}, volumeNames(createdPod.Spec.Volumes))
		require.Equal(t, []string{
			"kube-api-access-rmxfs@/var/run/secrets/kubernetes.io/serviceaccount",
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeKNSTokenMountPath,
			cmnnvcastorage.SharedStorageSecretsVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeSecretsMountPath,
			cmnnvcastorage.SharedStorageTaskDataVolumeName + "@" + cmnnvcastorage.SharedStorageVolumeTaskDataMountPath,
			cmnnvcastorage.ModelCachePodVolumeName + "@" + cmnnvcastorage.ModelCachePodModelMountPath,
			cmnnvcastorage.ModelCachePodVolumeName + "@" + cmnnvcastorage.ModelCachePodResourcesMountPath,
		}, volumeMountKeys(createdPod.Spec.Containers[0].VolumeMounts))
		require.Equal(t, expAffinity, createdPod.Spec.Affinity)

		updateRaw, _ := json.Marshal(&createdPod)
		updateReq := admission.Request{}
		updateReq.Namespace = testNamespace
		updateReq.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
		updateReq.Operation = admissionv1.Update
		updateReq.Object = runtime.RawExtension{Raw: updateRaw}

		updateResp := wh.Handle(ctx, updateReq)
		require.True(t, updateResp.Allowed, "expected update Allowed, got: %v", updateResp.Result)

		updateMutatedRaw := applyPatches(t, updateRaw, updateResp.Patches)
		var gotUpdatedPod corev1.Pod
		require.NoError(t, json.Unmarshal(updateMutatedRaw, &gotUpdatedPod))

		assert.Equal(t, createdPod.Spec.Volumes, gotUpdatedPod.Spec.Volumes, "volume order must remain stable on update")
		assert.Equal(
			t,
			createdPod.Spec.Containers[0].VolumeMounts,
			gotUpdatedPod.Spec.Containers[0].VolumeMounts,
			"volumeMount order must remain stable on update",
		)
		require.Equal(t, expAffinity, gotUpdatedPod.Spec.Affinity)
	})
}

// ─── Unknown CRD (operator type) ─────────────────────────────────────────────

func TestMiniserviceOperatorWebhook_UnknownCRD_MetadataOnlyInjected(t *testing.T) {
	wh := makeWebhook(t, testConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	dgd := map[string]any{
		"apiVersion": "nvidia.com/v1alpha1",
		"kind":       "DynamoGraphDeployment",
		"metadata": map[string]any{
			"name":      "llama-disagg",
			"namespace": testNamespace,
			"labels": map[string]any{
				nvcatypes.MiniserviceNameLabel: testInstanceID,
			},
		},
		"spec": map[string]any{
			"services": map[string]any{
				"prefill": map[string]any{"replicas": 2},
			},
		},
	}
	raw, _ := json.Marshal(dgd)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed, "unknown CRD should be allowed with metadata injection, got: %v", resp.Result)

	mutated := applyPatches(t, raw, resp.Patches)
	var got map[string]any
	require.NoError(t, json.Unmarshal(mutated, &got))

	meta, _ := got["metadata"].(map[string]any)
	require.NotNil(t, meta)
	labels, _ := meta["labels"].(map[string]any)
	require.NotNil(t, labels)

	assert.Equal(t, testFunctionID, labels[nvcatypes.FunctionIDKey])
	assert.Equal(t, testInstanceID, labels[nvcatypes.MiniserviceNameLabel])

	// Spec must be untouched (no env injection into unknown types).
	spec, _ := got["spec"].(map[string]any)
	require.NotNil(t, spec)
	services, _ := spec["services"].(map[string]any)
	require.NotNil(t, services)
	prefill, _ := services["prefill"].(map[string]any)
	require.NotNil(t, prefill)
	assert.Equal(t, float64(2), prefill["replicas"])
}

// ─── Init containers ─────────────────────────────────────────────────────────

func TestMiniserviceOperatorWebhook_Pod_InitContainerEnvInjected(t *testing.T) {
	wh := makeWebhook(t, testConfigMap(t, testNamespace))
	ctx := core.WithDefaultLogger(context.Background())

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-with-init", Namespace: testNamespace, Labels: miniserviceNameLabels()},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "init"}},
			Containers:     []corev1.Container{{Name: "app"}},
		},
	}
	raw, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Namespace = testNamespace
	req.Kind = metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	req.Object = runtime.RawExtension{Raw: raw}
	req.Operation = admissionv1.Create

	resp := wh.Handle(ctx, req)
	require.True(t, resp.Allowed)

	mutated := applyPatches(t, raw, resp.Patches)
	var got corev1.Pod
	require.NoError(t, json.Unmarshal(mutated, &got))

	assertFunctionEnvVars(t, got.Spec.InitContainers[0].Env)
	assertFunctionEnvVars(t, got.Spec.Containers[0].Env)
}

// ─── miniserviceMetadata unit tests ──────────────────────────────────────────

func TestMiniserviceMetadataFromConfigMap(t *testing.T) {
	cm := testConfigMap(t, testNamespace)
	wh := makeWebhook(t, cm)
	m, err := wh.miniserviceMetadataFromConfigMap(testInstanceID, cm.Data)
	require.NoError(t, err)

	assert.Equal(t, common.FunctionCreationAction, m.MessageAction)
	assert.Equal(t, testFunctionID, m.Labels[nvcatypes.FunctionIDKey])
	assert.Equal(t, testVersionID, m.Labels[nvcatypes.FunctionVersionIDKey])
	assert.Equal(t, testInstanceID, m.Labels[nvcatypes.MiniserviceNameLabel])
	assert.Equal(t, testReqID, m.Annotations[nvcatypes.ICMSRequestIDKey])
	assert.Equal(t, testNcaID, m.Annotations[nvcatypes.NCAIDKey])
	assert.Equal(t, "miniservice-instance-permissions", m.ServiceAccountName)
	require.NotEmpty(t, m.EnvVars)
}

// ─── Assertion helpers ────────────────────────────────────────────────────────

func assertFunctionLabels(t *testing.T, lbls map[string]string) {
	t.Helper()
	assert.Equal(t, map[string]string{
		nvcatypes.FunctionIDKey:        testFunctionID,
		nvcatypes.FunctionVersionIDKey: testVersionID,
		nvcatypes.MiniserviceNameLabel: testInstanceID,
	}, lbls)
}

func assertTaskLabels(t *testing.T, lbls map[string]string) {
	t.Helper()
	assert.Equal(t, map[string]string{
		nvcatypes.TaskIDKey:            testTaskID,
		nvcatypes.MiniserviceNameLabel: testInstanceID,
	}, lbls)
}

func assertFunctionAnnotations(t *testing.T, annos map[string]string) {
	t.Helper()
	assert.Equal(t, map[string]string{
		nvcatypes.ICMSRequestIDKey: testReqID,
		nvcatypes.NCAIDKey:         testNcaID,
		cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey:     "kns-pvc-ro",
		cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey: "secrets-pvc-ro",
	}, annos)
}

func assertTaskAnnotations(t *testing.T, annos map[string]string) {
	t.Helper()
	assert.Equal(t, map[string]string{
		nvcatypes.ICMSRequestIDKey: testReqID,
		nvcatypes.NCAIDKey:         testNcaID,
		cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey:       "kns-pvc-ro",
		cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey:   "secrets-pvc-ro",
		cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: "task-pvc-rw",
	}, annos)
}

func assertFunctionEnvVars(t *testing.T, envs []corev1.EnvVar) {
	t.Helper()
	byName := envMap(envs)
	assert.Equal(t, testNcaID, byName["NVCF_NCA_ID"], "NVCF_NCA_ID")
	assert.Equal(t, testFunctionID, byName["NVCF_FUNCTION_ID"], "NVCF_FUNCTION_ID")
	assert.Equal(t, testVersionID, byName["NVCF_FUNCTION_VERSION_ID"], "NVCF_FUNCTION_VERSION_ID")
	assert.Contains(t, byName, nvcatypes.NVCFInstIDEnvKey, "NVCF_INSTANCE_ID via fieldRef")
}

func assertTaskEnvVars(t *testing.T, envs []corev1.EnvVar) {
	t.Helper()
	byName := envMap(envs)
	assert.Equal(t, testNcaID, byName["NVCT_NCA_ID"], "NVCT_NCA_ID")
	assert.Equal(t, "task-007", byName["NVCT_TASK_ID"], "NVCT_TASK_ID")
	assert.Contains(t, byName, nvcatypes.NVCTInstIDEnvKey, "NVCT_INSTANCE_ID via fieldRef")
}

// envMap flattens a slice of EnvVar into name→value, using "<fieldRef>" for ValueFrom entries.
func envMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		if e.ValueFrom != nil {
			m[e.Name] = "<fieldRef>"
		} else {
			m[e.Name] = e.Value
		}
	}
	return m
}

// applyPatches applies a slice of JSON Patch operations to base and returns the patched JSON.
func applyPatches(t *testing.T, base []byte, patches []jsonpatch.Operation) []byte {
	t.Helper()
	if len(patches) == 0 {
		return base
	}
	patchData, err := json.Marshal(patches)
	require.NoError(t, err)

	jp, err := evanphxpatch.DecodePatch(patchData)
	require.NoError(t, err)

	patched, err := jp.Apply(base)
	require.NoError(t, err)
	return patched
}

// ─── mutatePodSpec ServiceAccountName tests ───────────────────────────────────

func TestMiniserviceMutatePodSpec_ServiceAccountName(t *testing.T) {
	const msSA = "miniservice-instance-permissions"
	tests := []struct {
		name     string
		fff      featureflag.Fetcher
		metaSA   string
		podSA    string
		expectSA string
	}{
		{
			name:     "flag disabled, pod has no SA: overrides with meta SA",
			fff:      &featureflagmock.Fetcher{},
			metaSA:   msSA,
			podSA:    "",
			expectSA: msSA,
		},
		{
			name:     "flag disabled, pod has default SA: overrides with meta SA",
			fff:      &featureflagmock.Fetcher{},
			metaSA:   msSA,
			podSA:    "default",
			expectSA: msSA,
		},
		{
			name:     "flag disabled, pod has custom SA: overrides with meta SA",
			fff:      &featureflagmock.Fetcher{},
			metaSA:   msSA,
			podSA:    "operator-created-sa",
			expectSA: msSA,
		},
		{
			name: "flag enabled, pod has no SA: overrides with meta SA",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.AllowWorkloadKubernetesAPIAccess},
			},
			metaSA:   msSA,
			podSA:    "",
			expectSA: msSA,
		},
		{
			name: "flag enabled, pod has default SA: overrides with meta SA",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.AllowWorkloadKubernetesAPIAccess},
			},
			metaSA:   msSA,
			podSA:    "default",
			expectSA: msSA,
		},
		{
			name: "flag enabled, pod has custom SA: preserves pod SA",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.AllowWorkloadKubernetesAPIAccess},
			},
			metaSA:   msSA,
			podSA:    "operator-created-sa",
			expectSA: "operator-created-sa",
		},
		{
			name:     "meta has no SA: pod SA unchanged",
			fff:      &featureflagmock.Fetcher{},
			metaSA:   "",
			podSA:    "operator-created-sa",
			expectSA: "operator-created-sa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wh := &miniserviceMutatingWebhook{fff: tt.fff}
			ps := corev1.PodSpec{
				ServiceAccountName: tt.podSA,
				Containers:         []corev1.Container{{Name: "app"}},
			}
			meta := nvcatypes.MiniserviceMetadata{
				ServiceAccountName: tt.metaSA,
			}
			wh.mutatePodSpec(&ps, meta)
			assert.Equal(t, tt.expectSA, ps.ServiceAccountName)
		})
	}
}

func TestMiniserviceMutatePodSpec_NodeAffinityWithPodAffinityWebhook(t *testing.T) {
	const (
		instanceTypeKey   = "nvca.nvcf.nvidia.io/instance-type"
		instanceTypeValue = "ON-PREM.GPU.A100"
	)

	meta := nvcatypes.MiniserviceMetadata{
		NodeAffinityKey:   instanceTypeKey,
		NodeAffinityValue: instanceTypeValue,
	}
	sharedClusterOn := &atomic.Bool{}
	sharedClusterOn.Store(true)
	podWebhook := &podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
		SharedClusterOn:       sharedClusterOn,
		UniformInstanceLabels: true,
	}}
	presetInstanceTypeAffinity := func() *corev1.Affinity {
		return &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      instanceTypeKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"ON-PREM.GPU.H100"},
						}},
					}},
				},
			},
		}
	}

	tests := []struct {
		name              string
		podSpec           corev1.PodSpec
		miniserviceFirst  bool
		wantInstanceType  bool
		wantCPUPreference bool
	}{
		{
			name: "GPU pod, miniservice webhook first",
			podSpec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
				}},
			}}},
			miniserviceFirst: true,
			wantInstanceType: true,
		},
		{
			name: "GPU pod, pod affinity webhook first",
			podSpec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
				}},
			}}},
			wantInstanceType: true,
		},
		{
			name: "GPU pod with preset instance type affinity, miniservice webhook first",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
					}},
				}},
				Affinity: presetInstanceTypeAffinity(),
			},
			miniserviceFirst: true,
			wantInstanceType: true,
		},
		{
			name: "GPU pod with preset instance type affinity, pod affinity webhook first",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
					}},
				}},
				Affinity: presetInstanceTypeAffinity(),
			},
			wantInstanceType: true,
		},
		{
			name:              "CPU pod, miniservice webhook first",
			podSpec:           corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			miniserviceFirst:  true,
			wantCPUPreference: true,
		},
		{
			name:              "CPU pod, pod affinity webhook first",
			podSpec:           corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			wantCPUPreference: true,
		},
		{
			name: "CPU pod with preset instance type affinity, miniservice webhook first",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app"}},
				Affinity:   presetInstanceTypeAffinity(),
			},
			miniserviceFirst:  true,
			wantCPUPreference: true,
		},
		{
			name: "CPU pod with preset instance type affinity, pod affinity webhook first",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app"}},
				Affinity:   presetInstanceTypeAffinity(),
			},
			wantCPUPreference: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wh := &miniserviceMutatingWebhook{fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.HelmAllowCPUNodes},
			}}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: testNamespace},
				Spec:       *tt.podSpec.DeepCopy(),
			}

			applyWebhooks := func() {
				if tt.miniserviceFirst {
					wh.mutatePodSpec(&pod.Spec, meta)
					require.NoError(t, podWebhook.Default(t.Context(), pod))
				} else {
					require.NoError(t, podWebhook.Default(t.Context(), pod))
					wh.mutatePodSpec(&pod.Spec, meta)
				}
			}

			applyWebhooks()
			afterFirstPass := pod.Spec.DeepCopy()
			applyWebhooks()
			assert.Equal(t, afterFirstPass, &pod.Spec, "webhook mutations must be idempotent")

			require.NotNil(t, pod.Spec.Affinity)
			require.NotNil(t, pod.Spec.Affinity.NodeAffinity)
			required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			require.NotNil(t, required)
			require.Len(t, required.NodeSelectorTerms, 1)

			var instanceTypeRequirements, sharedClusterRequirements int
			for _, expr := range required.NodeSelectorTerms[0].MatchExpressions {
				switch expr.Key {
				case instanceTypeKey:
					instanceTypeRequirements++
					assert.Equal(t, corev1.NodeSelectorOpIn, expr.Operator)
					assert.Equal(t, []string{instanceTypeValue}, expr.Values)
				case sharedcluster.ScheduleLabelKey:
					sharedClusterRequirements++
					assert.Equal(t, corev1.NodeSelectorOpIn, expr.Operator)
					assert.Equal(t, []string{"true"}, expr.Values)
				}
			}
			if tt.wantInstanceType {
				assert.Equal(t, 1, instanceTypeRequirements)
			} else {
				assert.Zero(t, instanceTypeRequirements)
			}
			assert.Equal(t, 1, sharedClusterRequirements)

			var cpuPreferences int
			for _, term := range pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
				for _, expr := range term.Preference.MatchExpressions {
					if term.Weight == 100 && expr.Key == instanceTypeKey &&
						expr.Operator == corev1.NodeSelectorOpDoesNotExist {
						cpuPreferences++
					}
				}
			}
			if tt.wantCPUPreference {
				assert.Equal(t, 1, cpuPreferences)
			} else {
				assert.Zero(t, cpuPreferences)
			}
		})
	}
}

// ─── mutatePodSpec BYOObservability tests ─────────────────────────────────────

func TestMiniserviceMutatePodSpec_BYOObservability(t *testing.T) {
	metaWithEnvs := nvcatypes.MiniserviceMetadata{
		EnvVars: []corev1.EnvVar{
			{Name: "NVCF_NCA_ID", Value: testNcaID},
			{Name: "NVCF_FUNCTION_ID", Value: testFunctionID},
		},
	}

	tests := []struct {
		name   string
		fff    featureflag.Fetcher
		meta   nvcatypes.MiniserviceMetadata
		ps     corev1.PodSpec
		verify func(*testing.T, *corev1.PodSpec)
	}{
		{
			name: "empty overrideable env vars are filtered from containers and init containers",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.BYOObservability},
			},
			meta: metaWithEnvs,
			ps: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "init1",
						Env: []corev1.EnvVar{
							{Name: common.OTelExporterLogsEndpointEnv, Value: ""},
							{Name: "KEEP_ME", Value: ""},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "container1",
						Env: []corev1.EnvVar{
							{Name: common.OTelExporterMetricsEndpointEnv, Value: ""},
							{Name: common.OTelExporterTracesEndpointEnv, Value: ""},
							{Name: "APP_ENV", Value: "prod"},
						},
					},
				},
			},
			verify: func(t *testing.T, ps *corev1.PodSpec) {
				// Empty overrideable env vars removed; non-overrideable kept; metadata envs added.
				initEnvNames := envNames(ps.InitContainers[0].Env)
				assert.NotContains(t, initEnvNames, common.OTelExporterLogsEndpointEnv)
				assert.Contains(t, initEnvNames, "KEEP_ME")
				assert.Contains(t, initEnvNames, "NVCF_NCA_ID")
				assert.Contains(t, initEnvNames, "NVCF_FUNCTION_ID")

				containerEnvNames := envNames(ps.Containers[0].Env)
				assert.NotContains(t, containerEnvNames, common.OTelExporterMetricsEndpointEnv)
				assert.NotContains(t, containerEnvNames, common.OTelExporterTracesEndpointEnv)
				assert.Contains(t, containerEnvNames, "APP_ENV")
				assert.Contains(t, containerEnvNames, "NVCF_NCA_ID")
			},
		},
		{
			name: "non-empty overrideable env vars are preserved over metadata values",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.BYOObservability},
			},
			meta: nvcatypes.MiniserviceMetadata{
				EnvVars: []corev1.EnvVar{
					{Name: common.OTelExporterLogsEndpointEnv, Value: "http://default:4318/v1/logs"},
					{Name: common.OTelExporterTracesEndpointEnv, Value: "http://default:4318/v1/traces"},
					{Name: "NVCF_NCA_ID", Value: testNcaID},
				},
			},
			ps: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "init1",
						Env: []corev1.EnvVar{
							{Name: common.OTelExporterLogsEndpointEnv, Value: "http://custom:4318/v1/logs"},
							{Name: common.OTelExporterLogsProtocolEnv, Value: "http/protobuf"},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "container1",
						Env: []corev1.EnvVar{
							{Name: common.OTelExporterMetricsEndpointEnv, Value: "http://custom:4318/v1/metrics"},
							{Name: common.OTelExporterTracesEndpointEnv, Value: ""},
							{Name: common.OTelHealthCheckEndpointEnv, Value: "0.0.0.0:13133"},
						},
					},
				},
			},
			verify: func(t *testing.T, ps *corev1.PodSpec) {
				// Overrideable env vars with non-empty customer values are preserved.
				initByName := envMap(ps.InitContainers[0].Env)
				assert.Equal(t, "http://custom:4318/v1/logs", initByName[common.OTelExporterLogsEndpointEnv])
				assert.Equal(t, "http/protobuf", initByName[common.OTelExporterLogsProtocolEnv])

				containerByName := envMap(ps.Containers[0].Env)
				assert.Equal(t, "http://custom:4318/v1/metrics", containerByName[common.OTelExporterMetricsEndpointEnv])
				assert.Equal(t, "http://default:4318/v1/traces", containerByName[common.OTelExporterTracesEndpointEnv])
				assert.Equal(t, "0.0.0.0:13133", containerByName[common.OTelHealthCheckEndpointEnv])
			},
		},
		{
			name: "overrideable env vars with ValueFrom are preserved",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.BYOObservability},
			},
			meta: metaWithEnvs,
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Env: []corev1.EnvVar{
							{
								Name: common.OTelExporterLogsEndpointEnv,
								ValueFrom: &corev1.EnvVarSource{
									ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: "otel-config"},
										Key:                  "logs-endpoint",
									},
								},
							},
							{Name: common.OTelExporterMetricsEndpointEnv, Value: ""},
						},
					},
				},
			},
			verify: func(t *testing.T, ps *corev1.PodSpec) {
				containerEnvNames := envNames(ps.Containers[0].Env)
				// ValueFrom env var preserved despite empty Value field.
				assert.Contains(t, containerEnvNames, common.OTelExporterLogsEndpointEnv)
				// Empty literal overrideable env var filtered.
				assert.NotContains(t, containerEnvNames, common.OTelExporterMetricsEndpointEnv)
				// Metadata envs still added.
				assert.Contains(t, containerEnvNames, "NVCF_NCA_ID")

				// Confirm the ValueFrom was not clobbered.
				for _, e := range ps.Containers[0].Env {
					if e.Name == common.OTelExporterLogsEndpointEnv {
						require.NotNil(t, e.ValueFrom)
						break
					}
				}
			},
		},
		{
			name: "pods with no env vars receive metadata envs normally",
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{featureflag.BYOObservability},
			},
			meta: metaWithEnvs,
			ps: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "init1"}},
				Containers:     []corev1.Container{{Name: "container1"}},
			},
			verify: func(t *testing.T, ps *corev1.PodSpec) {
				assert.Contains(t, envNames(ps.InitContainers[0].Env), "NVCF_NCA_ID")
				assert.Contains(t, envNames(ps.Containers[0].Env), "NVCF_NCA_ID")
				assert.Contains(t, envNames(ps.Containers[0].Env), "NVCF_FUNCTION_ID")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wh := &miniserviceMutatingWebhook{fff: tt.fff}
			wh.mutatePodSpec(&tt.ps, tt.meta)
			tt.verify(t, &tt.ps)
		})
	}
}

func TestMiniserviceMutatePodSpec_BYOOOTelCollectorEnvVarsOnlyCollector(t *testing.T) {
	meta := nvcatypes.MiniserviceMetadata{
		EnvVars: []corev1.EnvVar{
			{Name: "SHARED_ENV", Value: "shared"},
		},
		OTelCollectorEnvVars: []corev1.EnvVar{
			{Name: nvcaconfig.BYOOLogChunkingEnabledEnv, Value: "true"},
			{Name: nvcaconfig.BYOOLogChunkMaxPayloadBytesEnv, Value: "983040"},
			{Name: nvcaconfig.BYOODebugModeEnv, Value: "true"},
			{Name: nvcaconfig.BYOOMetricSubsetEnabledEnv, Value: "true"},
			{Name: nvcaconfig.BYOOOTelCollectorConfigEnv, Value: "eyJleHBvcnRlckhlbHBlciI6eyJ0aW1lb3V0IjoiMzBzIn19"},
		},
	}
	ps := corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "init"}},
		Containers: []corev1.Container{
			{Name: "app"},
			{
				Name: common.ByooOTelCollectorPodNameBase,
				Env: []corev1.EnvVar{
					{Name: nvcaconfig.BYOOLogChunkMaxPayloadBytesEnv, Value: "1000000"},
				},
			},
		},
	}

	wh := &miniserviceMutatingWebhook{fff: &featureflagmock.Fetcher{}}
	wh.mutatePodSpec(&ps, meta)

	initByName := envMap(ps.InitContainers[0].Env)
	appByName := envMap(ps.Containers[0].Env)
	collectorByName := envMap(ps.Containers[1].Env)
	assert.Equal(t, "shared", initByName["SHARED_ENV"])
	assert.Equal(t, "shared", appByName["SHARED_ENV"])
	assert.Equal(t, "shared", collectorByName["SHARED_ENV"])
	assert.NotContains(t, initByName, nvcaconfig.BYOOLogChunkingEnabledEnv)
	assert.NotContains(t, initByName, nvcaconfig.BYOOLogChunkMaxPayloadBytesEnv)
	assert.NotContains(t, initByName, nvcaconfig.BYOODebugModeEnv)
	assert.NotContains(t, appByName, nvcaconfig.BYOOLogChunkingEnabledEnv)
	assert.NotContains(t, appByName, nvcaconfig.BYOOLogChunkMaxPayloadBytesEnv)
	assert.NotContains(t, initByName, nvcaconfig.BYOOMetricSubsetEnabledEnv)
	assert.NotContains(t, appByName, nvcaconfig.BYOOMetricSubsetEnabledEnv)
	assert.NotContains(t, initByName, nvcaconfig.BYOOOTelCollectorConfigEnv)
	assert.NotContains(t, appByName, nvcaconfig.BYOOOTelCollectorConfigEnv)
	assert.NotContains(t, appByName, nvcaconfig.BYOODebugModeEnv)
	assert.Equal(t, "true", collectorByName[nvcaconfig.BYOOLogChunkingEnabledEnv])
	assert.Equal(t, "983040", collectorByName[nvcaconfig.BYOOLogChunkMaxPayloadBytesEnv])
	assert.Equal(t, "true", collectorByName[nvcaconfig.BYOODebugModeEnv])
	assert.Equal(t, "true", collectorByName[nvcaconfig.BYOOMetricSubsetEnabledEnv])
	assert.Equal(t, "eyJleHBvcnRlckhlbHBlciI6eyJ0aW1lb3V0IjoiMzBzIn19", collectorByName[nvcaconfig.BYOOOTelCollectorConfigEnv])
}

func envNames(envs []corev1.EnvVar) []string {
	names := make([]string, len(envs))
	for i, e := range envs {
		names[i] = e.Name
	}
	return names
}

func volumeNames(vols []corev1.Volume) []string {
	names := make([]string, len(vols))
	for i, v := range vols {
		names[i] = v.Name
	}
	return names
}

func volumeMountKeys(vms []corev1.VolumeMount) []string {
	keys := make([]string, len(vms))
	for i, vm := range vms {
		keys[i] = vm.Name + "@" + vm.MountPath
	}
	return keys
}
