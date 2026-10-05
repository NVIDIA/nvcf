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
	"math"
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
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
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
	assert.True(t, *state.DefaultStorageClassOK,
		"a StorageClass with the default annotation must set DefaultStorageClassOK=true")
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
	assert.False(t, *state.DefaultStorageClassOK,
		"StorageClass without default annotation must set DefaultStorageClassOK=false")
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
// A bare fake serves no API groups, so discovery succeeds and observes the
// Gateway API group as absent: an answer, not an error. A discovery error is
// unknown instead (TestObservationErrors_LeaveEachRowUnknownWithTheCause).

func TestCheckGatewayAPICRDs_AbsentOnFakeClient(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)

	require.NotNil(t, state.GatewayAPICRDsOK,
		"a group discovery observed as absent is a result")
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

	// Nothing to judge the LB controller by is not a failure of it.
	assert.Nil(t, state.ExternalLBOK, "no LB Service at all before install is unknown, not a failure")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "no Service of type LoadBalancer exists yet")
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
	assert.Empty(t, state.NodeToNodeNotApplicable, "no node at all is unobserved, not moot")
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

// The probe tolerates every taint, so a GPU-tainted node is probed rather than
// leaving the cluster UNKNOWN, while a NotReady node, which cannot run it, is
// not expected to have a pod. It is still named as a coverage gap.
func TestCheckNodeToNode_TaintedNodeIsProbedNotReadyIsNot(t *testing.T) {
	n1, n2, n3 := makeNode("node-1", true, 0), makeNode("node-2", true, 0), makeNode("node-3", false, 0)
	n2.Spec.Taints = []corev1.Taint{{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}}
	f := newN2NFixture(t, []*corev1.Node{n1, n2, n3},
		[]corev1.Pod{runningProbePod("s-1", "node-1", "10.0.0.1"), runningProbePod("s-2", "node-2", "10.0.0.2")},
		nil, checkerExit(0))

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK)
	// The checker runs on one of the two nodes, picked at random, and dials
	// the other, so the GPU-tainted node is probed either way.
	assert.NotEqual(t, strings.Contains(f.checkerCmd, "10.0.0.1"), strings.Contains(f.checkerCmd, "10.0.0.2"),
		"the checker on one node dials the other: %s", f.checkerCmd)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "Node-to-Node: not probed on 1 node(s): node-3 (NotReady)",
		"a node left out of a Verified row is a named coverage gap")
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

			assert.Nil(t, state.NodeToNodeOK, "a probe that was never created must not fail the overlay")
			assert.Contains(t, strings.Join(state.Warnings, "; "),
				"could not create the probe DaemonSet in "+nodeToNodeNSPrefix)
			assert.Contains(t, strings.Join(state.Warnings, "; "), createErr.Error(), "the cause is named")
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

	// Fail the probe pod list fast so the test does not wait out the real
	// timeout; cleanup must still run on this path. Forbidden is terminal.
	client.PrependReactor("list", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("simulated pod read failure"))
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	require.NotEmpty(t, createdNS, "probe must create its own namespace, not use default")
	assert.True(t, strings.HasPrefix(createdNS, nodeToNodeNSPrefix),
		"probe namespace %q must carry the sweepable prefix %q", createdNS, nodeToNodeNSPrefix)
	assert.True(t, deleted["daemonsets"], "deferred cleanup must delete the server DaemonSet")
	assert.False(t, deleted["pods"], "no checker pod was created on this path, so none is deleted")
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
	// The labels are public constants, so the generated name is required too.
	unprefixed := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              "team-namespace",
		Labels:            labels,
		CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
	}}
	client := fake.NewSimpleClientset(stale, fresh, unprefixed)

	sweepOrphanN2NNamespaces(context.Background(), testLog(), client, orphanN2NNamespaceTTL)

	_, err := client.CoreV1().Namespaces().Get(context.Background(), stale.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "namespace older than the TTL must be swept")

	_, err = client.CoreV1().Namespaces().Get(context.Background(), fresh.Name, metav1.GetOptions{})
	assert.NoError(t, err, "namespace inside the TTL may belong to a concurrent run and must survive")

	_, err = client.CoreV1().Namespaces().Get(context.Background(), unprefixed.Name, metav1.GetOptions{})
	assert.NoError(t, err, "a labelled namespace without the probe prefix is not the validator's")
}

// A probe namespace stuck Terminating is held by a probe pod on a node whose
// kubelet is gone. The sweep does not delete it again, but force-deletes the
// probe pods inside so it can finish.
func TestSweepOrphanN2NNamespaces_ForcesPodsOutOfATerminatingNamespace(t *testing.T) {
	now := metav1.Now()
	stuck := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: nodeToNodeNSPrefix + "stuck1",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
			"app.kubernetes.io/component":  "n2n-probe",
		},
		CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		DeletionTimestamp: &now,
		Finalizers:        []string{"kubernetes"},
	}}
	probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "nvcf-n2n-server-abc", Namespace: stuck.Name,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "nvcf-cluster-validator"},
	}}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: stuck.Name}}
	client := fake.NewSimpleClientset(stuck, probe, other)
	var podDeletes []string
	nsDeletes := 0
	client.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		del := a.(ktesting.DeleteActionImpl)
		require.NotNil(t, del.DeleteOptions.GracePeriodSeconds)
		assert.Zero(t, *del.DeleteOptions.GracePeriodSeconds, "a pod on a gone node is only removed by force")
		podDeletes = append(podDeletes, del.Name)
		return false, nil, nil
	})
	client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		nsDeletes++
		return false, nil, nil
	})

	sweepOrphanN2NNamespaces(context.Background(), testLog(), client, orphanN2NNamespaceTTL)

	assert.Equal(t, []string{probe.Name}, podDeletes)
	assert.Zero(t, nsDeletes, "a namespace already Terminating is not deleted, or counted, again")
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
			Generation:    3, // new spec written, a minute ago
			ManagedFields: []metav1.ManagedFieldsEntry{specWrite(installManager, time.Now().Add(-time.Minute))},
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
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "pre-install (no deployments) must pass trivially")
}

// When the launcher says the control plane is installed, finding no
// Deployment in any of its namespaces is a failure: a relocated release or an
// unset override, not a pre-install cluster.
func TestCheckTier1Deployments_NoDeploymentsAfterInstallFails(t *testing.T) {
	state := &ValidationState{Log: testLog(), PostInstall: true}
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

// makeQuorumSTS builds a StatefulSet in the shape an HA mode renders, its pods
// carrying hostname anti-affinity (withoutSpread removes it, as mode none),
// settled on revision <name>-r1. Like a real apiserver it holds the
// ControllerRevision and one owned pod per entry of nodes, the first ready of
// them Ready, labelled with their revision and index. All of it was written
// at installedAt; the controller's status writes are recent, as they always
// are. rollTo starts a rollout over it.
func makeQuorumSTS(name, ns string, replicas, ready int32, nodes []string) []runtime.Object {
	sel := map[string]string{"app": name}
	// IsControlledBy compares the controller reference UID, so the fixture needs
	// a real one on both sides.
	uid := types.UID("uid-" + name)
	controller := true
	owner := []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "StatefulSet", Name: name, UID: uid, Controller: &controller,
	}}
	installed, rev, count := metav1.NewTime(installedAt), name+"-r1", int32(len(nodes))
	objs := []runtime.Object{&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: uid, Generation: 1, CreationTimestamp: installed,
			ManagedFields: []metav1.ManagedFieldsEntry{
				specWrite(installManager, installedAt), statusWrite(time.Now()),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: corev1.PodSpec{
				Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{MatchLabels: sel},
							TopologyKey:   corev1.LabelHostname,
						},
					}},
				}},
			}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           count, CurrentReplicas: count, UpdatedReplicas: count,
			ReadyReplicas: ready, AvailableReplicas: ready,
			CurrentRevision: rev, UpdateRevision: rev,
		},
	}, &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: rev, Namespace: ns, CreationTimestamp: installed, Labels: sel, OwnerReferences: owner,
		},
		Revision: 1,
	}}
	for i, node := range nodes {
		readyStatus := corev1.ConditionFalse
		if int32(i) < ready {
			readyStatus = corev1.ConditionTrue
		}
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: installed,
				Name:              fmt.Sprintf("%s-%d", name, i), Namespace: ns,
				Labels: map[string]string{
					"app": name, appsv1.ControllerRevisionHashLabelKey: rev, appsv1.PodIndexLabel: fmt.Sprint(i),
				},
				OwnerReferences: owner,
			},
			Spec: corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: readyStatus}},
			},
		})
	}
	return objs
}

// withoutSpread drops the StatefulSet's pod anti-affinity, the shape
// highAvailability.mode none renders.
func withoutSpread(objs []runtime.Object) []runtime.Object {
	objs[0].(*appsv1.StatefulSet).Spec.Template.Spec.Affinity = nil
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
	assert.Equal(t, []string{tier2CoLocatedAdvice}, state.Recommendations)
}

// StatefulSets roll one pod at a time, so a below-target ready count is the
// steady state for the whole duration of any upgrade. That must warn, not fail.
func TestCheckTier2StatefulSets_RollingUpdateWarnsNotFails(t *testing.T) {
	// A minute into a rollout, the replaced pod not yet recreated.
	objs := rollTo(makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"}),
		"nats-r2", time.Now().Add(-time.Minute))
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
	rolling := rollTo(makeQuorumSTS("openbao", "vault-system", 3, 2, []string{"node-1", "node-2"}),
		"openbao-r2", time.Now().Add(-time.Minute))

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
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "sis", Generation: 3,
				ManagedFields: []metav1.ManagedFieldsEntry{specWrite(installManager, time.Now())}},
			Spec: appsv1.DeploymentSpec{Replicas: &two},
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
			ObjectMeta: metav1.ObjectMeta{Name: "rolling", Namespace: "sis", Generation: 3,
				ManagedFields: []metav1.ManagedFieldsEntry{specWrite(installManager, time.Now())}},
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
	assert.Equal(t, "Multiple Defaults", state.StorageClassFailure, "the row names the cause, not Not Found")
}

// Only NotFound is evidence Envoy is absent. A 403 or an apiserver 500 means we
// never observed it, so the row must be unknown rather than a definite failure.
func TestCheckEnvoyGateway_APIErrorIsUnknownNotFailure(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, envoyGatewayNamespace)
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

	// Unset, the controller is looked for in every namespace, and a failed
	// lookup is unknown too.
	t.Setenv(envoyGatewayNamespaceEnv, "")
	client = fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	state = &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)
	assert.Nil(t, state.EnvoyGatewayOK)
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
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw,gw/nats-gw")
	assigned := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "envoy-gateway-lb", Namespace: envoyGatewayNamespaceName(),
			Labels: map[string]string{owningGatewayNameLabel: "shared-gw", owningGatewayNamespaceLabel: "gw"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}},
		}},
	}
	pending := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "envoy-nats-gateway-lb", Namespace: envoyGatewayNamespaceName(),
			Labels: map[string]string{owningGatewayNameLabel: "nats-gw", owningGatewayNamespaceLabel: "gw"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	client := fake.NewSimpleClientset(assigned, pending, envoyController(envoyGatewayNamespaceName()))
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
		envoyController(envoyGatewayNamespaceName()),
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
	t.Setenv(nvcfGatewayNamesEnv, "gateway/shared-gw")
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

// The probe tolerates every taint: a dedicated control plane, GPU nodes and any
// other taint would otherwise keep it off nodes and leave the check UNKNOWN.
// NodeName on the checker bypasses the scheduler but not taint admission.
func TestBuildNodeToNodeDaemonSet_ToleratesEveryTaint(t *testing.T) {
	everything := []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	ds := buildNodeToNodeDaemonSet("n2n", "ns", map[string]string{"a": "b"}, "img", []string{"node-1", "node-2"})
	assert.Equal(t, everything, ds.Spec.Template.Spec.Tolerations)
	// Tolerating everything, it is pinned to the nodes it is expected on.
	affinity := ds.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.NodeAffinity)
	required := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.NotNil(t, required)
	assert.Equal(t, []string{"node-1", "node-2"}, pinnedNodes(t, required.NodeSelectorTerms))
	pod := buildNodeToNodeCheckerPod("checker", "ns", "node-1", nil, []string{"10.0.0.1"}, "img")
	assert.Equal(t, everything, pod.Spec.Tolerations)
}

// The API server rejects a node field selector with In or NotIn that has
// more than one value, so a pin listing the nodes in one requirement made
// every DaemonSet create fail with 422 on a cluster with two or more nodes.
// The fake clientset does not validate, so the shape is checked here.
func TestBuildNodeToNodeDaemonSet_PinHasOneNodePerTerm(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	ds := buildNodeToNodeDaemonSet("n2n", "ns", nil, "img", nodes)
	terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, len(nodes))
	for i, term := range terms {
		assert.Empty(t, term.MatchExpressions)
		require.Len(t, term.MatchFields, 1)
		f := term.MatchFields[0]
		assert.Equal(t, metav1.ObjectNameField, f.Key)
		assert.Equal(t, corev1.NodeSelectorOpIn, f.Operator)
		assert.Len(t, f.Values, 1, "term %d: a node field selector with In takes exactly one value", i)
	}
	assert.Equal(t, nodes, pinnedNodes(t, terms))
}

// pinnedNodes returns the nodes a metadata.name node pin selects, requiring
// every term to be one the API server accepts: field requirements only, each
// on metadata.name with In and exactly one value.
func pinnedNodes(t *testing.T, terms []corev1.NodeSelectorTerm) []string {
	t.Helper()
	var nodes []string
	for _, term := range terms {
		require.Empty(t, term.MatchExpressions)
		for _, f := range term.MatchFields {
			require.Equal(t, metav1.ObjectNameField, f.Key)
			require.Equal(t, corev1.NodeSelectorOpIn, f.Operator)
			require.Len(t, f.Values, 1, "the API server rejects more than one value for In on a node field")
			nodes = append(nodes, f.Values[0])
		}
	}
	return nodes
}

