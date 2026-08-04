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
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const transportTrustTestPEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2wpSek6nFhYi
Aivep2lMBrXuN6zzesLKOjv4GhIrlGUCID/5IHAxPH/aSgR5UEr5lKAFOENMrYnq
sUcTxMQqHOWL
-----END CERTIFICATE-----
`

func TestNVCAConfigMapper_MapsSecretBackedTransportTLSToAgentConfig(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	createTransportTrustSource(t, ctx, clients)

	config, found, err := newNVCAOperatorConfigMapMapper(
		NVCAOperatorNamespace,
		clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace),
		clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace),
	).getConfig(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, config.Workload.TransportTLS)
	assert.Equal(t, nvcaconfig.TrustModeBundle, config.Workload.TransportTLS.TrustMode)
	assert.Equal(t, transportTrustTestPEM, config.Workload.TransportTLS.TrustBundlePEM)
	assert.Equal(t, "sha256:95b3dc7dfd3212a6f02c644527f0a65890a9a9c80acf7551be6aa89b1f98fe86",
		config.Workload.TransportTLS.TrustBundleFingerprint)
}

func TestNVCAConfigMapper_MissingConfigIsNoop(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()

	config, found, err := newNVCAOperatorConfigMapMapper(
		NVCAOperatorNamespace,
		clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace),
		clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace),
	).getConfig(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, nvcaconfig.Config{}, config)
}

func TestNVCAConfigMapper_ValidatesSecretBackedTransportTLS(t *testing.T) {
	const sourceConfig = `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: nvcf-trust
        key: ca.crt
    fingerprint: %q
`

	tests := []struct {
		name          string
		config        string
		secretData    map[string][]byte
		wantFound     bool
		wantErrSubstr string
	}{
		{
			name: "empty selector disables source",
			config: `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: ""
        key: ca.crt
`,
		},
		{
			name:          "selected Secret must exist",
			config:        fmt.Sprintf(sourceConfig, ""),
			wantErrSubstr: "read transport TLS Secret",
		},
		{
			name:   "selected Secret must contain configured key",
			config: fmt.Sprintf(sourceConfig, ""),
			secretData: map[string][]byte{
				"other": []byte(transportTrustTestPEM),
			},
			wantErrSubstr: "has no \"ca.crt\" data",
		},
		{
			name:   "Secret data must be certificate-only PEM",
			config: fmt.Sprintf(sourceConfig, ""),
			secretData: map[string][]byte{
				"ca.crt": []byte("not a certificate"),
			},
			wantErrSubstr: "contains non-PEM data",
		},
		{
			name:   "supplied fingerprint must match",
			config: fmt.Sprintf(sourceConfig, "sha256:not-the-secret"),
			secretData: map[string][]byte{
				"ca.crt": []byte(transportTrustTestPEM),
			},
			wantErrSubstr: "does not match computed fingerprint",
		},
		{
			name:   "matching fingerprint is accepted",
			config: fmt.Sprintf(sourceConfig, "sha256:95b3dc7dfd3212a6f02c644527f0a65890a9a9c80acf7551be6aa89b1f98fe86"),
			secretData: map[string][]byte{
				"ca.crt": []byte(transportTrustTestPEM),
			},
			wantFound: true,
		},
		{
			name: "unknown configuration fields are rejected",
			config: `workload:
  transportTLS:
    unexpected: value
`,
			wantErrSubstr: "field unexpected not found",
		},
		{
			name: "multiple YAML documents are rejected",
			config: `workload:
---
workload: {}
`,
			wantErrSubstr: "expected a single YAML document",
		},
		{
			name: "selected Secret requires key",
			config: `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: nvcf-trust
        key: ""
`,
			wantErrSubstr: "secretKeyRef.key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClientsForIntegrationTests()
			_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
				Data:       map[string]string{agentConfigFile: tt.config},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			if tt.secretData != nil {
				_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "nvcf-trust"},
					Data:       tt.secretData,
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			_, found, err := newNVCAOperatorConfigMapMapper(
				NVCAOperatorNamespace,
				clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace),
				clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace),
			).getConfig(ctx)
			if tt.wantErrSubstr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}

func TestValidateCertificateOnlyPEM_RejectsMalformedInput(t *testing.T) {
	for _, pemData := range [][]byte{
		[]byte("unexpected data\n" + transportTrustTestPEM),
		[]byte(transportTrustTestPEM + "unexpected data\n" + transportTrustTestPEM),
		[]byte("-----BEGIN PRIVATE KEY-----\n" + transportTrustTestPEM),
		[]byte("-----BEGIN CERTIFICATE-----\nmalformed data\n" + transportTrustTestPEM),
		[]byte("-----BEGIN CERTIFICATE-----\nnot-base64!\n-----END CERTIFICATE-----\n" + transportTrustTestPEM),
	} {
		assert.Error(t, validateCertificateOnlyPEM(pemData))
	}
}

func createTransportTrustSource(t *testing.T, ctx context.Context, clients *kubeclients.KubeClients) {
	t.Helper()
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
		Data: map[string]string{agentConfigFile: `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: nvcf-trust
        key: ca.crt
`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-trust"},
		Data:       map[string][]byte{"ca.crt": []byte(transportTrustTestPEM)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}
