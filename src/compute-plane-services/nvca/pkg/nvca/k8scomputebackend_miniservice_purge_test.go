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

package nvca

import (
	"context"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func createPurgeTestMiniService(t *testing.T, hv2 client.Client, name string) {
	t.Helper()
	ms := &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: name}}
	require.NoError(t, hv2.Create(context.Background(), ms))
	ms.Status.Phase = v1alpha1.MiniServiceInstallFailed
	require.NoError(t, hv2.Status().Update(context.Background(), ms))
}

func newPurgeTestRequest(instanceID string) (*nvcav2beta1.ICMSRequest, nvcav2beta1.InstanceStatus) {
	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-1",
			FunctionDetails: function.Details{
				FunctionID:        testFunctionID,
				FunctionVersionID: testFunctionVersionID,
			},
		},
	}
	return req, nvcav2beta1.InstanceStatus{ID: instanceID}
}

func Test_GetICMSRequestUpdatesForMiniServiceRequest_PurgesWhenBudgetAvailable(t *testing.T) {
	instanceID := "sr-purge-available"
	clients := mockKubeClients()
	createPurgeTestMiniService(t, clients.HelmV2, instanceID)
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		Start(newTestContext())
	require.NoError(t, err)
	bc.systemNamespace = testRecreationBudgetNamespace
	c := K8sComputeBackend{clients: clients, bk8s: bc}
	req, st := newPurgeTestRequest(instanceID)

	_, err = c.GetICMSRequestUpdatesForMiniServiceRequest(newTestContext(), req, st)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	err = clients.HelmV2.Get(context.Background(), client.ObjectKey{Name: instanceID}, ms)
	assert.True(t, apierrors.IsNotFound(err), "MiniService should have been purged when budget was available")
}

func Test_GetICMSRequestUpdatesForMiniServiceRequest_SkipsPurgeWhenBudgetExhausted(t *testing.T) {
	instanceID := "sr-purge-exhausted"
	cmName := recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID)
	spent := formatPurgeTimestamps([]time.Time{time.Now(), time.Now(), time.Now()})
	budgetCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: testRecreationBudgetNamespace},
		Data:       map[string]string{recreationBudgetTimestampsKey: spent},
	}

	clients := mockKubeClients(budgetCM)
	createPurgeTestMiniService(t, clients.HelmV2, instanceID)
	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		Start(newTestContext())
	require.NoError(t, err)
	bc.systemNamespace = testRecreationBudgetNamespace
	c := K8sComputeBackend{clients: clients, bk8s: bc}
	req, st := newPurgeTestRequest(instanceID)

	_, err = c.GetICMSRequestUpdatesForMiniServiceRequest(newTestContext(), req, st)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	err = clients.HelmV2.Get(context.Background(), client.ObjectKey{Name: instanceID}, ms)
	require.NoError(t, err, "MiniService should still exist when the purge budget is exhausted")
}

func Test_GetICMSRequestUpdatesForMiniServiceRequest_SkipsPurgeWhenBudgetCheckErrors(t *testing.T) {
	instanceID := "sr-purge-budget-error"
	clients := mockKubeClients()
	createPurgeTestMiniService(t, clients.HelmV2, instanceID)

	fakeK8s, ok := clients.K8s.(*fake.Clientset)
	require.True(t, ok, "expected mockKubeClients to back K8s with a fake.Clientset")
	fakeK8s.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok || getAction.GetName() != recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID) {
			return false, nil, nil
		}
		return true, nil, apierrors.NewInternalError(assert.AnError)
	})

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		Start(newTestContext())
	require.NoError(t, err)
	bc.systemNamespace = testRecreationBudgetNamespace
	c := K8sComputeBackend{clients: clients, bk8s: bc}
	req, st := newPurgeTestRequest(instanceID)

	_, err = c.GetICMSRequestUpdatesForMiniServiceRequest(newTestContext(), req, st)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	err = clients.HelmV2.Get(context.Background(), client.ObjectKey{Name: instanceID}, ms)
	require.NoError(t, err, "MiniService should not be purged when the budget check itself errors (fail closed)")
}

func Test_GetICMSRequestUpdatesForMiniServiceRequest_ReleasesBudgetSlotWhenDeleteFails(t *testing.T) {
	instanceID := "sr-purge-delete-fails"
	clients := mockKubeClients()

	deleteCalls := 0
	clients.HelmV2 = ctrlfake.NewClientBuilder().
		WithScheme(newMiniServiceScheme()).
		WithStatusSubresource(&v1alpha1.MiniService{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deleteCalls++
				return apierrors.NewInternalError(assert.AnError)
			},
		}).
		Build()
	createPurgeTestMiniService(t, clients.HelmV2, instanceID)

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		Start(newTestContext())
	require.NoError(t, err)
	bc.systemNamespace = testRecreationBudgetNamespace
	c := K8sComputeBackend{clients: clients, bk8s: bc}
	req, st := newPurgeTestRequest(instanceID)

	_, err = c.GetICMSRequestUpdatesForMiniServiceRequest(newTestContext(), req, st)
	require.NoError(t, err)
	assert.Equal(t, 1, deleteCalls, "Delete should have been attempted")

	ms := &v1alpha1.MiniService{}
	err = clients.HelmV2.Get(context.Background(), client.ObjectKey{Name: instanceID}, ms)
	require.NoError(t, err, "MiniService should still exist since Delete failed")

	cm, err := clients.K8s.CoreV1().ConfigMaps(testRecreationBudgetNamespace).
		Get(context.Background(), recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID), metav1.GetOptions{})
	require.NoError(t, err)
	remaining := recentPurgeTimestamps(cm.Data[recreationBudgetTimestampsKey])
	assert.Empty(t, remaining, "the reserved slot must be released when Delete fails, not left consumed for a purge that never happened")
}
