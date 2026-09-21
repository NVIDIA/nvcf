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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
			Namespace: envoyGatewayNamespace,
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
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}},
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
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}},
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
	dataPlane := makePod("envoy-envoy-gateway-system-eg-abc123", envoyGatewayNamespace, corev1.PodRunning)
	dataPlane.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}},
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
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}},
		makePod("envoy-gateway-abc", envoyGatewayNamespace, corev1.PodPending),
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
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway", Namespace: envoyGatewayNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}},
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK, "a LB service with an assigned IP must set ExternalLBOK=true")
}

func TestCheckExternalLoadBalancer_ServiceWithHostname(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway", Namespace: envoyGatewayNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
			},
		},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK, "a LB service with a hostname must set ExternalLBOK=true")
}

func TestCheckExternalLoadBalancer_NoLBServices(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-ip-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK, "no LB service must set ExternalLBOK=false")
	assert.NotEmpty(t, state.Warnings)
}

func TestCheckExternalLoadBalancer_LBServicePendingNoIP(t *testing.T) {
	// LB type but .status.loadBalancer.ingress is empty → no IP assigned yet.
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-lb", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		// No Status.LoadBalancer.Ingress
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, state)

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

func TestCheckNodeToNode_SingleNode_Skip(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK,
		"a single-node cluster has no second node to reach, so the result must be unknown")
	assert.NotEmpty(t, state.Warnings)
}

func TestCheckNodeToNode_UnschedulableNodesSkipped(t *testing.T) {
	// Two nodes but both unschedulable — should also skip.
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

	// Fail checker pod creation so the test exits quickly without needing to
	// simulate full pod lifecycle (no Get/poll needed).
	var checkerPodCreateCalled bool
	client.PrependReactor("create", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		checkerPodCreateCalled = true
		return true, nil, fmt.Errorf("no pods scheduled")
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	// checkerPodCreateCalled must be true: if the check had sized its wait from
	// len(schedulable)=3 rather than polling for DesiredNumberScheduled=2, it
	// would have timed out before reaching pod creation and this flag would
	// stay false, catching the regression.
	require.True(t, checkerPodCreateCalled, "check must reach checker pod creation step")
	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK, "NodeToNodeOK false because checker pod creation failed")
}

func TestCheckNodeToNode_DaemonSetCreateFailure(t *testing.T) {
	// Two schedulable nodes, but DaemonSet creation fails.
	client := fake.NewSimpleClientset(
		makeNode("node-1", true, 0),
		makeNode("node-2", true, 0),
	)
	client.PrependReactor("create", "daemonsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("quota exceeded")
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK, "DaemonSet create failure must set NodeToNodeOK=false")
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
	client.PrependReactor("get", "daemonsets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated status read failure")
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
	checkTier1Deployments(context.Background(), client, state)

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
	checkTier1Deployments(context.Background(), client, state)

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
	checkTier1Deployments(context.Background(), client, state)

	assert.Nil(t, state.Tier1DeploymentsOK,
		"the only Deployment is mid-rollout, so readiness is unknown, not a pass or a failure")
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
	checkTier1Deployments(context.Background(), client, state)

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
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"the under-replicated Deployment must still fail the check alongside a rolling one")
}

func TestCheckTier1Deployments_PreInstallPassesTrivially(t *testing.T) {
	client := fake.NewSimpleClientset() // no namespaces, no deployments
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "pre-install (no deployments) must pass trivially")
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
	checkTier1Deployments(context.Background(), client, state)

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

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"the only quorum StatefulSet is mid-rollout, so quorum is unknown, not failed")
	require.NotEmpty(t, state.Warnings)
	assert.Contains(t, state.Warnings[0], "revision mismatch")
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

// An even replica count cannot form a quorum majority, so it is not a Tier-2
// component and must not be evaluated as one.
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
	checkTier1Deployments(context.Background(), client, state)

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

