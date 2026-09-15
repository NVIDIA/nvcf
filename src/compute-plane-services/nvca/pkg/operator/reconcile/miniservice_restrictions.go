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
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func (bc *BackendK8sCache) setupNVCAMiniServiceInfra(ctx context.Context, nb *nvidiaiov1.NVCFBackend, webhookCert WebhookCert) error {
	log := core.GetLogger(ctx)

	log.Info("setting-up mini service infra")

	err := bc.setupMiniServiceRBACConfigmap(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %v", MiniServiceRBACConfigmapName,
			nb.Namespace, nb.Name, err.Error())
	}

	err = bc.setupMiniServiceValidatingWebhook(ctx, nb, webhookCert)
	if err != nil {
		return fmt.Errorf("failed setupMiniServiceValidatingWebhook, err:%v", err)
	}
	return nil
}

func (bc *BackendK8sCache) setupMiniServiceRBACConfigmap(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	rbacData, err := bc.getMiniServiceRBACCmData(ctx, nb)
	if err != nil {
		return err
	}

	ec := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        MiniServiceRBACConfigmapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: rbacData,
	}

	return bc.createOrUpdateConfigMap(ctx, ec)
}

func (bc *BackendK8sCache) setupMiniServiceValidatingWebhook(ctx context.Context, nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert) error {
	sec := admissionregistrationv1.SideEffectClassNone
	fpt := admissionregistrationv1.Fail
	mp := admissionregistrationv1.Equivalent
	st := admissionregistrationv1.NamespacedScope
	vpath := "/validate"
	sport := getWebHooksSvcPort(nb)

	targetLabelSels := makeWorkloadNamespaceLabelSelectors(WorkloadInstanceTypeValueMiniService)
	nsLabelSelReqs := makeLabelSelectorRequirements(targetLabelSels)

	vw := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nvcaoptypes.NVCAModuleName,
			Labels: getAppLabels(),
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{
				Name:                    "validate-helm-charts.nvca.nvcf.nvidia.io",
				AdmissionReviewVersions: []string{"v1"},
				FailurePolicy:           &fpt,
				SideEffects:             &sec,
				MatchPolicy:             &mp,
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: nsLabelSelReqs,
				},
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: webhookCert.CACertBytes,
					Service: &admissionregistrationv1.ServiceReference{
						Name:      nvcaoptypes.NVCAModuleName,
						Namespace: getSystemNamespace(nb),
						Path:      &vpath,
						Port:      &sport,
					},
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods", "services", "serviceaccounts"},
							Scope:       &st,
						},
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
					},
					{
						Rule: admissionregistrationv1.Rule{APIGroups: []string{"apps"},
							APIVersions: []string{"v1"},
							Resources:   []string{"deployments", "statefulsets"},
							Scope:       &st,
						},
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
					},
					{
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"rbac.authorization.k8s.io"},
							APIVersions: []string{"v1"},
							Resources:   []string{"roles", "rolebindings"},
							Scope:       &st,
						},
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
					},
					{
						Rule: admissionregistrationv1.Rule{APIGroups: []string{"batch"},
							APIVersions: []string{"v1"},
							Resources:   []string{"jobs", "cronjobs"},
							Scope:       &st,
						},
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
					},
				},
			},
		},
	}

	return bc.createOrUpdateValidatingWebhookConfiguration(ctx, vw)
}

func (bc *BackendK8sCache) getMiniServiceRBACCmData(ctx context.Context, _ *nvidiaiov1.NVCFBackend) (map[string]string, error) {
	log := core.GetLogger(ctx)
	type templateInputBase struct {
		Name                        string
		AppName                     string
		InstanceName                string
		ManagedBy                   string
		AllowedExtraKubernetesTypes []nvcaconfig.AllowedExtraKubernetesTypeConfig
	}

	data := templateInputBase{
		Name:         MiniServicesPermissionsRoleName,
		AppName:      nvcaoptypes.NVCAModuleName,
		InstanceName: nvcaoptypes.NVCAModuleName,
		ManagedBy:    NVCAOperatorName,
	}

	cfg, foundCfg, err := bc.getAgentConfigToMerge(ctx)
	if err != nil {
		return nil, fmt.Errorf("get NVCA merge config: %w", err)
	}
	if foundCfg && cfg.Cluster.ValidationPolicy != nil {
		data.AllowedExtraKubernetesTypes = cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes
	}

	t, err := template.ParseFS(manifests, filepath.Join("manifests", "rbacTemplate.yaml"))
	if err != nil {
		return nil, fmt.Errorf("parse RBAC manifest template: %w", err)
	}
	if l := len(t.Templates()); l != 1 {
		return nil, fmt.Errorf("expected 1 RBAC template, got: %d", l)
	}

	tt := t.Templates()[0]

	b := &bytes.Buffer{}
	if err := tt.Execute(b, data); err != nil {
		log.WithError(err).Errorf("Failed to execute template for Role manifest %s", data.Name)
		return nil, err
	}

	return map[string]string{
		data.Name: b.String(),
	}, nil
}