// gatewayDiscoveryClient returns a fake clientset whose discovery surface
// serves exactly the given "<groupVersion>/<resource>" pairs. Until this
// existed no test in the package populated a discovery surface at all, so
// every pinned route pair and the hasPair lookup itself were uncovered: the
// Gateway tests passed identically against a bare fake, which serves nothing.
func gatewayDiscoveryClient(pairs ...string) *fake.Clientset {
	client := fake.NewSimpleClientset(envoyController(envoyGatewayNamespace))
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

// envoyController is a Ready Envoy Gateway controller Deployment in ns. Proxies
// and proxy Services are credited in its namespace or their Gateway's only.
func envoyController(ns string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway", Namespace: ns, Generation: 1,
			Labels: map[string]string{"control-plane": "envoy-gateway"}},
		Spec:   appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, ReadyReplicas: 1},
	}
}

// nvcfService is a healthy NVCF service Deployment, so an installed control
// plane is not reported as missing.
func nvcfService() *appsv1.Deployment {
	two := int32(2)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 1},
		Spec:   appsv1.DeploymentSpec{Replicas: &two},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2}}
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
	assert.Contains(t, strings.Join(routeState.Warnings, "; "), "ingress.gatewayApi.routes.llmWorker.enabled",
		"the warning names the stack value that needs the type")
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
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not read its pods: pods is forbidden")
}

// The wait returns as soon as every expected node has a Running pod with an
// IP, and otherwise returns what it last saw for the caller to classify.
func TestWaitForProbePods_ReturnsWhatItSawAtTheDeadline(t *testing.T) {
	labels := map[string]string{"app": "n2n"}
	mk := func(name, node, ip string) *corev1.Pod {
		p := runningProbePod(name, node, ip)
		p.Namespace, p.Labels = "probe", labels
		return &p
	}
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels})
	nodes := []string{"node-1", "node-2", "node-3"}

	stuck := probePod("node-3", "Pending", "", "ContainerCreating")
	stuck.ObjectMeta = metav1.ObjectMeta{Name: "c", Namespace: "probe", Labels: labels}
	client := fake.NewSimpleClientset(mk("a", "node-1", "10.0.0.1"), mk("b", "node-2", "10.0.0.2"), &stuck)
	pods, err := waitForProbePods(context.Background(), client, "probe", selector, nodes, time.Second)
	require.NoError(t, err)
	assert.Len(t, pods, 3, "the straggler is returned for classification, not dropped")

	client = fake.NewSimpleClientset(mk("a", "node-1", "10.0.0.1"), mk("b", "node-2", "10.0.0.2"),
		mk("c", "node-3", "10.0.0.3"))
	start := time.Now()
	pods, err = waitForProbePods(context.Background(), client, "probe", selector, nodes, 30*time.Second)
	require.NoError(t, err)
	assert.Len(t, pods, 3)
	assert.Less(t, time.Since(start), 5*time.Second, "all up returns at once")
}

// A list that fails after earlier ones succeeded keeps what they showed. The
// last attempt usually starts just past the deadline, and discarding the pods
// already seen turned a real CNI fault into UNKNOWN.
func TestWaitForProbePods_KeepsTheLastSuccessfulList(t *testing.T) {
	labels := map[string]string{"app": "n2n"}
	stuck := probePod("node-2", "Pending", "", "ContainerCreating")
	stuck.ObjectMeta = metav1.ObjectMeta{Name: "b", Namespace: "probe", Labels: labels}
	up := runningProbePod("a", "node-1", "10.0.0.1")
	up.Namespace, up.Labels = "probe", labels
	client := fake.NewSimpleClientset(&up, &stuck)
	calls := 0
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return false, nil, nil
		}
		return true, nil, apierrors.NewTooManyRequestsError("slow down")
	})
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels})
	nodes := []string{"node-1", "node-2"}
	pods, err := waitForProbePods(context.Background(), client, "probe", selector, nodes, time.Second)
	require.NoError(t, err)
	assert.Len(t, pods, 2)
	failed := map[string]sandboxEvidence{"b": {state: sandboxFailed, reason: "FailedCreatePodSandBox"}}
	faults := classifyProbeNodes(pods, nodes, failed, nil).networkFaults
	require.Len(t, faults, 1, "the straggler kept from the earlier list is still classified")
	assert.Equal(t, "node-2", faults[0].node)

	// A list older than the freshness bound shows the pods as they were when
	// it was taken, not as they are, so it is not classified at all.
	prev := probeSnapshotMaxAge
	probeSnapshotMaxAge = 200 * time.Millisecond
	t.Cleanup(func() { probeSnapshotMaxAge = prev })
	calls = 0
	_, err = waitForProbePods(context.Background(), client, "probe", selector, nodes, time.Second)
	var unobserved *probeNotObservedError
	require.ErrorAs(t, err, &unobserved)
	assert.Contains(t, unobserved.reason, "old")
}

// A pod list that fails until the deadline observed nothing at all.
func TestWaitForProbePods_UnreadableListIsNotObserved(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequestsError("slow down")
	})
	_, err := waitForProbePods(context.Background(), client, "probe", "app=n2n", []string{"a", "b"}, time.Second)
	var unobserved *probeNotObservedError
	require.ErrorAs(t, err, &unobserved)
	assert.Contains(t, unobserved.reason, "listing probe pods")
}

// A permission error is terminal: retrying it just burns the deadline.
func TestWaitForProbePods_ForbiddenIsTerminal(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
	})
	start := time.Now()
	_, err := waitForProbePods(context.Background(), client, "probe", "app=n2n", []string{"a", "b"}, 30*time.Second)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "a denial must return immediately")
}

// Supersedes TestCheckNodeToNode_SingleNode_Skip, which asserted the opposite.
// A single-node control plane has no cross-node path to exercise, so the
// overlay requirement is vacuously met. Leaving the pointer nil would make it
// a critical UNKNOWN, which fails the verdict, so a k3d or single-node control
// plane would report NVCF-Not-Ready on every tick.
func TestCheckNodeToNode_SingleNodeIsNotApplicableNotUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("only-node", true, 0))
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	// Not a pass: no cross-node packet was sent.
	assert.Nil(t, state.NodeToNodeOK, "an unexercised check must not be reported as passed")
	assert.NotEmpty(t, state.NodeToNodeNotApplicable,
		"one schedulable node is not applicable, not unobserved")
	// The row has no value, so the warnings list says why, as METRICS.md
	// tells an absent() guard to expect.
	assert.Contains(t, strings.Join(state.Warnings, "; "), "Node-to-Node: not applicable")
}

// A cordoned second node leaves one schedulable node: same reasoning.
func TestCheckNodeToNode_AllButOneCordonedIsNotApplicable(t *testing.T) {
	cordoned := makeNode("node-2", true, 0)
	cordoned.Spec.Unschedulable = true
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), cordoned)
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	assert.Nil(t, state.NodeToNodeOK)
	assert.NotEmpty(t, state.NodeToNodeNotApplicable)
}

// An RBAC denial on the probe DaemonSet is genuinely unobserved, so it must
// stay UNKNOWN. This is the case the operator ClusterRole now grants for.
func TestCheckNodeToNode_DaemonSetDenialStaysUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
	client.PrependReactor("create", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "daemonsets"}, "", fmt.Errorf("denied"))
	})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, "busybox:1.36")

	assert.Nil(t, state.NodeToNodeOK,
		"a denial is not evidence the overlay works, so it must not pass")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not create the probe DaemonSet")
}

// A checker whose status cannot be read produced no result, so the overlay
// stays unobserved rather than failing on the first Get error.
func TestCheckNodeToNode_UnreadableCheckerIsUnknown(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0), makeNode("node-2", true, 0))
	var dsLabels map[string]string
	var dsNS string
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		dsLabels, dsNS = ds.Labels, ds.Namespace
		return true, ds, nil
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

// Only a pod whose latest sandbox event is a failure to create it is evidence
// about the overlay. A pod that could not pull its image, start its container,
// be scheduled, be admitted by the kubelet, or be created at all says nothing
// about it, and neither does silence: no sandbox event, however long after
// scheduling, or events that could not be read.
func TestClassifyProbeNodes(t *testing.T) {
	nodes := []string{"node-1", "node-2"}
	running := probePod("node-1", "Running", "10.0.0.1", "")
	running.Name = "a"
	second := func(p corev1.Pod) []corev1.Pod {
		p.Name = "b"
		return []corev1.Pod{running, p}
	}
	evicted := probePod("node-2", "Failed", "", "")
	evicted.Status.Reason = "Evicted"
	creating := func() []corev1.Pod { return second(probePod("node-2", "Pending", "", "ContainerCreating")) }
	// Scheduled 70 seconds ago with no sandbox event: past the grace an
	// earlier version read as a CNI fault.
	silent := probePod("node-2", "Pending", "", "ContainerCreating")
	silent.CreationTimestamp = metav1.NewTime(time.Now().Add(-70 * time.Second))
	silent.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-70 * time.Second)),
	}}
	latest := func(state sandboxState, reason, message string) map[string]sandboxEvidence {
		return map[string]sandboxEvidence{"b": {state: state, reason: reason, message: message}}
	}
	unreadable := apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	cases := []struct {
		name       string
		pods       []corev1.Pod
		sandboxes  map[string]sandboxEvidence
		eventsErr  error
		fault      bool
		gap        string
		imageError bool
	}{
		{"pod never created", []corev1.Pod{running}, nil, nil, false, "no probe pod was created", false},
		{"image pull backoff", second(probePod("node-2", "Pending", "", "ImagePullBackOff")),
			nil, nil, false, "ImagePullBackOff", true},
		{"first pull error", second(probePod("node-2", "Pending", "", "ErrImagePull")),
			nil, nil, false, "ErrImagePull", true},
		{"pull still in progress", creating(), latest(sandboxCreated, "Pulling", ""), nil, false,
			"image pull still in progress", true},
		{"container cannot start", second(probePod("node-2", "Running", "", "CrashLoopBackOff")),
			nil, nil, false, "CrashLoopBackOff", false},
		{"kubelet eviction", second(evicted), nil, nil, false, "rejected by the kubelet (Evicted)", false},
		{"start under way", creating(), latest(sandboxCreated, "Started", ""), nil, false,
			"sandbox created", false},
		{"created only", creating(), latest(sandboxCreated, "Created", ""), nil, false, "sandbox created", false},
		{"sandbox creation failed", creating(), latest(sandboxFailed, "FailedCreatePodSandBox", "plugin type=calico"),
			nil, true, "", false},
		{"volume mount failing", creating(), latest(sandboxPending, "FailedMount", "kube-api-access timed out"),
			nil, false, "sandbox not created yet (FailedMount: kube-api-access timed out)", false},
		{"no sandbox event long after scheduling", second(silent), nil, nil, false,
			"scheduled, but no sandbox event yet", false},
		{"events unreadable", creating(), nil, unreadable, false,
			"pod events could not be read (Internal error occurred: etcd timeout)", false},
		{"running without IP", second(probePod("node-2", "Running", "", "")), nil, nil, false,
			"Running, but its pod IP is not reported yet", false},
	}
	// Only a scheduled pod with no sandbox event at all is silent, which is
	// what a hung CNI plugin looks like. One whose events could not be read
	// showed none either.
	silentCases := map[string]bool{"no sandbox event long after scheduling": true, "events unreadable": true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProbeNodes(tc.pods, nodes, tc.sandboxes, tc.eventsErr)
			assert.Len(t, got.running, 1)
			if tc.fault {
				assert.Equal(t, []sandboxFault{{node: "node-2", message: "plugin type=calico"}}, got.networkFaults,
					"the fault keeps the event message that produced it")
				assert.Empty(t, got.gaps)
				return
			}
			assert.Empty(t, got.networkFaults)
			require.Len(t, got.gaps, 1)
			assert.Contains(t, got.gaps[0], "node-2: "+tc.gap)
			assert.Equal(t, tc.imageError, got.imageProblem)
			if silentCases[tc.name] {
				assert.Equal(t, []string{"node-2"}, got.silent)
			} else {
				assert.Empty(t, got.silent)
			}
		})
	}
}

// A CNI plugin that hangs on pod network setup reports FailedCreatePodSandBox
// only when the runtime times it out, after the probe stops waiting. A pod
// scheduled on an eligible node with no sandbox event is no fault, but the
// overlay there is untested, so the row is unknown and the warning names the
// node, even though every other node was reached.
func TestCheckNodeToNode_SilentSandboxLeavesTheRowUnknown(t *testing.T) {
	silent := probePod("node-3", "Pending", "", "ContainerCreating")
	silent.Name = "s-3"
	servers := append(runningServers("node-1", "node-2"), silent)
	// The scheduler's event is no sandbox event.
	events := []corev1.Event{podEvent("s-3", "Scheduled", 0)}

	f := newN2NFixture(t, readyNodes(3), servers, events, checkerExit(0))
	state := runN2N(f)
	assert.Nil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	warnings := strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "Node-to-Node: status unknown (the overlay was verified from")
	assert.Contains(t, warnings, "the probe pod on node-3 was scheduled and showed no pod sandbox event")
	assert.Contains(t, state.Recommendations, nodeToNodeSilentSandboxRecommendation)
	assert.Empty(t, f.leftovers(t))

	// A checker that could not reach another node still fails the row: that
	// is evidence, whatever the silent node hides.
	f = newN2NFixture(t, readyNodes(3), servers, events, func() (*corev1.Pod, error) {
		return checkerReported(nodeToNodeUnreachableExit, "unreachable: "+ipOf(f.others("node-1", "node-2")[0])), nil
	})
	state = runN2N(f)
	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK)

	// Events that could not be read show no sandbox event either, so the
	// row is unknown as well, not a pass on the nodes that were reached.
	f = newN2NFixture(t, readyNodes(3), servers, events, checkerExit(0))
	f.client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("events"), "", fmt.Errorf("denied"))
	})
	state = runN2N(f)
	assert.Nil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	warnings = strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "node-3: pod events could not be read")
	assert.Contains(t, warnings, "the probe pod on node-3 was scheduled and showed no pod sandbox event")

	// A node that went NotReady while the probe waited explains its own pod,
	// so it is a coverage gap and the others pass.
	f = newN2NFixture(t, readyNodes(3), servers, events, checkerExit(0))
	f.client.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		f.markNotReady(t, "node-3")
		return false, nil, nil
	})
	state = runN2N(f)
	require.NotNil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	assert.True(t, *state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "node-3: scheduled, but no sandbox event yet")
}

