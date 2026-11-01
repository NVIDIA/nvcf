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

	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestGetHelmPersistentStorageWebhook(t *testing.T) {
	tests := []struct {
		name                             string
		defaultStorageClassName          string
		internalPersistentStorageEnabled bool
	}{
		{
			name:                             "enabled with default storage class",
			defaultStorageClassName:          "fast-storage",
			internalPersistentStorageEnabled: true,
		},
		{
			name:                             "disabled with default storage class",
			defaultStorageClassName:          "standard",
			internalPersistentStorageEnabled: false,
		},
		{
			name:                             "enabled with empty storage class",
			defaultStorageClassName:          "",
			internalPersistentStorageEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhook := getHelmPersistentStorageWebhook(tt.defaultStorageClassName, tt.internalPersistentStorageEnabled)

			assert.NotNil(t, webhook)
			assert.Equal(t, tt.defaultStorageClassName, webhook.defaultStorageClassName)
			assert.Equal(t, tt.internalPersistentStorageEnabled, webhook.internalPersistentStorageEnabled)
			assert.NotNil(t, webhook.scheme)
			assert.NotNil(t, webhook.decoder)
		})
	}
}

func TestNewHelmPersistentStorageWebhook(t *testing.T) {
	handler := newHelmPersistentStorageWebhook("test-storage", true)
	assert.NotNil(t, handler)

	webhook, ok := handler.(*helmPersistentStorageWebhook)
	assert.True(t, ok)
	assert.Equal(t, "test-storage", webhook.defaultStorageClassName)
	assert.True(t, webhook.internalPersistentStorageEnabled)
}

func TestHelmPersistentStorageWebhook_Handle_Disabled(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("test-storage", false)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_UnrecognizedGVK(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("test-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "unknown.io",
				Version: "v1",
				Kind:    "UnknownKind",
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_ConnectOperation(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("test-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Connect,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_DeleteOperation(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("test-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Delete,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_CreatePVC(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	existingStorageClass := "existing-storage"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Annotations: map[string]string{
				nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "custom-storage",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &existingStorageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	rawPVC, err := json.Marshal(pvc)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: rawPVC,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
	assert.NotNil(t, resp.Patches)
}

func TestHelmPersistentStorageWebhook_Handle_UpdatePVC(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Annotations: map[string]string{
				nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "updated-storage",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	rawPVC, err := json.Marshal(pvc)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: rawPVC,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_CreateStatefulSet(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ss",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test:latest",
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		},
	}

	rawSS, err := json.Marshal(ss)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
			Object: runtime.RawExtension{
				Raw: rawSS,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
	assert.NotNil(t, resp.Patches)
}

func TestHelmPersistentStorageWebhook_Handle_InvalidJSON(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: []byte("invalid json"),
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.False(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_EmptyRawObject(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: []byte{},
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "no raw data to decode")
}

func TestHelmPersistentStorageWebhook_Handle_MarshalError(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	// Create a PVC without annotations to ensure no mutation happens
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	rawPVC, err := json.Marshal(pvc)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: rawPVC,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_UnknownOperation(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Operation("Unknown"),
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "unknown operation")
}

func TestHelmPersistentStorageWebhook_Decode(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	tests := []struct {
		name    string
		obj     interface{}
		gvk     metav1.GroupVersionKind
		wantErr bool
	}{
		{
			name: "valid PVC",
			obj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
			},
			gvk: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			wantErr: false,
		},
		{
			name: "valid StatefulSet",
			obj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ss",
					Namespace: "default",
				},
			},
			gvk: metav1.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.obj)
			require.NoError(t, err)

			obj, err := webhook.decode(runtime.RawExtension{Raw: raw}, tt.gvk)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, obj)
			}
		})
	}
}

func TestHelmPersistentStorageWebhook_Decode_EmptyRaw(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	obj, err := webhook.decode(runtime.RawExtension{}, metav1.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "PersistentVolumeClaim",
	})

	assert.Error(t, err)
	assert.Nil(t, obj)
	assert.Contains(t, err.Error(), "no raw data to decode")
}

func TestHelmPersistentStorageWebhook_Decode_InvalidJSON(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	obj, err := webhook.decode(
		runtime.RawExtension{Raw: []byte("invalid json")},
		metav1.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PersistentVolumeClaim",
		},
	)

	assert.Error(t, err)
	assert.Nil(t, obj)
}

