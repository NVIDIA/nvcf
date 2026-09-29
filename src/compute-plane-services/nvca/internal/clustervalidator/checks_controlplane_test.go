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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// -- checkStorageClass --

func TestCheckStorageClass_DefaultPresent(t *testing.T) {
	client := fake.NewSimpleClientset(&storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "standard",
			Annotations: map[string]string{
				"storageclass.kubernetes.io/is-default-class": "true",
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.True(t, *state.DefaultStorageClassOK, "a StorageClass with the default annotation must set DefaultStorageClassOK=true")
	assert.Empty(t, state.Recommendations)
}

func TestCheckStorageClass_BetaAnnotationAlsoAccepted(t *testing.T) {
	client := fake.NewSimpleClientset(&storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "local-path",
			Annotations: map[string]string{
				"storageclass.beta.kubernetes.io/is-default-class": "true",
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.True(t, *state.DefaultStorageClassOK)
}

func TestCheckStorageClass_NoDefault(t *testing.T) {
	client := fake.NewSimpleClientset(&storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "no-annotation-class"},
	})
	state := &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.False(t, *state.DefaultStorageClassOK, "StorageClass without default annotation must set DefaultStorageClassOK=false")
	assert.NotEmpty(t, state.Recommendations, "missing default StorageClass must add a recommendation")
}

func TestCheckStorageClass_NoStorageClasses(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.False(t, *state.DefaultStorageClassOK)
}

// -- checkGatewayAPICRDs --
// The fake discovery client does not populate ServerResourcesForGroupVersion,
// so checkGatewayAPICRDs will always see the group as absent.
// We test that it runs without panic and sets GatewayAPICRDsOK=false.

func TestCheckGatewayAPICRDs_AbsentOnFakeClient(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)

	require.NotNil(t, state.GatewayAPICRDsOK,
		"GatewayAPICRDsOK must be set even when discovery returns an error")
	assert.False(t, *state.GatewayAPICRDsOK,
		"absent Gateway API CRDs must set GatewayAPICRDsOK=false")
	assert.NotEmpty(t, state.Recommendations)
}

// -- checkEnvoyGateway --

// makeEnvoyControllerPod builds a pod carrying the controller label the check
// selects on. ready=false yields a pod that is Running but not Ready, which is
// what a CrashLoopBackOff controller looks like via the API.
func makeEnvoyControllerPod(name string, ready bool) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: envoyGatewayNamespaceName(),
			Labels:    map[string]string{"control-plane": "envoy-gateway"},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
		},
	}
}

func TestCheckEnvoyGateway_ReadyControllerPod(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespaceName()}},
		makeEnvoyControllerPod("envoy-gateway-abc", true),
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.True(t, *state.EnvoyGatewayOK, "a Ready controller pod must set EnvoyGatewayOK=true")
}

// A crash-looping controller keeps .status.phase == Running, so the check must
// key on the Ready condition or a dead gateway reports healthy.
func TestCheckEnvoyGateway_RunningButNotReadyFails(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespaceName()}},
		makeEnvoyControllerPod("envoy-gateway-abc", false),
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK,
		"Running-but-not-Ready controller (CrashLoopBackOff) must not pass")
}

// The data-plane proxies and the certgen Job share this namespace. Counting
// them lets a dead controller pass, so they must be excluded by the selector.
func TestCheckEnvoyGateway_IgnoresNonControllerPods(t *testing.T) {
	dataPlane := makePod("envoy-envoy-gateway-system-eg-abc123", envoyGatewayNamespaceName(), corev1.PodRunning)
	dataPlane.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespaceName()}},
		dataPlane,
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK,
		"a Ready data-plane proxy must not satisfy the controller check")
}

func TestCheckEnvoyGateway_NamespaceAbsent(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK, "absent namespace must set EnvoyGatewayOK=false")
	assert.NotEmpty(t, state.Recommendations)
}

func TestCheckEnvoyGateway_NamespacePresentNoRunningPods(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespaceName()}},
		makePod("envoy-gateway-abc", envoyGatewayNamespaceName(), corev1.PodPending),
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK, "no running pods must set EnvoyGatewayOK=false")
}

// -- checkGatewayRoutes --

func TestCheckGatewayRoutes_MissingCRDs(t *testing.T) {
	// Fake client with no gateway.networking.k8s.io group registered.
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkGatewayRoutes(context.Background(), client, state)
	require.NotNil(t, state.GatewayRoutesOK)
	assert.False(t, *state.GatewayRoutesOK, "missing route CR types must set GatewayRoutesOK=false")
}

// -- checkExternalLoadBalancer --

func TestCheckExternalLoadBalancer_ServiceWithIP(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway", Namespace: envoyGatewayNamespaceName()},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}},
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK, "a LB service with an assigned IP must set ExternalLBOK=true")
}

func TestCheckExternalLoadBalancer_ServiceWithHostname(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway", Namespace: envoyGatewayNamespaceName()},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK, "a LB service with a hostname must set ExternalLBOK=true")
}

func TestCheckExternalLoadBalancer_NoLBServices(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-ip-svc", Namespace: envoyGatewayNamespaceName()},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK, "no LB service must set ExternalLBOK=false")
	assert.NotEmpty(t, state.Warnings)
}

func TestCheckExternalLoadBalancer_LBServicePendingNoIP(t *testing.T) {
	// LB type but .status.loadBalancer.ingress is empty -> no IP assigned yet.
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-lb", Namespace: envoyGatewayNamespaceName()},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		// No Status.LoadBalancer.Ingress
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK, "LB service with no assigned IP must set ExternalLBOK=false")
}

// -- checkNodeToNode --

func TestCheckNodeToNode_NoNodes(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK,
		"zero schedulable nodes exercised no overlay path, so the result must be unknown, not Verified")
	assert.NotEmpty(t, state.Warnings, "skip must add a warning so the banner is qualified")
}

func TestCheckNodeToNode_UnschedulableNodesSkipped(t *testing.T) {
	// Two nodes but both unschedulable; should also skip.
	n1 := makeNode("node-1", true, 0)
	n1.Spec.Unschedulable = true
	n2 := makeNode("node-2", true, 0)
	n2.Spec.Unschedulable = true

	client := fake.NewSimpleClientset(n1, n2)
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK, "no schedulable nodes means the probe never ran")
}

func TestCheckNodeToNode_TaintedNodeExcluded(t *testing.T) {
	// Three nodes: two schedulable, one with a NoSchedule taint.
	// DesiredNumberScheduled=2 (tainted node excluded by scheduler), so
	// waitForDaemonSetPods must converge on 2 pods, not 3. If the old
	// len(schedulable)=3 path were used the test would block until deadline.
	n1 := makeNode("node-1", true, 0)
	n2 := makeNode("node-2", true, 0)
	n3 := makeNode("node-3", true, 0)
	n3.Spec.Taints = []corev1.Taint{{
		Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule,
	}}

	client := fake.NewSimpleClientset(n1, n2, n3)

	// Capture DaemonSet labels (which include a random suffix) so the pod-list
	// reactor can return pods that survive FakePods.List label filtering.
	// capturedLabels is set synchronously by the daemonset create reactor
	// before any list call, so no synchronisation is needed.
	var capturedLabels map[string]string
	var capturedName, capturedNS string
	// Return a ZEROED status, exactly as a real apiserver does: the DaemonSet
	// controller populates status asynchronously, so the Create response never
	// carries DesiredNumberScheduled. Reading it here would yield 0 and fall
	// back to len(schedulable)=3, which is the regression this guards.
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		capturedLabels = ds.Labels
		capturedName = ds.Name
		capturedNS = ds.Namespace
		ds.Status = appsv1.DaemonSetStatus{}
		return true, ds, nil
	})

	// The subsequent Get is where the reconciled status appears: 2, because the
	// scheduler excludes the tainted node.
	client.PrependReactor("get", "daemonsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       capturedName,
				Namespace:  capturedNS,
				Labels:     capturedLabels,
				Generation: 1,
			},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 2,
				ObservedGeneration:     1,
			},
		}, nil
	})

	// Return 2 Running pods whose labels match the DaemonSet selector.
	// FakePods.List filters by label after the reactor returns, so pods must
	// carry the full label set including the random instance suffix.
	client.PrependReactor("list", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		lbl := capturedLabels
		return true, &corev1.PodList{Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: capturedNS, Labels: lbl},
				Spec:       corev1.PodSpec{NodeName: "node-1"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-2", Namespace: capturedNS, Labels: lbl},
				Spec:       corev1.PodSpec{NodeName: "node-2"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
			},
		}}, nil
	})

	// Deny checker pod creation so the test exits quickly without needing to
	// simulate full pod lifecycle (no Get/poll needed). A 403 here is also the
	// checker-side admission guard: it must stay UNKNOWN, not fail the overlay.
	var checkerPodCreateCalled bool
	client.PrependReactor("create", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		checkerPodCreateCalled = true
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("exceeded quota"))
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	// checkerPodCreateCalled must be true: if the check had sized its wait from
	// len(schedulable)=3 rather than polling for DesiredNumberScheduled=2, it
	// would have timed out before reaching pod creation and this flag would
	// stay false, catching the regression.
	require.True(t, checkerPodCreateCalled, "check must reach checker pod creation step")
	assert.Nil(t, state.NodeToNodeOK, "a checker that was never admitted is not evidence about the overlay")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "checker pod was not admitted")
}