// Every kubelet event that follows sandbox creation says the sandbox exists,
// Pulled included (an image already on the node is Pulled without Pulling),
// and Created alone too. The latest event decides whatever order the list
// returns them in, so a sandbox created on retry counts as created, and a
// mount failure after a sandbox failure leaves it undecided.
func TestProbeSandboxEvents(t *testing.T) {
	client := fake.NewSimpleClientset()
	failed := podEvent("failed", "FailedCreatePodSandBox", 1)
	failed.Message = "failed to get sandbox image \"registry.k8s.io/pause:3.9\""
	events := []corev1.Event{
		podEvent("pulled-only", "Pulled", 0),
		podEvent("created-only", "Created", 0),
		podEvent("retried", "Started", 1), podEvent("retried", "FailedCreatePodSandBox", 0),
		failed, podEvent("failed", "Pulling", 0),
		podEvent("mount", "FailedMount", 2), podEvent("mount", "FailedCreatePodSandBox", 1),
		podEvent("scheduled-only", "Scheduled", 0),
	}
	client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.EventList{Items: events}, nil
	})
	got, err := probeSandboxEvents(context.Background(), client, "ns")
	require.NoError(t, err)
	states := map[string]sandboxState{}
	for name, ev := range got {
		states[name] = ev.state
	}
	assert.Equal(t, map[string]sandboxState{
		"pulled-only": sandboxCreated, "created-only": sandboxCreated, "retried": sandboxCreated,
		"failed": sandboxFailed, "mount": sandboxPending,
	}, states)
	assert.Equal(t, `failed to get sandbox image "registry.k8s.io/pause:3.9"`, got["failed"].message,
		"the sandbox failure keeps its message, which tells the CNI from the runtime")
}

// An events list that keeps failing is retried until the read gives up, and
// then leaves every pod undecided with the cause.
func TestProbeSandboxEvents_UnreadableIsRetriedThenUndecided(t *testing.T) {
	client := fake.NewSimpleClientset()
	calls := 0
	client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	got, err := probeSandboxEvents(context.Background(), client, "ns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etcd timeout")
	assert.Nil(t, got)
	assert.Greater(t, calls, 1)
}

// An unbound DaemonSet pod has no spec.nodeName, but the controller pins it to
// its node through a metadata.name affinity field, so a capacity-blocked pod
// still names its node.
func TestClassifyProbeNodes_UnscheduledPodNamesItsNode(t *testing.T) {
	running := probePod("node-1", "Running", "10.0.0.1", "")
	pending := probePod("", "Pending", "", "")
	pending.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}
	pending.Spec.Affinity = daemonSetNodeAffinity("node-2")
	got := classifyProbeNodes([]corev1.Pod{running, pending}, []string{"node-1", "node-2"}, nil, nil)
	require.Len(t, got.gaps, 1)
	assert.Equal(t, "node-2: Unschedulable", got.gaps[0])
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
	"HTTPRoute": "v1", "GRPCRoute": "v1", "TCPRoute": "v1alpha2", "UDPRoute": "v1alpha2", "Gateway": "v1",
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
	kinds := map[schema.GroupVersionResource]string{gatewayClassGVR: "GatewayClassList"}
	for kind, v := range routeVersions {
		kinds[schema.GroupVersionResource{
			Group: gatewayAPIGroup, Version: v, Resource: strings.ToLower(kind) + "s",
		}] = kind + "List"
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)
}

var gatewayClassGVR = schema.GroupVersionResource{Group: gatewayAPIGroup, Version: "v1", Resource: "gatewayclasses"}

// envoyGatewayClient holds the given Gateways and two GatewayClasses: "eg",
// run by Envoy Gateway, and "istio", run by another implementation.
func envoyGatewayClient(t *testing.T, gws ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := gatewayClient(t, gws...)
	for name, controller := range map[string]string{
		"eg": envoyGatewayControllerName, "istio": "istio.io/gateway-controller",
	} {
		class := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": gatewayAPIGroup + "/v1",
			"kind":       "GatewayClass",
			"metadata":   map[string]interface{}{"name": name},
			"spec":       map[string]interface{}{"controllerName": controller},
		}}
		require.NoError(t, dyn.Tracker().Create(gatewayClassGVR, class, ""))
	}
	return dyn
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

// The configured list replaces discovery: it alone decides the set. A Gateway
// the NVCF routes attach to that the list leaves out is reported, since the
// checks do not assess it.
func TestResolveNVCFGateways_ConfiguredReplacesDiscovery(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	dyn := routeClient(route("HTTPRoute", "nvcf", "r", "nvcf-gateway-routes-1.0.0", parentRef("name", "other")))
	got, _, err := resolveNVCFGateways(context.Background(), routeDiscoveryClient(), dyn)
	require.NoError(t, err)
	assert.Equal(t, gatewaySet{"gw/shared-gw": true}, got)

	surface, surfaceErr := discoverGatewayAPIResources(context.Background(), routeDiscoveryClient())
	own := resolveGatewayOwnershipIn(context.Background(), nil, surface, surfaceErr, dyn)
	assert.Equal(t, []string{"nvcf/other"}, own.unlisted)
	state := &ValidationState{Log: testLog()}
	own.reportInvalid(state.Log, state)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nvcf/other, which "+gatewayNamesSetting+" leaves out")
	own.reportInvalid(state.Log, state)
	assert.Len(t, state.Warnings, 1, "reported once per run")
}

// A configured list replaces discovery, but must not hide a missing routes
// release: after install, no NVCF route at all is warned about, once per run.
// Before install there are no routes yet, and with routes there is nothing
// to say.
func TestResolveNVCFGateways_ConfiguredWithoutRoutesWarnsAfterInstall(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	surface, surfaceErr := discoverGatewayAPIResources(context.Background(), routeDiscoveryClient())
	withRoutes := routeClient(route("HTTPRoute", "nvcf", "api", "nvcf-gateway-routes-1.18.2",
		parentRef("name", "shared-gw", "namespace", "gw")))
	for name, tc := range map[string]struct {
		routes      *dynamicfake.FakeDynamicClient
		postInstall bool
		warned      bool
	}{
		"installed, no routes":      {routeClient(), true, true},
		"before install, no routes": {routeClient(), false, false},
		"installed, routes":         {withRoutes, true, false},
	} {
		own := resolveGatewayOwnershipIn(context.Background(), nil, surface, surfaceErr, tc.routes)
		state := &ValidationState{Log: testLog(), PostInstall: tc.postInstall}
		own.reportInvalid(state.Log, state)
		own.reportInvalid(state.Log, state)
		if !tc.warned {
			assert.Empty(t, state.Warnings, name)
			continue
		}
		require.Len(t, state.Warnings, 1, name)
		assert.Contains(t, state.Warnings[0], "no NVCF routes found although the control plane is installed", name)
		assert.Contains(t, state.Warnings[0], "gw/shared-gw", name)
	}
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
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw",
				corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
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
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw",
				corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
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
			gatewayLBService(envoyNS, "envoy-shared", "nvcf", "shared-gw",
				corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
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
	assert.NotContains(t, strings.Join(state.Recommendations, "; "), "gateway routes release")

	// Nothing addressed is unknown too: the pending Service may be another
	// team's, and NVCF's proxies may live in a namespace that was not listed.
	client = routeDiscoveryClient()
	addServices(t, client,
		gatewayLBService(envoyNS, "envoy-a", "x", "a", corev1.ServiceTypeLoadBalancer, ""))
	state = &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, denied(), state)
	assert.Nil(t, state.ExternalLBOK, "a foreign pending Service must not fail the NVCF row")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "1 pending")
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
	three, noDeadline := int32(3), int32(math.MaxInt32)
	one := intstr.FromInt32(1)
	// Each shape would be a tolerated rollout at its floor of 2 if it were
	// not paused: one the controller reports as moving, and one with no
	// progress deadline and old pods still running, a minute after its spec
	// was written.
	moving := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 2,
				ManagedFields: []metav1.ManagedFieldsEntry{specWrite(installManager, time.Now().Add(-time.Minute))}},
			Spec: appsv1.DeploymentSpec{Replicas: &three, Paused: true, Strategy: appsv1.DeploymentStrategy{
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &one}}},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 1, ReadyReplicas: 2,
				Conditions: []appsv1.DeploymentCondition{rolloutProgressing()},
			},
		}
	}
	unbounded := moving()
	unbounded.Spec.ProgressDeadlineSeconds = &noDeadline
	unbounded.Status.Conditions = nil

	for name, d := range map[string]*appsv1.Deployment{"progressing": moving(), "no deadline": unbounded} {
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), fake.NewSimpleClientset(d), nil, state)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.False(t, *state.Tier1DeploymentsOK, "a paused Deployment at 2/3 is held, not rolling: %s", name)

		unpaused := d.DeepCopy()
		unpaused.Spec.Paused = false
		state = &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), fake.NewSimpleClientset(unpaused), nil, state)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.True(t, *state.Tier1DeploymentsOK, "the same rollout unpaused is tolerated: %s", name)
	}

	ready := moving()
	ready.Status.ReadyReplicas = 3
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(ready), nil, state)
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
	assert.NotContains(t, joined, "grant the cluster-validator")
	assert.Contains(t, joined, "could not read Deployments in")
	assert.Contains(t, joined, "Too many requests: slow down")
	assert.Contains(t, joined, "check apiserver health")
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
				Conditions: []appsv1.DeploymentCondition{rolloutProgressing()},
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
			client := fake.NewSimpleClientset(rolling(tc.strategy, tc.ready))
			checkTier1Deployments(context.Background(), client, nil, state)
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
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: envoyGatewayNamespace, Generation: 1, Labels: map[string]string{
					owningGatewayNameLabel: gw, owningGatewayNamespaceLabel: "nvcf",
				}},
			Spec:   appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
		}
	}

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(envoyController(envoyGatewayNamespace),
		proxy("envoy-nvcf", "shared-gw", 2), proxy("envoy-team-b", "team-b-gw", 1)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "another team's proxy is not NVCF's to fail on")

	state = &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(envoyController(envoyGatewayNamespace),
		proxy("envoy-nvcf", "shared-gw", 1)), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "NVCF's own proxy is still assessed")
}

// A known quorum component is assessed at an even size too: a 4-node Cassandra
// ring at 0/4 is down whatever its parity, and a NATS mid-rollout beside it
// must not hide that.
func TestCheckTier2StatefulSets_EvenKnownQuorumComponentIsAssessed(t *testing.T) {
	four := int32(4)
	cassandra := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cassandra", Namespace: "cassandra-system"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &four},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	natsRolling := rollTo(makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"}),
		"nats-r2", time.Now().Add(-time.Minute))
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(append(natsRolling, cassandra)...), state)

	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// A skipped StatefulSet stays in the warnings even when the row exits through
// the tolerated-rollout pass.
func TestCheckTier2StatefulSets_SkippedListSurvivesRolloutExit(t *testing.T) {
	four := int32(4)
	other := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "nvcf"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &four},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	natsRolling := rollTo(makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"}),
		"nats-r2", time.Now().Add(-time.Minute))
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(append(natsRolling, other)...), state)

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

// A node whose probe pod cannot be placed for capacity leaves a coverage gap,
// not an UNKNOWN for the whole check: the other nodes are still probed, and
// the warning names the node from the pod's affinity.
func TestCheckNodeToNode_CapacityBlockedNodeIsACoverageGap(t *testing.T) {
	pending := probePod("", "Pending", "", "")
	pending.Name = "s-3"
	pending.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}
	pending.Spec.Affinity = daemonSetNodeAffinity("node-3")
	f := newN2NFixture(t,
		[]*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0), makeNode("node-3", true, 0)},
		[]corev1.Pod{
			runningProbePod("s-1", "node-1", "10.0.0.1"), runningProbePod("s-2", "node-2", "10.0.0.2"), pending,
		},
		nil, checkerExit(nodeToNodeUnreachableExit))

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK, "the nodes that did start were probed")
	assert.False(t, *state.NodeToNodeOK, "and a broken path between them still fails")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "node-3: Unschedulable")
}

// A first image pull longer than the wait is not a CNI fault: the kubelet
// creates the sandbox before it pulls, but publishes no pod IP until the pull
// returns. Pulling, Pulled, Created and Started all say the sandbox exists.
func TestCheckNodeToNode_SlowFirstPullIsNotANetworkFault(t *testing.T) {
	pulling := probePod("node-3", "Pending", "", "ContainerCreating")
	pulling.Name = "s-3"
	nodes := func() []*corev1.Node {
		return []*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0), makeNode("node-3", true, 0)}
	}
	servers := []corev1.Pod{
		runningProbePod("s-1", "node-1", "10.0.0.1"), runningProbePod("s-2", "node-2", "10.0.0.2"), pulling,
	}

	for _, tc := range []struct {
		events []corev1.Event
		gap    string
	}{
		{[]corev1.Event{podEvent("s-3", "Scheduled", 0), podEvent("s-3", "Pulling", 1)},
			"node-3: image pull still in progress"},
		{[]corev1.Event{podEvent("s-3", "Pulling", 0), podEvent("s-3", "Pulled", 1)},
			"node-3: sandbox created"},
		{[]corev1.Event{podEvent("s-3", "Pulled", 0), podEvent("s-3", "Created", 1), podEvent("s-3", "Started", 2)},
			"node-3: sandbox created"},
	} {
		f := newN2NFixture(t, nodes(), servers, tc.events, checkerExit(0))
		state := &ValidationState{Log: testLog()}
		checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)
		require.NotNil(t, state.NodeToNodeOK)
		assert.True(t, *state.NodeToNodeOK)
		assert.Contains(t, strings.Join(state.Warnings, "; "), tc.gap)
	}

	// A sandbox failure after the pull started is the network after all.
	f := newN2NFixture(t, nodes(), servers,
		[]corev1.Event{podEvent("s-3", "Pulling", 0), podEvent("s-3", "FailedCreatePodSandBox", 1)}, checkerExit(0))
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)
	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK)
}