func TestHelmPersistentStorageWebhook_MutatePVC(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)
	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	tests := []struct {
		name                 string
		pvc                  *corev1.PersistentVolumeClaim
		expectedStorageClass *string
	}{
		{
			name: "with annotation",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Annotations: map[string]string{
						nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "custom-storage",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{},
			},
			expectedStorageClass: strPtr("custom-storage"),
		},
		{
			name: "without annotation",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
				Spec: corev1.PersistentVolumeClaimSpec{},
			},
			expectedStorageClass: nil,
		},
		{
			name: "with empty annotation",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Annotations: map[string]string{
						nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{},
			},
			expectedStorageClass: nil,
		},
		{
			name: "replaces existing storage class",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Annotations: map[string]string{
						nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "new-storage",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: strPtr("old-storage"),
				},
			},
			expectedStorageClass: strPtr("new-storage"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhook.mutatePVC(ctx, tt.pvc)

			if tt.expectedStorageClass == nil {
				assert.Equal(t, tt.expectedStorageClass, tt.pvc.Spec.StorageClassName)
			} else {
				require.NotNil(t, tt.pvc.Spec.StorageClassName)
				assert.Equal(t, *tt.expectedStorageClass, *tt.pvc.Spec.StorageClassName)
			}
		})
	}
}

func TestHelmPersistentStorageWebhook_MutateStatefulSet(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)
	ctx := context.Background()

	tests := []struct {
		name string
		ss   *appsv1.StatefulSet
	}{
		{
			name: "single volume claim template",
			ss: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{
							ObjectMeta: metav1.ObjectMeta{Name: "data"},
							Spec:       corev1.PersistentVolumeClaimSpec{},
						},
					},
				},
			},
		},
		{
			name: "multiple volume claim templates",
			ss: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{
							ObjectMeta: metav1.ObjectMeta{Name: "data"},
							Spec:       corev1.PersistentVolumeClaimSpec{},
						},
						{
							ObjectMeta: metav1.ObjectMeta{Name: "logs"},
							Spec:       corev1.PersistentVolumeClaimSpec{},
						},
					},
				},
			},
		},
		{
			name: "no volume claim templates",
			ss: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{},
				},
			},
		},
		{
			name: "replaces existing storage class",
			ss: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{
							ObjectMeta: metav1.ObjectMeta{Name: "data"},
							Spec: corev1.PersistentVolumeClaimSpec{
								StorageClassName: strPtr("old-storage"),
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhook.mutateStatefulSet(ctx, tt.ss)

			for _, vct := range tt.ss.Spec.VolumeClaimTemplates {
				require.NotNil(t, vct.Spec.StorageClassName)
				assert.Equal(t, "default-storage", *vct.Spec.StorageClassName)
			}
		})
	}
}

func TestHelmPersistentStorageWebhook_Mutate(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)
	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	t.Run("PVC object", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pvc",
			},
		}
		ok, warnings, err := webhook.mutate(ctx, pvc)

		assert.True(t, ok)
		assert.Nil(t, warnings)
		assert.NoError(t, err)
	})

	t.Run("StatefulSet object", func(t *testing.T) {
		ss := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ss",
			},
		}
		ok, warnings, err := webhook.mutate(ctx, ss)

		assert.True(t, ok)
		assert.Nil(t, warnings)
		assert.NoError(t, err)
	})
}

func TestHelmPersistentStorageWebhook_Handle_CreatePVC_NoMutation(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	// PVC without the annotation - won't be mutated, will return allowed
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	rawPVC, err := json.Marshal(pvc)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "PersistentVolumeClaim",
			},
			Object: runtime.RawExtension{
				Raw: rawPVC,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
}

func TestHelmPersistentStorageWebhook_Handle_UpdateStatefulSet(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	ctx := context.Background()
	ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ss",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test:latest",
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		},
	}

	rawSS, err := json.Marshal(ss)
	require.NoError(t, err)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Kind: metav1.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
			Object: runtime.RawExtension{
				Raw: rawSS,
			},
		},
	}

	resp := webhook.Handle(ctx, req)
	assert.True(t, resp.Allowed)
	assert.NotNil(t, resp.Patches)
}

func TestHelmPersistentStorageWebhook_Handle_AllOperations(t *testing.T) {
	webhook := getHelmPersistentStorageWebhook("default-storage", true)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	rawPVC, err := json.Marshal(pvc)
	require.NoError(t, err)

	tests := []struct {
		name      string
		operation admissionv1.Operation
		allowed   bool
	}{
		{
			name:      "Create operation",
			operation: admissionv1.Create,
			allowed:   true,
		},
		{
			name:      "Update operation",
			operation: admissionv1.Update,
			allowed:   true,
		},
		{
			name:      "Delete operation",
			operation: admissionv1.Delete,
			allowed:   true,
		},
		{
			name:      "Connect operation",
			operation: admissionv1.Connect,
			allowed:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctx = core.WithLogger(ctx, logrus.NewEntry(logrus.New()))

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tt.operation,
					Kind: metav1.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "PersistentVolumeClaim",
					},
					Object: runtime.RawExtension{
						Raw: rawPVC,
					},
				},
			}

			resp := webhook.Handle(ctx, req)
			assert.Equal(t, tt.allowed, resp.Allowed)
		})
	}
}

func strPtr(s string) *string {
	return &s
}
