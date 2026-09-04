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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func TestCheckEnvoyGateway_RunningPods(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}},
		makePod("envoy-gateway-abc", envoyGatewayNamespace, corev1.PodRunning),
	)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)

	require.NotNil(t, state.EnvoyGatewayOK)
	assert.True(t, *state.EnvoyGatewayOK, "running Envoy Gateway pods must set EnvoyGatewayOK=true")
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
	checkNodeToNode(context.Background(), client, state)

	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK, "zero schedulable nodes must skip with pass, not fail")
	assert.NotEmpty(t, state.Warnings, "skip must add a warning")
}

func TestCheckNodeToNode_SingleNode_Skip(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state)

	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK, "single-node cluster must skip with pass, not fail")
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
	checkNodeToNode(context.Background(), client, state)

	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK, "no schedulable nodes must skip, not fail")
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
	client.PrependReactor("create", "daemonsets", func(action ktesting.Action) (bool, runtime.Object, error) {
		ds := action.(ktesting.CreateAction).GetObject().(*appsv1.DaemonSet)
		capturedLabels = ds.Labels
		ds.Status.DesiredNumberScheduled = 2
		return true, ds, nil
	})

	// Return 2 Running pods whose labels match the DaemonSet selector.
	// FakePods.List filters by label after the reactor returns, so pods must
	// carry the full label set including the random instance suffix.
	client.PrependReactor("list", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		lbl := capturedLabels
		return true, &corev1.PodList{Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: nodeToNodeNamespace, Labels: lbl},
				Spec:       corev1.PodSpec{NodeName: "node-1"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "s-2", Namespace: nodeToNodeNamespace, Labels: lbl},
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
	checkNodeToNode(context.Background(), client, state)

	// checkerPodCreateCalled must be true: if waitForDaemonSetPods had
	// waited for 3 pods (len(schedulable)) instead of 2 (DesiredNumberScheduled),
	// it would have timed out before reaching pod creation and this flag
	// would stay false, catching the regression.
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
	checkNodeToNode(context.Background(), client, state)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK, "DaemonSet create failure must set NodeToNodeOK=false")
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

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "in-progress rollout must not set Tier1DeploymentsOK=false")
	assert.NotEmpty(t, state.Warnings, "rollout in progress must emit a warning")
	assert.Contains(t, state.Warnings[0], "rollout in progress")
}

func TestCheckTier1Deployments_PreInstallPassesTrivially(t *testing.T) {
	client := fake.NewSimpleClientset() // no namespaces, no deployments
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "pre-install (no deployments) must pass trivially")
}

// init is required to register types with the fake client's object tracker.
func init() {
	_ = []runtime.Object{
		&appsv1.DaemonSet{},
		&storagev1.StorageClass{},
		&corev1.Namespace{},
		&corev1.Pod{},
		&corev1.Service{},
	}
}