// The checker's exit code comes from the terminal pod the wait already read.
// Fetching it again let a 429 on the second Get turn a real connection failure
// into UNKNOWN and drop the firewall advice.
func TestCheckNodeToNode_ConnectionFailureSurvivesAThrottledRead(t *testing.T) {
	calls := 0
	f := newN2NFixture(t, []*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0)},
		[]corev1.Pod{runningProbePod("s-1", "node-1", "10.0.0.1"), runningProbePod("s-2", "node-2", "10.0.0.2")},
		nil, func() (*corev1.Pod, error) {
			calls++
			if calls == 1 {
				return checkerPod(nodeToNodeUnreachableExit), nil
			}
			return nil, apierrors.NewTooManyRequestsError("slow down")
		})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK)
	assert.NotEmpty(t, state.Recommendations)
}

// A node that is NotReady, being drained or removed, or without a pod network
// is not probed, even though the probe tolerates every taint: a pod there
// tests nothing and may never be deleted. A GPU taint is no such fence.
func TestNodeToNodeTargets_SkipsFencedNodes(t *testing.T) {
	tainted := func(name, key string) corev1.Node {
		n := *makeNode(name, true, 0)
		n.Spec.Taints = []corev1.Taint{{Key: key, Effect: corev1.TaintEffectNoSchedule}}
		return n
	}
	noNetwork := *makeNode("no-network", true, 0)
	noNetwork.Status.Conditions = append(noNetwork.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeNetworkUnavailable, Status: corev1.ConditionTrue})
	now := metav1.Now()
	deleting := *makeNode("deleting", true, 0)
	deleting.DeletionTimestamp = &now
	labelled := func(name, key, value string) corev1.Node {
		n := *makeNode(name, true, 0)
		n.Labels = map[string]string{key: value}
		return n
	}
	targets, skipped, unsupported := nodeToNodeTargets([]corev1.Node{
		*makeNode("ready", true, 0),
		tainted("gpu", "nvidia.com/gpu"),
		labelled("linux", corev1.LabelOSStable, "linux"),
		*makeNode("not-ready", false, 0),
		tainted("disrupted", "karpenter.sh/disrupted"),
		tainted("scale-down", "ToBeDeletedByClusterAutoscaler"),
		tainted("cilium", "node.cilium.io/agent-not-ready"),
		noNetwork, deleting,
		tainted("virtual", "virtual-kubelet.io/provider"),
		labelled("windows", corev1.LabelOSStable, "windows"),
		labelled("fargate", "eks.amazonaws.com/compute-type", "fargate"),
	})
	assert.Equal(t, []string{"ready", "gpu", "linux"}, targets)
	assert.Equal(t, []string{
		"not-ready (NotReady)", "disrupted (tainted karpenter.sh/disrupted)",
		"scale-down (tainted ToBeDeletedByClusterAutoscaler)", "cilium (tainted node.cilium.io/agent-not-ready)",
		"no-network (network unavailable)", "deleting (being deleted)",
	}, skipped)
	assert.Equal(t, []string{"virtual (virtual-kubelet)", "windows (windows)", "fargate (Fargate)"}, unsupported,
		"a node no Linux DaemonSet pod runs on is never a target")
}

// Every fencing taint keeps the probe off its node, including the ones for a
// node not yet registered or initialized.
func TestNodeToNodeTargets_EveryFencingTaint(t *testing.T) {
	assert.True(t, nodeFencingTaints["karpenter.sh/unregistered"])
	assert.True(t, nodeFencingTaints["node.cluster.x-k8s.io/uninitialized"])
	for key := range nodeFencingTaints {
		t.Run(key, func(t *testing.T) {
			n := *makeNode("fenced", true, 0)
			n.Spec.Taints = []corev1.Taint{{Key: key, Effect: corev1.TaintEffectNoSchedule}}
			targets, skipped, _ := nodeToNodeTargets([]corev1.Node{*makeNode("ready", true, 0), n})
			assert.Equal(t, []string{"ready"}, targets)
			assert.Equal(t, []string{"fenced (tainted " + key + ")"}, skipped)
		})
	}
}

// The kubelet can refuse the NodeName-pinned checker on a node at its pod
// limit. That says nothing about the overlay, so the checker moves to another
// node rather than leaving the row UNKNOWN on every run.
func TestCheckNodeToNode_RefusedCheckerMovesToAnotherNode(t *testing.T) {
	refused := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfpods"}}
	calls := 0
	f := newN2NFixture(t, []*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0)},
		[]corev1.Pod{runningProbePod("s-1", "node-1", "10.0.0.1"), runningProbePod("s-2", "node-2", "10.0.0.2")},
		nil, func() (*corev1.Pod, error) {
			calls++
			if calls == 1 {
				return refused, nil
			}
			return checkerPod(0), nil
		})
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)
	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK)
	assert.Equal(t, 2, calls, "the second node's checker ran")
	require.Len(t, f.checkerNodes, 2)
	assert.NotEqual(t, f.checkerNodes[0], f.checkerNodes[1], "the checker moved to the other node")
	assert.Empty(t, f.leftovers(t), "both checker pods are cleaned up")
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
		name     string
		status   corev1.PodStatus
		reported bool
		imageRec bool
	}{
		{"connection failed", exited(nodeToNodeUnreachableExit), true, false},
		{"connection refused", exited(nodeToNodeRefusedExit), true, false},
		{"nc failed its self-test", exited(nodeToNodeNoNetcatExit), false, true},
		{"other exit", exited(137), false, false},
		{"kubelet rejected it", corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfcpu"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			why, rec := checkerFailureCause(pod(tc.status))
			assert.Equal(t, tc.reported, why == "", why)
			assert.Equal(t, tc.imageRec, strings.HasPrefix(rec, ProbeImageRecommendation), rec)
		})
	}
}

// The script separates a failed connection from everything else by exit code,
// first proves the image's nc can listen and connect with the probe's own
// flags, dials every target, and names the ones that failed last, where the
// kubelet's termination message keeps them.
func TestBuildNodeToNodeCheckerPod_ExitCodesSeparateNetworkFailures(t *testing.T) {
	pod := buildNodeToNodeCheckerPod("c", "ns", "node-1", nil, []string{"10.0.0.2", "10.0.0.3"}, "img")
	cmd := pod.Spec.Containers[0].Command[2]
	assert.Contains(t, cmd, fmt.Sprintf("command -v nc >/dev/null 2>&1 || exit %d", nodeToNodeNoNetcatExit))
	assert.Contains(t, cmd, fmt.Sprintf("nc -l -p %d >/dev/null 2>&1 &", nodeToNodeTestPort))
	assert.Contains(t, cmd, fmt.Sprintf("until nc -v -z -w 5 127.0.0.1 %d", nodeToNodeTestPort))
	assert.Contains(t, cmd, "for ip in 10.0.0.2 10.0.0.3; do")
	assert.Contains(t, cmd, fmt.Sprintf("out=$(nc -v -z -w 5 $ip %d 2>&1) && ok=1", nodeToNodeTestPort))
	assert.Contains(t, cmd, fmt.Sprintf("if [ $n -ge %d ]; then stopped=1; break; fi", nodeToNodeMaxUnreachable))
	assert.Contains(t, cmd, "*refused*)")
	assert.Contains(t, cmd, fmt.Sprintf(`echo "unreachable:$unreachable"; exit %d;`, nodeToNodeUnreachableExit))
	assert.Contains(t, cmd, fmt.Sprintf(`[ -z "$refused" ] || exit %d`, nodeToNodeRefusedExit))
	assert.Equal(t, corev1.TerminationMessageFallbackToLogsOnError, pod.Spec.Containers[0].TerminationMessagePolicy,
		"the IPs the script names reach the pod status without a pods/log grant")
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
	var dsNS string
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		dsLabels, dsNS = ds.Labels, ds.Namespace
		return true, ds, nil
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

// With no NVCF routes, proxies cannot be attributed, so they are skipped with a
// warning rather than silently.
func TestCheckTier1Deployments_UnattributedProxiesAreReported(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	two := int32(2)
	proxy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-x", Namespace: envoyGatewayNamespace, Generation: 1,
			Labels: map[string]string{owningGatewayNameLabel: "x-gw", owningGatewayNamespaceLabel: "x"}},
		Spec:   appsv1.DeploymentSpec{Replicas: &two},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 1},
	}
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(proxy), nil, state)

	assert.Contains(t, strings.Join(state.Warnings, "; "), "no NVCF routes found")
}

func TestPostInstallMode(t *testing.T) {
	for v, want := range map[string]bool{"true": true, " TRUE ": true, "1": true, "": false, "no": false} {
		assert.Equal(t, want, postInstallMode(v), v)
	}
}

func gatewayObject(ns, name, class string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gatewayAPIGroup + "/v1",
		"kind":       "Gateway",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"gatewayClassName": class},
	}}
}

// gatewayClient returns a route client holding the given Gateways. They are
// added under an explicit resource: the fake guesses the resource from the
// kind, and its naive pluralizer turns Gateway into "gatewaies".
func gatewayClient(t *testing.T, gws ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := routeClient()
	gvr := schema.GroupVersionResource{Group: gatewayAPIGroup, Version: "v1", Resource: "gateways"}
	for _, gw := range gws {
		require.NoError(t, dyn.Tracker().Create(gvr, gw, gw.GetNamespace()))
	}
	return dyn
}

// Serves the route kinds plus gateways, so GatewayClass lookups can run.
func gatewayClassDiscoveryClient() *fake.Clientset {
	return gatewayDiscoveryClient(
		gatewayAPIGroup+"/v1/httproutes",
		gatewayAPIGroup+"/v1/gateways",
	)
}

// In merged-gateways mode the single proxy carries only its GatewayClass. It
// serves NVCF's Gateway when their classes match, so it is assessed; a
// class-only proxy of another class, and another team's per-Gateway proxy that
// happens to share the class, are not.
func TestCheckTier1Deployments_MergedProxyIsAttributedByClass(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	two := int32(2)
	proxy := func(name string, labels map[string]string, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envoyGatewayNamespace, Generation: 1, Labels: labels},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
		}
	}
	run := func(objs ...*appsv1.Deployment) *ValidationState {
		client := gatewayClassDiscoveryClient()
		for _, d := range objs {
			_, err := client.AppsV1().Deployments(d.Namespace).Create(context.Background(), d, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		state := &ValidationState{Log: testLog()}
		gws := gatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg"))
		checkTier1Deployments(context.Background(), client, gws, state)
		return state
	}

	state := run(proxy("envoy-merged", map[string]string{owningGatewayClassLabel: "eg"}, 1))
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "the merged proxy serving NVCF's class is NVCF's")

	state = run(
		proxy("envoy-merged-other", map[string]string{owningGatewayClassLabel: "team-b"}, 1),
		proxy("envoy-team-b", map[string]string{
			owningGatewayNameLabel: "team-b-gw", owningGatewayNamespaceLabel: "team-b", owningGatewayClassLabel: "eg",
		}, 1),
		proxy("envoy-merged", map[string]string{owningGatewayClassLabel: "eg"}, 2),
	)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "other classes and other teams' per-Gateway proxies are not NVCF's")
}

// The LoadBalancer row counts a merged-gateways Service for every NVCF Gateway
// of its class, instead of reporting each as missing.
func TestCheckExternalLoadBalancer_MergedProxyServesNVCFGateways(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw,nvcf/grpc-gw")
	merged := func(ip string) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "envoy-merged", Namespace: envoyGatewayNamespace,
				Labels: map[string]string{owningGatewayClassLabel: "eg"}},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		}
		if ip != "" {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
		}
		return svc
	}
	gws := func() *dynamicfake.FakeDynamicClient {
		return gatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg"), gatewayObject("nvcf", "grpc-gw", "eg"))
	}

	client := gatewayClassDiscoveryClient()
	addServices(t, client, merged("203.0.113.1"))
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, gws(), state)
	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK)

	client = gatewayClassDiscoveryClient()
	addServices(t, client, merged(""))
	state = &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, gws(), state)
	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK, "the merged proxy still pending is NVCF's pending address")
}

// Without the Gateways' classes a merged proxy cannot be attributed, so the
// Gateways it may be serving are unknown rather than missing.
func TestCheckExternalLoadBalancer_UnreadableClassesAreUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	client := gatewayClassDiscoveryClient()
	addServices(t, client, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-merged", Namespace: envoyGatewayNamespace,
			Labels: map[string]string{owningGatewayClassLabel: "eg"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}},
		}},
	})
	dyn := routeClient()
	dyn.PrependReactor("list", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayAPIGroup, Resource: "gateways"}, "", fmt.Errorf("denied"))
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, dyn, state)

	assert.Nil(t, state.ExternalLBOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not be attributed")
}

// Each NVCF Gateway contributes its class, and a Gateway that is not NVCF's,
// including a same-named one in another namespace, contributes none.
func TestNVCFGatewayClasses_MatchesQualifiedEntries(t *testing.T) {
	dyn := gatewayClient(t,
		gatewayObject("nvcf", "shared-gw", "eg"),
		gatewayObject("gw", "grpc-gw", "eg-grpc"),
		gatewayObject("team-b", "grpc-gw", "team-b"),
	)
	got, err := nvcfGatewayClasses(context.Background(), gatewayClassDiscoveryClient(), dyn,
		gatewaySet{"nvcf/shared-gw": true, "gw/grpc-gw": true})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"eg": {"nvcf/shared-gw"}, "eg-grpc": {"gw/grpc-gw"}}, got)
}

