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

package nvcfdra

import (
	"testing"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Test_containerRequestsStaticGPU(t *testing.T) {
	tests := []struct {
		name string
		c    corev1.Container
		want bool
	}{
		{
			name: "empty container",
			c:    corev1.Container{},
			want: false,
		},
		{
			name: "cpu only in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				},
			},
			want: false,
		},
		{
			name: "nvidia.com/gpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/gpu in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/pgpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/pgpu": resource.MustParse("2")},
				},
			},
			want: true,
		},
		{
			name: "nvidia.com/gpu.shared in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu.shared": resource.MustParse("4")},
				},
			},
			want: true,
		},
		{
			name: "mig resource in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/mig-1g.5gb": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "mig resource in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"nvidia.com/mig-3g.20gb": resource.MustParse("2")},
				},
			},
			want: true,
		},
		{
			name: "zero nvidia.com/gpu in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("0")},
				},
			},
			want: false,
		},
		{
			name: "zero mig resource in limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/mig-1g.5gb": resource.MustParse("0")},
				},
			},
			want: false,
		},
		{
			name: "gpu in both limits and requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "zero gpu in limits, nonzero in requests",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("0")},
					Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				},
			},
			want: true,
		},
		{
			name: "unrelated nvidia resource",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/rdma": resource.MustParse("1")},
				},
			},
			want: false,
		},
		{
			name: "gpu mixed with cpu",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:                           resource.MustParse("4"),
						corev1.ResourceName("nvidia.com/gpu"):        resource.MustParse("2"),
						corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("0"),
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containerRequestsStaticGPU(tt.c)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTransformNVLinkOptimizedDRAObjects(t *testing.T) {
	type spec struct {
		name       string
		objs       []client.Object
		keyToHash  string
		expObjs    []client.Object
		expDRAObjs []client.Object
		expError   string
	}

	nvlinkDomainPartitionKeyFoo := "x2c26b46b68ffc68ff9x"
	nvlinkDomainPartitionKeyFooIdx0 := "xbb4eca334f61af3b67x"
	newDefaultPrefAffinity := func() *corev1.Affinity {
		return &corev1.Affinity{
			PodAffinity: &corev1.PodAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      NVLinkDomainPartitionLabel,
									Operator: metav1.LabelSelectorOpExists,
								},
								{
									Key:      NVLinkDomainPartitionLabel,
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{nvlinkDomainPartitionKeyFoo},
								},
							},
						},
						TopologyKey: GPUCliqueNodeLabel,
					},
				}},
			},
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      GPUCliqueNodeLabel,
							Operator: corev1.NodeSelectorOpExists,
						}},
					}},
				},
			},
		}
	}
	defaultComputeDomain := &nvresourcev1beta1.ComputeDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultComputeDomainName,
		},
		Spec: nvresourcev1beta1.ComputeDomainSpec{
			Channel: &nvresourcev1beta1.ComputeDomainChannelSpec{
				ResourceClaimTemplate: nvresourcev1beta1.ComputeDomainResourceClaimTemplate{
					Name: defaultComputeDomainChannelName,
				},
			},
		},
	}
	computeDomainChannelName := defaultComputeDomainChannelName
	staticGPUResourceKey := gpuResourceKeys[0]
	defaultGPULimit := resource.MustParse("2")

	newDefaultPodSpec := func() corev1.PodSpec {
		return corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "foo",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
				},
			}},
			InitContainers: []corev1.Container{{
				Name: "foo-init",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
				},
			}},
		}
	}
	newDefaultExpPodSpec := func() corev1.PodSpec {
		return corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "foo",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
					Claims: []corev1.ResourceClaim{{
						Name: defaultComputeDomainName,
					}},
				},
			}},
			InitContainers: []corev1.Container{{
				Name: "foo-init",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
					Claims: []corev1.ResourceClaim{{
						Name: defaultComputeDomainName,
					}},
				},
			}},
			ResourceClaims: []corev1.PodResourceClaim{{
				Name:                      defaultComputeDomainName,
				ResourceClaimTemplateName: &computeDomainChannelName,
			}},
			Affinity: newDefaultPrefAffinity(),
		}
	}
	newDefaultExpBinpackObjectMetadata := func() metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Labels: map[string]string{NVLinkDomainPartitionLabel: nvlinkDomainPartitionKeyFoo},
		}
	}

	for _, tt := range []spec{
		{
			name:     "no key",
			expError: "key to partition NVLink domains is empty",
		},
		{
			name: "single pod",
			objs: []client.Object{
				&corev1.Pod{
					Spec: newDefaultPodSpec(),
				},
			},
			keyToHash: "foo",
			expObjs: []client.Object{
				&corev1.Pod{
					ObjectMeta: newDefaultExpBinpackObjectMetadata(),
					Spec:       newDefaultExpPodSpec(),
				},
			},
			expDRAObjs: []client.Object{
				defaultComputeDomain,
			},
		},
		{
			name: "cpu pod",
			objs: []client.Object{
				&corev1.Pod{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
							},
						}},
					},
				},
			},
			keyToHash: "foo",
			expObjs: []client.Object{
				&corev1.Pod{
					ObjectMeta: newDefaultExpBinpackObjectMetadata(),
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
							},
						}},
						Affinity: newDefaultPrefAffinity(),
					},
				},
			},
			expDRAObjs: []client.Object{
				defaultComputeDomain,
			},
		},
		{
			name: "zero gpu pod",
			objs: []client.Object{
				&corev1.Pod{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{staticGPUResourceKey: resource.MustParse("0")},
							},
						}},
					},
				},
			},
			keyToHash: "foo",
			expObjs: []client.Object{
				&corev1.Pod{
					ObjectMeta: newDefaultExpBinpackObjectMetadata(),
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{staticGPUResourceKey: resource.MustParse("0")},
							},
						}},
						Affinity: newDefaultPrefAffinity(),
					},
				},
			},
			expDRAObjs: []client.Object{
				defaultComputeDomain,
			},
		},
		{
			name:      "all types",
			keyToHash: "foo",
			objs: []client.Object{
				&corev1.Pod{
					Spec: newDefaultPodSpec(),
				},
				&appsv1.Deployment{
					Spec: appsv1.DeploymentSpec{
						Template: corev1.PodTemplateSpec{
							Spec: newDefaultPodSpec(),
						},
					},
				},
				&appsv1.ReplicaSet{
					Spec: appsv1.ReplicaSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: newDefaultPodSpec(),
						},
					},
				},
				&appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: newDefaultPodSpec(),
						},
					},
				},
				&batchv1.Job{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: newDefaultPodSpec(),
						},
					},
				},
				&batchv1.CronJob{
					Spec: batchv1.CronJobSpec{
						JobTemplate: batchv1.JobTemplateSpec{
							Spec: batchv1.JobSpec{
								Template: corev1.PodTemplateSpec{
									Spec: newDefaultPodSpec(),
								},
							},
						},
					},
				},
			},
			expObjs: []client.Object{
				&corev1.Pod{
					ObjectMeta: newDefaultExpBinpackObjectMetadata(),
					Spec:       newDefaultExpPodSpec(),
				},
				&appsv1.Deployment{
					Spec: appsv1.DeploymentSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: newDefaultExpBinpackObjectMetadata(),
							Spec:       newDefaultExpPodSpec(),
						},
					},
				},
				&appsv1.ReplicaSet{
					Spec: appsv1.ReplicaSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: newDefaultExpBinpackObjectMetadata(),
							Spec:       newDefaultExpPodSpec(),
						},
					},
				},
				&appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: newDefaultExpBinpackObjectMetadata(),
							Spec:       newDefaultExpPodSpec(),
						},
					},
				},
				&batchv1.Job{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: newDefaultExpBinpackObjectMetadata(),
							Spec:       newDefaultExpPodSpec(),
						},
					},
				},
				&batchv1.CronJob{
					Spec: batchv1.CronJobSpec{
						JobTemplate: batchv1.JobTemplateSpec{
							Spec: batchv1.JobSpec{
								Template: corev1.PodTemplateSpec{
									ObjectMeta: newDefaultExpBinpackObjectMetadata(),
									Spec:       newDefaultExpPodSpec(),
								},
							},
						},
					},
				},
			},
			expDRAObjs: []client.Object{
				defaultComputeDomain,
			},
		},
		{
			name: "single pod with req",
			objs: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{RequiredNVLinkDomainIndexAnnotation: "0"},
					},
					Spec: newDefaultPodSpec(),
				},
			},
			keyToHash: "foo",
			expObjs: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      map[string]string{NVLinkDomainPartitionLabel: nvlinkDomainPartitionKeyFooIdx0},
						Annotations: map[string]string{RequiredNVLinkDomainIndexAnnotation: "0"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
								Claims: []corev1.ResourceClaim{{
									Name: defaultComputeDomainName,
								}},
							},
						}},
						InitContainers: []corev1.Container{{
							Name: "foo-init",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{staticGPUResourceKey: defaultGPULimit},
								Claims: []corev1.ResourceClaim{{
									Name: defaultComputeDomainName,
								}},
							},
						}},
						ResourceClaims: []corev1.PodResourceClaim{{
							Name:                      defaultComputeDomainName,
							ResourceClaimTemplateName: &computeDomainChannelName,
						}},
						Affinity: &corev1.Affinity{
							PodAffinity: &corev1.PodAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      NVLinkDomainPartitionLabel,
												Operator: metav1.LabelSelectorOpExists,
											},
											{
												Key:      NVLinkDomainPartitionLabel,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{nvlinkDomainPartitionKeyFooIdx0},
											},
										},
									},
									TopologyKey: GPUCliqueNodeLabel,
								}},
							},
							NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{{
										MatchExpressions: []corev1.NodeSelectorRequirement{{
											Key:      GPUCliqueNodeLabel,
											Operator: corev1.NodeSelectorOpExists,
										}},
									}},
								},
							},
						},
					},
				},
			},
			expDRAObjs: []client.Object{
				defaultComputeDomain,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotObjs, gotDRAObjs, err := TransformNVLinkOptimizedDRAObjects(tt.objs, tt.keyToHash)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else if assert.NoError(t, err) && assert.Len(t, gotObjs, len(tt.expObjs)) && assert.Len(t, gotDRAObjs, len(tt.expDRAObjs)) {
				for i := range tt.expObjs {
					assert.Equal(t, tt.expObjs[i], gotObjs[i])
				}
				for i := range tt.expDRAObjs {
					assert.Equal(t, tt.expDRAObjs[i], gotDRAObjs[i])
				}
			}
		})
	}
}
