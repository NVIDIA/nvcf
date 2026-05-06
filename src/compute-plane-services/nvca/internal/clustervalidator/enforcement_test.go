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

package clustervalidator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// isPodReady
// ---------------------------------------------------------------------------

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			want: true,
		},
		{
			name: "not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			want: false,
		},
		{
			name: "no conditions",
			pod:  &corev1.Pod{},
			want: false,
		},
		{
			name: "other condition only",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPodReady(tt.pod))
		})
	}
}

// ---------------------------------------------------------------------------
// buildServerPod
// ---------------------------------------------------------------------------

func TestBuildServerPod(t *testing.T) {
	pod := buildServerPod("test-ns", "busybox:1.36")

	assert.Equal(t, enforcementServerPod, pod.Name)
	assert.Equal(t, "test-ns", pod.Namespace)
	assert.Equal(t, "server", pod.Labels["role"])
	assert.Equal(t, "netpol-test", pod.Labels["app"])
	require.Len(t, pod.Spec.Containers, 1)
	assert.Equal(t, "busybox:1.36", pod.Spec.Containers[0].Image)
	require.Len(t, pod.Spec.Containers[0].Ports, 1)
	assert.Equal(t, int32(enforcementTestPort), pod.Spec.Containers[0].Ports[0].ContainerPort)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
}

// ---------------------------------------------------------------------------
// buildProbePod
// ---------------------------------------------------------------------------

func TestBuildProbePod(t *testing.T) {
	pod := buildProbePod("test-ns", "probe-client-1", "busybox:1.36", "client", "10.0.0.5", 0)

	assert.Equal(t, "probe-client-1", pod.Name)
	assert.Equal(t, "test-ns", pod.Namespace)
	assert.Equal(t, "client", pod.Labels["role"])
	require.Len(t, pod.Spec.Containers, 1)
	assert.Equal(t, []string{"sh", "-c"}, pod.Spec.Containers[0].Command[:2])
	assert.Contains(t, pod.Spec.Containers[0].Command[2], "10.0.0.5")
	assert.NotContains(t, pod.Spec.Containers[0].Command[2], "sleep")
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

	allowed := buildProbePod("test-ns", "probe-allowed-1", "busybox:1.36", "allowed", "10.0.0.5", 0)
	assert.Equal(t, "allowed", allowed.Labels["role"])
}

func TestBuildProbePodWithSettleDelay(t *testing.T) {
	pod := buildProbePod("test-ns", "probe-egress-1", "busybox:1.36", "client", "10.0.0.5", 3)

	require.Len(t, pod.Spec.Containers, 1)
	cmd := pod.Spec.Containers[0].Command[2]
	assert.Contains(t, cmd, "sleep 3 && wget")
	assert.Contains(t, cmd, "10.0.0.5")
}

// ---------------------------------------------------------------------------
// Policy builders
// ---------------------------------------------------------------------------

func TestBuildDenyAllIngressPolicy(t *testing.T) {
	pol := buildDenyAllIngressPolicy("test-ns")

	assert.Equal(t, enforcementIngressPol, pol.Name)
	assert.Equal(t, "test-ns", pol.Namespace)
	assert.Equal(t, map[string]string{"role": "server"}, pol.Spec.PodSelector.MatchLabels)
	require.Len(t, pol.Spec.PolicyTypes, 1)
	assert.Equal(t, networkingv1.PolicyTypeIngress, pol.Spec.PolicyTypes[0])
	assert.Empty(t, pol.Spec.Ingress, "deny-all must have empty ingress list")
}

func TestBuildSelectiveAllowPolicy(t *testing.T) {
	pol := buildSelectiveAllowPolicy("test-ns")

	assert.Equal(t, enforcementIngressPol, pol.Name)
	assert.Equal(t, map[string]string{"role": "server"}, pol.Spec.PodSelector.MatchLabels)
	require.Len(t, pol.Spec.Ingress, 1)
	require.Len(t, pol.Spec.Ingress[0].From, 1)
	assert.Equal(t, map[string]string{"role": "allowed"},
		pol.Spec.Ingress[0].From[0].PodSelector.MatchLabels)
	require.Len(t, pol.Spec.Ingress[0].Ports, 1)

	proto := corev1.ProtocolTCP
	port := intstr.FromInt(enforcementTestPort)
	assert.Equal(t, &proto, pol.Spec.Ingress[0].Ports[0].Protocol)
	assert.Equal(t, &port, pol.Spec.Ingress[0].Ports[0].Port)
}

