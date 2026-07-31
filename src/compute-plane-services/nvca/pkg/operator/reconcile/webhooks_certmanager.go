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
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	NVCAWebhookCertificateName          = "nvca-webhook-cert"
	certManagerInjectCAFromAnnotation   = "cert-manager.io/inject-ca-from"
	defaultWebhookCertManagerIssuer     = "compute-plane-ca-issuer"
	defaultWebhookCertManagerIssuerKind = "ClusterIssuer"
)

var certificateGVR = schema.GroupVersionResource{
	Group:    "cert-manager.io",
	Version:  "v1",
	Resource: "certificates",
}

func webhookCertManagerEnabled(nb *nvidiaiov1.NVCFBackend) bool {
	cm := nb.Spec.WebhookConfig.CertManager
	return cm != nil && cm.Enabled
}

func webhookCertManagerIssuerRef(nb *nvidiaiov1.NVCFBackend) (name, kind string) {
	name = defaultWebhookCertManagerIssuer
	kind = defaultWebhookCertManagerIssuerKind
	if cm := nb.Spec.WebhookConfig.CertManager; cm != nil {
		if cm.IssuerName != "" {
			name = cm.IssuerName
		}
		if cm.IssuerKind != "" {
			kind = cm.IssuerKind
		}
	}
	return name, kind
}

func webhookCertManagerInjectCAFrom(nb *nvidiaiov1.NVCFBackend) string {
	return fmt.Sprintf("%s/%s", getSystemNamespace(nb), NVCAWebhookCertificateName)
}

func (bc *BackendK8sCache) setupWebhookCertificate(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	ns := getSystemNamespace(nb)
	issuerName, issuerKind := webhookCertManagerIssuerRef(nb)
	dnsNames := getTLSDNSNames(nb)

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   certificateGVR.Group,
		Version: certificateGVR.Version,
		Kind:    "Certificate",
	})
	cert.SetName(NVCAWebhookCertificateName)
	cert.SetNamespace(ns)
	cert.SetAnnotations(getNBAnnotations(nb))
	cert.Object["spec"] = map[string]interface{}{
		"secretName":  NVCAWebhookTLSCertSecretName,
		"duration":    "8760h",
		"renewBefore": "360h",
		"dnsNames":    toInterfaceSlice(dnsNames),
		"issuerRef": map[string]interface{}{
			"name": issuerName,
			"kind": issuerKind,
		},
		"usages": []interface{}{"server auth"},
	}

	client := bc.clients.DynamicClient.Resource(certificateGVR).Namespace(ns)
	existing, err := client.Get(ctx, NVCAWebhookCertificateName, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("get webhook Certificate: %w", err)
		}
		if _, err := client.Create(ctx, cert, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create webhook Certificate: %w", err)
		}
		log.Infof("Created cert-manager Certificate %s/%s", ns, NVCAWebhookCertificateName)
		return nil
	}
	cert.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.Update(ctx, cert, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update webhook Certificate: %w", err)
	}
	return nil
}

func (bc *BackendK8sCache) waitForWebhookTLSSecret(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	_, err := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("webhook TLS secret %q not ready yet", NVCAWebhookTLSCertSecretName)
		}
		return fmt.Errorf("get webhook TLS secret: %w", err)
	}
	return nil
}

func webhookServerCertsVolume(nb *nvidiaiov1.NVCFBackend) corev1.Volume {
	if webhookCertManagerEnabled(nb) {
		return corev1.Volume{
			Name: SrvCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: NVCAWebhookTLSCertSecretName,
				},
			},
		}
	}
	return corev1.Volume{
		Name: SrvCertsVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}

func toInterfaceSlice(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
