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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestInstanceTypeNodeAffinityValidateWebhook(t *testing.T) {
	type spec struct {
		name    string
		obj     runtime.Object
		req     admissionv1.AdmissionRequest
		expCRes admission.Response
		expURes admission.Response
	}

	nodeAff := &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{{
					MatchExpressions: []v1.NodeSelectorRequirement{{
						Key:      instanceTypeLK,
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"bar"},
					}},
				}},
			},
		},
	}

	cases := []spec{
		{
			name: "skipped",
			obj:  &v1.Service{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(v1.SchemeGroupVersion.WithKind("Service")),
			},
			expCRes: admission.Allowed("gvk \"/v1, Kind=Service\" not handled by webhook"),
		},
		{
			name: "empty pod",
			obj:  &v1.Pod{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(v1.SchemeGroupVersion.WithKind("Pod")),
			},
			expCRes: admission.Denied("no valid node selector for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "valid pod node selector",
			obj: &v1.Pod{
				Spec: v1.PodSpec{
					NodeSelector: map[string]string{
						instanceTypeLK: "foo",
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(v1.SchemeGroupVersion.WithKind("Pod")),
			},
			expCRes: admission.Allowed(""),
		},
		{
			name: "valid pod node affinity",
			obj: &v1.Pod{
				Spec: v1.PodSpec{
					Affinity: nodeAff,
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(v1.SchemeGroupVersion.WithKind("Pod")),
			},
			expCRes: admission.Allowed(""),
		},
		{
			name: "invalid pod node selector and affinity",
			obj: &v1.Pod{
				Spec: v1.PodSpec{
					NodeSelector: map[string]string{
						"foo": "bar",
					},
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      "foo",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"bar"},
									}},
								}},
							},
						},
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(v1.SchemeGroupVersion.WithKind("Pod")),
			},
			expCRes: admission.Denied("no valid node selector or affinity for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "empty deployment",
			obj:  &appsv1.Deployment{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment")),
			},
			expCRes: admission.Denied("no valid node selector for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "valid deployment node affinity",
			obj: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Affinity: nodeAff,
						},
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment")),
			},
			expCRes: admission.Allowed(""),
		},
		{
			name: "empty statefulset",
			obj:  &appsv1.StatefulSet{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
			},
			expCRes: admission.Denied("no valid node selector for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "valid statefulset node affinity",
			obj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Affinity: nodeAff,
						},
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
			},
			expCRes: admission.Allowed(""),
		},
		{
			name: "empty job",
			obj:  &batchv1.Job{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(batchv1.SchemeGroupVersion.WithKind("Job")),
			},
			expCRes: admission.Denied("no valid node selector for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "valid job node affinity",
			obj: &batchv1.Job{
				Spec: batchv1.JobSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Affinity: nodeAff,
						},
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(batchv1.SchemeGroupVersion.WithKind("Job")),
			},
			expCRes: admission.Allowed(""),
		},
		{
			name: "empty cronjob",
			obj:  &batchv1.Job{},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(batchv1.SchemeGroupVersion.WithKind("Job")),
			},
			expCRes: admission.Denied("no valid node selector for nvca.nvcf.nvidia.io/instance-type"),
		},
		{
			name: "valid cronjob node affinity",
			obj: &batchv1.CronJob{
				Spec: batchv1.CronJobSpec{
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									Affinity: nodeAff,
								},
							},
						},
					},
				},
			},
			req: admissionv1.AdmissionRequest{
				Kind: metav1.GroupVersionKind(batchv1.SchemeGroupVersion.WithKind("CronJob")),
			},
			expCRes: admission.Allowed(""),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()

			raw, err := json.Marshal(c.obj)
			require.NoError(t, err)
			req := c.req
			req.Object = runtime.RawExtension{
				Raw: raw,
			}

			wh := newInstanceTypeNodeAffinityValWebhookHandler()

			// Create request.
			creq := admission.Request{AdmissionRequest: req}
			creq.Operation = admissionv1.Create

			gotCRes := wh.Handle(ctx, creq)
			assert.Equal(t, c.expCRes, gotCRes)

			// Update request with the same data.
			ureq := admission.Request{AdmissionRequest: req}
			ureq.Operation = admissionv1.Update
			ureq.OldObject = req.Object

			gotURes := wh.Handle(ctx, ureq)
			expURes := c.expURes
			if expURes.Result == nil {
				expURes = c.expCRes
			}
			assert.Equal(t, expURes, gotURes)
		})
	}
}

func TestValidationResponseFromStatus(t *testing.T) {
	assert.Equal(t, validationResponseFromStatus(metav1.Status{
		Status: string(metav1.StatusReasonForbidden),
	}).Result.Status, string(metav1.StatusReasonForbidden))
}