// Any rejection of the probe DaemonSet means the probe never ran. Classifying
// by 403 alone reported a Kyverno, ValidatingAdmissionPolicy or fail-closed
// webhook denial as a broken overlay, while a quota 403 read as RBAC.
func TestCheckNodeToNode_DaemonSetCreateRejectionIsUnknown(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "daemonsets"}
	cases := map[string]error{
		"quota 403":      apierrors.NewForbidden(gr, "", fmt.Errorf("exceeded quota")),
		"kyverno 400":    apierrors.NewBadRequest("image busybox:1.36 is not allowed"),
		"vap 422":        apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "DaemonSet"}, "x", nil),
		"webhook 500":    apierrors.NewInternalError(fmt.Errorf("failed calling webhook")),
		"webhook 503":    apierrors.NewServiceUnavailable("webhook timed out"),
		"transport fail": fmt.Errorf("connection reset by peer"),
	}
	for name, createErr := range cases {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
			client.PrependReactor("create", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, createErr
			})
			state := &ValidationState{Log: testLog()}
			checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

			assert.Nil(t, state.NodeToNodeOK, "a probe that was never admitted must not fail the overlay")
			assert.Contains(t, strings.Join(state.Warnings, "; "), "probe DaemonSet was not admitted")
		})
	}
}

// The probe creates a namespace, a DaemonSet, and a pod on every node. If the
// deferred cleanup regresses, every validator run leaks all three, so assert
// the deletes are actually issued rather than only that the verdict is right.
func TestCheckNodeToNode_CleansUpProbeResources(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("node-1", true, 0),
		makeNode("node-2", true, 0),
	)

	var createdNS string
	client.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace)
		createdNS = ns.Name
		return false, nil, nil // fall through to the tracker
	})

	deleted := map[string]bool{}
	for _, res := range []string{"namespaces", "daemonsets", "pods"} {
		r := res
		client.PrependReactor("delete", r, func(_ ktesting.Action) (bool, runtime.Object, error) {
			deleted[r] = true
			return false, nil, nil
		})
	}

	// Fail the DaemonSet status poll fast so the test does not wait out the
	// real timeout; cleanup must still run on this path.
	// Forbidden is terminal for the poll; a generic error is retried for the
	// full 30s status timeout.
	client.PrependReactor("get", "daemonsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "daemonsets"}, "", fmt.Errorf("simulated status read failure"))
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	require.NotEmpty(t, createdNS, "probe must create its own namespace, not use default")
	assert.True(t, strings.HasPrefix(createdNS, nodeToNodeNSPrefix),
		"probe namespace %q must carry the sweepable prefix %q", createdNS, nodeToNodeNSPrefix)
	assert.True(t, deleted["daemonsets"], "deferred cleanup must delete the server DaemonSet")
	assert.True(t, deleted["pods"], "deferred cleanup must delete the checker pod")
	assert.True(t, deleted["namespaces"], "deferred cleanup must delete the probe namespace")
}

// An orphaned probe namespace older than the TTL must be reclaimed; one inside
// the TTL belongs to a possibly-concurrent run and must be left alone.
func TestSweepOrphanN2NNamespaces_TTL(t *testing.T) {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
		"app.kubernetes.io/component":  "n2n-probe",
	}
	stale := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              nodeToNodeNSPrefix + "stale1",
		Labels:            labels,
		CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
	}}
	fresh := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              nodeToNodeNSPrefix + "fresh1",
		Labels:            labels,
		CreationTimestamp: metav1.NewTime(time.Now()),
	}}
	client := fake.NewSimpleClientset(stale, fresh)

	sweepOrphanN2NNamespaces(context.Background(), testLog(), client, orphanN2NNamespaceTTL)

	_, err := client.CoreV1().Namespaces().Get(context.Background(), stale.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "namespace older than the TTL must be swept")

	_, err = client.CoreV1().Namespaces().Get(context.Background(), fresh.Name, metav1.GetOptions{})
	assert.NoError(t, err, "namespace inside the TTL may belong to a concurrent run and must survive")
}

// -- checkTier1Deployments --

func TestCheckTier1Deployments_AllReady(t *testing.T) {
	replicas := int32(2)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api", Namespace: "nvcf"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			UpdatedReplicas:    2,
			ReadyReplicas:      2,
		},
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.Empty(t, state.Warnings)
}

func TestCheckTier1Deployments_UnderReplicated(t *testing.T) {
	replicas := int32(2)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nvcf-api", Namespace: "nvcf",
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			UpdatedReplicas:    2,
			ReadyReplicas:      1, // one pod crashed
		},
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "crashed pod must set Tier1DeploymentsOK=false")
}

func TestCheckTier1Deployments_RollingOutEmitsWarningNotFailure(t *testing.T) {
	replicas := int32(2)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nvcf-api", Namespace: "nvcf",
			Generation: 3, // new spec written
		},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, // controller hasn't caught up yet
			UpdatedReplicas:    1, // only 1 of 2 pods updated
			ReadyReplicas:      2, // old pods still serving (maxUnavailable=0)
		},
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	// Tolerated, not unknown: nil is reserved for "did not observe", and a
	// rollout at full ready count hides nothing. Reporting it as an unobserved
	// critical check made every control-plane upgrade NVCF-Not-Ready.
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	require.NotEmpty(t, state.Warnings, "rollout in progress must emit a warning")
	assert.Contains(t, state.Warnings[0], "rollout in progress")
}

// A stalled rollout is not transient: Kubernetes caps the new ReplicaSet at
// maxSurge and never progresses, so ProgressDeadlineExceeded must fall through
// to the replica check rather than being skipped forever as "in progress".
func TestCheckTier1Deployments_StalledRolloutFails(t *testing.T) {
	replicas := int32(3)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api", Namespace: "nvcf", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			UpdatedReplicas:    1, // wedged on a bad image, never reaches 3
			ReadyReplicas:      0,
			Conditions: []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentProgressing,
				Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded",
			}},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"a rollout that exceeded its progress deadline with 0 ready must fail, not be skipped")
}

// With one Deployment mid-rollout and another fully ready, the ready one must
// still be evaluated: the rollout skip must not suppress the whole check.
func TestCheckTier1Deployments_MixedRollingAndUnderReplicated(t *testing.T) {
	two := int32(2)
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "nvcf", Generation: 3},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, UpdatedReplicas: 1, ReadyReplicas: 2,
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "degraded", Namespace: "sis", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 1,
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"the under-replicated Deployment must still fail the check alongside a rolling one")
}

func TestCheckTier1Deployments_PreInstallPassesTrivially(t *testing.T) {
	client := fake.NewSimpleClientset() // no namespaces, no deployments
	state := &ValidationState{Log: testLog(), Preflight: true}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "pre-install (no deployments) must pass trivially")
}

// Outside preflight the control plane is installed, so finding no Deployment
// in any of its namespaces is a failure: a relocated release or an unset
// override, not a pre-install cluster.
func TestCheckTier1Deployments_NoDeploymentsAfterInstallFails(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(), nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.NotEmpty(t, state.Recommendations)
}

// An RBAC denial means the namespace was never observed. Treating it as a pass
// publishes a green quorum for a control plane nobody looked at.
func TestCheckTier1Deployments_ForbiddenIsNotAPass(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "deployments", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "deployments"}, "", fmt.Errorf("denied"))
	})

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	assert.Nil(t, state.Tier1DeploymentsOK, "a 403 in every namespace must not reach the trivial-pass exit")
	assert.NotEmpty(t, state.Warnings, "an RBAC denial must be surfaced as a warning")
}

// -- checkTier2StatefulSets --

// makeQuorumSTS builds a StatefulSet plus the pods its selector matches, so the
// co-location scan has something to walk. nodes gives one node name per pod.
func makeQuorumSTS(name, ns string, replicas, ready int32, nodes []string) []runtime.Object {
	sel := map[string]string{"app": name}
	// IsControlledBy compares the controller reference UID, so the fixture needs
	// a real one on both sides.
	uid := types.UID("uid-" + name)
	controller := true
	objs := []runtime.Object{&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: sel},
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas:   ready,
			CurrentRevision: name + "-r1",
			UpdateRevision:  name + "-r1",
		},
	}}
	for i, node := range nodes {
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%d", name, i), Namespace: ns, Labels: sel,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "StatefulSet",
					Name: name, UID: uid, Controller: &controller,
				}},
			},
			Spec: corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}
	return objs
}

func TestCheckTier2StatefulSets_HealthyQuorum(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "3 Ready pods on distinct nodes must pass")
}

func TestCheckTier2StatefulSets_BelowQuorumFails(t *testing.T) {
	objs := makeQuorumSTS("openbao", "vault-system", 3, 2, []string{"node-1", "node-2"})
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "readyReplicas below spec.replicas must fail")
}

// Two peers on one node means a single node loss takes out the quorum, which is
// the whole point of the placement half of this check.
func TestCheckTier2StatefulSets_CoLocatedPeersFail(t *testing.T) {
	objs := makeQuorumSTS("cassandra", "cassandra-system", 3, 3, []string{"node-1", "node-1", "node-2"})
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "two peers on the same node must fail placement")
}

// StatefulSets roll one pod at a time, so a below-target ready count is the
// steady state for the whole duration of any upgrade. That must warn, not fail.
func TestCheckTier2StatefulSets_RollingUpdateWarnsNotFails(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"})
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Status.UpdateRevision = "nats-r2" // differs from CurrentRevision
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	// Tolerated, not unknown: rolling one pod at a time is what an upgrade
	// looks like, and nil is reserved for "did not observe".
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	require.NotEmpty(t, state.Warnings)
	assert.Contains(t, state.Warnings[0], "rolling update in progress")
}

// Requiring exactly 3 silently drops an operator-scaled 5-member Cassandra from
// the check instead of validating it.
func TestCheckTier2StatefulSets_FiveReplicasStillChecked(t *testing.T) {
	objs := makeQuorumSTS("cassandra", "cassandra-system", 5, 4,
		[]string{"node-1", "node-2", "node-3", "node-4"})
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK,
		"a 5-replica quorum with only 4 Ready must fail, not be skipped")
}

