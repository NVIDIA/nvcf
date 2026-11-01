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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
)

func TestPodNodeSelectorWebhook_SharedCluster(t *testing.T) {
	type spec struct {
		name     string
		pod      *v1.Pod
		expPod   *v1.Pod
		expError string
	}

	cases := []spec{
		{
			name: "no affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      sharedcluster.ScheduleLabelKey,
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{trueVal},
									}},
								}},
							},
						},
					},
				},
			},
		},
		{
			name: "existing affinity 1",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      sharedcluster.ScheduleLabelKey,
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{trueVal},
									}},
								}},
							},
						},
					},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      sharedcluster.ScheduleLabelKey,
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{trueVal},
									}},
								}},
							},
						},
					},
				},
			},
		},
		{
			name: "existing affinity 2",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      "foo",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"bar", "baz"},
									}},
								}},
							},
						},
					},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{
										{
											Key:      "foo",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"bar", "baz"},
										},
										{
											Key:      sharedcluster.ScheduleLabelKey,
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{trueVal},
										},
									},
								}},
							},
						},
					},
				},
			},
		},
		{
			name: "existing affinity 3",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{{
											Key:      "foo1",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"bar1"},
										}},
									},
									{
										MatchExpressions: []v1.NodeSelectorRequirement{{
											Key:      "foo2",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"bar2"},
										}},
									},
								},
							},
						},
					},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "foo1",
												Operator: v1.NodeSelectorOpIn,
												Values:   []string{"bar1"},
											},
											{
												Key:      sharedcluster.ScheduleLabelKey,
												Operator: v1.NodeSelectorOpIn,
												Values:   []string{trueVal},
											},
										},
									},
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "foo2",
												Operator: v1.NodeSelectorOpIn,
												Values:   []string{"bar2"},
											},
											{
												Key:      sharedcluster.ScheduleLabelKey,
												Operator: v1.NodeSelectorOpIn,
												Values:   []string{trueVal},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "affinity conflict",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      sharedcluster.ScheduleLabelKey,
										Operator: v1.NodeSelectorOpDoesNotExist,
									}},
								}},
							},
						},
					},
				},
			},
			expError: `match expression key "nvca.nvcf.nvidia.io/schedule" exists, but either operation is not "In" or values are not ["true"]`,
		},
	}

	ab := &atomic.Bool{}
	ab.Store(true)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			pod := c.pod.DeepCopy()
			err := (&podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
				SharedClusterOn: ab,
				HostIsolation:   false,
			}}).Default(ctx, pod)
			if c.expError != "" {
				assert.EqualError(t, err, c.expError)
			} else {
				assert.Equal(t, c.expPod, pod)
			}
		})
	}

	var err error

	err = (&podAffinityWebhook{}).Default(context.Background(), nil)
	assert.EqualError(t, err, "expected *v1.Pod, got: <nil>")

	err = (&podAffinityWebhook{}).Default(context.Background(), &v1.Secret{})
	assert.EqualError(t, err, "expected *v1.Pod, got: *v1.Secret")
}

func TestPodNodeSelectorWebhook_UniformInstanceTypeLabels(t *testing.T) {
	type spec struct {
		name       string
		podSpec    v1.PodSpec
		expPodSpec v1.PodSpec
		expError   string
	}

	cases := []spec{
		{
			name: "no instance type affinity or selector",
			podSpec: v1.PodSpec{
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
									Values:   []string{"bar", "baz"},
								}},
							}},
						},
					},
				},
			},
			expPodSpec: v1.PodSpec{
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
									Values:   []string{"bar", "baz"},
								}},
							}},
						},
					},
				},
			},
		},
		{
			name: "existing uniform selector",
			podSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					instanceTypeLK: "foo-gpu",
				},
			},
			expPodSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					instanceTypeLK: "foo-gpu",
				},
			},
		},
		{
			name: "existing deprecated selector",
			podSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					nodefeatures.DeprecatedInstanceTypeLabelKey: "foo-gpu",
				},
			},
			expPodSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					instanceTypeLK: "foo-gpu",
				},
			},
		},
		{
			name: "existing uniform selector no instance type affinity",
			podSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					instanceTypeLK: "foo-gpu",
				},
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{
								{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      "foo",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"bar", "baz"},
									}},
								},
							},
						},
					},
				},
			},
			expPodSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					instanceTypeLK: "foo-gpu",
				},
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{
								{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      "foo",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"bar", "baz"},
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "existing uniform affinity",
			podSpec: v1.PodSpec{
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      instanceTypeLK,
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"foo-gpu"},
								}},
							}},
						},
					},
				},
			},
			expPodSpec: v1.PodSpec{
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      instanceTypeLK,
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"foo-gpu"},
								}},
							}},
						},
					},
				},
			},
		},
		{
			name: "existing deprecated affinity",
			podSpec: v1.PodSpec{
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      nodefeatures.DeprecatedInstanceTypeLabelKey,
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"foo-gpu"},
								}},
							}},
						},
					},
				},
			},
			expPodSpec: v1.PodSpec{
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      instanceTypeLK,
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"foo-gpu"},
								}},
							}},
						},
					},
				},
			},
		},
		{
			name: "mutlitple affinities and selectors",
			podSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					"foo": "bar",
					nodefeatures.DeprecatedInstanceTypeLabelKey: "foo-gpu",
				},
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{
								{
									MatchExpressions: []v1.NodeSelectorRequirement{{
										Key:      "foo",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"bar", "baz"},
									}},
								},
								{
									MatchExpressions: []v1.NodeSelectorRequirement{
										{
											Key:      instanceTypeLK,
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"foo-gpu"},
										},
										{
											Key:      "baz",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"buf"},
										},
									},
								},
							},
						},
					},
				},
			},
			expPodSpec: v1.PodSpec{
				NodeSelector: map[string]string{
					"foo":          "bar",
					instanceTypeLK: "foo-gpu",
				},
				Affinity: &v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{
								{
									MatchExpressions: []v1.NodeSelectorRequirement{
										{
											Key:      "foo",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"bar", "baz"},
										},
										{
											Key:      instanceTypeLK,
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"foo-gpu"},
										},
									},
								},
								{
									MatchExpressions: []v1.NodeSelectorRequirement{
										{
											Key:      instanceTypeLK,
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"foo-gpu"},
										},
										{
											Key:      "baz",
											Operator: v1.NodeSelectorOpIn,
											Values:   []string{"buf"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			gotPod := (&v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"},
				Spec:       c.podSpec,
			}).DeepCopy()
			err := (&podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
				UniformInstanceLabels: true,
				HostIsolation:         false,
			}}).Default(ctx, gotPod)
			if c.expError != "" {
				assert.EqualError(t, err, c.expError)
			} else {
				assert.Equal(t, c.expPodSpec, gotPod.Spec)
			}
		})
	}

	var err error

	err = (&podAffinityWebhook{}).Default(context.Background(), nil)
	assert.EqualError(t, err, "expected *v1.Pod, got: <nil>")

	err = (&podAffinityWebhook{}).Default(context.Background(), &v1.Secret{})
	assert.EqualError(t, err, "expected *v1.Pod, got: *v1.Secret")
}
