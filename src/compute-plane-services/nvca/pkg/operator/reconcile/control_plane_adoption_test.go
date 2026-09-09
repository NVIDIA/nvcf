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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestAdoptLegacyControlPlaneObjects_DefaultAdoptsUnownedKnownObjects(t *testing.T) {
	ctx := newTestContext()
	foreign, err := controlplane.NewIdentity("plane-b")
	require.NoError(t, err)

	k8sClient := fakek8sclient.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: DefaultNVCARequestsNamespace,
			Labels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: WorkloadInstanceTypeValuePodSpec,
			},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: storage.ModelCacheInitNamespace,
			Labels: map[string]string{
				controlplane.OwnerLabel: foreign.String(),
			},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "workload-a",
			Labels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: WorkloadInstanceTypeValueMiniService,
			},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "workload-b",
			Labels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: WorkloadInstanceTypeValueMiniService,
				controlplane.OwnerLabel:             foreign.String(),
			},
		}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name:      nvcaoptypes.NVCAModuleName,
			Namespace: DefaultNVCASystemNamespace,
		}},
		&admissionregistrationv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{
			Name: nvcaoptypes.NVCAModuleName,
		}},
		&admissionregistrationv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{
			Name: nvcaoptypes.NVCAModuleName,
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: nvcaoptypes.NVCAModuleName}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: nvcaoptypes.NVCAModuleName}},
	)
	bc := testAdoptionBackend(k8sClient, controlplane.DefaultIdentity())

	result, err := bc.adoptLegacyControlPlaneObjects(ctx, false)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"Namespace/nvca-system",
		"Namespace/nvcf-backend",
		"Namespace/workload-a",
		"ServiceAccount/nvca-system/nvca",
		"ValidatingWebhookConfiguration/nvca",
		"MutatingWebhookConfiguration/nvca",
		"ClusterRole/nvca",
		"ClusterRoleBinding/nvca",
	}, result.Adopted)
	assert.Empty(t, result.WouldAdopt)

	assertNamespaceOwner(t, k8sClient, DefaultNVCASystemNamespace, controlplane.DefaultOwner)
	assertNamespaceOwner(t, k8sClient, DefaultNVCARequestsNamespace, controlplane.DefaultOwner)
	assertNamespaceOwner(t, k8sClient, "workload-a", controlplane.DefaultOwner)
	assertNamespaceOwner(t, k8sClient, storage.ModelCacheInitNamespace, foreign.String())
	assertNamespaceOwner(t, k8sClient, "workload-b", foreign.String())

	sa, err := k8sClient.CoreV1().ServiceAccounts(DefaultNVCASystemNamespace).
		Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, controlplane.DefaultOwner, sa.Labels[controlplane.OwnerLabel])

	validatingWebhook, err := k8sClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().
		Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, controlplane.DefaultOwner, validatingWebhook.Labels[controlplane.OwnerLabel])

	mutatingWebhook, err := k8sClient.AdmissionregistrationV1().MutatingWebhookConfigurations().
		Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, controlplane.DefaultOwner, mutatingWebhook.Labels[controlplane.OwnerLabel])

	clusterRole, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, controlplane.DefaultOwner, clusterRole.Labels[controlplane.OwnerLabel])

	clusterRoleBinding, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, controlplane.DefaultOwner, clusterRoleBinding.Labels[controlplane.OwnerLabel])
}

func TestAdoptLegacyControlPlaneObjects_DryRunDoesNotMutate(t *testing.T) {
	ctx := newTestContext()
	k8sClient := fakek8sclient.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: DefaultNVCARequestsNamespace,
			Labels: map[string]string{
				nvcatypes.WorkloadInstanceTypeLabel: WorkloadInstanceTypeValuePodSpec,
			},
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: nvcaoptypes.NVCAModuleName}},
	)
	bc := testAdoptionBackend(k8sClient, controlplane.DefaultIdentity())

	result, err := bc.adoptLegacyControlPlaneObjects(ctx, true)

	require.NoError(t, err)
	assert.Empty(t, result.Adopted)
	assert.ElementsMatch(t, []string{
		"Namespace/nvcf-backend",
		"ClusterRole/nvca",
	}, result.WouldAdopt)
	assertNamespaceOwner(t, k8sClient, DefaultNVCARequestsNamespace, "")

	clusterRole, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, clusterRole.Labels[controlplane.OwnerLabel])
}

func TestAdoptLegacyControlPlaneObjects_NamedPlaneDoesNotAdoptLegacyObjects(t *testing.T) {
	ctx := newTestContext()
	identity, err := controlplane.NewIdentity("plane-a")
	require.NoError(t, err)
	k8sClient := fakek8sclient.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&admissionregistrationv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{
			Name: nvcaoptypes.NVCAModuleName,
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: nvcaoptypes.NVCAModuleName}},
	)
	bc := testAdoptionBackend(k8sClient, identity)

	result, err := bc.adoptLegacyControlPlaneObjects(ctx, false)

	require.NoError(t, err)
	assert.Empty(t, result.Adopted)
	assert.Empty(t, result.WouldAdopt)
	assertNamespaceOwner(t, k8sClient, DefaultNVCASystemNamespace, "")

	mutatingWebhook, err := k8sClient.AdmissionregistrationV1().MutatingWebhookConfigurations().
		Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, mutatingWebhook.Labels[controlplane.OwnerLabel])

	clusterRole, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, clusterRole.Labels[controlplane.OwnerLabel])
}

func TestAdoptLegacyControlPlaneObjects_Idempotent(t *testing.T) {
	ctx := newTestContext()
	k8sClient := fakek8sclient.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNVCASystemNamespace}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: nvcaoptypes.NVCAModuleName}},
	)
	bc := testAdoptionBackend(k8sClient, controlplane.DefaultIdentity())

	first, err := bc.adoptLegacyControlPlaneObjects(ctx, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"Namespace/nvca-system",
		"ClusterRoleBinding/nvca",
	}, first.Adopted)

	second, err := bc.adoptLegacyControlPlaneObjects(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, second.Adopted)
	assert.Empty(t, second.WouldAdopt)
	assertNamespaceOwner(t, k8sClient, DefaultNVCASystemNamespace, controlplane.DefaultOwner)
}

func testAdoptionBackend(k8sClient *fakek8sclient.Clientset, identity controlplane.Identity) *BackendK8sCache {
	return &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			K8s: k8sClient,
		},
		controlPlaneIdentity: identity,
	}
}

func assertNamespaceOwner(
	t *testing.T,
	k8sClient *fakek8sclient.Clientset,
	name string,
	expected string,
) {
	t.Helper()
	ns, err := k8sClient.CoreV1().Namespaces().Get(newTestContext(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expected, ns.Labels[controlplane.OwnerLabel])
}
