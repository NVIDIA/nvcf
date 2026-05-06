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
	"errors"
	"net/http"
	"sort"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestHelmMiniServiceValWebhookHandler_Handle(t *testing.T) {
	fff := &featureflagmock.Fetcher{}
	handler := newHelmMiniServiceValWebhookHandler(fff)

	tests := []struct {
		name       string
		req        admission.Request
		setup      func()
		wantStatus int32
		wantErr    bool
	}{
		{
			name: "valid create pod request",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Kind:      metav1.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
					Object: runtime.RawExtension{
						Raw: encodeObject(t, &corev1.Pod{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: "sr-test",
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "test-container",
										Resources: corev1.ResourceRequirements{
											Limits: corev1.ResourceList{
												corev1.ResourceCPU:    resource.MustParse("1"),
												corev1.ResourceMemory: resource.MustParse("1Gi"),
											},
										},
									},
								},
							},
						}),
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
			wantStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "invalid create pod request missing limits",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Kind:      metav1.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
					Object: runtime.RawExtension{
						Raw: encodeObject(t, &corev1.Pod{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: "sr-test",
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "test-container",
									},
								},
							},
						}),
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
			wantStatus: http.StatusForbidden,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}

			resp := handler.Handle(context.Background(), tt.req)
			if tt.wantErr {
				assert.NotEqual(t, tt.wantStatus, resp.Result.Code)
			} else {
				assert.Equal(t, tt.wantStatus, resp.Result.Code)
			}
		})
	}
}

func TestHelmMiniServiceValWebhookHandler_Validate(t *testing.T) {
	fff := &featureflagmock.Fetcher{}

	tests := []struct {
		name    string
		obj     client.Object
		setup   func()
		wantErr string
	}{
		{
			name: "valid pod with resource limits kata",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("1"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
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
			name: "valid pod with resource limits Helm resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("1"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
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
			name: "valid pod with resource limits container resource limit enforcement enabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "nvcf-backend",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: common.UtilsContainerName,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("1"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
						{
							Name: "test-container",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("1"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
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
			name: "invalid pod missing cpu limits",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
			wantErr: `container test-container missing resource limits: ["cpu"]`,
		},
		{
			name: "invalid pod missing resource limits",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
						},
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
			wantErr: "container test-container has no resource limits",
		},
		{
			name: "kata runtime isolation disabled",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
						},
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{}
			},
		},
		{
			name: "namespace does not start with sr-",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test-container",
						},
					},
				},
			},
			setup: func() {
				fff = &featureflagmock.Fetcher{
					EnabledAttrs: []*featureflag.Attribute{
						featureflag.AttrKataRuntimeIsolation,
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}
			handler := newHelmMiniServiceValWebhookHandler(fff)
			_, err := handler.(*helmMiniServiceValWebhookHandler).validate(context.Background(), tt.obj)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidatePodSpecLimits(t *testing.T) {
	tests := []struct {
		name      string
		ps        corev1.PodSpec
		wantErrs  []string
		wantWarns []string
	}{
		{
			name: "valid pod spec with resource limits",
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("1"),
								corev1.ResourceMemory:           resource.MustParse("1Gi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			wantErrs:  nil,
			wantWarns: nil,
		},
		{
			name: "missing resource limits",
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container",
					},
				},
			},
			wantErrs:  []string{"container test-container has no resource limits"},
			wantWarns: nil,
		},
		{
			name: "missing CPU limit",
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			wantErrs: []string{
				`container test-container missing resource limits: ["cpu"]`,
			},
			wantWarns: nil,
		},
		{
			name: "missing memory limit",
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
			wantErrs: []string{
				`container test-container missing resource limits: ["memory"]`,
			},
			wantWarns: nil,
		},
		{
			name: "multiple containers with mixed resource limits",
			ps: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "container-1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("1"),
								corev1.ResourceMemory:           resource.MustParse("1Gi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
							},
						},
					},
					{
						Name: "container-2",
					},
					{
						Name: "container-3",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
					{
						Name: "container-4",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
			wantErrs: []string{
				"container container-2 has no resource limits",
				`container container-3 missing resource limits: ["cpu"]`,
				`container container-4 missing resource limits: ["memory"]`,
			},
			wantWarns: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, errs := validatePodSpecLimits(context.Background(), tt.ps)
			sort.Strings(warnings)
			sort.Slice(errs, func(i, j int) bool {
				return errs[i].Error() < errs[j].Error()
			})
			sort.Strings(tt.wantWarns)
			sort.Strings(tt.wantErrs)
			var wantErrs []error
			for _, err := range tt.wantErrs {
				wantErrs = append(wantErrs, errors.New(err))
			}
			assert.Equal(t, admission.Warnings(tt.wantWarns), warnings)
			assert.Equal(t, wantErrs, errs)
		})
	}
}
func encodeObject(t *testing.T, obj runtime.Object) []byte {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	codec := serializer.NewCodecFactory(scheme).LegacyCodec(corev1.SchemeGroupVersion)
	data, err := runtime.Encode(codec, obj)
	require.NoError(t, err)
	return data
}

// Additional coverage tests for helm_mini_service_validate_webhook.go
func TestValidateResourceLimitsVariousObjects(t *testing.T) {
	namespace := "sr-test"
	// Feature flags to make shouldEnforceResourceLimits return true.
	fff := &featureflagmock.Fetcher{
		EnabledAttrs: []*featureflag.Attribute{featureflag.AttrKataRuntimeIsolation},
	}
	handler := newHelmMiniServiceValWebhookHandler(fff).(*helmMiniServiceValWebhookHandler)

	makeContainer := func() corev1.Container {
		return corev1.Container{
			Name: "c",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		}
	}

	// Objects which SHOULD pass validation (no errors expected).
	goodObjs := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{makeContainer()}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{makeContainer()}}}},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{makeContainer()}}}},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{makeContainer()}}}},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{makeContainer()}}}},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec:       batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{makeContainer()}}}}}},
		},
	}

	for _, obj := range goodObjs {
		_, errs := handler.validateResourceLimits(context.Background(), obj)
		assert.Empty(t, errs, "expected no errors for %T", obj)
	}
}

func TestValidateContainerLimits_DisallowedResource(t *testing.T) {
	badResName := corev1.ResourceName("example.com/foo")
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "bad",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
							badResName:            resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	warns, errs := validatePodSpecLimits(context.Background(), pod.Spec)
	assert.Empty(t, warns)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "non-zero disallowed resources")
}