// When the NVCF Gateways cannot be determined, NVCF's own proxies may be among
// those skipped, so a clean row is UNKNOWN rather than a pass. An observed
// failure still decides the row.
func TestCheckTier1Deployments_UnidentifiedProxiesAreUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	two := int32(2)
	dep := func(ns, name string, labels map[string]string, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1, Labels: labels},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
		}
	}
	denied := func() *dynamicfake.FakeDynamicClient {
		dyn := routeClient()
		dyn.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: gatewayAPIGroup, Resource: "httproutes"}, "", fmt.Errorf("denied"))
		})
		return dyn
	}
	run := func(objs ...*appsv1.Deployment) *ValidationState {
		client := routeDiscoveryClient()
		for _, d := range objs {
			_, err := client.AppsV1().Deployments(d.Namespace).Create(context.Background(), d, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), client, denied(), state)
		return state
	}
	proxy := dep(envoyGatewayNamespace, "envoy-x", map[string]string{owningGatewayNameLabel: "x"}, 1)

	state := run(dep("nvcf", "api", nil, 2), proxy)
	assert.Nil(t, state.Tier1DeploymentsOK, "unidentified proxies are unobserved, not passed")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not be identified")

	state = run(dep("nvcf", "api", nil, 1), proxy.DeepCopy())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "an observed failure still decides the row")
}

// deniedGatewayList is a route client whose Gateway List is forbidden, so the
// NVCF Gateways' classes cannot be read.
func deniedGatewayList() *dynamicfake.FakeDynamicClient {
	dyn := routeClient()
	dyn.PrependReactor("list", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayAPIGroup, Resource: "gateways"}, "", fmt.Errorf("denied"))
	})
	return dyn
}

// Unreadable classes leave only merged-gateways proxies unattributed. Another
// team's per-Gateway proxy names its own Gateway, so skipping it does not make
// a clean row UNKNOWN. An unattributed merged proxy is still assessed: Ready,
// it says nothing bad about the tier; not Ready, it may be NVCF's outage.
func TestCheckTier1Deployments_UnreadableClassesOnlyAffectMergedProxies(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	two := int32(2)
	depReady := func(ns, name string, labels map[string]string, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1, Labels: labels},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
		}
	}
	dep := func(ns, name string, labels map[string]string) *appsv1.Deployment {
		return depReady(ns, name, labels, 2)
	}
	run := func(objs ...*appsv1.Deployment) *ValidationState {
		client := gatewayClassDiscoveryClient()
		for _, d := range objs {
			_, err := client.AppsV1().Deployments(d.Namespace).Create(context.Background(), d, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), client, deniedGatewayList(), state)
		return state
	}

	state := run(dep("nvcf", "api", nil), dep(envoyGatewayNamespace, "envoy-team-b", map[string]string{
		owningGatewayNameLabel: "team-b-gw", owningGatewayNamespaceLabel: "team-b", owningGatewayClassLabel: "eg",
	}))
	require.NotNil(t, state.Tier1DeploymentsOK, "a foreign per-Gateway proxy does not depend on the classes")
	assert.True(t, *state.Tier1DeploymentsOK)

	state = run(dep("nvcf", "api", nil), dep(envoyGatewayNamespace, "envoy-merged",
		map[string]string{owningGatewayClassLabel: "eg"}))
	require.NotNil(t, state.Tier1DeploymentsOK, "a Ready proxy of unknown owner hides nothing")
	assert.True(t, *state.Tier1DeploymentsOK)

	state = run(dep("nvcf", "api", nil), depReady(envoyGatewayNamespace, "envoy-merged",
		map[string]string{owningGatewayClassLabel: "eg"}, 1))
	assert.Nil(t, state.Tier1DeploymentsOK, "a merged proxy that is not Ready may be NVCF's outage")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "merged-gateways proxies")
}

// An NVCF proxy observed without an address fails the row even when a merged
// proxy could not be attributed to the other NVCF Gateway.
func TestCheckExternalLoadBalancer_PendingBeatsUnattributed(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw,nvcf/api-gw")
	client := gatewayClassDiscoveryClient()
	addServices(t, client,
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "envoy-merged", Namespace: envoyGatewayNamespace,
				Labels: map[string]string{owningGatewayClassLabel: "eg"}},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}},
			}},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "envoy-api", Namespace: envoyGatewayNamespace,
				Labels: map[string]string{owningGatewayNameLabel: "api-gw", owningGatewayNamespaceLabel: "nvcf"}},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, deniedGatewayList(), state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK)
	warnings := strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "have no external address")
	assert.NotContains(t, warnings, "no proxy Service found", "an unattributed Gateway is not reported as missing")
	assert.Contains(t, strings.Join(state.Recommendations, "; "),
		"kubectl -n "+envoyGatewayNamespace+" describe svc envoy-api",
		"a pending Service gets advice naming it")
}

// The Gateway CRD can be removed between discovery and the List; no Gateways
// means no classes, not an error.
func TestNVCFGatewayClasses_ListNotFoundIsEmpty(t *testing.T) {
	dyn := routeClient()
	dyn.PrependReactor("list", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: gatewayAPIGroup, Resource: "gateways"}, "")
	})
	got, err := nvcfGatewayClasses(context.Background(), gatewayClassDiscoveryClient(), dyn,
		gatewaySet{"nvcf/shared-gw": true})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// rollingNATSWithDownPod is a 3-replica StatefulSet a minute into a
// RollingUpdate to nats-r2, with two Ready pods on the given nodes and a
// third, just created and not Ready, on revision rev.
func rollingNATSWithDownPod(readyNodes []string, rev string, mutate func(*corev1.Pod)) []runtime.Object {
	objs := rollTo(makeQuorumSTS("nats", "nats-system", 3, 2, readyNodes), "nats-r2", time.Now().Add(-time.Minute))
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Status.Replicas = 3
	controller := true
	down := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.Now(),
			Name:              "nats-2", Namespace: "nats-system",
			Labels: map[string]string{
				"app": "nats", appsv1.ControllerRevisionHashLabelKey: rev, appsv1.PodIndexLabel: "2",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: "nats", UID: sts.UID, Controller: &controller,
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "node-3"},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	if mutate != nil {
		mutate(down)
	}
	return append(objs, down)
}

func runTier2(objs []runtime.Object) *ValidationState {
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(objs...), state)
	return state
}

// A rollout is tolerated one pod down only while it can finish. Some evidence
// says at once that it cannot: an image name that never parses, in a
// container or an init container, a pod that failed, or an old-revision pod
// held back by a partition, which the rollout will never replace.
func TestCheckTier2StatefulSets_StalledRolloutFails(t *testing.T) {
	failed := func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }
	invalidInit := func(p *corev1.Pod) {
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name:  "init",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "InvalidImageName"}},
		}}
	}
	down := func(rev string, mutate func(*corev1.Pod)) []runtime.Object {
		return rollingNATSWithDownPod([]string{"node-1", "node-2"}, rev, mutate)
	}
	for name, objs := range map[string][]runtime.Object{
		"invalid image name":                down("nats-r2", waiting("InvalidImageName")),
		"invalid init container image name": down("nats-r2", invalidInit),
		"failed pod":                        down("nats-r2", failed),
		"old pod below partition":           belowPartition(down("nats-r1", nil)),
	} {
		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, name)
		assert.NotContains(t, strings.Join(state.Warnings, "; "), "rolling update in progress", name)
	}
}

// A replacement pod that is still being created is a rollout in progress.
func TestCheckTier2StatefulSets_StartingRolloutPodIsTolerated(t *testing.T) {
	state := runTier2(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", waiting("ContainerCreating")))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "rolling update in progress")
}

// A tolerated rollout still gets the placement scan: the two Ready peers on one
// node are one node loss from losing quorum, as they would be at 3/3.
func TestCheckTier2StatefulSets_TolerateRolloutStillChecksPlacement(t *testing.T) {
	state := runTier2(rollingNATSWithDownPod([]string{"node-1", "node-1"}, "nats-r2", nil))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// A rollout staged with a partition stops once it has updated the pods at and
// above the partition, and CurrentRevision does not move until the partition
// is lowered. With every pod Ready that is no rollout in progress: warning
// "rolling update in progress" on every run kept --wait polling forever.
func TestCheckTier2StatefulSets_RolloutHeldAtPartitionIsSettled(t *testing.T) {
	held := func(updated int32) []runtime.Object {
		objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
		sts := objs[0].(*appsv1.StatefulSet)
		partition := int32(2)
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
		}
		sts.Status.UpdateRevision = "nats-r2"
		sts.Status.UpdatedReplicas = updated
		return objs
	}
	state := runTier2(held(1))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "rolling update in progress")

	// Short of the partition, the rollout is still in progress.
	state = runTier2(held(0))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "rolling update in progress")
}

// belowPartition holds pods below ordinal 3 on their old revision, so the
// fixture's nats-2 is one the rollout will not replace.
func belowPartition(objs []runtime.Object) []runtime.Object {
	partition := int32(3)
	objs[0].(*appsv1.StatefulSet).Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type:          appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
	}
	return objs
}

// The controller deletes the pod it replaces, so a terminating old-revision pod
// that is NotReady while it drains is the rollout moving, not stalling, even
// when every pod and the revision are old: the deletion is the progress.
func TestCheckTier2StatefulSets_TerminatingOldRevisionPodIsProgress(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	terminating := func(p *corev1.Pod) {
		now := metav1.Now()
		p.DeletionTimestamp = &now
		p.Status.Phase = corev1.PodRunning
	}
	objs := rollTo(createdAt(belowPartition(
		rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r1", terminating)), old), "nats-r2", old)
	state := runTier2(objs)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)

	// Without a partition an old-revision pod that is down but not stuck is
	// one the rollout still reaches.
	state = runTier2(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r1", nil))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
}

// createdAt dates every pod in objs.
func createdAt(objs []runtime.Object, created time.Time) []runtime.Object {
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			p.CreationTimestamp = metav1.NewTime(created)
		}
	}
	return objs
}

// A one-down rollout that makes no progress for too long fails however its
// down pod looks: a replacement never created, one stuck in ContainerCreating,
// or one Running but never Ready. A recent one is still tolerated. With the
// update revision unreadable, the last write to the spec still dates it.
func TestCheckTier2StatefulSets_RolloutWithoutProgressFails(t *testing.T) {
	old, recent := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute)
	notJoining := func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }
	missing := func() []runtime.Object {
		return makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"})
	}
	down := func(mutate func(*corev1.Pod)) []runtime.Object {
		return rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", mutate)
	}
	for name, objs := range map[string][]runtime.Object{
		"never created":      rollTo(createdAt(missing(), old), "nats-r2", old),
		"ContainerCreating":  rollTo(createdAt(down(waiting("ContainerCreating")), old), "nats-r2", old),
		"Running, not Ready": rollTo(createdAt(down(notJoining), old), "nats-r2", old),
		"revision unreadable, pod missing": withoutRevision(
			rollTo(createdAt(missing(), old), "nats-r2", old), "nats-r2"),
	} {
		state, log := runTier2Logged(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, name)
		assert.Contains(t, log, "no progress for 2h", name)
	}

	for name, objs := range map[string][]runtime.Object{
		"just started, pod not yet recreated": rollTo(createdAt(missing(), old), "nats-r2", recent),
		"replacement created recently": rollTo(createdAt(down(waiting("ContainerCreating")), recent),
			"nats-r2", old),
		"revision unreadable, spec written recently": withoutRevision(
			rollTo(createdAt(missing(), old), "nats-r2", recent), "nats-r2"),
		"spec write unrecorded, revision recent": unrecorded(rollTo(createdAt(missing(), old), "nats-r2", recent)),
	} {
		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.True(t, *state.Tier2StatefulSetsOK, name)
	}

	// A pod missing with nothing to date the rollout, no readable revision and
	// no recorded spec write, is not observed: passing it would tolerate a
	// replacement that is never created, forever.
	for name, revErr := range map[string]error{
		"revision absent": nil,
		"revision denied": apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "controllerrevisions"}, "nats-r2", fmt.Errorf("denied")),
		"revision read fails": apierrors.NewInternalError(fmt.Errorf("etcd timeout")),
	} {
		client := fake.NewSimpleClientset(
			withoutRevision(unrecorded(rollTo(createdAt(missing(), old), "nats-r2", old)), "nats-r2")...)
		if revErr != nil {
			client.PrependReactor("get", "controllerrevisions", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, revErr
			})
		}
		state := &ValidationState{Log: testLog()}
		checkTier2StatefulSets(context.Background(), client, state)
		assert.Nil(t, state.Tier2StatefulSetsOK, name)
		joined := strings.Join(state.Warnings, "; ")
		assert.Contains(t, joined, "could not read ControllerRevision nats-system/nats-r2", name)
		assert.Contains(t, joined, "status unknown (nats-system/nats could not be observed)", name)
		if revErr != nil {
			assert.Contains(t, joined, revErr.Error(), name)
		}
	}
}

// HA is read from what was rendered. A component with pod anti-affinity is an
// HA install and below three replicas has lost quorum. Without it, as mode
// none renders, one replica is the deployed shape and placement is not judged,
// but readiness still is: a crashed single Cassandra is the database down.
func TestCheckTier2StatefulSets_HAIsReadFromAntiAffinity(t *testing.T) {
	single := func(ready int32) []runtime.Object {
		return makeQuorumSTS("cassandra", "cassandra-system", 1, ready, []string{"node-1"})
	}

	state := runTier2(single(1))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "spread for HA, but below the quorum minimum")
	assert.Equal(t, []string{tier2BelowQuorumAdvice}, state.Recommendations, "told to scale, not to spread")

	state = runTier2(withoutSpread(single(1)))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "one replica is what mode none deploys")
	assert.Equal(t, 1, state.Tier2PlacementNotAssessed)

	state = runTier2(withoutSpread(single(0)))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "a single replica that is down is still down")
	assert.Equal(t, []string{tier2NotReadyAdvice}, state.Recommendations,
		"a down non-HA replica is not told to spread across nodes")

	state = runTier2(withoutSpread(makeQuorumSTS("cassandra", "cassandra-system", 0, 0, nil)))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.Equal(t, []string{tier2ScaledToZeroAdvice}, state.Recommendations)

	state = runTier2(withoutSpread(makeQuorumSTS("nats", "nats-system", 3, 3,
		[]string{"node-1", "node-1", "node-1"})))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "co-located on a single node is what mode none allows")
	assert.Equal(t, 1, state.Tier2PlacementNotAssessed, "the pass does not claim distinct nodes")

	state = runTier2(makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"}))
	assert.Zero(t, state.Tier2PlacementNotAssessed)
}