// An even-replica StatefulSet is not a quorum shape this check can reason
// about, but it is not nothing either: a 4-replica Cassandra ring with RF=3 can
// have lost quorum. When it is the only StatefulSet present, the tier must be
// unknown rather than a pass that certifies a ring nothing examined.
func TestCheckTier2StatefulSets_EvenReplicasNotAssessed(t *testing.T) {
	objs := makeQuorumSTS("worker", "nvcf", 4, 2, []string{"node-1", "node-2"})
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"nothing was assessed, so the tier is unknown, not a pass")
	require.NotEmpty(t, state.Warnings, "the skipped StatefulSet must be surfaced")
	assert.Contains(t, state.Warnings[0], "nvcf/worker")
}

// An odd-replica quorum member alongside an even-replica one still gets
// assessed; the even one is reported as a warning rather than silently dropped.
func TestCheckTier2StatefulSets_EvenReplicasWarnAlongsideQuorum(t *testing.T) {
	objs := makeQuorumSTS("worker", "nvcf", 4, 2, []string{"node-1", "node-2"})
	objs = append(objs, makeQuorumSTS("nats", "nats-system", 3, 3,
		[]string{"node-1", "node-2", "node-3"})...)
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "the odd-replica quorum member is healthy")
	joined := strings.Join(state.Warnings, "; ")
	assert.Contains(t, joined, "nvcf/worker", "the even-replica StatefulSet must still be reported")
}

func TestCheckTier2StatefulSets_ForbiddenIsNotAPass(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "statefulsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "", fmt.Errorf("denied"))
	})

	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"a 403 in every namespace must not publish a green quorum")
	assert.NotEmpty(t, state.Warnings)
}

// A denial in only some namespaces still means part of the control plane was
// never observed, so the healthy remainder must not be published as a pass.
func TestCheckTier2StatefulSets_PartialDenialIsNotAPass(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	client := fake.NewSimpleClientset(objs...)
	client.PrependReactor("list", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "vault-system" {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "", fmt.Errorf("denied"))
		}
		return false, nil, nil
	})

	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"a healthy StatefulSet elsewhere must not mask an unreadable namespace")
	assert.NotEmpty(t, state.Warnings)
}

func TestCheckTier1Deployments_PartialDenialIsNotAPass(t *testing.T) {
	two := int32(2)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api", Namespace: "nvcf", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &two},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2,
		},
	})
	client.PrependReactor("list", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "sis" {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "", fmt.Errorf("denied"))
		}
		return false, nil, nil
	})

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	assert.Nil(t, state.Tier1DeploymentsOK,
		"a ready Deployment elsewhere must not mask an unreadable namespace")
	assert.NotEmpty(t, state.Warnings)
}

// A pod matching the selector but not owned by this StatefulSet, or not Ready,
// must not be counted: either would turn a healthy quorum into a false
// co-location failure.
func TestCheckTier2StatefulSets_IgnoresUnownedAndUnreadyPods(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})

	// A surplus pod on an already-used node: matches the selector, but is
	// neither owned by the StatefulSet nor Ready.
	stray := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stray", Namespace: "nats-system", Labels: map[string]string{"app": "nats"},
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
	client := fake.NewSimpleClientset(append(objs, stray)...)

	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK,
		"an unowned, not-Ready pod on an occupied node must not be read as a co-located peer")
}

// An owner reference naming the StatefulSet but belonging to another kind must
// not count: matching on name alone would let its pod cause a co-location
// failure on a healthy quorum.
func TestCheckTier2StatefulSets_IgnoresSameNamedOwnerOfAnotherKind(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	controller := true
	impostor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "impostor", Namespace: "nats-system", Labels: map[string]string{"app": "nats"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment",
				Name: "nats", UID: types.UID("some-other-uid"), Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	client := fake.NewSimpleClientset(append(objs, impostor)...)

	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK,
		"a Ready pod owned by a same-named Deployment must not be treated as a StatefulSet peer")
}

// A StatefulSet one pod down mid-rollout is tolerated: the row passes, but the
// rollout is reported in the warnings rather than hidden behind a healthy peer.
func TestCheckTier2StatefulSets_HealthyPeerDoesNotMaskRollingOne(t *testing.T) {
	healthy := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	rolling := makeQuorumSTS("openbao", "vault-system", 3, 2, []string{"node-1", "node-2"})
	rolling[0].(*appsv1.StatefulSet).Status.UpdateRevision = "openbao-r2"

	client := fake.NewSimpleClientset(append(healthy, rolling...)...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK,
		"a tolerated rollout is a pass with a warning, not an unobserved check")
	assert.NotEmpty(t, state.Warnings)
}

// Same shape for Tier-1: a ready Deployment does not certify one still rolling.
// A mid-rollout Deployment that is still serving its full replica count hides
// nothing, so it must not pin this critical row to UNKNOWN. rollingOut is not
// self-limiting: progressDeadlineSeconds=2147483647 and a wedged controller
// both stay "rolling" forever without ever setting ProgressDeadlineExceeded.
func TestCheckTier1Deployments_RollingAtFullReplicasStillPasses(t *testing.T) {
	two := int32(2)
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "nvcf", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2,
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "sis", Generation: 3},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, UpdatedReplicas: 1, ReadyReplicas: 2,
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK,
		"a rollout at full ready count must not leave the tier permanently unknown")
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.NotEmpty(t, state.Warnings, "the in-flight rollout is still reported")
}

// A mid-rollout Deployment below its target but at or above its rollout floor
// is tolerated: the row passes with a warning.
func TestCheckTier1Deployments_RollingAndUnderReplicatedIsTolerated(t *testing.T) {
	two := int32(2)
	oneUnavailable := intstr.FromInt(1)
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "nvcf", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2,
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "sis", Generation: 3},
			// maxUnavailable=1 makes 1/2 a state the controller really
			// produces mid-rollout; the 25% default never drops below 2/2.
			Spec: appsv1.DeploymentSpec{Replicas: &two, Strategy: appsv1.DeploymentStrategy{
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &oneUnavailable},
			}},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, UpdatedReplicas: 1, ReadyReplicas: 1,
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	// A mid-rollout Deployment below its target is what rolling one pod at a
	// time looks like, so it is tolerated with a warning rather than reported
	// as unobserved.
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	require.NotEmpty(t, state.Warnings)
}

func TestControlPlaneNamespaceSet_HonoursOpenBaoOverride(t *testing.T) {
	t.Setenv(openBaoNamespaceEnv, "vault-system-dev")
	assert.Contains(t, controlPlaneNamespaceSet(), "vault-system-dev")

	t.Setenv(openBaoNamespaceEnv, "vault-system") // already in the base list
	set := controlPlaneNamespaceSet()
	count := 0
	for _, ns := range set {
		if ns == "vault-system" {
			count++
		}
	}
	assert.Equal(t, 1, count, "an override matching the default must not duplicate the entry")
}

// A Deployment scaled to zero satisfies "ReadyReplicas >= spec.replicas" with
// nothing running, so counting it as healthy lets a maintenance scale-down or a
// replicaCount:0 values error publish the critical row as All Ready.
func TestCheckTier1Deployments_ScaledToZeroIsNotReady(t *testing.T) {
	zero := int32(0)
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "scaled-down", Namespace: "nvcf", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &zero},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"a control plane whose only Deployment is scaled to zero is not ready")
}

// Alongside a healthy peer the scale-down still fails the row.
func TestCheckTier1Deployments_ScaledToZeroFailsAlongsideHealthy(t *testing.T) {
	zero, two := int32(0), int32(2)
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "scaled-down", Namespace: "nvcf", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &zero},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "sis", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2,
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	// A failure, not a warning. This is the replicaCount:0 values error, and
	// the same Deployment at 2/3 already fails, so warning here made "fully
	// down" score better than "degraded".
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"a Deployment scaled to zero is down, so the tier is not ready")
}

// A revision mismatch can be permanent (a non-zero partition, a wedged
// rollout). At full ready count that must not pin the tier to UNKNOWN, and more
// than one pod down is beyond what rolling one at a time explains.
func TestCheckTier2StatefulSets_PermanentRevisionMismatchAtFullReadyPasses(t *testing.T) {
	objs := makeQuorumSTS("openbao", "vault-system", 3, 3,
		[]string{"node-1", "node-2", "node-3"})
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Status.CurrentRevision = "rev-1"
	sts.Status.UpdateRevision = "rev-2"
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK,
		"a permanent revision mismatch at full ready count must not pin the tier to unknown")
	assert.True(t, *state.Tier2StatefulSetsOK)
}

func TestCheckTier2StatefulSets_RevisionMismatchTwoPodsDownFails(t *testing.T) {
	objs := makeQuorumSTS("openbao", "vault-system", 3, 1, []string{"node-1"})
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Status.CurrentRevision = "rev-1"
	sts.Status.UpdateRevision = "rev-2"
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK,
		"losing two of three peers is a quorum finding, not an in-flight rollout")
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// A DaemonSet stranded in "default" by a validator version that predates the
// per-run probe namespace is invisible to the namespace sweep, so it would
// otherwise persist forever as one probe pod per node.
func TestSweepLegacyOrphanN2NDaemonSets_DeletesStaleOnly(t *testing.T) {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
		"app.kubernetes.io/component":  "n2n-server",
	}
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	fresh := metav1.NewTime(time.Now())
	client := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: nodeToNodeDSName + "-ab12cd", Namespace: "default",
			Labels: labels, CreationTimestamp: old,
		}},
	)
	sweepLegacyOrphanN2NDaemonSets(context.Background(), testLog(), client, orphanN2NNamespaceTTL)
	_, err := client.AppsV1().DaemonSets("default").Get(
		context.Background(), nodeToNodeDSName+"-ab12cd", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a stale legacy DaemonSet must be reclaimed")

	// A DaemonSet inside the TTL may belong to a concurrent run.
	client2 := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: nodeToNodeDSName + "-ef34gh", Namespace: "default",
			Labels: labels, CreationTimestamp: fresh,
		}},
	)
	sweepLegacyOrphanN2NDaemonSets(context.Background(), testLog(), client2, orphanN2NNamespaceTTL)
	_, err = client2.AppsV1().DaemonSets("default").Get(
		context.Background(), nodeToNodeDSName+"-ef34gh", metav1.GetOptions{})
	assert.NoError(t, err, "a DaemonSet inside the TTL must be left alone")
}

