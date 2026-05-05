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
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func TestGenerateWebhookCerts(t *testing.T) {
	tests := []struct {
		name    string
		nb      *nvidiaiov1.NVCFBackend
		wantErr bool
	}{
		{
			name: "valid backend with default namespace",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			wantErr: false,
		},
		{
			name: "valid backend with custom namespace",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "custom-ns",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cert, err := generateWebhookCerts(tt.nb, time.Now())
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.NotEmpty(t, cert.CACertBytes)
			assert.NotEmpty(t, cert.TLSCert)
			assert.NotEmpty(t, cert.TLSKey)
		})
	}
}

func TestGetWebHooksSvcPort(t *testing.T) {
	tests := []struct {
		name         string
		nb           *nvidiaiov1.NVCFBackend
		expectedPort int32
	}{
		{
			name: "default port",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			expectedPort: DefaultWebhooksServicePortHTTPS,
		},
		{
			name: "custom port",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						WebhookConfig: nvidiaiov1.WebhookConfig{
							ServicePort: 8443,
						},
					},
				},
			},
			expectedPort: 8443,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := getWebHooksSvcPort(tt.nb)
			assert.Equal(t, tt.expectedPort, port)
		})
	}
}

func TestGetWebHooksListenPort(t *testing.T) {
	tests := []struct {
		name         string
		nb           *nvidiaiov1.NVCFBackend
		expectedPort int32
	}{
		{
			name: "default port",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			expectedPort: DefaultWebhooksListenPortHTTP,
		},
		{
			name: "custom port",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						WebhookConfig: nvidiaiov1.WebhookConfig{
							ListenPort: 9443,
						},
					},
				},
			},
			expectedPort: 9443,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := getWebHooksListenPort(tt.nb)
			assert.Equal(t, tt.expectedPort, port)
		})
	}
}

func TestGetTLSDNSNames(t *testing.T) {
	tests := []struct {
		name          string
		nb            *nvidiaiov1.NVCFBackend
		expectedNames []string
	}{
		{
			name: "default namespace",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			expectedNames: []string{
				"nvca.nvca-system.svc",
				"nvca.nvca-system.svc.cluster.local",
			},
		},
		{
			name: "custom namespace",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "custom-ns",
				},
			},
			expectedNames: []string{
				"nvca.nvca-system.svc",
				"nvca.nvca-system.svc.cluster.local",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names := getTLSDNSNames(tt.nb)
			assert.Equal(t, tt.expectedNames, names)
		})
	}
}

func TestMakeLabelSelectorRequirements(t *testing.T) {
	tests := []struct {
		name         string
		labels       map[string][]string
		expectedReqs []metav1.LabelSelectorRequirement
	}{
		{
			name: "single label",
			labels: map[string][]string{
				"key1": {"value1"},
			},
			expectedReqs: []metav1.LabelSelectorRequirement{
				{
					Key:      "key1",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"value1"},
				},
			},
		},
		{
			name: "multiple labels",
			labels: map[string][]string{
				"key1": {"value1", "value2"},
				"key2": {"value3"},
			},
			expectedReqs: []metav1.LabelSelectorRequirement{
				{
					Key:      "key1",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"value1", "value2"},
				},
				{
					Key:      "key2",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"value3"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := makeLabelSelectorRequirements(tt.labels)
			// Sort both slices by Key to make the comparison order-independent
			sort.Slice(reqs, func(i, j int) bool {
				return reqs[i].Key < reqs[j].Key
			})
			sort.Slice(tt.expectedReqs, func(i, j int) bool {
				return tt.expectedReqs[i].Key < tt.expectedReqs[j].Key
			})
			assert.Equal(t, tt.expectedReqs, reqs)
		})
	}
}