// A healthy StatefulSet must not stand in for one that was skipped mid-rollout:
// its quorum was never assessed, so the tier result is partial, not a pass.
func TestCheckTier2StatefulSets_HealthyPeerDoesNotMaskRollingOne(t *testing.T) {
	healthy := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	rolling := makeQuorumSTS("openbao", "vault-system", 3, 2, []string{"node-1", "node-2"})
	rolling[0].(*appsv1.StatefulSet).Status.UpdateRevision = "openbao-r2"

	client := fake.NewSimpleClientset(append(healthy, rolling...)...)
	state := &ValidationState{Log: testLog()}
	checkTier2StatefulSets(context.Background(), client, state)

	assert.Nil(t, state.Tier2StatefulSetsOK,
		"one StatefulSet still rolling means the tier assessment is partial, not a pass")
	assert.NotEmpty(t, state.Warnings)
}

// Same shape for Tier-1: a ready Deployment does not certify one still rolling.
// A mid-rollout Deployment that is still serving its full replica count hides
// nothing, so it must not pin this critical row to UNKNOWN. rollingOut is not
// self-limiting: a paused rollout, progressDeadlineSeconds=2147483647, and a
// wedged controller all stay "rolling" forever without ever setting
// ProgressDeadlineExceeded.
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
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK,
		"a rollout at full ready count must not leave the tier permanently unknown")
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.NotEmpty(t, state.Warnings, "the in-flight rollout is still reported")
}

// A mid-rollout Deployment that is ALSO below its ready target is the case
// that can hide a real outage, so the tier assessment is partial.
func TestCheckTier1Deployments_RollingAndUnderReplicatedIsUnknown(t *testing.T) {
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
				ObservedGeneration: 2, UpdatedReplicas: 1, ReadyReplicas: 1,
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, state)

	assert.Nil(t, state.Tier1DeploymentsOK,
		"a rollout below its replica target leaves the tier assessment partial")
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
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK,
		"a control plane whose only Deployment is scaled to zero is not ready")
}

// Alongside a healthy peer the scale-down is a warning, not a silent pass.
func TestCheckTier1Deployments_ScaledToZeroWarnsAlongsideHealthy(t *testing.T) {
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
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nvcf/scaled-down")
}

// An OnDelete StatefulSet never advances CurrentRevision, so a revision
// mismatch is permanent. At full ready count that must not hide the tier, and
// more than one pod down is beyond what rolling one at a time explains.
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
		"an OnDelete StatefulSet at full ready count must not pin the tier to unknown")
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
			Name: nodeToNodeDSName, Namespace: "default",
			Labels: labels, CreationTimestamp: old,
		}},
	)
	sweepLegacyOrphanN2NDaemonSets(context.Background(), testLog(), client, orphanN2NNamespaceTTL)
	_, err := client.AppsV1().DaemonSets("default").Get(
		context.Background(), nodeToNodeDSName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a stale legacy DaemonSet must be reclaimed")

	// A DaemonSet inside the TTL may belong to a concurrent run.
	client2 := fake.NewSimpleClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: nodeToNodeDSName, Namespace: "default",
			Labels: labels, CreationTimestamp: fresh,
		}},
	)
	sweepLegacyOrphanN2NDaemonSets(context.Background(), testLog(), client2, orphanN2NNamespaceTTL)
	_, err = client2.AppsV1().DaemonSets("default").Get(
		context.Background(), nodeToNodeDSName, metav1.GetOptions{})
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
	assigned := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gateway-lb", Namespace: envoyGatewayNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}},
		}},
	}
	pending := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-nats-gateway-lb", Namespace: envoyGatewayNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	client := fake.NewSimpleClientset(assigned, pending)
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, state)

	require.NotNil(t, state.ExternalLBOK)
	assert.False(t, *state.ExternalLBOK,
		"a Gateway still waiting on an address is the failure this check exists to catch")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "envoy-nats-gateway-lb")
}

// The stack exposes controllerNamespace with no default, so an install can
// place Envoy outside envoy-gateway-system. Probing the wrong namespace
// reports a live gateway as missing.
func TestEnvoyGatewayNamespaceName_HonoursOverride(t *testing.T) {
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
