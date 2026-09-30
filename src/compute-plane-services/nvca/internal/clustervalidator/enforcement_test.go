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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
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
	ok, _, err := waitForPodDone(context.Background(), client, "ns", "p", 5*time.Second)
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestWaitForPodDone_Failed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	client := fake.NewSimpleClientset(pod)
	ok, _, err := waitForPodDone(context.Background(), client, "ns", "p", 5*time.Second)
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestWaitForPodDone_Timeout(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewSimpleClientset(pod)
	_, _, err := waitForPodDone(context.Background(), client, "ns", "p", 100*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete")
}

// A single 429 must not decide the result: the Get is retried in the deadline.
func TestWaitForPodDone_RetriesTransientErrors(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	client := fake.NewSimpleClientset(pod)
	calls := 0
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewTooManyRequestsError("slow down")
		}
		return false, nil, nil
	})
	ok, _, err := waitForPodDone(context.Background(), client, "ns", "p", 10*time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Greater(t, calls, 1)
}

// A missing pod cannot reappear, so it returns at once instead of waiting out
// the deadline.
func TestWaitForPodDone_NotFoundIsTerminal(t *testing.T) {
	start := time.Now()
	_, _, err := waitForPodDone(context.Background(), fake.NewSimpleClientset(), "ns", "p", 30*time.Second)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// hangingClient is a real clientset against an apiserver that never answers,
// holding each request until the client gives up on it.
func hangingClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	// Close waits for in-flight handlers, and a hung Get that outlived
	// finishesWithin would hold this one forever. Dropping the connections
	// first cancels the request context the handler is blocked on.
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)
	return client
}

// finishesWithin fails the test if fn has not returned after limit, instead of
// letting a hung wait run into the package timeout.
func finishesWithin(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("did not return within %v", limit)
	}
}

// The client sets no request timeout, so a hung Get must be bounded by the
// wait's own deadline, not left to block the check forever.
func TestWaitForPodReady_HungGetEndsAtTheDeadline(t *testing.T) {
	client := hangingClient(t)
	finishesWithin(t, 5*time.Second, func() {
		err := waitForPodReady(context.Background(), client, "ns", "srv", time.Second)
		assert.Error(t, err)
	})
}

func TestWaitForPodDone_HungGetEndsAtTheDeadline(t *testing.T) {
	client := hangingClient(t)
	finishesWithin(t, 6*time.Second, func() {
		_, _, err := waitForPodDone(context.Background(), client, "ns", "p", time.Second)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not complete",
			"a timed-out Get is transient, so the deadline ends the wait")
	})
}

// A spent budget returns at once without another API call.
func TestWaitForPod_ExpiredBudgetMakesNoCall(t *testing.T) {
	client := fake.NewSimpleClientset()
	calls := 0
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		return false, nil, nil
	})
	require.Error(t, waitForPodReady(context.Background(), client, "ns", "srv", 0))
	_, _, err := waitForPodDone(context.Background(), client, "ns", "p", 0)
	require.Error(t, err)
	assert.Zero(t, calls)
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

// ---------------------------------------------------------------------------
// sweepOrphanTestNamespaces
// ---------------------------------------------------------------------------

func makeNetpolValidationNs(name string, age time.Duration) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            map[string]string{"app": "netpol-validation", "purpose": "enforcement-test"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
	}
}

func TestSweepOrphanTestNamespaces_DeletesOldNamespaces(t *testing.T) {
	// An orphan namespace older than the TTL should be deleted.
	client := fake.NewSimpleClientset(
		makeNetpolValidationNs("netpol-validation-12345", 2*time.Hour),
	)
	sweepOrphanTestNamespaces(context.Background(), testLog(), client, 1*time.Hour)

	_, err := client.CoreV1().Namespaces().Get(
		context.Background(), "netpol-validation-12345", metav1.GetOptions{})
	assert.Error(t, err, "old namespace should have been deleted by the sweep")
}

