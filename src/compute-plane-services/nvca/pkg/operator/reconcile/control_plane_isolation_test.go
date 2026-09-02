/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	fakenvcaopclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
)

func assertNoMutatingIsolationActions(t *testing.T, actions []ktesting.Action) {
	t.Helper()
	for _, action := range actions {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete", "deletecollection":
			t.Errorf("unexpected mutating action after isolation rejection: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func newNamedIsolationTestCache() (*BackendK8sCache, *fakek8sclient.Clientset, *fakenvcaopclient.Clientset) {
	clients := mockKubeClients()
	k8sClient := clients.K8s.(*fakek8sclient.Clientset)
	nvcaClient := clients.NVCAOP.(*fakenvcaopclient.Clientset)
	return &BackendK8sCache{
		clients:           clients,
		operatorNamespace: "plane-a-nvca-operator",
		controlPlaneID:    "plane-a",
	}, k8sClient, nvcaClient
}

func TestControlPlaneNamespaces(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{}
	nb.Spec.ClusterConfig.ControlPlaneID = "plane-a"

	assert.Equal(t, "plane-a-nvca-system", getSystemNamespace(nb))
	assert.Equal(t, "plane-a-nvcf-backend", getRequestsNamespace(nb))

	nb.Spec.ClusterConfig.SystemNamespace = "explicit-system"
	nb.Spec.ClusterConfig.RequestsNamespace = "explicit-requests"
	assert.Equal(t, "explicit-system", getSystemNamespace(nb))
	assert.Equal(t, "explicit-requests", getRequestsNamespace(nb))
}

func TestApplyControlPlaneIdentity(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{}
	require.NoError(t, applyControlPlaneIdentity(nb, "plane-a"))
	assert.Equal(t, "plane-a", nb.Spec.ClusterConfig.ControlPlaneID)

	nb.Spec.ClusterConfig.ControlPlaneID = "plane-b"
	assert.Error(t, applyControlPlaneIdentity(nb, "plane-a"))
	assert.Error(t, applyControlPlaneIdentity(nb, ""), "legacy operator must not reconcile a named backend")

	nb.Spec.ClusterConfig.ControlPlaneID = "default"
	assert.Error(t, applyControlPlaneIdentity(nb, ""))
}

func TestSyncNVCFBackendRejectsForeignScopeBeforeMutation(t *testing.T) {
	tests := map[string]func(*nvidiaiov1.NVCFBackend){
		"control plane ID": func(nb *nvidiaiov1.NVCFBackend) {
			nb.Spec.ClusterConfig.ControlPlaneID = "plane-b"
		},
		"operator namespace": func(nb *nvidiaiov1.NVCFBackend) {
			nb.Namespace = "plane-b-nvca-operator"
		},
		"system namespace": func(nb *nvidiaiov1.NVCFBackend) {
			nb.Spec.ClusterConfig.SystemNamespace = "plane-b-nvca-system"
		},
		"requests namespace": func(nb *nvidiaiov1.NVCFBackend) {
			nb.Spec.ClusterConfig.RequestsNamespace = "plane-b-nvcf-backend"
		},
		"deleting foreign backend": func(nb *nvidiaiov1.NVCFBackend) {
			nb.Spec.ClusterConfig.ControlPlaneID = "plane-b"
			nb.Finalizers = []string{cleanup.NVCAOperatorFinalizer}
			now := metav1.NewTime(time.Now())
			nb.DeletionTimestamp = &now
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			bc, k8sClient, nvcaClient := newNamedIsolationTestCache()
			nb := getTestNVCFBackendMinimal()
			nb.Namespace = bc.operatorNamespace
			nb.Spec.Overrides = nil
			nb.Spec.ClusterConfig.ControlPlaneID = bc.controlPlaneID
			mutate(nb)
			original := nb.DeepCopy()

			err := bc.syncNVCFBackend(t.Context(), nb, false)
			require.ErrorContains(t, err, "refusing to reconcile NVCFBackend")
			assert.Equal(t, original, nb, "sync must not mutate the informer object")
			assertNoMutatingIsolationActions(t, k8sClient.Actions())
			assertNoMutatingIsolationActions(t, nvcaClient.Actions())
		})
	}
}

func TestSyncNVCFBackendRejectsForeignOverrideBeforeMutation(t *testing.T) {
	tests := map[string]func(*nvidiaiov1.NVCFBackendSpecT){
		"control plane ID": func(overrides *nvidiaiov1.NVCFBackendSpecT) {
			overrides.ClusterConfig.ControlPlaneID = "plane-b"
		},
		"system namespace": func(overrides *nvidiaiov1.NVCFBackendSpecT) {
			overrides.ClusterConfig.SystemNamespace = "plane-b-nvca-system"
		},
		"requests namespace": func(overrides *nvidiaiov1.NVCFBackendSpecT) {
			overrides.ClusterConfig.RequestsNamespace = "plane-b-nvcf-backend"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			bc, k8sClient, nvcaClient := newNamedIsolationTestCache()
			nb := getTestNVCFBackendMinimal()
			nb.Namespace = bc.operatorNamespace
			nb.Finalizers = []string{cleanup.NVCAOperatorFinalizer}
			nb.Spec.ClusterConfig.ControlPlaneID = bc.controlPlaneID
			nb.Spec.Overrides = &nvidiaiov1.NVCFBackendSpecT{Version: nb.Spec.Version}
			mutate(nb.Spec.Overrides)
			_, err := nvcaClient.NvcfV1().NVCFBackends(bc.operatorNamespace).Create(
				t.Context(), nb, metav1.CreateOptions{})
			require.NoError(t, err)
			k8sClient.ClearActions()
			nvcaClient.ClearActions()

			err = bc.syncNVCFBackend(t.Context(), nb.DeepCopy(), false)
			require.ErrorContains(t, err, "refusing to reconcile NVCFBackend")
			assertNoMutatingIsolationActions(t, k8sClient.Actions())
			assertNoMutatingIsolationActions(t, nvcaClient.Actions())
		})
	}
}

