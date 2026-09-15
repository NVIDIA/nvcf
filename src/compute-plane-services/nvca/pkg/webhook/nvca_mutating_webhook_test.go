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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	cmnnvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestNVCAMutatingHandle(t *testing.T) {
	// enable all feature flags
	ctx := core.WithDefaultLogger(context.Background())
	wh := getNVCAMutatingWebhook(featureflag.DefaultFetcher, corev1.ResourceList{})

	tpod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ns",
			Namespace: "test-pod",
		},
		Spec: corev1.PodSpec{},
	}

	marshalledPod, err := json.Marshal(tpod)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "Pod"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "pods"},
			SubResource: "fakeresource",
			Name:        "fakepod",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: marshalledPod},
		},
	}

	resp := wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Result)

	tt := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
		},
	}

	mTT, err := json.Marshal(tt)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "Namespace"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "namespaces"},
			SubResource: "fakeresource",
			Name:        "fakepod",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: mTT},
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.Result.Message)
	assert.Contains(t, resp.Result.Message, "not handled by webhook")

	tc := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj-name",
			Namespace: "test-ns",
		},
		Data: map[string]string{},
	}

	mTT, err = json.Marshal(tc)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "ConfigMap"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "configmaps"},
			SubResource: "fakeresource",
			Name:        "fakeobject",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: mTT},
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Result.Message)

	ts := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj-name",
			Namespace: "test-ns",
		},
		Spec: corev1.ServiceSpec{},
	}

	mTT, err = json.Marshal(ts)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "Service"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "services"},
			SubResource: "fakeresource",
			Name:        "fakeobject",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: mTT},
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Result.Message)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "CREATE",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "DELETE",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "Random",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
}

func TestHelmPVCMutatingHandle(t *testing.T) {
	// enable all feature flags
	ctx := core.WithDefaultLogger(context.Background())
	wh := getHelmPersistentStorageWebhook("nvcf-sc", true)

	tpod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ns",
			Namespace: "test-pod",
		},
		Spec: corev1.PodSpec{},
	}

	marshalledPod, err := json.Marshal(tpod)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "Pod"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "pods"},
			SubResource: "fakeresource",
			Name:        "fakepod",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: marshalledPod},
		},
	}

	resp := wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.Result.Message)
	assert.Contains(t, resp.Result.Message, "not handled by webhook")

	tt := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "test-ns",
			Annotations: map[string]string{
				"nvca.nvcf.nvidia.io/storage-internalpersistentstorage-storage-class-name": "nvcf-sc",
			},
		},
	}

	mTT, err := json.Marshal(tt)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: corev1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "PersistentVolumeClaim"},
			Resource:    metav1.GroupVersionResource{Group: metav1.GroupName, Version: "v1", Resource: "persistentvolumeclaims"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: mTT},
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	scName := "random-class"

	ts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "test-ns",
			Annotations: map[string]string{
				"nvca.nvcf.nvidia.io/storage-internalpersistentstorage-storage-class-name": "nvcf-sc",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					Spec: corev1.PersistentVolumeClaimSpec{
						StorageClassName: &scName,
					},
				},
			},
		},
	}

	mTT, err = json.Marshal(ts)
	if err != nil {
		t.Fatal("failed to marshal")
	}
	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "CREATE",
			Object:      runtime.RawExtension{Raw: mTT},
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "CREATE",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "DELETE",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)

	arev = admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:         "fakeUUID123",
			Kind:        metav1.GroupVersionKind{Group: appsv1.GroupName, Version: corev1.SchemeGroupVersion.Version, Kind: "StatefulSet"},
			Resource:    metav1.GroupVersionResource{Group: appsv1.GroupName, Version: "v1", Resource: "statefulsets"},
			SubResource: "fakeresource",
			Name:        "fakeobj",
			Namespace:   "default",
			Operation:   "Random",
		},
	}

	resp = wh.Handle(ctx, arev)
	assert.NotNil(t, resp)
}

