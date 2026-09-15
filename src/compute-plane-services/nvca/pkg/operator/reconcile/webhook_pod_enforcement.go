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

package operator

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	// This label is set by NVCA on worker Pods and mini service Namespaces
	// to match these webhooks.
	needsEnforceLabel = "nvca.nvcf.nvidia.io/needs-enforce"
)

func (bc *BackendK8sCache) makePodEnforcementMutatingWebhooks(
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
) []admissionregistrationv1.MutatingWebhook {
	newBaseWebhook := func(shortName string) admissionregistrationv1.MutatingWebhook {
		sec := admissionregistrationv1.SideEffectClassNone
		fpt := admissionregistrationv1.Fail
		mp := admissionregistrationv1.Equivalent
		st := admissionregistrationv1.NamespacedScope
		whPath := "/mutate-pod-enforcement"

		return admissionregistrationv1.MutatingWebhook{
			Name:                    shortName + ".nvca.nvcf.nvidia.io",
			AdmissionReviewVersions: []string{"v1"},
			FailurePolicy:           &fpt,
			SideEffects:             &sec,
			MatchPolicy:             &mp,
			ClientConfig:            makeWebhookClientConfig(nb, webhookCert, whPath),
			Rules: []admissionregistrationv1.RuleWithOperations{
				{
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{""},
						APIVersions: []string{"v1"},
						Resources:   []string{"pods"},
						Scope:       &st,
					},
					Operations: []admissionregistrationv1.OperationType{
						admissionregistrationv1.Create,
						admissionregistrationv1.Update,
					},
				},
			},
		}
	}

	mwMiniService := newBaseWebhook("mutate-miniservice-pod-enforcement")
	targetLabelSels := makeWorkloadNamespaceLabelSelectors(WorkloadInstanceTypeValueMiniService)
	targetLabelSels[needsEnforceLabel] = []string{"true"}
	mwMiniService.NamespaceSelector = &metav1.LabelSelector{
		MatchExpressions: makeLabelSelectorRequirements(targetLabelSels),
	}

	mwPod := newBaseWebhook("mutate-pod-enforcement")
	podSpecTargetLabels := makeWorkloadNamespaceLabelSelectors(WorkloadInstanceTypeValuePodSpec)
	mwPod.NamespaceSelector = &metav1.LabelSelector{
		MatchExpressions: makeLabelSelectorRequirements(podSpecTargetLabels),
	}
	mwPod.ObjectSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      needsEnforceLabel,
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"true"},
			},
		},
	}

	return []admissionregistrationv1.MutatingWebhook{
		mwMiniService,
		mwPod,
	}
}