func TestValidateAndApplyControlPlaneScopeCompatibility(t *testing.T) {
	tests := []struct {
		name              string
		operatorNamespace string
		operatorID        string
		backend           *nvidiaiov1.NVCFBackend
		expectedID        string
	}{
		{
			name:              "legacy remains unscoped",
			operatorNamespace: NVCAOperatorNamespace,
			backend: &nvidiaiov1.NVCFBackend{ObjectMeta: metav1.ObjectMeta{
				Namespace: NVCAOperatorNamespace,
			}},
		},
		{
			name:              "named backend is normalized",
			operatorNamespace: "plane-a-nvca-operator",
			operatorID:        "plane-a",
			backend: &nvidiaiov1.NVCFBackend{ObjectMeta: metav1.ObjectMeta{
				Namespace: "plane-a-nvca-operator",
			}},
			expectedID: "plane-a",
		},
		{
			name:              "named backend accepts its derived namespaces",
			operatorNamespace: "plane-a-nvca-operator",
			operatorID:        "plane-a",
			backend: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{Namespace: "plane-a-nvca-operator"},
				Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
					ClusterConfig: nvidiaiov1.ClusterConfig{
						ControlPlaneID:    "plane-a",
						SystemNamespace:   "plane-a-nvca-system",
						RequestsNamespace: "plane-a-nvcf-backend",
					},
				}},
			},
			expectedID: "plane-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{operatorNamespace: tt.operatorNamespace, controlPlaneID: tt.operatorID}
			require.NoError(t, bc.validateAndApplyControlPlaneScope(tt.backend))
			assert.Equal(t, tt.expectedID, tt.backend.Spec.ClusterConfig.ControlPlaneID)
		})
	}
}