func TestBuildDenyAllEgressPolicy(t *testing.T) {
	pol := buildDenyAllEgressPolicy("test-ns")

	assert.Equal(t, enforcementEgressPol, pol.Name)
	assert.Equal(t, map[string]string{"role": "client"}, pol.Spec.PodSelector.MatchLabels)
	require.Len(t, pol.Spec.PolicyTypes, 1)
	assert.Equal(t, networkingv1.PolicyTypeEgress, pol.Spec.PolicyTypes[0])
	assert.Empty(t, pol.Spec.Egress, "deny-all must have empty egress list")
}

// ---------------------------------------------------------------------------
// waitForPodReady (with fake client, pre-set pod status)
// ---------------------------------------------------------------------------

func TestWaitForPodReady_AlreadyReady(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	client := fake.NewSimpleClientset(pod)
	err := waitForPodReady(context.Background(), client, "ns", "srv", 5*time.Second)
	assert.NoError(t, err)
}

func TestWaitForPodReady_Failed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	client := fake.NewSimpleClientset(pod)
	err := waitForPodReady(context.Background(), client, "ns", "srv", 5*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Failed phase")
}

func TestWaitForPodReady_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := waitForPodReady(ctx, client, "ns", "missing", 200*time.Millisecond)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// waitForPodDone (with fake client, pre-set pod status)
// ---------------------------------------------------------------------------

func TestWaitForPodDone_Succeeded(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	client := fake.NewSimpleClientset(pod)
	ok, err := waitForPodDone(context.Background(), client, "ns", "p", 5*time.Second)
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestWaitForPodDone_Failed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	client := fake.NewSimpleClientset(pod)
	ok, err := waitForPodDone(context.Background(), client, "ns", "p", 5*time.Second)
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestWaitForPodDone_Timeout(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewSimpleClientset(pod)
	_, err := waitForPodDone(context.Background(), client, "ns", "p", 100*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete")
}

// ---------------------------------------------------------------------------
// getPodIP
// ---------------------------------------------------------------------------

func TestGetPodIP(t *testing.T) {
	t.Run("has IP", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
			Status:     corev1.PodStatus{PodIP: "10.0.0.5"},
		}
		client := fake.NewSimpleClientset(pod)
		ip, err := getPodIP(context.Background(), client, "ns", "srv")
		assert.NoError(t, err)
		assert.Equal(t, "10.0.0.5", ip)
	})

	t.Run("no IP", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
			Status:     corev1.PodStatus{},
		}
		client := fake.NewSimpleClientset(pod)
		_, err := getPodIP(context.Background(), client, "ns", "srv")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no IP")
	})

	t.Run("pod not found", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		_, err := getPodIP(context.Background(), client, "ns", "missing")
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// Policy CRUD via fake client
// ---------------------------------------------------------------------------

func TestApplyDenyAllIngressPolicy(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := applyDenyAllIngressPolicy(context.Background(), client, "test-ns")
	assert.NoError(t, err)

	pol, err := client.NetworkingV1().NetworkPolicies("test-ns").Get(
		context.Background(), enforcementIngressPol, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, pol.Spec.Ingress)
}

func TestApplySelectiveAllowPolicy_CreateThenUpdate(t *testing.T) {
	client := fake.NewSimpleClientset()

	require.NoError(t, applyDenyAllIngressPolicy(context.Background(), client, "ns"))

	require.NoError(t, applySelectiveAllowPolicy(context.Background(), client, "ns"))
	pol, err := client.NetworkingV1().NetworkPolicies("ns").Get(
		context.Background(), enforcementIngressPol, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, pol.Spec.Ingress, 1)
	assert.Equal(t, map[string]string{"role": "allowed"},
		pol.Spec.Ingress[0].From[0].PodSelector.MatchLabels)
}

func TestApplySelectiveAllowPolicy_CreateWhenMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	require.NoError(t, applySelectiveAllowPolicy(context.Background(), client, "ns"))

	pol, err := client.NetworkingV1().NetworkPolicies("ns").Get(
		context.Background(), enforcementIngressPol, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, pol.Spec.Ingress, 1)
}

