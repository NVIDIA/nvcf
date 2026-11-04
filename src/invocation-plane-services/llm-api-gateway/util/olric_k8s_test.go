/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package util

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestPodAddrs_Filters exercises the pure filter (no Kubernetes API involved).
// It is the single place that enforces the Running+Ready+HasIP contract, so
// every rule gets its own row.
func TestPodAddrs_Filters(t *testing.T) {
	ready := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}
	notReady := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}

	pods := &corev1.PodList{Items: []corev1.Pod{
		pod("running-ready", corev1.PodRunning, "10.0.0.1", ready),
		pod("running-not-ready", corev1.PodRunning, "10.0.0.2", notReady),
		pod("pending-with-ip", corev1.PodPending, "10.0.0.3", ready),
		pod("running-no-conditions", corev1.PodRunning, "10.0.0.4"),
		pod("running-ready-no-ip", corev1.PodRunning, "", ready),
		pod("succeeded", corev1.PodSucceeded, "10.0.0.5", ready),
		pod("failed", corev1.PodFailed, "10.0.0.6", ready),
	}}

	got := podAddrs(pods)
	require.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.4"}, got,
		"want only Running pods with either Ready=True or no Ready condition, and a non-empty IP")
}

func TestPodAddrs_Nil(t *testing.T) {
	require.Nil(t, podAddrs(nil))
	require.Empty(t, podAddrs(&corev1.PodList{}))
}

// TestK8sDiscovery_DiscoverPeers_UsesLabelSelector confirms that the label
// selector is actually pushed down to the API server (via the fake client) and
// that unmatched pods are filtered out before pod-status filtering runs. If
// someone drops the LabelSelector accidentally, this test catches it.
func TestK8sDiscovery_DiscoverPeers_UsesLabelSelector(t *testing.T) {
	ready := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}

	matching := pod("gw-1", corev1.PodRunning, "10.1.0.1", ready)
	matching.Labels = map[string]string{"app.kubernetes.io/part-of": "llm-api-gateway"}

	matchingSyncWorker := pod("gw-2", corev1.PodRunning, "10.1.0.2", ready)
	matchingSyncWorker.Labels = map[string]string{"app.kubernetes.io/part-of": "llm-api-gateway"}

	other := pod("other", corev1.PodRunning, "10.1.0.3", ready)
	other.Labels = map[string]string{"app.kubernetes.io/part-of": "unrelated"}

	client := fake.NewSimpleClientset(&matching, &matchingSyncWorker, &other)

	d := &k8sDiscovery{
		ctx:           context.Background(),
		namespace:     "default",
		labelSelector: "app.kubernetes.io/part-of=llm-api-gateway",
		clientset:     client,
	}

	addrs, err := d.DiscoverPeers()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"10.1.0.1", "10.1.0.2"}, addrs)
}

// TestK8sDiscovery_DiscoverPeers_EmptyIsError guards the "rolling restart"
// corner case: if the selector legitimately matches zero ready pods we want
// Olric to retry, not to form a solo cluster.
func TestK8sDiscovery_DiscoverPeers_EmptyIsError(t *testing.T) {
	client := fake.NewSimpleClientset()

	d := &k8sDiscovery{
		ctx:           context.Background(),
		namespace:     "default",
		labelSelector: "app.kubernetes.io/part-of=llm-api-gateway",
		clientset:     client,
	}

	_, err := d.DiscoverPeers()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ready peers")
}

// TestNewK8sDiscovery_RejectsEmptySelector guards the API contract: callers
// must pass a non-empty selector, because an empty one would match every pod
// in the namespace (sync worker, gateway, anything else sharing the namespace)
// and form wildly incorrect clusters.
func TestNewK8sDiscovery_RejectsEmptySelector(t *testing.T) {
	_, err := NewK8sDiscovery(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "labelSelector must not be empty")
}

// TestNewK8sDiscovery_RequiresPodNamespace guards the other half of the API
// contract: POD_NAMESPACE is how we know which namespace to list. Without it
// we'd silently use the default namespace, which is never what we want.
func TestNewK8sDiscovery_RequiresPodNamespace(t *testing.T) {
	t.Setenv(PodNamespaceEnv, "")
	_, err := NewK8sDiscovery(context.Background(), "app=x")
	require.Error(t, err)
	require.Contains(t, err.Error(), PodNamespaceEnv)
}

func pod(name string, phase corev1.PodPhase, ip string, conds ...corev1.PodCondition) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status: corev1.PodStatus{
			Phase:      phase,
			PodIP:      ip,
			Conditions: conds,
		},
	}
}
