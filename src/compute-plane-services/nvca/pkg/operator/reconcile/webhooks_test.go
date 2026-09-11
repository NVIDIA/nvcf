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
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/controlplane"
	"github.com/stretchr/testify/require"

	nvcfv1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestWebhookCertSetup(t *testing.T) {
	tmpFunc := getTLSDNSNames
	t.Cleanup(func() {
		getTLSDNSNames = tmpFunc
	})
	getTLSDNSNames = func(*nvcfv1.NVCFBackend) []string { return []string{"localhost"} }

	nb := &nvcfv1.NVCFBackend{}
	whCert, err := generateWebhookCerts(nb, time.Now())
	require.NoError(t, err)

	rootCAs := x509.NewCertPool()
	appendedCA := rootCAs.AppendCertsFromPEM(whCert.CACertBytes)
	require.True(t, appendedCA)

	serverCert, err := tls.X509KeyPair([]byte(whCert.TLSCert), []byte(whCert.TLSKey))
	require.NoError(t, err)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	trpt := http.DefaultTransport.(*http.Transport)
	trpt.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs,
	}
	client := &http.Client{Transport: trpt}

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	nu := "https://localhost:" + u.Port()

	req, err := http.NewRequest("GET", nu, nil)
	require.NoError(t, err)
	res, err := client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
}

func TestWebhookConfigurationsAreScopedByControlPlane(t *testing.T) {
	ctx := context.Background()
	planeA, err := controlplane.NewIdentity("plane-a")
	require.NoError(t, err)
	planeB, err := controlplane.NewIdentity("plane-b")
	require.NoError(t, err)
	planeAName, err := controlplane.DNSLabelName(planeA, nvcaoptypes.NVCAModuleName)
	require.NoError(t, err)
	planeBName, err := controlplane.DNSLabelName(planeB, nvcaoptypes.NVCAModuleName)
	require.NoError(t, err)

	clientset := k8sfake.NewSimpleClientset()
	nbA := &nvcfv1.NVCFBackend{
		Spec: nvcfv1.NVCFBackendSpec{
			NVCFBackendSpecT: nvcfv1.NVCFBackendSpecT{
				ClusterConfig: nvcfv1.ClusterConfig{
					SystemNamespace: "plane-a-nvca-system",
				},
			},
		},
	}
	nbB := &nvcfv1.NVCFBackend{
		Spec: nvcfv1.NVCFBackendSpec{
			NVCFBackendSpecT: nvcfv1.NVCFBackendSpecT{
				ClusterConfig: nvcfv1.ClusterConfig{
					SystemNamespace: "plane-b-nvca-system",
				},
			},
		},
	}
	cert := WebhookCert{CACertBytes: []byte("ca")}
	bcA := &BackendK8sCache{
		clients:              &kubeclients.KubeClients{K8s: clientset},
		controlPlaneIdentity: planeA,
	}
	bcB := &BackendK8sCache{
		clients:              &kubeclients.KubeClients{K8s: clientset},
		controlPlaneIdentity: planeB,
	}

	require.NoError(t, bcA.setupNVCAMutatingWebhookConfiguration(ctx, nbA, cert))
	require.NoError(t, bcA.setupMiniServiceValidatingWebhook(ctx, nbA, cert))
	require.NoError(t, bcB.setupNVCAMutatingWebhookConfiguration(ctx, nbB, cert))
	require.NoError(t, bcB.setupMiniServiceValidatingWebhook(ctx, nbB, cert))

	aMutating, err := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, planeAName, metav1.GetOptions{})
	require.NoError(t, err)
	bMutating, err := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, planeBName, metav1.GetOptions{})
	require.NoError(t, err)
	aValidating, err := clientset.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, planeAName, metav1.GetOptions{})
	require.NoError(t, err)
	bValidating, err := clientset.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, planeBName, metav1.GetOptions{})
	require.NoError(t, err)

	require.Equal(t, planeA.String(), aMutating.Labels[controlplane.OwnerLabel])
	require.Equal(t, planeB.String(), bMutating.Labels[controlplane.OwnerLabel])
	require.Equal(t, planeA.String(), aValidating.Labels[controlplane.OwnerLabel])
	require.Equal(t, planeB.String(), bValidating.Labels[controlplane.OwnerLabel])
	require.NotEqual(t, webhookNames(aMutating.Webhooks), webhookNames(bMutating.Webhooks))
	assertWebhookServiceTargetsNamespace(t, aMutating.Webhooks, "plane-a-nvca-system")
	assertWebhookServiceTargetsNamespace(t, bMutating.Webhooks, "plane-b-nvca-system")
	assertValidatingWebhookServiceTargetsNamespace(t, aValidating.Webhooks, "plane-a-nvca-system")
	assertValidatingWebhookServiceTargetsNamespace(t, bValidating.Webhooks, "plane-b-nvca-system")
	assertMutatingWebhooksScopedToPlane(t, aMutating.Webhooks, planeA)
	assertMutatingWebhooksScopedToPlane(t, bMutating.Webhooks, planeB)
	assertValidatingWebhooksScopedToPlane(t, aValidating.Webhooks, planeA)
	assertValidatingWebhooksScopedToPlane(t, bValidating.Webhooks, planeB)

	_, err = clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.True(t, k8serrors.IsNotFound(err), "named planes must not write the legacy mutating webhook name")
	_, err = clientset.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.True(t, k8serrors.IsNotFound(err), "named planes must not write the legacy validating webhook name")
}

