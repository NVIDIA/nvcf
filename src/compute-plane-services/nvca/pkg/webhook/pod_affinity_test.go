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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestAccountIsolationPodAntiAffinity(t *testing.T) {
	type spec struct {
		name     string
		pod      *v1.Pod
		expPod   *v1.Pod
		expError string
	}

	ncaId1 := "nca-123"
	ncaId2 := "nca-456"

	cases := []spec{
		{
			name: "no nca_id label",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
			},
			expError: "pod default/pod-name has no tenant label cannot set pod tolerations",
		},
		{
			name: "empty nca_id value",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ""},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ""},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{""},
											},
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpExists,
											},
										},
									},
									TopologyKey: hostnameLabelKey,
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.NCAIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    "",
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "pod with existing affinity but no anti-affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "gpu",
												Operator: v1.NodeSelectorOpExists,
											},
										},
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
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "gpu",
												Operator: v1.NodeSelectorOpExists,
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{ncaId1},
											},
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpExists,
											},
										},
									},
									TopologyKey: hostnameLabelKey,
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.NCAIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    ncaId1,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "pod with existing pod affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAffinity: &v1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "service",
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"web"},
											},
										},
									},
									TopologyKey: "kubernetes.io/hostname",
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
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAffinity: &v1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "service",
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"web"},
											},
										},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{ncaId1},
											},
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpExists,
											},
										},
									},
									TopologyKey: hostnameLabelKey,
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.NCAIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    ncaId1,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "add anti-affinity for different nca_ids",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{ncaId1},
											},
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpExists,
											},
										},
									},
									TopologyKey: hostnameLabelKey,
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.NCAIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    ncaId1,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "existing anti-affinity preserved",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId2},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "foo",
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
									TopologyKey: "zone",
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
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId2},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "foo",
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
									TopologyKey: "zone",
								},
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{ncaId2},
											},
											{
												Key:      nvcatypes.NCAIDKey,
												Operator: metav1.LabelSelectorOpExists,
											},
										},
									},
									TopologyKey: hostnameLabelKey,
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.NCAIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    ncaId2,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "feature disabled",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId1},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			pod := c.pod.DeepCopy()
			err := (&podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
				AccountIsolation: c.name != "feature disabled",
			}}).Default(ctx, pod)
			if err != nil {
				assert.EqualError(t, err, c.expError)
			} else {
				assert.Equal(t, c.expPod, pod)
			}
		})
	}
}

func TestHostIsolationPodTolerations(t *testing.T) {
	type spec struct {
		name     string
		pod      *v1.Pod
		expPod   *v1.Pod
		expError string
	}

	funcVerID := "fvid"
	taskID := "taskid"
	ncaId := "nca-123"

	cases := []spec{
		{
			name: "host isolation with nca_id label - error",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.NCAIDKey: ncaId},
				},
			},
			expError: "pod default/pod-name has no tenant label or conflicting labels, cannot set pod tolerations",
		},
		{
			name: "function isolation - preferred anti-affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{funcVerID},
												},
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.FunctionVersionIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    funcVerID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "task isolation - preferred anti-affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.TaskIDKey: taskID},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.TaskIDKey: taskID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.TaskIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{taskID},
												},
												{
													Key:      nvcatypes.TaskIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.TaskIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    taskID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "no tenant label",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
				},
			},
			expError: "pod default/pod-name has no tenant label or conflicting labels, cannot set pod tolerations",
		},
		{
			name: "conflicting tenant labels",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels: map[string]string{
						nvcatypes.FunctionVersionIDKey: funcVerID,
						nvcatypes.TaskIDKey:            taskID,
					},
				},
			},
			expError: "pod default/pod-name has no tenant label or conflicting labels, cannot set pod tolerations",
		},
		{
			name: "function isolation - preferred anti-affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{funcVerID},
												},
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.FunctionVersionIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    funcVerID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "task isolation - preferred anti-affinity",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.TaskIDKey: taskID},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.TaskIDKey: taskID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.TaskIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{taskID},
												},
												{
													Key:      nvcatypes.TaskIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.TaskIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    taskID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "existing tolerations - function isolation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.FunctionVersionIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    funcVerID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{funcVerID},
												},
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      nvcatypes.FunctionVersionIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    funcVerID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
		{
			name: "existing tolerations - function isolation with additional toleration",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Tolerations: []v1.Toleration{
						{
							Key:      "foo",
							Operator: v1.TolerationOpEqual,
							Value:    "bar",
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
			expPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-name",
					Namespace: "default",
					Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
				},
				Spec: v1.PodSpec{
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: v1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpNotIn,
													Values:   []string{funcVerID},
												},
												{
													Key:      nvcatypes.FunctionVersionIDKey,
													Operator: metav1.LabelSelectorOpExists,
												},
											},
										},
										TopologyKey: hostnameLabelKey,
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      "foo",
							Operator: v1.TolerationOpEqual,
							Value:    "bar",
							Effect:   v1.TaintEffectNoExecute,
						},
						{
							Key:      nvcatypes.FunctionVersionIDKey,
							Operator: v1.TolerationOpEqual,
							Value:    funcVerID,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			pod := c.pod.DeepCopy()
			err := (&podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
				HostIsolation: true,
			}}).Default(ctx, pod)
			if err != nil {
				assert.EqualError(t, err, c.expError)
			} else {
				assert.Equal(t, c.expPod, pod)
			}
		})
	}

	t.Run("off", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-name",
				Namespace: "default",
				Labels:    map[string]string{nvcatypes.FunctionVersionIDKey: funcVerID},
			},
		}
		expPod := pod.DeepCopy()

		err := (&podAffinityWebhook{PodAffinityOptions: PodAffinityOptions{
			HostIsolation: false,
		}}).Default(context.Background(), pod)
		require.NoError(t, err)
		assert.Equal(t, expPod, pod)
	})
}