// The labels are three public constants, so the generated name is required too
// before deleting anything from a shared namespace.
func TestSweepLegacyOrphanN2NDaemonSets_RequiresTheGeneratedName(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: "operator-owned", Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
				"app.kubernetes.io/component":  "n2n-server",
			},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		}},
	)
	sweepLegacyOrphanN2NDaemonSets(context.Background(), testLog(), client, orphanN2NNamespaceTTL)
	_, err := client.AppsV1().DaemonSets("default").Get(
		context.Background(), "operator-owned", metav1.GetOptions{})
	assert.NoError(t, err, "an object that does not carry our generated name must not be deleted")
}

// From Kubernetes 1.26 the apiserver resolves multiple defaults by picking the
// newest, so PVCs bind and failing the critical row reports NVCF-Not-Ready on a
// working cluster. Mid-CSI-migration clusters (gp2 plus gp3) hit this.
func TestCheckStorageClass_MultipleDefaultsWarnOnModernKubernetes(t *testing.T) {
	mk := func(name string) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
		}}
	}
	client := fake.NewSimpleClientset(mk("gp2"), mk("gp3"))
	state := &ValidationState{Log: testLog(), K8sVersion: "v1.30.0"}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.True(t, *state.DefaultStorageClassOK,
		"the apiserver picks the newest default, so PVCs still bind")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "Multiple default StorageClasses")
}

func TestCheckStorageClass_MultipleDefaultsFailBefore126(t *testing.T) {
	mk := func(name string) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
		}}
	}
	client := fake.NewSimpleClientset(mk("a"), mk("b"))
	state := &ValidationState{Log: testLog(), K8sVersion: "v1.25.9"}
	checkStorageClass(context.Background(), client, state)

	require.NotNil(t, state.DefaultStorageClassOK)
	assert.False(t, *state.DefaultStorageClassOK,
		"below 1.26 two defaults reject every PVC")
}

// Only NotFound is evidence Envoy is absent. A 403 or an apiserver 500 means we
// never observed it, so the row must be unknown rather than a definite failure.
func TestCheckEnvoyGateway_APIErrorIsUnknownNotFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "namespaces"}, envoyGatewayNamespace, fmt.Errorf("denied"))
	})
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	assert.Nil(t, state.EnvoyGatewayOK,
		"a denial is not evidence that Envoy Gateway is missing")
	assert.NotEmpty(t, state.Warnings)
}

func TestCheckEnvoyGateway_NotFoundStillFails(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK,
		"an absent namespace is a real observation")
	assert.False(t, *state.EnvoyGatewayOK)
}

// Envoy Gateway provisions one proxy Service per Gateway and the stack defines
// several, so a partially satisfied address pool must not pass on the strength
// of its assigned siblings.
func TestCheckExternalLoadBalancer_PendingServiceIsNotAPass(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "shared-gw,nats-gw")
	assigned := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "envoy-gateway-lb", Namespace: envoyGatewayNamespaceName(),
			Labels: map[string]string{owningGatewayNameLabel: "shared-gw"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}},
		}},
	}
	pending := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "envoy-nats-gateway-lb", Namespace: envoyGatewayNamespaceName(),
			Labels: map[string]string{owningGatewayNameLabel: "nats-gw"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	client := fake.NewSimpleClientset(assigned, pending)
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK,
		"a Gateway still waiting on an address is the failure this check exists to catch")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "envoy-nats-gateway-lb")
}

func lbService(name string, gateway, gatewayNS, ip string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envoyGatewayNamespaceName(), Labels: map[string]string{}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	if gateway != "" {
		svc.Labels[owningGatewayNameLabel] = gateway
		svc.Labels[owningGatewayNamespaceLabel] = gatewayNS
	}
	if ip != "" {
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
	}
	return svc
}

// Envoy Gateway puts every Gateway's proxy Service in its namespace, so another
// team's Service at <pending> must not fail a healthy NVCF gateway.
func TestCheckExternalLoadBalancer_ForeignPendingGatewayIgnored(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gateway/shared-gw")
	client := fake.NewSimpleClientset(
		lbService("envoy-nvcf", "shared-gw", "gateway", "203.0.113.1"),
		lbService("envoy-team-b", "team-b-gw", "team-b", ""),
		// Same Gateway name in another namespace: the namespace/name entry excludes it.
		lbService("envoy-other-shared", "shared-gw", "other", ""),
	)
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "envoy-team-b")
}

// Named Gateways with no proxy Service at all is a failure, not a pass on
// whatever else happens to hold an address.
func TestCheckExternalLoadBalancer_NamedGatewayMissing(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "shared-gw")
	client := fake.NewSimpleClientset(lbService("envoy-team-b", "team-b-gw", "team-b", "203.0.113.9"))
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "shared-gw")
}

// Without Gateway names the check cannot attribute a pending Service, so it
// warns and points at the setting instead of failing on a foreign Service.
func TestCheckExternalLoadBalancer_UnnamedPendingOnlyWarns(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "")
	client := fake.NewSimpleClientset(
		lbService("envoy-nvcf", "shared-gw", "gateway", "203.0.113.1"),
		lbService("envoy-team-b", "team-b-gw", "team-b", ""),
	)
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), nvcfGatewayNamesEnv)
}

// The install hint must name the namespace the check actually looked in.
func TestCheckEnvoyGateway_RemediationUsesConfiguredNamespace(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "gateway")
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), fake.NewSimpleClientset(), state)

	joined := strings.Join(state.Recommendations, "; ")
	assert.Contains(t, joined, "-n gateway ")
	assert.NotContains(t, joined, "envoy-gateway-system")
}

// The stack exposes controllerNamespace with no default, so an install can
// place Envoy outside envoy-gateway-system. Probing the wrong namespace
// reports a live gateway as missing.
func TestEnvoyGatewayNamespaceName_HonoursOverride(t *testing.T) {
	// Pin the unset case explicitly: an override exported in the developer's
	// shell would otherwise leak in and make this assert the wrong default.
	t.Setenv(envoyGatewayNamespaceEnv, "")
	assert.Equal(t, envoyGatewayNamespace, envoyGatewayNamespaceName())
	t.Setenv(envoyGatewayNamespaceEnv, "gateway")
	assert.Equal(t, "gateway", envoyGatewayNamespaceName())
	assert.Contains(t, controlPlaneNamespaceSet(), "gateway",
		"the relocated namespace must also be covered by the Tier checks")
}

// The probe DaemonSet needs the same tolerations the validator CronJob carries:
// the DaemonSet controller auto-tolerates not-ready and unschedulable but not
// the control-plane taint, so a dedicated control plane schedules zero pods.
func TestBuildNodeToNodeDaemonSet_ToleratesControlPlaneTaint(t *testing.T) {
	ds := buildNodeToNodeDaemonSet("n2n", "ns", map[string]string{"a": "b"}, "img")
	var keys []string
	for _, tol := range ds.Spec.Template.Spec.Tolerations {
		keys = append(keys, tol.Key)
	}
	assert.Contains(t, keys, "node-role.kubernetes.io/control-plane")
	assert.Contains(t, keys, "node-role.kubernetes.io/master")

	pod := buildNodeToNodeCheckerPod("checker", "ns", "node-1", []string{"10.0.0.1"}, "img")
	assert.NotEmpty(t, pod.Spec.Tolerations,
		"NodeName bypasses the scheduler but not taint admission")
}

// gatewayDiscoveryClient returns a fake clientset whose discovery surface
// serves exactly the given "<groupVersion>/<resource>" pairs. Until this
// existed no test in the package populated a discovery surface at all, so
// every pinned route pair and the hasPair lookup itself were uncovered: the
// Gateway tests passed identically against a bare fake, which serves nothing.
func gatewayDiscoveryClient(pairs ...string) *fake.Clientset {
	client := fake.NewSimpleClientset()
	byGV := map[string][]metav1.APIResource{}
	for _, p := range pairs {
		i := strings.LastIndex(p, "/")
		gv, res := p[:i], p[i+1:]
		byGV[gv] = append(byGV[gv], metav1.APIResource{Name: res})
	}
	disco := client.Discovery().(*fakediscovery.FakeDiscovery)
	for gv, resources := range byGV {
		disco.Resources = append(disco.Resources, &metav1.APIResourceList{
			GroupVersion: gv,
			APIResources: resources,
		})
	}
	return client
}

// The full set the charts actually apply, at the versions the manifests pin.
func gatewayRequiredPairs() []string {
	return []string{
		gatewayAPIGroup + "/v1/gatewayclasses",
		gatewayAPIGroup + "/v1/gateways",
		gatewayAPIGroup + "/v1/httproutes",
		gatewayAPIGroup + "/v1/grpcroutes",
		gatewayAPIGroup + "/v1alpha2/tcproutes",
		gatewayAPIGroup + "/v1beta1/referencegrants",
	}
}

func TestCheckGatewayAPICRDs_AllRequiredPairsPresent(t *testing.T) {
	client := gatewayDiscoveryClient(gatewayRequiredPairs()...)
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)

	require.NotNil(t, state.GatewayAPICRDsOK)
	assert.True(t, *state.GatewayAPICRDsOK)
}