func TestSharedVolumeDropAllMutation(t *testing.T) {
	// enable all feature flags
	ctx := core.WithDefaultLogger(context.Background())

	tps := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{
				Name: "non-shared-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "random-nvcf-secrets-data-ro",
					},
				},
			},
			{
				Name: "shared-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "nvcf-secrets-data-ro",
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name: "test-container",
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "nvcf-secrets-data-ro",
						MountPath: "/random/path",
					},
					{
						Name:      "random-secrets-data-ro",
						MountPath: "/random/path",
					},
				},
			},
			{
				Name: "test-container2",
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "nvcf-secrets-data-ro",
						MountPath: "/random/path",
					},
					{
						Name:      "nvcf-secrets-data-ro",
						MountPath: "/var/secrets",
					},
				},
			},
		},
		InitContainers: []corev1.Container{
			{
				Name: "init-container",
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "nvcf-secrets-data-ro",
						MountPath: "/random/path",
					},
					{
						Name:      "random-secrets-data-ro",
						MountPath: "/random/path",
					},
				},
			},
		},
	}

	getSharedVolumeDropAllReservedFunc(
		cmnnvcastorage.IsSharedStorageVolumeName,
		cmnnvcastorage.IsSharedStorageVolumeMountPath)(ctx, &tps)
	assert.Len(t, tps.Volumes, 1)
	assert.Equal(t, tps.Volumes[0].PersistentVolumeClaim.ClaimName, "random-nvcf-secrets-data-ro")
	assert.Equal(t, len(tps.Containers[0].VolumeMounts), 2)
	assert.Equal(t, len(tps.Containers[1].VolumeMounts), 1)
	assert.Equal(t, len(tps.InitContainers[0].VolumeMounts), 2)
	assert.Equal(t, tps.Containers[0].VolumeMounts[0].Name, "nvcf-secrets-data-ro")
	assert.Equal(t, tps.Containers[0].VolumeMounts[1].Name, "random-secrets-data-ro")
	assert.Equal(t, tps.InitContainers[0].VolumeMounts[0].Name, "nvcf-secrets-data-ro")
	assert.Equal(t, tps.InitContainers[0].VolumeMounts[1].Name, "random-secrets-data-ro")
	assert.Equal(t, tps.Containers[1].VolumeMounts[0].Name, "nvcf-secrets-data-ro")
}

func Test_defaultInfraContainerResources(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())

	cpuLimit := resource.MustParse("4")
	memLimit := resource.MustParse("4Gi")
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    cpuLimit,
		corev1.ResourceMemory: memLimit,
	}

	tests := []struct {
		name     string
		podSpec  corev1.PodSpec
		expected corev1.PodSpec
	}{
		{
			name: "Utils container with no resources",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: translatecommon.UtilsContainerName},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: translatecommon.UtilsContainerName,
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    cpuLimit,
								corev1.ResourceMemory: memLimit,
							},
						},
					},
				},
			},
		},
		{
			name: "Utils container with existing resources",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: translatecommon.UtilsContainerName,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("2"),
								corev1.ResourceMemory:           resource.MustParse("2Gi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: translatecommon.UtilsContainerName,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("2"),
								corev1.ResourceMemory:           resource.MustParse("2Gi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		},
		{
			name: "Non-utils container",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "non-utils-container"},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "non-utils-container"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaultInfraContainerResourceLimits(ctx, limits, &tt.podSpec)
			assert.Equal(t, tt.expected, tt.podSpec)
		})
	}
}

func Test_overwritePodResourceRequests(t *testing.T) {
	cpuLimit := resource.MustParse("4")
	memLimit := resource.MustParse("4Gi")
	storageLimit := resource.MustParse("0")

	tests := []struct {
		name     string
		podSpec  corev1.PodSpec
		expected corev1.PodSpec
	}{
		{
			name: "Containers with no resource limits",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "container1"},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{},
							Limits:   corev1.ResourceList{},
						},
					},
				},
			},
		},
		{
			name: "Containers with resource limits",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
						},
					},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
						},
					},
				},
			},
		},
		{
			name: "Containers with existing resource requests",
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
			expected: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:              cpuLimit,
								corev1.ResourceMemory:           memLimit,
								corev1.ResourceEphemeralStorage: storageLimit,
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overwritePodResourceRequests(context.Background(), &tt.podSpec)
			assert.Equal(t, tt.expected, tt.podSpec)
		})
	}
}