func TestDeleteNetworkPolicy(t *testing.T) {
	client := fake.NewSimpleClientset(buildDenyAllIngressPolicy("ns"))
	err := deleteNetworkPolicy(context.Background(), client, "ns", enforcementIngressPol)
	assert.NoError(t, err)

	_, err = client.NetworkingV1().NetworkPolicies("ns").Get(
		context.Background(), enforcementIngressPol, metav1.GetOptions{})
	assert.Error(t, err, "policy should be deleted")
}

func TestDeleteNetworkPolicy_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := deleteNetworkPolicy(context.Background(), client, "ns", "does-not-exist")
	assert.NoError(t, err, "deleting non-existent policy should not error")
}

// ---------------------------------------------------------------------------
// checkNetworkPolicyEnforcement — high-level
// ---------------------------------------------------------------------------

func TestNextProbe(t *testing.T) {
	env := &enforcementEnv{probeSeq: 0}
	assert.Equal(t, "probe-client-1", env.nextProbe("client"))
	assert.Equal(t, "probe-allowed-2", env.nextProbe("allowed"))
	assert.Equal(t, 2, env.probeSeq)
}

func TestCreateTestNamespace(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := createTestNamespace(context.Background(), client, "test-enforcement-ns")
	assert.NoError(t, err)

	ns, err := client.CoreV1().Namespaces().Get(context.Background(), "test-enforcement-ns", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "netpol-validation", ns.Labels["app"])
	assert.Equal(t, "enforcement-test", ns.Labels["purpose"])
}

func TestCreateServerPod(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := createServerPod(context.Background(), client, "ns", "busybox:1.36")
	assert.NoError(t, err)

	pod, err := client.CoreV1().Pods("ns").Get(context.Background(), enforcementServerPod, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "busybox:1.36", pod.Spec.Containers[0].Image)
}

func TestApplyDenyAllEgressPolicy(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := applyDenyAllEgressPolicy(context.Background(), client, "test-ns")
	assert.NoError(t, err)

	pol, err := client.NetworkingV1().NetworkPolicies("test-ns").Get(
		context.Background(), enforcementEgressPol, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, pol.Spec.Egress)
	assert.Equal(t, map[string]string{"role": "client"}, pol.Spec.PodSelector.MatchLabels)
}

func TestCleanupTestNamespace(t *testing.T) {
	t.Run("existing namespace", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "cleanup-ns"},
		})
		cleanupTestNamespace(testLog(), client, "cleanup-ns")
		_, err := client.CoreV1().Namespaces().Get(context.Background(), "cleanup-ns", metav1.GetOptions{})
		assert.Error(t, err)
	})

	t.Run("non-existent namespace", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		cleanupTestNamespace(testLog(), client, "does-not-exist")
	})
}

func TestCheckNetworkPolicyEnforcement_Disabled(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}

	checkNetworkPolicyEnforcement(context.Background(), client, state, nil)
	assert.Nil(t, state.EnforcementOK, "should be nil when not configured")

	checkNetworkPolicyEnforcement(context.Background(), client, state, &EnforcementConfig{Enabled: false})
	assert.Nil(t, state.EnforcementOK, "should be nil when disabled")
}

func TestCheckNetworkPolicyEnforcement_SetupFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}

	cfg := &EnforcementConfig{
		Enabled:        true,
		TestImage:      "busybox:1.36",
		TimeoutSeconds: 1,
	}
	checkNetworkPolicyEnforcement(context.Background(), client, state, cfg)
	assert.Nil(t, state.EnforcementOK, "should be nil when setup fails partway")
	assert.NotEmpty(t, state.Warnings)
}