// TCPRoute ships only in the experimental channel, and the chart renders one by
// default, so a standard-channel cluster genuinely cannot apply the stack.
func TestCheckGatewayAPICRDs_StandardChannelMissingTCPRouteFails(t *testing.T) {
	var pairs []string
	for _, p := range gatewayRequiredPairs() {
		if !strings.HasSuffix(p, "/tcproutes") {
			pairs = append(pairs, p)
		}
	}
	client := gatewayDiscoveryClient(pairs...)
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)

	require.NotNil(t, state.GatewayAPICRDsOK)
	assert.False(t, *state.GatewayAPICRDsOK)
	assert.Contains(t, strings.Join(state.Recommendations, "; "), "experimental-install.yaml",
		"the remediation must name the channel, not point back at the installer that just failed")
}

// The version is part of the requirement: a CRD served only under some other
// version still fails the Helm apply.
func TestCheckGatewayAPICRDs_WrongVersionForPinnedRouteFails(t *testing.T) {
	var pairs []string
	for _, p := range gatewayRequiredPairs() {
		if strings.HasSuffix(p, "/httproutes") {
			p = gatewayAPIGroup + "/v1beta1/httproutes" // charts pin v1
		}
		pairs = append(pairs, p)
	}
	client := gatewayDiscoveryClient(pairs...)
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)

	require.NotNil(t, state.GatewayAPICRDsOK)
	assert.False(t, *state.GatewayAPICRDsOK,
		"httproutes served only at v1beta1 does not satisfy a v1 pin")
}

// UDPRoute is rendered only when routes.llmWorker.enabled, which defaults to
// false, so its absence must not fail the critical CRD check. It still has to
// be reported somewhere, which is the non-critical route row's job.
func TestCheckGatewayRoutes_OptionalUDPRouteAbsentIsNonCritical(t *testing.T) {
	client := gatewayDiscoveryClient(gatewayRequiredPairs()...)

	crdState := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, crdState)
	require.NotNil(t, crdState.GatewayAPICRDsOK)
	assert.True(t, *crdState.GatewayAPICRDsOK,
		"a missing opt-in route type must not fail the critical check")

	routeState := &ValidationState{Log: testLog()}
	checkGatewayRoutes(context.Background(), client, routeState)
	require.NotNil(t, routeState.GatewayRoutesOK)
	assert.False(t, *routeState.GatewayRoutesOK)
	assert.Contains(t, strings.Join(routeState.Warnings, "; "), "udproutes",
		"UDPRoute is applied by udproute-llm-worker.yaml and must still be surfaced")
}

func TestCheckGatewayRoutes_OptionalUDPRoutePresentPasses(t *testing.T) {
	client := gatewayDiscoveryClient(
		append(gatewayRequiredPairs(), gatewayAPIGroup+"/v1alpha2/udproutes")...)
	state := &ValidationState{Log: testLog()}
	checkGatewayRoutes(context.Background(), client, state)

	require.NotNil(t, state.GatewayRoutesOK)
	assert.True(t, *state.GatewayRoutesOK)
}

// An RBAC gap on pods must not be reported as a broken quorum, which is what
// appending it to failures did, while the identical gap on statefulsets is
// correctly reported as unknown.
func TestCheckTier2StatefulSets_PodListDenialIsUnknownNotFailure(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3,
		[]string{"node-1", "node-2", "node-3"})
	client := fake.NewSimpleClientset(objs...)
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
	})
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"an unreadable pod list is not evidence of a broken quorum")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "placement check")
}

// Every scheduled pod must come up. Accepting a subset reported the overlay as
// Verified while a Ready node's pod sat in ContainerCreating with no IP, which
// is the one fault this check exists to catch.
func TestWaitForDaemonSetPods_RequiresEveryScheduledPod(t *testing.T) {
	labels := map[string]string{"app": "n2n"}
	mk := func(name, node string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "probe", Labels: labels},
			Spec:       corev1.PodSpec{NodeName: node},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0." + node[len(node)-1:]},
		}
	}
	// Three scheduled, two Running: the third node's CNI never gave it an IP,
	// so its pod sits in ContainerCreating.
	stuck := probePod("node-3", "Pending", "", "ContainerCreating")
	stuck.ObjectMeta = metav1.ObjectMeta{Name: "c", Namespace: "probe", Labels: labels}
	client := fake.NewSimpleClientset(mk("a", "node-1"), mk("b", "node-2"), &stuck)
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels})

	_, err := waitForDaemonSetPods(context.Background(), client, "probe", selector, 3, time.Second)
	require.Error(t, err, "a node whose probe pod never started must not be silently skipped")
	assert.Contains(t, err.Error(), "timed out waiting for 3 Running pods")

	// All three up: the wait succeeds.
	client2 := fake.NewSimpleClientset(mk("a", "node-1"), mk("b", "node-2"), mk("c", "node-3"))
	pods, err := waitForDaemonSetPods(context.Background(), client2, "probe", selector, 3, time.Second)
	require.NoError(t, err)
	assert.Len(t, pods, 3)
}

func TestWaitForDaemonSetPods_RetriesTransientErrors(t *testing.T) {
	labels := map[string]string{"app": "n2n"}
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "probe", Labels: labels},
			Spec:       corev1.PodSpec{NodeName: "node-1"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "probe", Labels: labels},
			Spec:       corev1.PodSpec{NodeName: "node-2"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
	)
	calls := 0
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewTooManyRequestsError("slow down")
		}
		return false, nil, nil
	})
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels})

	pods, err := waitForDaemonSetPods(context.Background(), client, "probe", selector, 2, 30*time.Second)
	require.NoError(t, err, "a single 429 must not abort a 30s wait")
	assert.Len(t, pods, 2)
	assert.Greater(t, calls, 1, "the loop must have retried")
}

// A pod list that keeps failing until the deadline observed nothing, so it
// must not read as pods that failed to come up.
func TestWaitForDaemonSetPods_UnreadableListIsNotObserved(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequestsError("slow down")
	})
	_, err := waitForDaemonSetPods(context.Background(), client, "probe", "app=n2n", 2, time.Second)
	var unobserved *probeNotObservedError
	require.ErrorAs(t, err, &unobserved)
	assert.Contains(t, unobserved.reason, "listing DaemonSet pods")
}

// A permission error is terminal: retrying it just burns the deadline.
func TestWaitForDaemonSetPods_ForbiddenIsTerminal(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
	})
	start := time.Now()
	_, err := waitForDaemonSetPods(context.Background(), client, "probe", "app=n2n", 2, 30*time.Second)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "a denial must return immediately")
}

// Supersedes TestCheckNodeToNode_SingleNode_Skip, which asserted the opposite.
// A single-node control plane has no cross-node path to exercise, so the
// overlay requirement is vacuously met. Leaving the pointer nil would make it
// a critical UNKNOWN, which fails the verdict, so a k3d or single-node control
// plane would report NVCF-Not-Ready on every tick.
func TestCheckNodeToNode_SingleNodeIsNotApplicableNotUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "only-node"}},
	)
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	// Not a pass: no cross-node packet was sent. Reporting Verified here is
	// the regression Vaibhav's "sets NodeToNodeOK = true having sent zero
	// packets" thread already closed once.
	assert.Nil(t, state.NodeToNodeOK, "an unexercised check must not be reported as passed")
	assert.NotEmpty(t, state.NodeToNodeNotApplicable,
		"one schedulable node is not applicable, not unobserved")
}

// A cordoned second node leaves one schedulable node: same reasoning.
func TestCheckNodeToNode_AllButOneCordonedIsNotApplicable(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
			Spec:       corev1.NodeSpec{Unschedulable: true},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	assert.Nil(t, state.NodeToNodeOK)
	assert.NotEmpty(t, state.NodeToNodeNotApplicable)
}

// An RBAC denial on the probe DaemonSet is genuinely unobserved, so it must
// stay UNKNOWN. This is the case the operator ClusterRole now grants for.
func TestCheckNodeToNode_DaemonSetDenialStaysUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}},
	)
	client.PrependReactor("create", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "daemonsets"}, "", fmt.Errorf("denied"))
	})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	assert.Nil(t, state.NodeToNodeOK,
		"a denial is not evidence the overlay works, so it must not pass")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "not admitted")
}

// A checker whose status cannot be read produced no result, so the overlay
// stays unobserved rather than failing on the first Get error.
func TestCheckNodeToNode_UnreadableCheckerIsUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
	var dsLabels map[string]string
	var dsName, dsNS string
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		dsLabels, dsName, dsNS = ds.Labels, ds.Name, ds.Namespace
		return true, ds, nil
	})
	client.PrependReactor("get", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: dsName, Namespace: dsNS, Generation: 1},
			Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, ObservedGeneration: 1},
		}, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: dsNS, Labels: dsLabels},
				Spec:       corev1.PodSpec{NodeName: "node-1"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-2", Namespace: dsNS, Labels: dsLabels},
				Spec:       corev1.PodSpec{NodeName: "node-2"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
			},
		}}, nil
	})
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "checker pod did not report a result")
}

func probePod(node, phase string, ip string, waiting string) corev1.Pod {
	p := corev1.Pod{
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodPhase(phase), PodIP: ip},
	}
	if waiting != "" {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}},
		}}
	}
	return p
}