func TestDefaultWebhookConfigurationNameIsLegacyCompatible(t *testing.T) {
	ctx := context.Background()
	clientset := k8sfake.NewSimpleClientset()
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{K8s: clientset},
	}

	require.NoError(t, bc.setupNVCAMutatingWebhookConfiguration(ctx, &nvcfv1.NVCFBackend{}, WebhookCert{}))

	webhook, err := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, controlplane.DefaultOwner, webhook.Labels[controlplane.OwnerLabel])
	assertMutatingWebhooksScopedToPlane(t, webhook.Webhooks, controlplane.DefaultIdentity())
}

func assertMutatingWebhooksScopedToPlane(
	t *testing.T,
	webhooks []admissionregistrationv1.MutatingWebhook,
	identity controlplane.Identity,
) {
	t.Helper()
	for _, webhook := range webhooks {
		require.True(t, strings.HasSuffix(webhook.Name, ".nvca.nvcf.nvidia.io"))
		if !identity.IsDefault() {
			require.True(t, strings.HasPrefix(webhook.Name, identity.String()+"-"), "webhook %q should be plane-prefixed", webhook.Name)
		}
		assertSelectorRequiresOwner(t, webhook.NamespaceSelector, identity)
	}
}

func assertValidatingWebhooksScopedToPlane(
	t *testing.T,
	webhooks []admissionregistrationv1.ValidatingWebhook,
	identity controlplane.Identity,
) {
	t.Helper()
	for _, webhook := range webhooks {
		require.True(t, strings.HasSuffix(webhook.Name, ".nvca.nvcf.nvidia.io"))
		if !identity.IsDefault() {
			require.True(t, strings.HasPrefix(webhook.Name, identity.String()+"-"), "webhook %q should be plane-prefixed", webhook.Name)
		}
		assertSelectorRequiresOwner(t, webhook.NamespaceSelector, identity)
	}
}

func assertSelectorRequiresOwner(t *testing.T, selector *metav1.LabelSelector, identity controlplane.Identity) {
	t.Helper()
	require.NotNil(t, selector)
	for key, value := range selector.MatchLabels {
		if key == controlplane.OwnerLabel {
			require.Equal(t, identity.String(), value)
			return
		}
	}
	for _, expr := range selector.MatchExpressions {
		if expr.Key == controlplane.OwnerLabel {
			require.Equal(t, metav1.LabelSelectorOpIn, expr.Operator)
			require.Equal(t, []string{identity.String()}, expr.Values)
			return
		}
	}
	require.Failf(t, "missing owner selector", "selector %v does not require %s=%s", selector, controlplane.OwnerLabel, identity.String())
}

func webhookNames(webhooks []admissionregistrationv1.MutatingWebhook) []string {
	names := make([]string, 0, len(webhooks))
	for _, webhook := range webhooks {
		names = append(names, webhook.Name)
	}
	return names
}

func assertWebhookServiceTargetsNamespace(
	t *testing.T,
	webhooks []admissionregistrationv1.MutatingWebhook,
	namespace string,
) {
	t.Helper()
	for _, webhook := range webhooks {
		require.NotNil(t, webhook.ClientConfig.Service)
		require.Equal(t, nvcaoptypes.NVCAModuleName, webhook.ClientConfig.Service.Name)
		require.Equal(t, namespace, webhook.ClientConfig.Service.Namespace)
	}
}

func assertValidatingWebhookServiceTargetsNamespace(
	t *testing.T,
	webhooks []admissionregistrationv1.ValidatingWebhook,
	namespace string,
) {
	t.Helper()
	for _, webhook := range webhooks {
		require.NotNil(t, webhook.ClientConfig.Service)
		require.Equal(t, nvcaoptypes.NVCAModuleName, webhook.ClientConfig.Service.Name)
		require.Equal(t, namespace, webhook.ClientConfig.Service.Namespace)
	}
}

func TestWorkloadNamespaceSelectorRequiresTypeAndOwner(t *testing.T) {
	plane, err := controlplane.NewIdentity("plane-a")
	require.NoError(t, err)
	bc := &BackendK8sCache{controlPlaneIdentity: plane}

	selector := bc.workloadNamespaceSelector(
		nvcatypes.WorkloadInstanceTypeValueMiniService,
		nvcatypes.WorkloadInstanceTypeValuePodSpec,
	)

	assertSelectorRequiresOwner(t, selector, plane)
	require.True(t, selectorRequiresValues(
		selector,
		nvcatypes.WorkloadInstanceTypeLabel,
		[]string{
			nvcatypes.WorkloadInstanceTypeValueMiniService,
			nvcatypes.WorkloadInstanceTypeValuePodSpec,
		},
	))
}

func selectorRequiresValues(selector *metav1.LabelSelector, key string, values []string) bool {
	for matchKey, value := range selector.MatchLabels {
		if matchKey == key && len(values) == 1 && values[0] == value {
			return true
		}
	}
	for _, expr := range selector.MatchExpressions {
		if expr.Key == key && expr.Operator == metav1.LabelSelectorOpIn && stringSlicesEqual(expr.Values, values) {
			return true
		}
	}
	return false
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