// A StatefulSet without anti-affinity has its placement left alone, but its
// rollout is still held to the same rule: one that cannot finish fails.
func TestCheckTier2StatefulSets_UnspreadStalledRolloutFails(t *testing.T) {
	state := runTier2(withoutSpread(rollingNATSWithDownPod([]string{"node-1", "node-1"}, "nats-r2",
		waiting("InvalidImageName"))))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// After install, each stack quorum component must be found unless it is
// declared external: a missing one leaves the row UNKNOWN with a warning
// naming it, even beside healthy peers. Before install nothing is expected.
func TestCheckTier2StatefulSets_MissingComponentAfterInstall(t *testing.T) {
	t.Setenv(openBaoNamespaceEnv, "")
	t.Setenv(externalComponentsEnv, "")
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(), state)
	assert.Nil(t, state.Tier2StatefulSetsOK)
	warnings := strings.Join(state.Warnings, "; ")
	for _, c := range []string{"nats", "openbao", "cassandra"} {
		assert.Contains(t, warnings, "quorum component "+c)
	}
	// OpenBao may only have moved namespace, so its warning names that setting.
	assert.Contains(t, warnings, "quorum component openbao (StatefulSet openbao or openbao-server in "+
		"vault-system) not found after install; set clusterValidator.openBaoNamespace")
	assert.NotContains(t, warnings, "nats-system) not found after install; set clusterValidator.openBaoNamespace")

	state = &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(), state)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Empty(t, state.Warnings, "before install, nothing to report")

	peers := append(makeQuorumSTS("openbao-server", "vault-system", 3, 3, []string{"n1", "n2", "n3"}),
		makeQuorumSTS("cassandra", "cassandra-system", 3, 3, []string{"n1", "n2", "n3"})...)
	state = &ValidationState{Log: testLog(), PostInstall: true}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(peers...), state)
	assert.Nil(t, state.Tier2StatefulSetsOK, "NATS is gone though its peers are healthy")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "quorum component nats")

	t.Setenv(externalComponentsEnv, " NATS ,bogus")
	state = &ValidationState{Log: testLog(), PostInstall: true}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(peers...), state)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "NATS is declared external")
	warnings = strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "bogus")
	assert.NotContains(t, warnings, "quorum component nats")

	t.Setenv(externalComponentsEnv, "nats,openbao,cassandra")
	state = &ValidationState{Log: testLog(), PostInstall: true}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(), state)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "everything runs outside the cluster")
}

// Another install's StatefulSet in a shared namespace is not assessed: it can
// neither fail the row nor stand in for a missing NVCF component.
func TestCheckTier2StatefulSets_SharedNamespaceForeignIsNotAssessed(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(openBaoNamespaceEnv, "")
	t.Setenv(externalComponentsEnv, "")
	nats := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"n1", "n2", "n3"})
	redis := makeQuorumSTS("redis", envoyGatewayNamespace, 3, 2, []string{"n1", "n2"})
	state := runTier2(append(nats, redis...))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "a foreign Redis at 2/3 is not NVCF's")

	t.Setenv(externalComponentsEnv, "openbao,cassandra")
	healthyRedis := makeQuorumSTS("redis", "cert-manager", 3, 3, []string{"n1", "n2", "n3"})
	state = &ValidationState{Log: testLog(), PostInstall: true}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(healthyRedis...), state)
	assert.Nil(t, state.Tier2StatefulSetsOK, "a healthy foreign StatefulSet does not hide a missing NATS")
}

// The rule matches the stack's own components only: by exact name, in their
// own namespace. A same-prefixed workload, or a nats elsewhere, is not one.
func TestIsKnownQuorumComponent(t *testing.T) {
	t.Setenv(openBaoNamespaceEnv, "")
	isKnown := func(ns, name string) bool {
		_, ok := knownQuorumComponent(ns, name)
		return ok
	}
	assert.True(t, isKnown("cassandra-system", "cassandra"))
	assert.True(t, isKnown("vault-system", "openbao-server"))
	assert.True(t, isKnown("nats-system", "nats"))
	assert.False(t, isKnown("cassandra-system", "cassandra-backup"))
	assert.False(t, isKnown("nvcf", "nvcf-nats"))
	assert.False(t, isKnown("nvcf", "nats"))

	t.Setenv(openBaoNamespaceEnv, "openbao")
	assert.True(t, isKnown("openbao", "openbao-server"), "a relocated OpenBao is still OpenBao")
	assert.False(t, isKnown("vault-system", "openbao-server"), "the default namespace is no longer OpenBao's")

	// Relocating OpenBao beside NATS keeps NATS a known component.
	t.Setenv(openBaoNamespaceEnv, "nats-system")
	for name, want := range map[string]string{"nats": "nats", "openbao-server": "openbao"} {
		got, ok := knownQuorumComponent("nats-system", name)
		assert.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}
}

// proxyDeployment is an Envoy proxy Deployment for gwNS/gw at ready of 2.
func proxyDeployment(name, gwNS, gw string, ready int32) *appsv1.Deployment {
	two := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envoyGatewayNamespace, Generation: 1,
			Labels: map[string]string{owningGatewayNameLabel: gw, owningGatewayNamespaceLabel: gwNS}},
		Spec:   appsv1.DeploymentSpec{Replicas: &two},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: ready},
	}
}

func runTier1(t *testing.T, postInstall bool, routes dynamic.Interface, objs ...runtime.Object) *ValidationState {
	t.Helper()
	state := &ValidationState{Log: testLog(), PostInstall: postInstall}
	runTier1On(t, state, routes, objs...)
	return state
}

// runTier1On runs Tier-1 over objs into state, so a test can read its log.
func runTier1On(t *testing.T, state *ValidationState, routes dynamic.Interface, objs ...runtime.Object) {
	t.Helper()
	client := gatewayDiscoveryClient(
		gatewayAPIGroup+"/v1/httproutes",
		gatewayAPIGroup+"/v1/grpcroutes",
		gatewayAPIGroup+"/v1alpha2/tcproutes",
		gatewayAPIGroup+"/v1/gateways",
		gatewayAPIGroup+"/v1/gatewayclasses",
	)
	for _, o := range objs {
		var err error
		switch v := o.(type) {
		case *appsv1.Deployment:
			_, err = client.AppsV1().Deployments(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
		case *appsv1.DaemonSet:
			_, err = client.AppsV1().DaemonSets(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
		default:
			err = client.Tracker().Add(o)
		}
		require.NoError(t, err)
	}
	checkTier1Deployments(context.Background(), client, routes, state)
}

// A failed ownership lookup leaves a Ready proxy nothing to say against the
// tier, so before install the row passes. After install the NVCF Gateways
// must each have a proxy, which cannot be confirmed without knowing them, so
// the row is UNKNOWN rather than a pass.
func TestCheckTier1Deployments_ReadyProxiesOfUnknownOwnerPass(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	denied := routeClient()
	denied.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	proxies := func() []runtime.Object {
		return []runtime.Object{proxyDeployment("envoy-a", "gw", "shared-gw", 2),
			proxyDeployment("envoy-b", "gw", "grpc-gw", 2)}
	}
	state := runTier1(t, false, denied, proxies()...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)

	state = runTier1(t, true, denied, append(proxies(), nvcfService())...)
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not confirm every NVCF Gateway has a proxy")
}

// Installed, with no route or setting naming an NVCF Gateway, NVCF's own
// proxies can be among those present: one that is down leaves the row UNKNOWN
// instead of reading as someone else's.
func TestCheckTier1Deployments_PostInstallWithoutNVCFRoutesAssessesProxies(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	down := proxyDeployment("envoy-gw-shared", "gw", "shared-gw", 0)

	state := runTier1(t, true, routeClient(), down, nvcfService())
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"no NVCF routes found although the control plane is installed")

	state = runTier1(t, false, routeClient(), down.DeepCopy())
	require.NotNil(t, state.Tier1DeploymentsOK, "before install no proxy is NVCF's yet")
	assert.True(t, *state.Tier1DeploymentsOK)
}

// Installed, every named NVCF Gateway that Envoy Gateway runs needs a proxy,
// judged on full readiness whatever its kind: a DaemonSet proxy with one of
// three pods Ready is under-replicated, as a Deployment at 1/3 is.
func TestCheckTier1Deployments_PostInstallNamedGatewayNeedsAProxy(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw,gw/nats-gw")
	gateways := func() dynamic.Interface {
		return envoyGatewayClient(t, gatewayObject("gw", "shared-gw", "eg"), gatewayObject("gw", "nats-gw", "eg"))
	}
	shared := func() runtime.Object { return proxyDeployment("envoy-shared", "gw", "shared-gw", 2) }
	natsDS := func(ready int32) *appsv1.DaemonSet {
		return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "envoy-nats", Namespace: envoyGatewayNamespace,
			Labels: map[string]string{owningGatewayNameLabel: "nats-gw", owningGatewayNamespaceLabel: "gw"}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberReady: ready}}
	}

	state := runTier1(t, true, gateways(), shared(), nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "gw/nats-gw has no proxy")

	for _, ready := range []int32{0, 1} {
		state = runTier1(t, true, gateways(), shared(), natsDS(ready), nvcfService())
		require.NotNil(t, state.Tier1DeploymentsOK, ready)
		assert.False(t, *state.Tier1DeploymentsOK, "a DaemonSet proxy at %d/3 is under-replicated", ready)
	}

	state = runTier1(t, true, gateways(), shared(), natsDS(3), nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)

	state = runTier1(t, false, gateways(), shared())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "before install a Gateway may not have its proxy yet")
}

// Coverage applies to Gateways Envoy Gateway runs. Another implementation's
// Gateway has no Envoy proxy to find; a Gateway that does not exist fails; and
// a proxy is found wherever it runs: beside its Gateway in an already scanned
// namespace (GatewayNamespace mode), or in a controller namespace the
// validator was not told about.
func TestCheckTier1Deployments_GatewayCoverageByImplementation(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	inNamespace := func(d *appsv1.Deployment, ns string) *appsv1.Deployment {
		d.Namespace = ns
		return d
	}
	two := int32(2)
	api := func() *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 1},
			Spec:   appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2}}
	}

	state := runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "istio")), api())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "an Istio Gateway has no Envoy proxy to look for")

	state = runTier1(t, true, envoyGatewayClient(t), api())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "a named Gateway that does not exist is a finding")
	recs := strings.Join(state.Recommendations, "; ")
	assert.Contains(t, recs, "describe gateway", "a coverage gap gets Gateway advice")
	assert.NotContains(t, recs, "replicaCount", "a coverage gap is not a replica shortfall")

	for _, ns := range []string{"nvcf", "custom-envoy-system"} {
		state = runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg")),
			inNamespace(proxyDeployment("envoy-nvcf", "nvcf", "nvcf-gw", 2), ns), api(),
			envoyController("custom-envoy-system"))
		require.NotNil(t, state.Tier1DeploymentsOK, ns)
		assert.True(t, *state.Tier1DeploymentsOK, "a proxy in %s covers the Gateway", ns)
	}

	// Envoy Gateway never creates a Gateway's proxy outside its own
	// namespace and the controller's, so a look-alike elsewhere covers
	// nothing.
	state = runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg")),
		inNamespace(proxyDeployment("pause", "nvcf", "nvcf-gw", 2), "team-x"), api())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "a labelled workload in team-x is not NVCF's proxy")
}

// GatewayNamespace mode puts a Gateway's proxy beside it, in a namespace
// Tier-1 already scans. A proxy there is attributed like one in the Envoy
// namespace: another team's down proxy is not counted against NVCF.
func TestCheckTier1Deployments_ProxyInScannedGatewayNamespaceIsAttributed(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	foreign := proxyDeployment("envoy-team-b", "nvcf", "team-b-gw", 0)
	foreign.Namespace = "nvcf"
	nvcfProxy := proxyDeployment("envoy-nvcf", "nvcf", "nvcf-gw", 2)
	nvcfProxy.Namespace = "nvcf"
	state := runTier1(t, false, routeClient(), foreign, nvcfProxy)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "team-b's down proxy is not NVCF's")
}

// Every way of not being able to look leaves coverage undecided, and the row
// UNKNOWN, rather than passing or failing it: the classes cannot be listed,
// or the proxies cannot be.
func TestCheckTier1Deployments_GatewayCoverageUndecidedIsUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")

	classesDenied := envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg"))
	classesDenied.PrependReactor("list", "gatewayclasses", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayAPIGroup, Resource: "gatewayclasses"}, "", fmt.Errorf("denied"))
	})
	state := runTier1(t, true, classesDenied, nvcfService())
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not tell which NVCF Gateways Envoy Gateway runs")
	assert.Contains(t, state.Warnings, tier1CoverageUnknown)
	assert.NotContains(t, state.Warnings, tier1ProxiesUnknown, "the proxies were identified; coverage was not")

	client := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/gateways", gatewayAPIGroup+"/v1/gatewayclasses")
	client.PrependReactor("list", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	_, err := client.AppsV1().Deployments("nvcf").Create(context.Background(), nvcfService(), metav1.CreateOptions{})
	require.NoError(t, err)
	state = &ValidationState{Log: testLog(), PostInstall: true}
	checkTier1Deployments(context.Background(), client,
		envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg")), state)
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "listing Envoy proxy DaemonSets")
}

