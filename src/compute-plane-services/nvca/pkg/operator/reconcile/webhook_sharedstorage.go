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

func makeHelmStorageMutatingWebhook(
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
) admissionregistrationv1.MutatingWebhook {
	st := admissionregistrationv1.NamespacedScope
	targetLabelSels := makeWorkloadNamespaceLabelSelectors(WorkloadInstanceTypeValueMiniService)
	return makeMutatingWebhook(
		"mutate-helm-storage.nvca.nvcf.nvidia.io",
		"/mutate-helm-storage",
		&metav1.LabelSelector{
			MatchExpressions: makeLabelSelectorRequirements(targetLabelSels),
		},
		[]admissionregistrationv1.RuleWithOperations{
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
		nb,
		webhookCert)
}