// Only a pod the node could not network is evidence about the overlay. A pod
// that could not pull its image, start its container, be scheduled, or be
// created at all says nothing about it.
func TestClassifyUnstartedProbePods(t *testing.T) {
	running := probePod("node-1", "Running", "10.0.0.1", "")
	cases := []struct {
		name       string
		pods       []corev1.Pod
		want       int
		unobserved bool
		recommend  bool
	}{
		{"pods never created", []corev1.Pod{running}, 3, true, false},
		{"image pull backoff", []corev1.Pod{running, probePod("node-2", "Pending", "", "ImagePullBackOff")}, 2, true, true},
		{"first pull error", []corev1.Pod{running, probePod("node-2", "Pending", "", "ErrImagePull")}, 2, true, true},
		{"container cannot start", []corev1.Pod{running, probePod("node-2", "Running", "", "CrashLoopBackOff")}, 2, true, false},
		{"cni never gave an IP", []corev1.Pod{running, probePod("node-2", "Pending", "", "ContainerCreating")}, 2, false, false},
		{"running without IP", []corev1.Pod{running, probePod("node-2", "Running", "", "")}, 2, false, false},
		// A slow image pull reports ContainerCreating after the sandbox, and
		// its IP, came up: the network worked.
		{"networked but still starting", []corev1.Pod{running, probePod("node-2", "Pending", "10.0.0.9", "ContainerCreating")}, 2, true, false},
		{"not scheduled", []corev1.Pod{running, probePod("", "Pending", "", "")}, 2, true, false},
		{
			"network fault beside a pull fault still fails",
			[]corev1.Pod{
				running,
				probePod("node-2", "Pending", "", "ImagePullBackOff"),
				probePod("node-3", "Pending", "", "ContainerCreating"),
			},
			3, false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUnstartedProbePods(tc.pods, tc.want)
			if !tc.unobserved {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.recommend, got.recommendation != "")
		})
	}

	unschedulable := probePod("node-2", "Pending", "", "")
	unschedulable.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}
	got := classifyUnstartedProbePods([]corev1.Pod{running, unschedulable}, 2)
	require.NotNil(t, got, "a pod the scheduler could not place never ran")
	assert.Contains(t, got.reason, "node-2: Unschedulable")
}

// A probe stuck in ImagePullBackOff is reported as not observed, with advice
// naming the dedicated image setting, instead of "got 0 on 0 node(s)".
func TestWaitForDaemonSetPods_ImagePullIsNotObserved(t *testing.T) {
	labels := map[string]string{"app": "n2n"}
	mk := func(name, node string) *corev1.Pod {
		p := probePod(node, "Pending", "", "ImagePullBackOff")
		p.ObjectMeta = metav1.ObjectMeta{Name: name, Namespace: "probe", Labels: labels}
		return &p
	}
	client := fake.NewSimpleClientset(mk("a", "node-1"), mk("b", "node-2"))
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels})

	_, err := waitForDaemonSetPods(context.Background(), client, "probe", selector, 2, time.Second)
	var unobserved *probeNotObservedError
	require.ErrorAs(t, err, &unobserved)
	assert.Contains(t, unobserved.recommendation, nodeToNodeImageEnv)
}

func TestNodeToNodeProbeImage_DedicatedOverrideWins(t *testing.T) {
	cfg := &NetworkCheckConfig{Enforcement: &EnforcementConfig{TestImage: "mirror/enforce:1"}}

	t.Setenv(nodeToNodeImageEnv, "")
	assert.Equal(t, enforcementDefaultImg, nodeToNodeProbeImage(nil))
	assert.Equal(t, "mirror/enforce:1", nodeToNodeProbeImage(cfg))

	t.Setenv(nodeToNodeImageEnv, " mirror/busybox:1.36 ")
	assert.Equal(t, "mirror/busybox:1.36", nodeToNodeProbeImage(cfg))
}

// A relocated component's namespace REPLACES the default. Appending left the
// default in the set, so after moving OpenBao the Tier checks still assessed
// whatever foreign workload now occupies vault-system.
func TestControlPlaneNamespaceSet_OverrideReplacesRatherThanAdds(t *testing.T) {
	t.Setenv(openBaoNamespaceEnv, "openbao")
	t.Setenv(envoyGatewayNamespaceEnv, "gateway")
	got := controlPlaneNamespaceSet()

	assert.Contains(t, got, "openbao")
	assert.Contains(t, got, "gateway")
	assert.NotContains(t, got, "vault-system",
		"a foreign Vault in the default namespace must not be assessed as ours")
	assert.NotContains(t, got, envoyGatewayNamespace,
		"a foreign Envoy in the default namespace must not be assessed as ours")
	assert.Contains(t, got, "nvcf", "unrelated namespaces are untouched")
}

func TestControlPlaneNamespaceSet_DefaultsWhenUnset(t *testing.T) {
	t.Setenv(openBaoNamespaceEnv, "")
	t.Setenv(envoyGatewayNamespaceEnv, "")
	got := controlPlaneNamespaceSet()
	assert.Contains(t, got, "vault-system")
	assert.Contains(t, got, envoyGatewayNamespace)
}

// A reconciled desired=0 means no node tolerates the probe. That is an answer,
// not a failure to observe one: requiring > 0 turned a fully tainted cluster
// into a blocking critical UNKNOWN after a 30s wait.
func TestWaitForDaemonSetDesiredCount_ReconciledZeroIsAnAnswer(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "n2n", Namespace: "probe", Generation: 1},
		Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 0},
	})
	got, err := waitForDaemonSetDesiredCount(context.Background(), client, "probe", "n2n", time.Second)
	require.NoError(t, err, "a reconciled zero target must not be reported as never published")
	assert.Equal(t, 0, got)
}

// An Envoy controller with no Ready pods must add a warning, or printSummary
// prints the green "meets all requirements" banner above its own failing row.
func TestCheckEnvoyGateway_NoReadyPodsAddsWarning(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespaceName()}},
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK)
	assert.NotEmpty(t, state.Warnings,
		"the banner is chosen from len(state.Warnings), so a failing row must add one")
}

// A known quorum component below three replicas has lost quorum and must fail
// rather than vanishing from the critical row.
func TestCheckTier2StatefulSets_KnownQuorumComponentBelowThreeFails(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 0, 0, nil)
	objs = append(objs, makeQuorumSTS("openbao", "vault-system", 3, 3,
		[]string{"node-1", "node-2", "node-3"})...)
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK,
		"NATS scaled to zero is the message bus down, not a shape to skip")
}

// OnDelete never advances CurrentRevision, so a mismatch there is the steady
// state and says nothing about readiness. It must not mask a down peer.
func TestCheckTier2StatefulSets_OnDeleteMismatchIsNotARollout(t *testing.T) {
	objs := makeQuorumSTS("openbao", "vault-system", 3, 2, []string{"node-1", "node-2"})
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
	sts.Status.CurrentRevision = "rev-1"
	sts.Status.UpdateRevision = "rev-2"
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK,
		"a CrashLooping OpenBao peer must be reported, not tolerated as a rollout")
}

// At full ready count an OnDelete mismatch must not warn on every run.
func TestCheckTier2StatefulSets_OnDeleteAtFullReadyIsClean(t *testing.T) {
	objs := makeQuorumSTS("openbao", "vault-system", 3, 3,
		[]string{"node-1", "node-2", "node-3"})
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
	sts.Status.CurrentRevision = "rev-1"
	sts.Status.UpdateRevision = "rev-2"
	client := fake.NewSimpleClientset(objs...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "rolling update in progress",
		"an OnDelete mismatch is the steady state, not an in-flight rollout")
}

// -- NVCF Gateway discovery --

var routeVersions = map[string]string{
	"HTTPRoute": "v1", "GRPCRoute": "v1", "TCPRoute": "v1alpha2", "UDPRoute": "v1alpha2",
}

func parentRef(kv ...string) map[string]interface{} {
	ref := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		ref[kv[i]] = kv[i+1]
	}
	return ref
}

func route(kind, ns, name, chart string, parents ...map[string]interface{}) *unstructured.Unstructured {
	refs := make([]interface{}, len(parents))
	for i, p := range parents {
		refs[i] = p
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gatewayAPIGroup + "/" + routeVersions[kind],
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"parentRefs": refs},
	}}
	if chart != "" {
		u.SetLabels(map[string]string{helmChartLabel: chart})
	}
	return u
}

func routeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	kinds := map[schema.GroupVersionResource]string{}
	for kind, v := range routeVersions {
		kinds[schema.GroupVersionResource{
			Group: gatewayAPIGroup, Version: v, Resource: strings.ToLower(kind) + "s",
		}] = kind + "List"
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)
}

// Every served route kind the NVCF stack renders, at its served version.
func routeDiscoveryClient() *fake.Clientset {
	return gatewayDiscoveryClient(
		gatewayAPIGroup+"/v1/httproutes",
		gatewayAPIGroup+"/v1/grpcroutes",
		gatewayAPIGroup+"/v1alpha2/tcproutes",
	)
}

func gatewayLBService(ns, name, gwNS, gw string, typ corev1.ServiceType, ip string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			owningGatewayNameLabel: gw, owningGatewayNamespaceLabel: gwNS,
		}},
		Spec: corev1.ServiceSpec{Type: typ},
	}
	if ip != "" {
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
	}
	return svc
}

func addServices(t *testing.T, client *fake.Clientset, svcs ...*corev1.Service) {
	t.Helper()
	for _, svc := range svcs {
		_, err := client.CoreV1().Services(svc.Namespace).Create(context.Background(), svc, metav1.CreateOptions{})
		require.NoError(t, err)
	}
}

// parentRefs defaults come from the Gateway API spec. An omitted namespace is
// the route's own, so "shared-gw" on a route in nvcf is nvcf/shared-gw, not
// "/shared-gw", which would match nothing.
func TestGatewayParentRefs_AppliesSpecDefaults(t *testing.T) {
	r := route("HTTPRoute", "nvcf", "r", "nvcf-gateway-routes-1.0.0",
		parentRef("name", "shared-gw"),
		parentRef("name", "grpc-gw", "namespace", "gw"),
		parentRef("name", "eg", "namespace", "gw", "group", gatewayAPIGroup, "kind", "Gateway"),
		parentRef("name", "svc", "group", "", "kind", "Service"),
		parentRef("name", "mesh", "group", "example.com", "kind", "Gateway"),
		parentRef("name", "listener-set", "kind", "ListenerSet"),
		parentRef("name", ""),
	)
	assert.Equal(t, []string{"nvcf/shared-gw", "gw/grpc-gw", "gw/eg"}, gatewayParentRefs(r))
}

