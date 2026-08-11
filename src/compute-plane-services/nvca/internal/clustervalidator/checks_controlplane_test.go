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

func TestCheckGatewayRoutes_NilClientSkips(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	// Should not panic or set GatewayRoutesOK.
	checkGatewayRoutes(context.Background(), nil, state)
	assert.Nil(t, state.GatewayRoutesOK, "nil dynClient must leave GatewayRoutesOK unset")
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

func TestCheckNodeToNode_ServerPodCreateFailure(t *testing.T) {
	// Two schedulable nodes, but pod creation fails.
	client := fake.NewSimpleClientset(
		makeNode("node-1", true, 0),
		makeNode("node-2", true, 0),
	)
	client.PrependReactor("create", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("pod quota exceeded")
	})

	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), client, state)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK, "server pod create failure must set NodeToNodeOK=false")
}

// init is required to register types with the fake client's object tracker.
func init() {
	_ = []runtime.Object{
		&storagev1.StorageClass{},
		&corev1.Namespace{},
		&corev1.Pod{},
		&corev1.Service{},
	}
}