func TestSweepOrphanTestNamespaces_KeepsRecentNamespaces(t *testing.T) {
	// A namespace younger than the TTL might belong to a concurrent run —
	// leave it alone.
	client := fake.NewSimpleClientset(
		makeNetpolValidationNs("netpol-validation-99999", 5*time.Minute),
	)
	sweepOrphanTestNamespaces(context.Background(), testLog(), client, 1*time.Hour)

	_, err := client.CoreV1().Namespaces().Get(
		context.Background(), "netpol-validation-99999", metav1.GetOptions{})
	assert.NoError(t, err, "recent namespace should NOT have been deleted")
}

func TestSweepOrphanTestNamespaces_IgnoresUnrelatedNamespaces(t *testing.T) {
	// Namespaces without the netpol-validation- prefix must be left alone
	// even if they have the matching label (defensive).
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "kube-system",
				Labels:            map[string]string{"app": "netpol-validation"},
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
		},
	)
	sweepOrphanTestNamespaces(context.Background(), testLog(), client, 1*time.Hour)

	_, err := client.CoreV1().Namespaces().Get(
		context.Background(), "kube-system", metav1.GetOptions{})
	assert.NoError(t, err, "non-netpol-validation namespace must not be deleted")
}

func TestSweepOrphanTestNamespaces_MixedAges(t *testing.T) {
	// Multiple orphans of varying ages — only the old ones get deleted.
	client := fake.NewSimpleClientset(
		makeNetpolValidationNs("netpol-validation-old-1", 3*time.Hour),
		makeNetpolValidationNs("netpol-validation-old-2", 90*time.Minute),
		makeNetpolValidationNs("netpol-validation-new-1", 10*time.Minute),
		makeNetpolValidationNs("netpol-validation-new-2", 30*time.Minute),
	)
	sweepOrphanTestNamespaces(context.Background(), testLog(), client, 1*time.Hour)

	ctx := context.Background()
	_, err1 := client.CoreV1().Namespaces().Get(ctx, "netpol-validation-old-1", metav1.GetOptions{})
	_, err2 := client.CoreV1().Namespaces().Get(ctx, "netpol-validation-old-2", metav1.GetOptions{})
	_, err3 := client.CoreV1().Namespaces().Get(ctx, "netpol-validation-new-1", metav1.GetOptions{})
	_, err4 := client.CoreV1().Namespaces().Get(ctx, "netpol-validation-new-2", metav1.GetOptions{})
	assert.Error(t, err1, "old-1 should be deleted")
	assert.Error(t, err2, "old-2 should be deleted")
	assert.NoError(t, err3, "new-1 should remain")
	assert.NoError(t, err4, "new-2 should remain")
}

// One poll attempt is bounded by the per-attempt cap and by the time left, but
// never below the floor, so the final attempt still gets sent.
func TestAttemptContext_BoundsEachCall(t *testing.T) {
	remaining := func(deadline time.Time) time.Duration {
		ctx, cancel := attemptContext(context.Background(), deadline)
		defer cancel()
		d, ok := ctx.Deadline()
		require.True(t, ok)
		return time.Until(d)
	}
	assert.LessOrEqual(t, remaining(time.Now().Add(time.Hour)), pollAttemptTimeout)
	assert.Less(t, remaining(time.Now().Add(3*time.Second)), 4*time.Second)
	assert.Greater(t, remaining(time.Now().Add(-time.Minute)), 500*time.Millisecond)
}

// A slow or throttled Get is not the pod's answer, so it is retried inside the
// deadline. Failing on it aborted the whole enforcement check.
func TestWaitForPodReady_RetriesTransientErrors(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	})
	calls := 0
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewTooManyRequestsError("slow down")
		}
		return false, nil, nil
	})
	require.NoError(t, waitForPodReady(context.Background(), client, "ns", "srv", 10*time.Second))
	assert.Greater(t, calls, 1)

	denied := fake.NewSimpleClientset()
	denied.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "srv", fmt.Errorf("denied"))
	})
	start := time.Now()
	require.Error(t, waitForPodReady(context.Background(), denied, "ns", "srv", 30*time.Second))
	assert.Less(t, time.Since(start), 5*time.Second, "a denial cannot change inside the deadline")
}