// Only routes from the NVCF routes chart count, matched on helm.sh/chart so a
// nameOverride (which rewrites app.kubernetes.io/name) does not hide them.
// Unserved kinds are skipped, and duplicate parents collapse.
func TestDiscoverNVCFGateways_OwnedRoutesOnly(t *testing.T) {
	overridden := route("HTTPRoute", "nvcf", "api", "nvcf-gateway-routes-1.18.2", parentRef("name", "shared-gw"))
	overridden.SetLabels(map[string]string{
		helmChartLabel: "nvcf-gateway-routes-1.18.2", "app.kubernetes.io/name": "custom-name",
	})
	dyn := routeClient(
		overridden,
		route("HTTPRoute", "nvcf", "ess", "nvcf-gateway-routes-1.18.2", parentRef("name", "shared-gw")),
		route("TCPRoute", "nvcf", "grpc", "nvcf-gateway-routes-1.18.2",
			parentRef("name", "grpc-gw", "namespace", "gw")),
		route("HTTPRoute", "team-b", "b", "team-b-routes-1.0.0", parentRef("name", "b-gw")),
		route("HTTPRoute", "team-c", "c", "", parentRef("name", "c-gw")),
	)
	// UDPRoute is not served here, so it must not even be listed.
	listed := map[string]bool{}
	dyn.PrependReactor("list", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		listed[a.GetResource().Resource] = true
		return false, nil, nil
	})

	got, err := discoverNVCFGateways(context.Background(), routeDiscoveryClient(), dyn)
	require.NoError(t, err)
	assert.Equal(t, gatewaySet{"nvcf/shared-gw": true, "gw/grpc-gw": true}, got)
	assert.False(t, listed["udproutes"], "an unserved route kind must be skipped, not listed")
}

// A denied list is not the same answer as an empty one. Reading it as "no NVCF
// routes" would quietly drop to the lenient pre-install rule.
func TestDiscoverNVCFGateways_ListErrorIsNotEmpty(t *testing.T) {
	dyn := routeClient()
	dyn.PrependReactor("list", "httproutes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayAPIGroup, Resource: "httproutes"}, "", fmt.Errorf("denied"))
	})
	_, err := discoverNVCFGateways(context.Background(), routeDiscoveryClient(), dyn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing httproutes")

	empty, err := discoverNVCFGateways(context.Background(), routeDiscoveryClient(), routeClient())
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// The configured list replaces discovery outright.
func TestResolveNVCFGateways_ConfiguredSkipsDiscovery(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	dyn := routeClient(route("HTTPRoute", "nvcf", "r", "nvcf-gateway-routes-1.0.0", parentRef("name", "other")))
	got, _, err := resolveNVCFGateways(context.Background(), routeDiscoveryClient(), dyn)
	require.NoError(t, err)
	assert.Equal(t, gatewaySet{"gw/shared-gw": true}, got)
	assert.Empty(t, dyn.Actions(), "a configured list must not trigger route discovery")
}

func discoveredStack() *dynamicfake.FakeDynamicClient {
	return routeClient(
		route("HTTPRoute", "nvcf", "api", "nvcf-gateway-routes-1.18.2", parentRef("name", "shared-gw")),
		route("TCPRoute", "nvcf", "nats", "nvcf-gateway-routes-1.18.2", parentRef("name", "nats-gw")),
	)
}

// Discovered Gateways are checked strictly, in both Envoy modes: shared-gw's
// proxy sits in the controller namespace, nats-gw's beside its Gateway. The
// foreign pending Service is ignored either way.
func TestCheckExternalLoadBalancer_DiscoveredGatewaysAreStrict(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "")
	envoyNS := envoyGatewayNamespaceName()
	foreign := gatewayLBService(envoyNS, "envoy-team-b", "team-b", "b-gw", corev1.ServiceTypeLoadBalancer, "")

	t.Run("NVCF Gateway pending fails", func(t *testing.T) {
		client := routeDiscoveryClient()
		addServices(t, client,
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
			gatewayLBService("nvcf", "envoy-nats", "nvcf", "nats-gw", corev1.ServiceTypeLoadBalancer, ""),
			foreign.DeepCopy(),
		)
		state := &ValidationState{Log: testLog()}
		checkExternalLoadBalancer(context.Background(), client, discoveredStack(), state)

		require.NotNil(t, state.ExternalLBOK)
		assert.False(t, *state.ExternalLBOK)
		warnings := strings.Join(state.Warnings, "; ")
		assert.Contains(t, warnings, "nvcf/envoy-nats", "the Gateway-namespace proxy must be found")
		assert.NotContains(t, warnings, "envoy-team-b")
	})

	t.Run("all NVCF Gateways addressed passes despite a foreign pending", func(t *testing.T) {
		client := routeDiscoveryClient()
		addServices(t, client,
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
			gatewayLBService("nvcf", "envoy-nats", "nvcf", "nats-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.2"),
			foreign.DeepCopy(),
		)
		state := &ValidationState{Log: testLog()}
		checkExternalLoadBalancer(context.Background(), client, discoveredStack(), state)

		require.NotNil(t, state.ExternalLBOK)
		assert.True(t, *state.ExternalLBOK)
		assert.NotContains(t, strings.Join(state.Warnings, "; "), "envoy-team-b")
	})

	t.Run("route points at a Gateway with no proxy Service", func(t *testing.T) {
		client := routeDiscoveryClient()
		addServices(t, client,
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
		)
		state := &ValidationState{Log: testLog()}
		checkExternalLoadBalancer(context.Background(), client, discoveredStack(), state)

		require.NotNil(t, state.ExternalLBOK)
		assert.False(t, *state.ExternalLBOK)
		assert.Contains(t, strings.Join(state.Warnings, "; "), "no proxy Service found for Gateway(s) nvcf/nats-gw")
	})

	t.Run("Gateways exposed without a LoadBalancer are not failed", func(t *testing.T) {
		client := routeDiscoveryClient()
		addServices(t, client,
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw", corev1.ServiceTypeNodePort, ""),
			gatewayLBService(envoyNS, "envoy-nats", "nvcf", "nats-gw", corev1.ServiceTypeNodePort, ""),
		)
		state := &ValidationState{Log: testLog()}
		checkExternalLoadBalancer(context.Background(), client, discoveredStack(), state)

		assert.Nil(t, state.ExternalLBOK, "no NVCF Gateway uses an LB, so there is nothing to verify")
	})
}

// When ownership could not be determined, nothing addressed is still a
// failure, but an address on some Service is not a pass.
func TestCheckExternalLoadBalancer_DiscoveryFailureIsNotAPass(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "")
	envoyNS := envoyGatewayNamespaceName()
	denied := func() *dynamicfake.FakeDynamicClient {
		dyn := routeClient()
		dyn.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: gatewayAPIGroup, Resource: "httproutes"}, "", fmt.Errorf("denied"))
		})
		return dyn
	}

	client := routeDiscoveryClient()
	addServices(t, client,
		gatewayLBService(envoyNS, "envoy-a", "x", "a", corev1.ServiceTypeLoadBalancer, "203.0.113.1"))
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, denied(), state)
	assert.Nil(t, state.ExternalLBOK)
	assert.Contains(t, strings.Join(state.Recommendations, "; "), "httproutes")

	client = routeDiscoveryClient()
	addServices(t, client,
		gatewayLBService(envoyNS, "envoy-a", "x", "a", corev1.ServiceTypeLoadBalancer, ""))
	state = &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, denied(), state)
	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK, "nothing addressed fails whoever owns it")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "envoy-a", "name the Service still pending")
}

// Without a client to list served route kinds, ownership is unknown, not empty.
func TestDiscoverNVCFGateways_NoClientIsAnError(t *testing.T) {
	_, err := discoverNVCFGateways(context.Background(), routeDiscoveryClient(), nil)
	require.Error(t, err)

	// Nothing served means no route can exist, so no client is needed.
	got, err := discoverNVCFGateways(context.Background(), fake.NewSimpleClientset(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A paused Deployment is not mid-rollout, so it is assessed at its current
// readiness. Tolerating it as a rollout passed one at 0/3 on every run.
func TestCheckTier1Deployments_PausedIsAssessedNotTolerated(t *testing.T) {
	three := int32(3)
	paused := func(ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 2},
			Spec:       appsv1.DeploymentSpec{Replicas: &three, Paused: true},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, UpdatedReplicas: 0, ReadyReplicas: ready,
			},
		}
	}

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(paused(0)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "a paused Deployment with no ready pods is down")

	state = &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(paused(3)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "a paused Deployment at full readiness serves traffic")
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "rollout in progress")
}

// A 429 is not an RBAC denial, so the warning must not say it is.
func TestCheckTier1Deployments_ListFailureIsNotCalledRBAC(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequestsError("slow down")
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	assert.Nil(t, state.Tier1DeploymentsOK)
	joined := strings.Join(state.Warnings, "; ")
	assert.NotContains(t, joined, "RBAC denied")
	assert.Contains(t, joined, "denied or failed")
}

// A Deployment scaled to zero is an observed failure, so it must not be hidden
// behind a tolerated rollout or an unreadable namespace when nothing else was
// assessed.
func TestCheckTier1Deployments_ScaledToZeroWinsWhenNothingAssessed(t *testing.T) {
	zero, two := int32(0), int32(2)
	scaled := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api", Namespace: "nvcf", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
	}

	t.Run("beside a rolling Deployment", func(t *testing.T) {
		rolling := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "sis", Generation: 3},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, ReadyReplicas: 2},
		}
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), fake.NewSimpleClientset(scaled.DeepCopy(), rolling), nil, state)
		require.NotNil(t, state.Tier1DeploymentsOK)
		assert.False(t, *state.Tier1DeploymentsOK)
	})

	t.Run("beside an unreadable namespace", func(t *testing.T) {
		client := fake.NewSimpleClientset(scaled.DeepCopy())
		client.PrependReactor("list", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "sis" {
				return true, nil, apierrors.NewTooManyRequestsError("slow down")
			}
			return false, nil, nil
		})
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), client, nil, state)
		require.NotNil(t, state.Tier1DeploymentsOK)
		assert.False(t, *state.Tier1DeploymentsOK)
	})
}

