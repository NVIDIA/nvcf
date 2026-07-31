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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
)

func TestWebhookCertManagerEnabled(t *testing.T) {
	t.Parallel()
	nb := &nvidiaiov1.NVCFBackend{}
	if webhookCertManagerEnabled(nb) {
		t.Fatal("expected disabled when CertManager nil")
	}
	nb.Spec.WebhookConfig.CertManager = &nvidiaiov1.WebhookCertManagerConfig{Enabled: true}
	if !webhookCertManagerEnabled(nb) {
		t.Fatal("expected enabled")
	}
}

func TestSetupWebhookCertificateCreatesCR(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClient(scheme)
	k8s := k8sfake.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s:           k8s,
			DynamicClient: dyn,
		},
	}
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				WebhookConfig: nvidiaiov1.WebhookConfig{
					CertManager: &nvidiaiov1.WebhookCertManagerConfig{
						Enabled:    true,
						IssuerName: "compute-plane-ca-issuer",
						IssuerKind: "ClusterIssuer",
					},
				},
			},
		},
	}
	if err := bc.setupWebhookCertificate(context.Background(), nb); err != nil {
		t.Fatalf("setupWebhookCertificate: %v", err)
	}
	got, err := dyn.Resource(certificateGVR).Namespace("nvca-system").Get(
		context.Background(), NVCAWebhookCertificateName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Certificate: %v", err)
	}
	issuer, _, _ := unstructuredIssuerRef(t, got.Object)
	if issuer["name"] != "compute-plane-ca-issuer" || issuer["kind"] != "ClusterIssuer" {
		t.Fatalf("unexpected issuerRef: %#v", issuer)
	}
}

func unstructuredIssuerRef(t *testing.T, obj map[string]interface{}) (map[string]interface{}, bool, bool) {
	t.Helper()
	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("missing spec")
	}
	issuer, ok := spec["issuerRef"].(map[string]interface{})
	return issuer, ok, true
}

func TestMakeWebhookClientConfigCertManagerOmitsCABundle(t *testing.T) {
	t.Parallel()
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				WebhookConfig: nvidiaiov1.WebhookConfig{
					CertManager: &nvidiaiov1.WebhookCertManagerConfig{Enabled: true},
				},
			},
		},
	}
	cfg := makeWebhookClientConfig(nb, WebhookCert{CACertBytes: []byte("ca")}, "/test")
	if len(cfg.CABundle) != 0 {
		t.Fatalf("expected empty caBundle, got %q", cfg.CABundle)
	}
}