func TestResolveOperatorSystemNamespace(t *testing.T) {
	assert.Equal(t, "plane-a-nvca-operator",
		resolveOperatorSystemNamespace("plane-a", NVCAOperatorNamespace, false))
	assert.Equal(t, "custom-operator",
		resolveOperatorSystemNamespace("plane-a", "custom-operator", true))
	assert.Equal(t, NVCAOperatorNamespace,
		resolveOperatorSystemNamespace("", NVCAOperatorNamespace, false))
}

func TestResolveOperatorSecretMirrorSourceNamespace(t *testing.T) {
	assert.Equal(t, "plane-a-nvca-operator",
		resolveOperatorSecretMirrorSourceNamespace("plane-a", NVCAOperatorNamespace, false))
	assert.Equal(t, "shared-secrets",
		resolveOperatorSecretMirrorSourceNamespace("plane-a", "shared-secrets", true))
	assert.Equal(t, NVCAOperatorNamespace,
		resolveOperatorSecretMirrorSourceNamespace("", NVCAOperatorNamespace, false))
}

func TestScopeWebhookForControlPlane(t *testing.T) {
	webhook := admissionregistrationv1.MutatingWebhook{
		Name:              "mutate.nvca.nvcf.nvidia.io",
		NamespaceSelector: &metav1.LabelSelector{},
	}
	scopeMutatingWebhook(&webhook, "plane-a")
	assert.Equal(t, "plane-a.mutate.nvca.nvcf.nvidia.io", webhook.Name)
	assert.Equal(t, "plane-a", webhook.NamespaceSelector.MatchLabels[nvcatypes.ControlPlaneIDLabel])
}

func TestControlPlaneClusterResourceName(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{}
	assert.Equal(t, "nvca", controlPlaneClusterResourceName(nb, "nvca"))
	nb.Spec.ClusterConfig.ControlPlaneID = "plane-a"
	assert.Equal(t, "plane-a-nvca", controlPlaneClusterResourceName(nb, "nvca"))
}

func TestBackendK8sCacheBuilderPropagatesNamespaceAndControlPlane(t *testing.T) {
	builder := NewBackendK8sCacheBuilder().
		WithSystemNamespace("plane-a-nvca-operator").
		WithControlPlaneID("plane-a")

	assert.Equal(t, "plane-a-nvca-operator", builder.operatorNamespace)
	assert.Equal(t, "plane-a-nvca-operator", builder.systemNamespace)
	assert.Equal(t, "plane-a", builder.controlPlaneID)
}

func TestBackendK8sCacheStartPropagatesOperatorNamespace(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	defer cancel()

	cache, _, err := NewBackendK8sCacheBuilder().
		WithClients(mockKubeClients()).
		WithSystemNamespace("plane-a-nvca-operator").
		WithControlPlaneID("plane-a").
		Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, "plane-a-nvca-operator", cache.operatorNamespace)
	assert.Equal(t, "plane-a-nvca-operator", cache.systemNamespace)
	assert.Equal(t, "plane-a", cache.controlPlaneID)
}

func TestControlPlaneAppLabels(t *testing.T) {
	legacy := getAppLabels()
	assert.NotContains(t, legacy, nvcatypes.ControlPlaneIDLabel)

	named := getAppLabels("plane-a")
	assert.Equal(t, "plane-a", named[nvcatypes.ControlPlaneIDLabel])
}

func TestAgentConfigCarriesControlPlaneID(t *testing.T) {
	data, err := encodeAgentConfig(nvcaconfig.Config{}, nvcaconfig.Config{}, nil, agentHostOverrides{
		ControlPlaneID: "plane-a",
	})
	assert.NoError(t, err)
	assert.Contains(t, string(data), "controlPlaneID: plane-a")
}

func TestAgentConfigLegacyOmitsControlPlaneID(t *testing.T) {
	data, err := encodeAgentConfig(nvcaconfig.Config{}, nvcaconfig.Config{}, nil, agentHostOverrides{
		ICMSHostHeaderOverride: "legacy.example.test",
	})
	assert.NoError(t, err)
	assert.NotContains(t, string(data), "controlPlaneID")
}