// A rollout is tolerated only down to the floor the Deployment controller
// itself guarantees. Below it, the missing pods are not explained by the
// rollout, whether or not the deadline ever reports a stall.
func TestCheckTier1Deployments_RolloutBelowItsFloorFails(t *testing.T) {
	three := int32(3)
	rolling := func(strategy appsv1.DeploymentStrategy, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 3},
			Spec:       appsv1.DeploymentSpec{Replicas: &three, Strategy: strategy},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 3, UpdatedReplicas: 1, ReadyReplicas: ready,
			},
		}
	}
	cases := []struct {
		name     string
		strategy appsv1.DeploymentStrategy
		ready    int32
		wantOK   bool
	}{
		// 25% of 3 rounds down to 0 unavailable, so the floor is 3.
		{"default strategy at 0/3", appsv1.DeploymentStrategy{}, 0, false},
		{"default strategy at full readiness", appsv1.DeploymentStrategy{}, 3, true},
		// Recreate replaces every pod at once, so 0/3 is a normal rollout.
		{"recreate at 0/3", appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &ValidationState{Log: testLog()}
			checkTier1Deployments(context.Background(), fake.NewSimpleClientset(rolling(tc.strategy, tc.ready)), nil, state)
			require.NotNil(t, state.Tier1DeploymentsOK)
			assert.Equal(t, tc.wantOK, *state.Tier1DeploymentsOK)
		})
	}
}

func TestRolloutReadyFloor(t *testing.T) {
	d := func(unavailable, surge *intstr.IntOrString) *appsv1.Deployment {
		return &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Strategy: appsv1.DeploymentStrategy{
			RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: unavailable, MaxSurge: surge},
		}}}
	}
	one, zero, half := intstr.FromInt(1), intstr.FromInt(0), intstr.FromString("50%")
	assert.Equal(t, int32(3), rolloutReadyFloor(&appsv1.Deployment{}, 3), "25 percent of 3 rounds down to 0")
	assert.Equal(t, int32(3), rolloutReadyFloor(&appsv1.Deployment{}, 4), "25 percent of 4 is 1")
	assert.Equal(t, int32(2), rolloutReadyFloor(d(&one, nil), 3))
	assert.Equal(t, int32(2), rolloutReadyFloor(d(&half, nil), 4))
	assert.Equal(t, int32(2), rolloutReadyFloor(d(&zero, &zero), 3), "both zero resolve to one unavailable")
}

// Envoy Gateway runs every team's proxy Deployment beside its controller, so
// a foreign proxy at 1/2 must not fail the critical row. NVCF's own proxy is
// still assessed.
func TestCheckTier1Deployments_OnlyNVCFEnvoyProxiesAreAssessed(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	two := int32(2)
	proxy := func(name, gw string, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envoyGatewayNamespace, Generation: 1, Labels: map[string]string{
				owningGatewayNameLabel: gw, owningGatewayNamespaceLabel: "nvcf",
			}},
			Spec:   appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
		}
	}

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(
		proxy("envoy-nvcf", "shared-gw", 2), proxy("envoy-team-b", "team-b-gw", 1)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "another team's proxy is not NVCF's to fail on")

	state = &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(
		proxy("envoy-nvcf", "shared-gw", 1)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "NVCF's own proxy is still assessed")
}

// A known quorum component is assessed at an even size too: a 4-node Cassandra
// ring at 0/4 is down whatever its parity, and a NATS mid-rollout beside it
// must not hide that.
func TestCheckTier2StatefulSets_EvenKnownQuorumComponentIsAssessed(t *testing.T) {
	four, three := int32(4), int32(3)
	cassandra := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cassandra", Namespace: "cassandra-system"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &four},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	natsRolling := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "nats", Namespace: "nats-system"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &three},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 2, CurrentRevision: "a", UpdateRevision: "b",
		},
	}
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(cassandra, natsRolling), state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// A skipped StatefulSet stays in the warnings even when the row exits through
// the tolerated-rollout pass.
func TestCheckTier2StatefulSets_SkippedListSurvivesRolloutExit(t *testing.T) {
	four, three := int32(4), int32(3)
	other := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "nvcf"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &four},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	natsRolling := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "nats", Namespace: "nats-system"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &three},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 2, CurrentRevision: "a", UpdateRevision: "b",
		},
	}
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(other, natsRolling), state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nvcf/redis")
}

// A failed list in one namespace must not discard a failure already observed
// in another.
func TestCheckTier2StatefulSets_ListFailureKeepsEarlierFindings(t *testing.T) {
	three := int32(3)
	nats := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "nats", Namespace: "nats-system"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &three},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	client := fake.NewSimpleClientset(nats)
	client.PrependReactor("list", "statefulsets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "cassandra-system" {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("boom"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "the observed NATS outage must survive a later list failure")
}

// A transient error while waiting for the DaemonSet's scheduling target is
// retried, not reported as a failure to observe.
func TestWaitForDaemonSetDesiredCount_RetriesTransientErrors(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "probe", Generation: 1},
		Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 3},
	})
	calls := 0
	client.PrependReactor("get", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewTooManyRequestsError("slow down")
		}
		return false, nil, nil
	})
	got, err := waitForDaemonSetDesiredCount(context.Background(), client, "probe", "ds", 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, 3, got)
	assert.Greater(t, calls, 1)
}

// A probe that fits on fewer than two nodes of a multi-node cluster was not
// exercised: UNKNOWN with a warning naming the taint, not N/A.
func TestCheckNodeToNode_ProbeOnOneOfManyNodesIsUnknown(t *testing.T) {
	n1, n2, n3 := makeNode("node-1", true, 0), makeNode("node-2", true, 0), makeNode("node-3", true, 0)
	for _, n := range []*corev1.Node{n2, n3} {
		n.Spec.Taints = []corev1.Taint{{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}}
	}
	client := fake.NewSimpleClientset(n1, n2, n3)
	var dsName, dsNS string
	client.PrependReactor("create", "daemonsets", func(a ktesting.Action) (bool, runtime.Object, error) {
		ds := a.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		dsName, dsNS = ds.Name, ds.Namespace
		return true, ds, nil
	})
	client.PrependReactor("get", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: dsName, Namespace: dsNS, Generation: 1},
			Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1},
		}, nil
	})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Empty(t, state.NodeToNodeNotApplicable, "three schedulable nodes is not a shape where the check is moot")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nvidia.com/gpu")
}

// The checker is pinned with NodeName, so a full node rejects it at kubelet
// admission with phase Failed. Only the script's own exit code is evidence of
// a failed connection.
func TestCheckerFailureCause(t *testing.T) {
	pod := func(status corev1.PodStatus) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}, Status: status}
	}
	exited := func(code int32) corev1.PodStatus {
		return corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code}},
		}}}
	}
	cases := []struct {
		name    string
		status  corev1.PodStatus
		network bool
	}{
		{"connection failed", exited(nodeToNodeUnreachableExit), true},
		{"no nc in the image", exited(nodeToNodeNoNetcatExit), false},
		{"other exit", exited(137), false},
		{"kubelet rejected it", corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfcpu"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(pod(tc.status))
			why := checkerFailureCause(context.Background(), client, "ns", "c")
			assert.Equal(t, tc.network, why == "", why)
		})
	}
}

func TestBuildNodeToNodeCheckerPod_ExitCodesSeparateNetworkFailures(t *testing.T) {
	cmd := buildNodeToNodeCheckerPod("c", "ns", "node-1", []string{"10.0.0.2"}, "img").Spec.Containers[0].Command[2]
	assert.Contains(t, cmd, fmt.Sprintf("|| exit %d", nodeToNodeNoNetcatExit))
	assert.Contains(t, cmd, fmt.Sprintf("nc -z -w 5 10.0.0.2 %d || exit %d", nodeToNodeTestPort, nodeToNodeUnreachableExit))
}

// Envoy is non-critical, so a missing namespace needs a warning or the run
// prints the green banner above a failing row.
func TestCheckEnvoyGateway_NotFoundAddsWarning(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), fake.NewSimpleClientset(), state)
	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK)
	assert.NotEmpty(t, state.Warnings)
}

// The probe namespace is deleted on cleanup, so one that already exists is
// not ours: the run stops before touching it.
func TestCheckNodeToNode_ExistingNamespaceIsNotAdopted(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
	client.PrependReactor("create", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, "x")
	})
	deleted := false
	client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		deleted = true
		return true, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK)
	assert.False(t, deleted, "a namespace this run did not create must not be deleted")
}

// End to end: a checker the kubelet rejected (OutOfcpu) ended Failed without
// running, so the overlay stays unobserved instead of failing with the
// firewall advice.
func TestCheckNodeToNode_KubeletRejectedCheckerIsUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
	var dsLabels map[string]string
	var dsName, dsNS string
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		dsLabels, dsName, dsNS = ds.Labels, ds.Name, ds.Namespace
		return true, ds, nil
	})
	client.PrependReactor("get", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: dsName, Namespace: dsNS, Generation: 1},
			Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, ObservedGeneration: 1},
		}, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: dsNS, Labels: dsLabels},
				Spec:       corev1.PodSpec{NodeName: "node-1"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-2", Namespace: dsNS, Labels: dsLabels},
				Spec:       corev1.PodSpec{NodeName: "node-2"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
			},
		}}, nil
	})
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "checker", Namespace: dsNS},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfcpu"},
		}, nil
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "OutOfcpu")
	assert.Empty(t, state.Recommendations, "no firewall advice for a pod that never ran")
}