// A Deployment with no progress deadline has no Progressing condition. A
// healthy rollout of one, sitting at its readiness floor with old pods still
// running, is tolerated as it is with a deadline, but only for
// stalledRolloutAfter from the spec write that started it: nothing else ever
// ends it.
func TestCheckTier1Deployments_RolloutWithoutProgressDeadlineIsTolerated(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	three, noDeadline := int32(3), int32(math.MaxInt32)
	one := intstr.FromInt32(1)
	zero := intstr.FromInt32(0)
	rolling := func(age time.Duration) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 2,
				CreationTimestamp: metav1.NewTime(installedAt),
				ManagedFields: []metav1.ManagedFieldsEntry{
					specWrite(installManager, time.Now().Add(-age)), statusWrite(time.Now()),
				}},
			Spec: appsv1.DeploymentSpec{Replicas: &three, ProgressDeadlineSeconds: &noDeadline,
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType,
					RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &one, MaxSurge: &zero}}},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 1, ReadyReplicas: 2},
		}
	}
	for age, want := range map[time.Duration]bool{
		time.Minute:        true,
		14 * time.Minute:   true,
		16 * time.Minute:   false,
		7 * 24 * time.Hour: false,
	} {
		state := runTier1(t, false, routeClient(), rolling(age))
		require.NotNil(t, state.Tier1DeploymentsOK, age)
		assert.Equal(t, want, *state.Tier1DeploymentsOK, "rollout started %s ago", age)
	}

	// Without old pods it is no rollout: a finished ReplicaSet short a pod.
	finished := rolling(time.Minute)
	finished.Status.Replicas, finished.Status.UpdatedReplicas = 2, 2
	state := runTier1(t, false, routeClient(), finished)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
}

// Gateway names must be namespace/name. A bare name could match another
// team's same-named Gateway, so it is ignored with a warning.
func TestNVCFGatewayNames_RequiresNamespace(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw, gateway ,/x,a/b/c")
	set, invalid := nvcfGatewayNames()
	assert.Equal(t, gatewaySet{"gw/shared-gw": true}, set)
	assert.Equal(t, []string{"gateway", "/x", "a/b/c"}, invalid)

	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gateway")
	teamB := proxyDeployment("envoy-team-b", "team-b", "gateway", 1)
	state := runTier1(t, false, routeClient(), teamB)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "team-b's gateway is not NVCF's")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "without a namespace")
}

// In a shared namespace only what a stack release installed is NVCF's. Another
// install's Deployment there is not assessed at any readiness; the stack's own
// fails at zero, as any NVCF service does.
func TestCheckTier1Deployments_SharedNamespaceOwnership(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	zero, one, two := int32(0), int32(1), int32(2)
	foreign := func(name string, want int32) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cert-manager"},
			Spec: appsv1.DeploymentSpec{Replicas: &want}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
	}
	state := runTier1(t, false, routeClient(), foreign("cm-webhook", zero), foreign("cm-cainjector", two),
		nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "another install's cert-manager is not NVCF's to judge")

	stack := foreign("cert-manager", zero)
	stack.Annotations = map[string]string{
		"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cert-manager",
	}
	state = runTier1(t, false, routeClient(), stack, nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "the stack's own cert-manager scaled to zero fails")

	stack = stack.DeepCopy()
	stack.Spec.Replicas = &one
	state = runTier1(t, false, routeClient(), stack, nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// After install, shared-namespace controllers and Envoy proxies outlive an
// uninstall, so they do not stand in for the control plane: with no NVCF
// Deployment the row fails, routes or not, and it is unknown when the NVCF
// namespaces could not be read.
func TestCheckTier1Deployments_PostInstallNeedsNVCFDeployments(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	certManager := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager", Namespace: "cert-manager",
		Annotations: map[string]string{
			"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cert-manager",
		}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
	routes := func() *dynamicfake.FakeDynamicClient {
		return routeClient(route("HTTPRoute", "gw", "api", "nvcf-gateway-routes-1.0.0", parentRef("name", "shared-gw")))
	}
	for name, dyn := range map[string]*dynamicfake.FakeDynamicClient{"no routes": routeClient(), "routes": routes()} {
		state := runTier1(t, true, dyn, certManager.DeepCopy(), proxyDeployment("envoy-shared", "gw", "shared-gw", 2))
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.False(t, *state.Tier1DeploymentsOK, name)
	}

	client := gatewayDiscoveryClient(gatewayAPIGroup + "/v1/httproutes")
	client.PrependReactor("list", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "nvcf" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "", fmt.Errorf("x"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkTier1Deployments(context.Background(), client, routeClient(), state)
	assert.Nil(t, state.Tier1DeploymentsOK, "an unreadable NVCF namespace is not an empty one")
}

// The dependency releases run Deployments of their own, such as the OpenBao
// agent injector, so a Ready one does not stand in for the NVCF services.
func TestCheckTier1Deployments_DependencyDeploymentsDoNotStandInForServices(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	two := int32(2)
	injector := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "openbao-server-agent-injector", Namespace: "vault-system", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &two},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2},
	}
	state := runTier1(t, true, routeClient(), injector)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Recommendations, "; "), "Tier-1 found no NVCF Deployments")
}

// A configured Gateway list over a failed Gateway API discovery could not be
// listed, so whether those Gateways exist is unknown: the read error is not
// evidence that they are gone.
func TestCheckTier1Deployments_DiscoveryErrorLeavesConfiguredGatewaysUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	client := fake.NewSimpleClientset(envoyController(envoyGatewayNamespace), nvcfService(),
		proxyDeployment("envoy-shared", "nvcf", "shared-gw", 2))
	routes := envoyGatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg"))
	own := resolveGatewayOwnershipIn(context.Background(), client, gatewayAPISurface{},
		apierrors.NewServiceUnavailable("discovery unavailable"), routes)
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkTier1DeploymentsFor(context.Background(), client, own, state)

	assert.Nil(t, state.Tier1DeploymentsOK)
	warnings := strings.Join(state.Warnings, "; ")
	assert.NotContains(t, warnings, "does not exist")
	assert.Contains(t, warnings, "could not confirm the NVCF Gateways exist")
}

// Installed with no NVCF Gateway named, a pending Service beside an addressed
// one may be NVCF's own, so the LoadBalancer row is UNKNOWN, not a pass.
func TestCheckExternalLoadBalancer_PostInstallWithoutNVCFRoutesIsUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	client := routeDiscoveryClient()
	addServices(t, client,
		gatewayLBService(envoyGatewayNamespace, "envoy-team-b", "team-b", "b",
			corev1.ServiceTypeLoadBalancer, "203.0.113.2"),
		gatewayLBService(envoyGatewayNamespace, "envoy-nvcf", "gw", "shared-gw", corev1.ServiceTypeLoadBalancer, ""),
	)
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkExternalLoadBalancer(context.Background(), client, routeClient(), state)
	assert.Nil(t, state.ExternalLBOK)
	// The routes were read, so the advice is about the routes release, not RBAC.
	advice := strings.Join(state.Recommendations, "; ")
	assert.Contains(t, advice, "gateway routes release is installed")
	assert.NotContains(t, advice, "httproutes")
}

// The Gateways are listed only when a merged-gateways proxy needs their
// classes, so an install without one never pays for it.
func TestGatewayOwnership_ListsGatewaysOnlyForMergedProxies(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	dyn := gatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg"))
	own := resolveGatewayOwnership(context.Background(), gatewayClassDiscoveryClient(), dyn)
	listed := func() int {
		n := 0
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "list" && a.GetResource().Resource == "gateways" {
				n++
			}
		}
		return n
	}
	entries, known := own.proxyOwner(context.Background(),
		map[string]string{owningGatewayNameLabel: "shared-gw", owningGatewayNamespaceLabel: "nvcf"})
	assert.True(t, known)
	assert.Equal(t, []string{"nvcf/shared-gw"}, entries)
	entries, known = own.proxyOwner(context.Background(),
		map[string]string{owningGatewayNameLabel: "b-gw", owningGatewayNamespaceLabel: "team-b"})
	assert.True(t, known)
	assert.Empty(t, entries, "another team's per-Gateway proxy")
	assert.Zero(t, listed())

	entries, known = own.proxyOwner(context.Background(), map[string]string{owningGatewayClassLabel: "eg"})
	assert.True(t, known)
	assert.Equal(t, []string{"nvcf/shared-gw"}, entries)
	own.proxyOwner(context.Background(), map[string]string{owningGatewayClassLabel: "eg"})
	assert.Equal(t, 1, listed(), "listed once, on first need")
}

// rolloutProgressing is the condition the Deployment controller keeps while a
// rollout is moving.
func rolloutProgressing() appsv1.DeploymentCondition {
	return appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated",
	}
}

// A ReplicaSet that cannot create a pod is not a rollout, and neither is a
// finished rollout that falls short later: the controller reports
// NewReplicaSetAvailable, not ProgressDeadlineExceeded, so tolerating either
// passed the Deployment forever.
func TestCheckTier1Deployments_ReplicaFailureIsNotARollout(t *testing.T) {
	four := int32(4)
	dep := func(conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 2},
			Spec:       appsv1.DeploymentSpec{Replicas: &four},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 2, UpdatedReplicas: 3, ReadyReplicas: 3, Conditions: conds,
			},
		}
	}
	quotaDenied := appsv1.DeploymentCondition{
		Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate",
	}
	finished := appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable",
	}
	for name, d := range map[string]*appsv1.Deployment{
		"replica failure":          dep(rolloutProgressing(), quotaDenied),
		"finished rollout, 3 of 4": dep(finished),
	} {
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), fake.NewSimpleClientset(d), nil, state)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.False(t, *state.Tier1DeploymentsOK, name)
	}

	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(dep(rolloutProgressing())), nil, state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "a moving rollout at its floor is tolerated")
}

// n2nFixture is a fake cluster for checkNodeToNode. Everything the run creates
// is stored, so a test can list what it left behind. Creating the DaemonSet
// stores the given probe pods in the run's namespace with its labels, as the
// DaemonSet controller would, and checker scripts what each Get of the
// checker pod returns.
type n2nFixture struct {
	client     *fake.Clientset
	checkerCmd string
	// checkerNode is the node of the latest checker pod, and checkerNodes all
	// of them in order.
	checkerNode  string
	checkerNodes []string
	ds           *appsv1.DaemonSet
}

func newN2NFixture(
	t *testing.T, nodes []*corev1.Node, pods []corev1.Pod, events []corev1.Event, checker func() (*corev1.Pod, error),
) *n2nFixture {
	t.Helper()
	prev := nodeToNodeDSTimeout
	nodeToNodeDSTimeout = 2 * time.Second
	t.Cleanup(func() { nodeToNodeDSTimeout = prev })
	objs := make([]runtime.Object, 0, len(nodes))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	f := &n2nFixture{client: fake.NewSimpleClientset(objs...)}
	podsGVR := corev1.SchemeGroupVersion.WithResource("pods")
	f.client.PrependReactor("create", "daemonsets", func(a ktesting.Action) (bool, runtime.Object, error) {
		ds := a.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		f.ds = ds.DeepCopy()
		for i := range pods {
			p := pods[i].DeepCopy()
			p.Namespace, p.Labels = ds.Namespace, ds.Spec.Template.Labels
			if err := f.client.Tracker().Create(podsGVR, p, ds.Namespace); err != nil {
				return true, nil, err
			}
		}
		return false, nil, nil
	})
	f.client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.EventList{Items: events}, nil
	})
	f.client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		p := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		f.checkerCmd = strings.Join(p.Spec.Containers[0].Command, " ")
		f.checkerNode = p.Spec.NodeName
		f.checkerNodes = append(f.checkerNodes, p.Spec.NodeName)
		return false, nil, nil
	})
	f.client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		p, err := checker()
		return true, p, err
	})
	return f
}

// leftovers lists every probe object the run left: DaemonSets and pods with
// the validator's label in any namespace, and probe namespaces.
func (f *n2nFixture) leftovers(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=nvcf-cluster-validator"}
	var left []string
	dss, err := f.client.AppsV1().DaemonSets("").List(ctx, sel)
	require.NoError(t, err)
	for _, ds := range dss.Items {
		left = append(left, "daemonset "+ds.Namespace+"/"+ds.Name)
	}
	pods, err := f.client.CoreV1().Pods("").List(ctx, sel)
	require.NoError(t, err)
	for _, p := range pods.Items {
		left = append(left, "pod "+p.Namespace+"/"+p.Name)
	}
	nss, err := f.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, ns := range nss.Items {
		if strings.HasPrefix(ns.Name, nodeToNodeNSPrefix) {
			left = append(left, "namespace "+ns.Name)
		}
	}
	return left
}

func runningProbePod(name, node, ip string) corev1.Pod {
	p := probePod(node, "Running", ip, "")
	p.Name = name
	return p
}

func checkerPod(exit int32) *corev1.Pod {
	phase := corev1.PodSucceeded
	if exit != 0 {
		phase = corev1.PodFailed
	}
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit}},
	}}}}
}

func checkerExit(exit int32) func() (*corev1.Pod, error) {
	return func() (*corev1.Pod, error) { return checkerPod(exit), nil }
}

// checkerReported is a checker that exited with exit after printing lines,
// which the kubelet keeps as its termination message.
func checkerReported(exit int32, lines ...string) *corev1.Pod {
	p := checkerPod(exit)
	p.Status.ContainerStatuses[0].State.Terminated.Message = strings.Join(lines, "\n")
	return p
}

// refusedChecker is a checker pod the kubelet refused at admission.
func refusedChecker() *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfpods"}}
}

// podEvent is a pod event seq seconds into the run, so events sort in order.
func podEvent(pod, reason string, seq int) corev1.Event {
	return corev1.Event{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod},
		Reason:         reason,
		LastTimestamp:  metav1.NewTime(time.Unix(1_700_000_000+int64(seq), 0)),
	}
}

// daemonSetNodeAffinity is the node pin the DaemonSet controller puts on each
// pod it creates.
func daemonSetNodeAffinity(node string) *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{
				Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node},
			}}}},
		},
	}}
}

// Test entry points that discover the Gateway API surface and resolve Gateway
// ownership on their own. Run does both once and calls the In/For variants.

func checkGatewayAPICRDs(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	surface, err := discoverGatewayAPIResources(ctx, client)
	checkGatewayAPICRDsIn(state, surface, err)
}

func checkGatewayRoutes(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	surface, err := discoverGatewayAPIResources(ctx, client)
	checkGatewayRoutesIn(state, surface, err)
}

func resolveGatewayOwnership(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface,
) *gatewayOwnership {
	surface, err := discoverGatewayAPIResources(ctx, client)
	return resolveGatewayOwnershipIn(ctx, client, surface, err, routes)
}

