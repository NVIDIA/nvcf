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
	"k8s.io/utils/ptr"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func (bc *BackendK8sCache) makeMiniServiceMutatingWebhooks(
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
) []admissionregistrationv1.MutatingWebhook {
	const whPath = "/mutate-miniservice"
	rulePodsNamespaced := admissionregistrationv1.Rule{
		APIGroups:   []string{""},
		APIVersions: []string{"v1"},
		Resources:   []string{"pods"},
		Scope:       ptr.To(admissionregistrationv1.NamespacedScope),
	}
	// Match conditions to exclude certain infra pods from the webhook.
	matchConds := []admissionregistrationv1.MatchCondition{
		{
			Name:       "exclude-smb-server-pod",
			Expression: `request.kind.kind == 'Pod' && (!has(request.name) || request.name != '` + storage.SMBServerPodName + `')`,
		},
		{
			Name: "exclude-cred-init-job-pods",
			Expression: `request.kind.kind == 'Pod' && (!has(object.metadata.ownerReferences) || ` +
				`!object.metadata.ownerReferences.exists(o, o.kind == 'Job' && ` +
				`o.name == request.namespace + '-cred-init'))`,
		},
	}

	whCreate := admissionregistrationv1.MutatingWebhook{
		Name:                    "miniservice-mutate-create.nvca.nvcf.nvidia.io",
		AdmissionReviewVersions: []string{"v1"},
		FailurePolicy:           ptr.To(admissionregistrationv1.Fail),
		SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
		MatchPolicy:             ptr.To(admissionregistrationv1.Exact),
		ClientConfig:            makeWebhookClientConfig(nb, webhookCert, whPath),
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValueMiniService,
			},
		},
		Rules: []admissionregistrationv1.RuleWithOperations{
			{
				Rule: rulePodsNamespaced,
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Create,
				},
			},
		},
		MatchConditions: matchConds,
	}

	whUpdate := admissionregistrationv1.MutatingWebhook{
		Name:                    "miniservice-mutate-update.nvca.nvcf.nvidia.io",
		AdmissionReviewVersions: []string{"v1"},
		FailurePolicy:           ptr.To(admissionregistrationv1.Ignore),
		SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
		MatchPolicy:             ptr.To(admissionregistrationv1.Exact),
		ClientConfig:            makeWebhookClientConfig(nb, webhookCert, whPath),
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValueMiniService,
			},
		},
		Rules: []admissionregistrationv1.RuleWithOperations{
			{
				Rule: rulePodsNamespaced,
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Update,
				},
			},
		},
		MatchConditions: matchConds,
	}

	return []admissionregistrationv1.MutatingWebhook{
		whCreate,
		whUpdate,
	}
}