func TestMutatePodSpecs(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())

	fff := &featureflagmock.Fetcher{}

	cpuLimit := resource.MustParse("4")
	memLimit := resource.MustParse("4Gi")
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    cpuLimit,
		corev1.ResourceMemory: memLimit,
	}

	tests := []struct {
		name         string
		obj          client.Object
		wantOk       bool
		wantWarnings admission.Warnings
		wantErrs     []error
		verify       func(*testing.T, client.Object)
		setup        func()
	}{
		{
			name: "Pod with env vars annotation",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Contains(t, pod.Spec.Tolerations, corev1.Toleration{
					Key:      translatecommon.NVIDIAGPUTolerationKey,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				})
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "Pod in Helm namespace with Kata enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
		},
		{
			name: "Utils container with Kata enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      translatecommon.UtilsContainerName,
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: translatecommon.UtilsContainerName},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, resource.MustParse("4"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse("4Gi"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory])
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
		},
		{
			name: "Function Pod in Helm namespace with Helm resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "sr-test",
					Labels:    map[string]string{types.FunctionIDUpperKey: "foo"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.EnforceHelmFunctionResourceLimits,
					},
				}
			},
		},
		{
			name: "Task Pod in Helm namespace with Helm resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "sr-test",
					Labels:    map[string]string{types.TaskIDUpperKey: "foo"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.EnforceHelmTaskResourceLimits,
					},
				}
			},
		},
		{
			name: "Utils container with Helm resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      translatecommon.UtilsContainerName,
					Namespace: "sr-test",
					Labels:    map[string]string{types.FunctionIDUpperKey: "foo"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: translatecommon.UtilsContainerName},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, resource.MustParse("4"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse("4Gi"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory])
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.EnforceHelmFunctionResourceLimits,
					},
				}
			},
		},
		{
			name: "Function Pod in container namespace with Container resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "0-sr-foo",
					Namespace: "nvcf-backend",
					Labels:    map[string]string{types.FunctionIDUpperKey: "foo"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:      translatecommon.UtilsContainerName,
							Env:       []corev1.EnvVar{{Name: k8sutil.InstanceIDEnvKey}},
							Resources: corev1.ResourceRequirements{},
						},
						{
							Name: "container1",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, resource.MustParse("4"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse("4Gi"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory])
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
				assert.Equal(t, pod.Spec.Containers[1].Resources.Requests, pod.Spec.Containers[1].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.EnforceContainerFunctionResourceLimits,
					},
				}
			},
		},
		{
			name: "Task Pod in container namespace with Container resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "0-sr-foo",
					Namespace: "nvcf-backend",
					Labels:    map[string]string{types.TaskIDUpperKey: "foo"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:      translatecommon.UtilsContainerName,
							Env:       []corev1.EnvVar{{Name: k8sutil.InstanceIDEnvKey}},
							Resources: corev1.ResourceRequirements{},
						},
						{
							Name: "container1",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.Equal(t, resource.MustParse("4"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse("4Gi"), pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory])
				assert.Equal(t, pod.Spec.Containers[0].Resources.Requests, pod.Spec.Containers[0].Resources.Limits)
				assert.Equal(t, pod.Spec.Containers[1].Resources.Requests, pod.Spec.Containers[1].Resources.Limits)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.EnforceContainerTaskResourceLimits,
					},
				}
			},
		},
		{
			name: "Pod with AutomountServiceAccountToken enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
					AutomountServiceAccountToken: &[]bool{true}[0],
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.NotNil(t, pod.Spec.AutomountServiceAccountToken)
				assert.False(t, *pod.Spec.AutomountServiceAccountToken)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "Pod with AutomountServiceAccountToken nil",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
					AutomountServiceAccountToken: nil,
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.False(t, *pod.Spec.AutomountServiceAccountToken)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "Pod with AutomountServiceAccountToken false",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
					AutomountServiceAccountToken: &[]bool{false}[0],
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.False(t, *pod.Spec.AutomountServiceAccountToken)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "Infra Pod with AutomountServiceAccountToken true",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Annotations: map[string]string{
						types.InfraObjectAnnotationKey: "true",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
					AutomountServiceAccountToken: &[]bool{true}[0],
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.True(t, *pod.Spec.AutomountServiceAccountToken)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "FCO Pod with AutomountServiceAccountToken true",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1"},
					},
					AutomountServiceAccountToken: &[]bool{true}[0],
				},
			},
			wantOk: true,
			verify: func(t *testing.T, obj client.Object) {
				pod := obj.(*corev1.Pod)
				assert.True(t, *pod.Spec.AutomountServiceAccountToken)
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledFFs: []*featureflag.FeatureFlag{
						featureflag.AllowWorkloadKubernetesAPIAccess,
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			wh := getNVCAMutatingWebhook(fff, limits)
			ok, warnings, errs := wh.mutatePodSpecs(ctx, tt.obj)

			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.wantWarnings, warnings)
			if tt.wantErrs != nil {
				assert.Equal(t, tt.wantErrs, errs)
			} else {
				assert.Empty(t, errs)
			}

			if tt.verify != nil {
				tt.verify(t, tt.obj)
			}
		})
	}
}

func TestNVCAMutatingWebhook_LateManagedNamespaceReplicaSetDoesNotDiverge(t *testing.T) {
	t.Parallel()

	ctx := core.WithDefaultLogger(context.Background())
	wh := getNVCAMutatingWebhook(featureflag.DefaultFetcher, corev1.ResourceList{})

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "repro-late-label",
			Namespace: "repro-late-label",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"debug-rev": "1"},
					Labels:      map[string]string{"app": "repro-late-label"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "nginx",
						Image: "nginx:1.25",
					}},
					Tolerations: []corev1.Toleration{{
						Key:      translatecommon.NVIDIAGPUTolerationKey,
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					}},
				},
			},
		},
	}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "repro-late-label-8776c5b6d",
			Namespace: "repro-late-label",
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: *deployment.Spec.Template.DeepCopy(),
		},
	}

	require.True(t, apiequality.Semantic.DeepEqual(deployment.Spec.Template.Spec, replicaSet.Spec.Template.Spec))

	ok, warnings, errs := wh.mutatePodSpecs(ctx, replicaSet)

	require.Empty(t, warnings)
	require.Empty(t, errs)
	require.False(t, ok, "replicaset mutation after a namespace becomes managed should not diverge from the stored deployment template")
	require.True(t, apiequality.Semantic.DeepEqual(deployment.Spec.Template.Spec, replicaSet.Spec.Template.Spec))
	require.Nil(t, replicaSet.Spec.Template.Spec.AutomountServiceAccountToken)
}