func checkEnvoyGateway(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	checkEnvoyGatewayFor(ctx, client, resolveGatewayOwnership(ctx, client, nil), state)
}

func checkExternalLoadBalancer(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface, state *ValidationState,
) {
	checkExternalLoadBalancerFor(ctx, client, resolveGatewayOwnership(ctx, client, routes), state)
}

func checkTier1Deployments(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface, state *ValidationState,
) {
	checkTier1DeploymentsFor(ctx, client, resolveGatewayOwnership(ctx, client, routes), state)
}

func resolveNVCFGateways(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface,
) (gatewaySet, string, error) {
	own := resolveGatewayOwnership(ctx, client, routes)
	return own.gateways, own.source, own.err
}

func discoverNVCFGateways(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface,
) (gatewaySet, error) {
	surface, err := discoverGatewayAPIResources(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("discovering Gateway API resources: %w", err)
	}
	return discoverNVCFGatewaysIn(ctx, surface, routes)
}

func nvcfGatewayClasses(
	ctx context.Context, client kubernetes.Interface, routes dynamic.Interface, g gatewaySet,
) (map[string][]string, error) {
	surface, err := discoverGatewayAPIResources(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("discovering Gateway API resources: %w", err)
	}
	classOf, err := nvcfGatewayClassOf(ctx, surface, routes, g)
	if err != nil {
		return nil, err
	}
	return classEntries(classOf), nil
}

// An explicit global.storageClass binds every claim to that class, so the row
// requires it to exist and ignores whether the cluster has a default.
func TestCheckStorageClass_NamedClassReplacesDefaultRule(t *testing.T) {
	t.Setenv(storageClassEnv, "ceph-rbd")
	ceph := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "ceph-rbd"}}
	state := &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), fake.NewSimpleClientset(ceph), state)
	require.NotNil(t, state.DefaultStorageClassOK)
	assert.True(t, *state.DefaultStorageClassOK, "no default class is needed when the class is named")

	state = &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), fake.NewSimpleClientset(), state)
	require.NotNil(t, state.DefaultStorageClassOK)
	assert.False(t, *state.DefaultStorageClassOK, "the named class does not exist")

	client := fake.NewSimpleClientset(ceph)
	client.PrependReactor("get", "storageclasses", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	state = &ValidationState{Log: testLog()}
	checkStorageClass(context.Background(), client, state)
	assert.Nil(t, state.DefaultStorageClassOK, "a failed read is unknown")
}

// From 1.26 the apiserver binds to the newest default, ties broken by name, so
// the warning names that class whatever order the list returns.
func TestCheckStorageClass_MultipleDefaultsNameTheAdmittedClass(t *testing.T) {
	t.Setenv(storageClassEnv, "")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name string, age time.Duration) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
			Name: name, CreationTimestamp: metav1.NewTime(base.Add(-age)),
			Annotations: map[string]string{defaultClassAnnotation: annotationTrue},
		}}
	}
	for _, tc := range []struct {
		classes []runtime.Object
		winner  string
	}{
		{[]runtime.Object{mk("z-csi-gp3", 0), mk("a-legacy-gp2", time.Hour)}, "z-csi-gp3"},
		{[]runtime.Object{mk("z-csi-gp3", time.Hour), mk("a-legacy-gp2", 0)}, "a-legacy-gp2"},
		{[]runtime.Object{mk("b", 0), mk("a", 0)}, "a"},
	} {
		state := &ValidationState{Log: testLog(), K8sVersion: "v1.30.0"}
		checkStorageClass(context.Background(), fake.NewSimpleClientset(tc.classes...), state)
		assert.Contains(t, strings.Join(state.Warnings, "; "), "binds PVCs to "+tc.winner+",")
		assert.Equal(t, tc.winner, admittedDefaultClass([]storagev1.StorageClass{
			*tc.classes[0].(*storagev1.StorageClass), *tc.classes[1].(*storagev1.StorageClass)}))
	}
}

// GRPCRoute is rendered only when a grpc route is enabled, so a cluster that
// serves it only at v1alpha2 passes the critical row and is warned about in
// the optional one.
func TestCheckGatewayAPICRDs_GRPCRouteIsOptional(t *testing.T) {
	var pairs []string
	for _, p := range gatewayRequiredPairs() {
		if strings.HasSuffix(p, "/grpcroutes") {
			p = gatewayAPIGroup + "/v1alpha2/grpcroutes"
		}
		pairs = append(pairs, p)
	}
	client := gatewayDiscoveryClient(append(pairs, gatewayAPIGroup+"/v1alpha2/udproutes")...)
	state := &ValidationState{Log: testLog()}
	checkGatewayAPICRDs(context.Background(), client, state)
	require.NotNil(t, state.GatewayAPICRDsOK)
	assert.True(t, *state.GatewayAPICRDsOK)

	checkGatewayRoutes(context.Background(), client, state)
	require.NotNil(t, state.GatewayRoutesOK)
	assert.False(t, *state.GatewayRoutesOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "grpcroutes (needed when ingress.gatewayApi.routes.nvcfApi")
}

// istioStack is an installed cluster whose only NVCF Gateway, gw/nvcf-gw, is
// run by Istio, with a Ready Istio LoadBalancer Service that carries none of
// the Envoy labels and no Envoy Gateway at all.
func istioStack(t *testing.T) (*fake.Clientset, *gatewayOwnership) {
	t.Helper()
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/nvcf-gw")
	client := fake.NewSimpleClientset(nvcfService(), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-gw-istio", Namespace: "gw"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}},
		}},
	})
	disco := client.Discovery().(*fakediscovery.FakeDiscovery)
	disco.Resources = append(disco.Resources, &metav1.APIResourceList{
		GroupVersion: gatewayAPIGroup + "/v1",
		APIResources: []metav1.APIResource{{Name: "httproutes"}, {Name: "gateways"}, {Name: "gatewayclasses"}},
	})
	dyn := envoyGatewayClient(t, gatewayObject("gw", "nvcf-gw", "istio"))
	require.NoError(t, dyn.Tracker().Create(
		schema.GroupVersionResource{Group: gatewayAPIGroup, Version: "v1", Resource: "httproutes"},
		route("HTTPRoute", "gw", "api", "nvcf-gateway-routes-1.0.0", parentRef("name", "nvcf-gw")), "gw"))
	return client, resolveGatewayOwnership(context.Background(), client, dyn)
}

// Every Envoy-specific row leaves a Gateway another implementation runs out of
// scope, and they agree within one run: the Envoy and LoadBalancer rows have
// no result and a warning each saying why, Tier-1 passes, and the verdict is
// Ready.
func TestEnvoyRows_NonEnvoyGatewayIsOutOfScope(t *testing.T) {
	client, own := istioStack(t)
	state := &ValidationState{Log: testLog(), PostInstall: true, Role: RoleControlPlane}
	checkEnvoyGatewayFor(context.Background(), client, own, state)
	checkExternalLoadBalancerFor(context.Background(), client, own, state)
	checkTier1DeploymentsFor(context.Background(), client, own, state)

	assert.Nil(t, state.EnvoyGatewayOK)
	assert.Nil(t, state.ExternalLBOK)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	require.Len(t, state.Warnings, 2, "each row without a result says why")
	assert.Contains(t, state.Warnings[0], "Envoy Gateway: not assessed (no NVCF Gateway is run by Envoy Gateway: ")
	assert.Contains(t, state.Warnings[1], "External Load Balancer: not assessed (no NVCF Gateway is run by Envoy")

	ok := true
	state.ControlPlaneHealthy, state.WebhooksSupported, state.NetworkPoliciesSupported = true, true, true
	state.NodesAllReady = true
	state.DefaultStorageClassOK, state.GatewayAPICRDsOK = &ok, &ok
	state.NodeToNodeOK, state.Tier2StatefulSetsOK = &ok, &ok
	assert.NoError(t, printSummary(state), "rows that are not critical do not block the verdict")

	// The same Gateway on an Envoy class with no controller anywhere still
	// fails the Envoy row.
	t.Setenv(nvcfGatewayNamesEnv, "gw/nvcf-gw")
	dyn := envoyGatewayClient(t, gatewayObject("gw", "nvcf-gw", "eg"))
	state = &ValidationState{Log: testLog()}
	checkEnvoyGatewayFor(context.Background(), client, resolveGatewayOwnership(context.Background(), client, dyn),
		state)
	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK)
	assert.NotEmpty(t, state.Warnings)
}

// One NVCF Gateway that Envoy Gateway runs keeps the Envoy row in scope
// whatever runs the others, so a missing controller still fails it.
func TestEnvoyRows_OneEnvoyGatewayKeepsTheRowInScope(t *testing.T) {
	client, _ := istioStack(t)
	t.Setenv(nvcfGatewayNamesEnv, "gw/nvcf-gw,gw/envoy-gw")
	dyn := envoyGatewayClient(t, gatewayObject("gw", "nvcf-gw", "istio"), gatewayObject("gw", "envoy-gw", "eg"))
	own := resolveGatewayOwnership(context.Background(), client, dyn)
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkEnvoyGatewayFor(context.Background(), client, own, state)
	require.NotNil(t, state.EnvoyGatewayOK)
	assert.False(t, *state.EnvoyGatewayOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "not assessed")
}

// A named NVCF Gateway that nothing can run fails after install: one whose
// GatewayClass does not exist, or that names none.
func TestCheckTier1Deployments_DanglingGatewayClassFails(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	for _, class := range []string{"eg-deleted", ""} {
		state := runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", class)), nvcfService())
		require.NotNil(t, state.Tier1DeploymentsOK, class)
		assert.False(t, *state.Tier1DeploymentsOK, class)
	}
}

// Only launcher-named Gateways can fail Tier-1. A route anyone can label pulls
// in a Gateway that does not exist, or one whose proxy is down: either leaves
// the row unknown, never failed.
func TestCheckTier1Deployments_DiscoveredGatewaysCannotFail(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	ghost := routeClient(route("HTTPRoute", "team-x", "r", "nvcf-gateway-routes-x", parentRef("name", "ghost")))
	state := runTier1(t, true, ghost, nvcfService())
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "team-x/ghost does not exist")

	tenant := func() *dynamicfake.FakeDynamicClient {
		dyn := envoyGatewayClient(t, gatewayObject("team-x", "tenant-gw", "eg"))
		require.NoError(t, dyn.Tracker().Create(
			schema.GroupVersionResource{Group: gatewayAPIGroup, Version: "v1", Resource: "httproutes"},
			route("HTTPRoute", "team-x", "r", "nvcf-gateway-routes-x", parentRef("name", "tenant-gw")), "team-x"))
		return dyn
	}
	state = runTier1(t, true, tenant(), nvcfService(), proxyDeployment("envoy-tenant", "team-x", "tenant-gw", 0))
	assert.Nil(t, state.Tier1DeploymentsOK, "a down proxy of a discovered Gateway is not a failure")

	t.Setenv(nvcfGatewayNamesEnv, "team-x/tenant-gw")
	state = runTier1(t, true, tenant(), nvcfService(), proxyDeployment("envoy-tenant", "team-x", "tenant-gw", 0))
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "a named Gateway's down proxy fails")
}

// A tolerated rollout is a pass with a warning whoever owns the proxy: one
// mid-rollout at its readiness floor while ownership is undecided is not
// UNKNOWN.
func TestCheckTier1Deployments_UnattributedProxyRolloutIsTolerated(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	four := int32(4)
	rolling := proxyDeployment("envoy-shared", "gw", "shared-gw", 3)
	rolling.Generation, rolling.Spec.Replicas = 2, &four
	rolling.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 5, UpdatedReplicas: 2,
		ReadyReplicas: 3, Conditions: []appsv1.DeploymentCondition{rolloutProgressing()}}
	state := runTier1(t, true, routeClient(), rolling, nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// A named Gateway's proxy outside the scanned namespaces, beside a controller
// the validator was not told about, is judged on full readiness too.
func TestCheckTier1Deployments_ProxyInUnscannedNamespaceIsJudged(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	proxy := proxyDeployment("envoy-shared", "gw", "shared-gw", 1)
	proxy.Namespace = "custom-envoy-system"
	state := runTier1(t, true, envoyGatewayClient(t, gatewayObject("gw", "shared-gw", "eg")),
		proxy, envoyController("custom-envoy-system"), nvcfService())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK, "1/2 Ready is under-replicated wherever it runs")
}

// With the Envoy namespace unset, the controller and the proxy Services are
// found wherever Envoy Gateway runs, and a proxy Service is credited only
// there or beside its Gateway.
func TestEnvoyRows_FindTheControllerByLabel(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	pod := makeEnvoyControllerPod("envoy-gateway-abc", true)
	pod.Namespace = "gateway"
	client := fake.NewSimpleClientset(pod, envoyController("gateway"),
		gatewayLBService("gateway", "envoy-shared", "gw", "shared-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.1"),
		gatewayLBService("team-x", "look-alike", "gw", "shared-gw", corev1.ServiceTypeLoadBalancer, ""))
	own := resolveGatewayOwnership(context.Background(), client, nil)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGatewayFor(context.Background(), client, own, state)
	checkExternalLoadBalancerFor(context.Background(), client, own, state)
	require.NotNil(t, state.EnvoyGatewayOK)
	assert.True(t, *state.EnvoyGatewayOK)
	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK, "the team-x look-alike is not NVCF's Service")
}

// An observed pending NVCF Service fails the LoadBalancer row even when
// another Service list failed.
func TestCheckExternalLoadBalancer_PendingSurvivesAListFailure(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw,gw/api-gw")
	client := fake.NewSimpleClientset(envoyController(envoyGatewayNamespace),
		gatewayLBService(envoyGatewayNamespace, "envoy-shared", "gw", "shared-gw", corev1.ServiceTypeLoadBalancer, ""))
	client.PrependReactor("list", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.ListAction).GetListRestrictions().Labels.String() == owningGatewayClassLabel {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)
	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK)
	warnings := strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, envoyGatewayNamespace+"/envoy-shared")
	assert.NotContains(t, warnings, "no proxy Service found", "api-gw is unknown, not missing")
}
