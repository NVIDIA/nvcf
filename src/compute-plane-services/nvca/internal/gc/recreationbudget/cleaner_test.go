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

package recreationbudget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNamespace = "nvca-system"

func newBudgetConfigMap(name string, timestamps ...time.Time) *corev1.ConfigMap {
	strs := make([]string, len(timestamps))
	for i, t := range timestamps {
		strs[i] = t.Format(time.RFC3339)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{purposeLabel: purposeValue, "nvca.nvcf.nvidia.io/function-id": "func-1"},
		},
		Data: map[string]string{timestampsKey: strings.Join(strs, ",")},
	}
}

func TestCleaner_Run_DeletesExpiredConfigMaps(t *testing.T) {
	expired := newBudgetConfigMap("expired", time.Now().Add(-recreationBudgetWindow-time.Minute))
	fresh := newBudgetConfigMap("fresh", time.Now())
	empty := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: testNamespace, Labels: map[string]string{purposeLabel: purposeValue}},
	}
	// Shares the generic function-id label with real budget ConfigMaps but
	// lacks the dedicated purpose label -- must never be touched by a
	// selector scoped specifically to this cleaner's own ConfigMaps.
	otherFeature := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-feature",
			Namespace: testNamespace,
			Labels:    map[string]string{"nvca.nvcf.nvidia.io/function-id": "func-1"},
		},
	}
	unrelated := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: testNamespace},
	}

	client := fake.NewSimpleClientset(expired, fresh, empty, otherFeature, unrelated)
	c := NewCleaner(client, testNamespace, nil)

	require.NoError(t, c.Run(context.Background()))

	_, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "expired", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expired ConfigMap should have been deleted")

	_, err = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "empty", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "ConfigMap with no timestamps should have been deleted")

	_, err = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "fresh", metav1.GetOptions{})
	assert.NoError(t, err, "ConfigMap with a recent timestamp should be kept")

	_, err = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "other-feature", metav1.GetOptions{})
	assert.NoError(t, err, "a ConfigMap sharing only the generic function-id label must not be swept up")

	_, err = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "unrelated", metav1.GetOptions{})
	assert.NoError(t, err, "ConfigMap without the recreation-budget label should never be touched")
}

func TestCleaner_Run_NothingExpired(t *testing.T) {
	fresh := newBudgetConfigMap("fresh", time.Now())
	client := fake.NewSimpleClientset(fresh)
	c := NewCleaner(client, testNamespace, nil)

	require.NoError(t, c.Run(context.Background()))

	_, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "fresh", metav1.GetOptions{})
	assert.NoError(t, err)
}

func TestCleaner_Run_SkipsDeleteWhenConfigMapChangedConcurrently(t *testing.T) {
	expired := newBudgetConfigMap("expired", time.Now().Add(-recreationBudgetWindow-time.Minute))
	client := fake.NewSimpleClientset(expired)

	// Simulate a legitimate reservation writing a fresh timestamp between
	// our List and this Delete: the real apiserver rejects a stale-
	// ResourceVersion delete with Conflict, and the cleaner must not treat
	// that as a hard failure or otherwise discard the concurrent write.
	client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteAction, ok := action.(k8stesting.DeleteActionImpl)
		require.True(t, ok)
		require.NotNil(t, deleteAction.DeleteOptions.Preconditions, "delete must set a ResourceVersion precondition")
		require.NotNil(t, deleteAction.DeleteOptions.Preconditions.ResourceVersion, "precondition must pin a ResourceVersion")
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "expired", assert.AnError)
	})

	c := NewCleaner(client, testNamespace, nil)
	require.NoError(t, c.Run(context.Background()), "a conflicting delete must not fail the cleaner run")

	_, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), "expired", metav1.GetOptions{})
	assert.NoError(t, err, "the ConfigMap must survive a conflicting delete instead of being force-removed")
}

func TestCleaner_Name(t *testing.T) {
	assert.Equal(t, "RecreationBudgetCleaner", NewCleaner(fake.NewSimpleClientset(), testNamespace, nil).Name())
}