func TestMakeWebhookClientConfig(t *testing.T) {
	tests := []struct {
		name           string
		nb             *nvidiaiov1.NVCFBackend
		webhookCert    WebhookCert
		path           string
		expectedConfig admissionregistrationv1.WebhookClientConfig
	}{
		{
			name: "default configuration",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			webhookCert: WebhookCert{
				CACertBytes: []byte("test-ca-cert"),
			},
			path: "/test-path",
			expectedConfig: admissionregistrationv1.WebhookClientConfig{
				CABundle: []byte("test-ca-cert"),
				Service: &admissionregistrationv1.ServiceReference{
					Name:      nvcaoptypes.NVCAModuleName,
					Namespace: "nvca-system",
					Path:      stringPtr("/test-path"),
					Port:      int32Ptr(DefaultWebhooksServicePortHTTPS),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := makeWebhookClientConfig(tt.nb, tt.webhookCert, tt.path)
			assert.Equal(t, tt.expectedConfig, config)
		})
	}
}

func TestSetupWebhookSecrets(t *testing.T) {
	tests := []struct {
		name        string
		nb          *nvidiaiov1.NVCFBackend
		webhookCert WebhookCert
		wantErr     bool
	}{
		{
			name: "successful setup",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			webhookCert: WebhookCert{
				CACertBytes: []byte("test-ca-cert"),
				TLSCert:     []byte("test-tls-cert"),
				TLSKey:      []byte("test-tls-key"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				clients: &kubeclients.KubeClients{
					K8s: fake.NewSimpleClientset(),
				},
			}
			err := bc.setupWebhookSecrets(context.Background(), tt.nb, tt.webhookCert)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			// Verify TLS cert secret
			tlsSecret, err := bc.clients.K8s.CoreV1().Secrets("nvca-system").Get(context.Background(), NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tt.webhookCert.TLSCert, tlsSecret.Data[TLSCertName])
			assert.Equal(t, tt.webhookCert.TLSKey, tlsSecret.Data[TLSKeyName])

			// Verify CA secret
			caSecret, err := bc.clients.K8s.CoreV1().Secrets("nvca-system").Get(context.Background(), NVCAWebhookTLSCASecretName, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tt.webhookCert.CACertBytes, caSecret.Data[TLSCAName])
		})
	}
}

func TestMakeMutatingWebhook(t *testing.T) {
	tests := []struct {
		name              string
		webhookName       string
		webhookPath       string
		namespaceSelector *metav1.LabelSelector
		rules             []admissionregistrationv1.RuleWithOperations
		nb                *nvidiaiov1.NVCFBackend
		webhookCert       WebhookCert
		expectedWebhook   admissionregistrationv1.MutatingWebhook
	}{
		{
			name:        "basic webhook configuration",
			webhookName: "test-webhook",
			webhookPath: "/test-path",
			namespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"key": "value",
				},
			},
			rules: []admissionregistrationv1.RuleWithOperations{
				{
					Operations: []admissionregistrationv1.OperationType{
						admissionregistrationv1.Create,
					},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{"*"},
						APIVersions: []string{"*"},
						Resources:   []string{"*"},
					},
				},
			},
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "default",
				},
			},
			webhookCert: WebhookCert{
				CACertBytes: []byte("test-ca-cert"),
			},
			expectedWebhook: admissionregistrationv1.MutatingWebhook{
				Name: "test-webhook",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: []byte("test-ca-cert"),
					Service: &admissionregistrationv1.ServiceReference{
						Name:      nvcaoptypes.NVCAModuleName,
						Namespace: "nvca-system",
						Path:      stringPtr("/test-path"),
						Port:      int32Ptr(DefaultWebhooksServicePortHTTPS),
					},
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"*"},
							APIVersions: []string{"*"},
							Resources:   []string{"*"},
						},
					},
				},
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"key": "value",
					},
				},
				SideEffects:             sideEffectClassPtr(admissionregistrationv1.SideEffectClassNone),
				AdmissionReviewVersions: []string{"v1"},
				FailurePolicy:           failurePolicyPtr(admissionregistrationv1.Fail),
				MatchPolicy:             matchPolicyPtr(admissionregistrationv1.Equivalent),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhook := makeMutatingWebhook(
				tt.webhookName,
				tt.webhookPath,
				tt.namespaceSelector,
				tt.rules,
				tt.nb,
				tt.webhookCert,
			)
			assert.Equal(t, tt.expectedWebhook, webhook)
		})
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func int32Ptr(i int32) *int32 {
	return &i
}

func sideEffectClassPtr(s admissionregistrationv1.SideEffectClass) *admissionregistrationv1.SideEffectClass {
	return &s
}

func failurePolicyPtr(f admissionregistrationv1.FailurePolicyType) *admissionregistrationv1.FailurePolicyType {
	return &f
}

func matchPolicyPtr(m admissionregistrationv1.MatchPolicyType) *admissionregistrationv1.MatchPolicyType {
	return &m
}
