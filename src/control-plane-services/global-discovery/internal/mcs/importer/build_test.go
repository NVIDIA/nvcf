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

package importer

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	discoveryv1ac "k8s.io/client-go/applyconfigurations/discovery/v1"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

const (
	testNS = "llm"
	uid    = types.UID("si-uid")
)

func endpoint(addr string, ready bool, ports ...*gdsv1.EndpointPort) *gdsv1.Endpoint {
	return &gdsv1.Endpoint{
		Addresses: []string{addr}, Conditions: &gdsv1.EndpointConditions{Ready: ready, Serving: ready},
		Ports: ports, Hostname: "h-" + addr, NodeName: "node-" + addr, Zone: "z1",
	}
}

var (
	httpPort    = &gdsv1.EndpointPort{Name: "http", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 8080}
	metricsPort = &gdsv1.EndpointPort{Name: "metrics", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 9090}
)

func testImport() *gdsv1.ServiceImport {
	return &gdsv1.ServiceImport{
		Clusterset: "cs", Namespace: testNS, Name: "frontend",
		Type:                  gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		Ports:                 []*gdsv1.ServicePort{{Name: "http", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 80, AppProtocol: "http"}},
		SessionAffinity:       gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP,
		SessionAffinityConfig: &gdsv1.SessionAffinityConfig{ClientIp: &gdsv1.ClientIPConfig{TimeoutSeconds: 60}},
		IpFamilies:            []gdsv1.IPFamily{gdsv1.IPFamily_IP_FAMILY_IPV4},
		Labels:                map[string]string{api.LabelRole: api.RolePoolFrontend, "bad key!": "x", api.LabelManagedBy: "spoofed"},
		Annotations:           map[string]string{api.AnnotationPoolModels: "m1", "Bad Key": "x"},
		Clusters: []*gdsv1.ClusterStatus{
			{Cluster: "a", Participating: true}, {Cluster: "b", Participating: true}, {Cluster: "c", Participating: false},
		},
		Endpoints: []*gdsv1.ClusterEndpoints{
			{Cluster: "a", Endpoints: []*gdsv1.Endpoint{endpoint("10.0.0.2", true, httpPort, metricsPort), endpoint("10.0.0.1", false, httpPort)}},
			{Cluster: "b", Endpoints: []*gdsv1.Endpoint{endpoint("10.1.0.1", true, httpPort)}},
		},
	}
}

func TestBuildServiceImport(t *testing.T) {
	u, err := BuildServiceImport(testImport(), "cs", []string{"10.96.0.10"})
	require.NoError(t, err)
	var si mcsv1beta1.ServiceImport
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &si))
	assert.Equal(t, "multicluster.x-k8s.io/v1beta1", u.GetAPIVersion())
	assert.Equal(t, "ServiceImport", u.GetKind())
	assert.Equal(t, map[string]string{api.LabelRole: api.RolePoolFrontend, api.LabelManagedBy: api.ManagedByController, api.LabelClusterSet: "cs"}, si.Labels)
	assert.Equal(t, map[string]string{api.AnnotationPoolModels: "m1", api.AnnotationMCSDerivedService: api.DerivedServiceName(testNS, "frontend")}, si.Annotations)
	assert.Equal(t, mcsv1beta1.ClusterSetIP, si.Spec.Type)
	assert.Equal(t, []string{"10.96.0.10"}, si.Spec.IPs)
	assert.Equal(t, corev1.ServiceAffinityClientIP, si.Spec.SessionAffinity)
	assert.Equal(t, int32(60), *si.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
	assert.Equal(t, []corev1.IPFamily{corev1.IPv4Protocol}, si.Spec.IPFamilies)
	require.Len(t, si.Spec.Ports, 1)
	assert.Equal(t, mcsv1beta1.ServicePort{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, AppProtocol: ptrTo("http")}, si.Spec.Ports[0])
	_, hasStatus := u.Object["status"]
	assert.False(t, hasStatus)

	empty, err := BuildServiceImport(&gdsv1.ServiceImport{Namespace: testNS, Name: "x", Type: gdsv1.ServiceType_SERVICE_TYPE_HEADLESS}, "cs", nil)
	require.NoError(t, err)
	ports, found, _ := unstructuredSlice(empty.Object, "spec", "ports")
	assert.True(t, found, "ports is required, even empty")
	assert.Empty(t, ports)
	typ, _, _ := unstructuredString(empty.Object, "spec", "type")
	assert.Equal(t, "Headless", typ)
}

func TestBuildServiceImportStatus(t *testing.T) {
	imp := testImport()
	st := BuildServiceImportStatus(imp, nil, 3)
	assert.Equal(t, []mcsv1beta1.ClusterStatus{{Cluster: "a"}, {Cluster: "b"}}, st.Clusters)
	require.Len(t, st.Conditions, 2)
	ready := st.Conditions[0]
	assert.Equal(t, "Ready", ready.Type)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, int64(3), ready.ObservedGeneration)
	assert.Contains(t, ready.Message, "2 of 3")
	assert.Equal(t, metav1.ConditionFalse, st.Conditions[1].Status)

	// Unchanged status keeps the transition time; a change moves it.
	old := metav1.NewTime(ready.LastTransitionTime.Add(-3600e9))
	st.Conditions[0].LastTransitionTime = old
	st.Conditions[1].LastTransitionTime = old
	imp.Conflict = &gdsv1.Conflict{Reason: api.ConditionReasonPortConflict, Message: "ports differ"}
	st2 := BuildServiceImportStatus(imp, st.Conditions, 3)
	assert.Equal(t, old, st2.Conditions[0].LastTransitionTime)
	assert.Equal(t, metav1.ConditionTrue, st2.Conditions[1].Status)
	assert.Equal(t, api.ConditionReasonPortConflict, st2.Conditions[1].Reason)
	assert.NotEqual(t, old, st2.Conditions[1].LastTransitionTime)
}

func TestBuildDerivedService(t *testing.T) {
	imp := testImport()
	ac := BuildDerivedService(imp, "cs", uid)
	assert.Equal(t, api.DerivedServiceName(testNS, "frontend"), *ac.Name)
	assert.Equal(t, testNS, *ac.Namespace)
	assert.Equal(t, map[string]string{api.LabelManagedBy: api.ManagedByController, api.LabelClusterSet: "cs", api.LabelMCSServiceName: "frontend"}, ac.Labels)
	require.Len(t, ac.OwnerReferences, 1)
	assert.Equal(t, uid, *ac.OwnerReferences[0].UID)
	assert.Equal(t, "ServiceImport", *ac.OwnerReferences[0].Kind)
	assert.True(t, *ac.OwnerReferences[0].Controller)
	assert.Equal(t, corev1.ServiceTypeClusterIP, *ac.Spec.Type)
	assert.Nil(t, ac.Spec.ClusterIP, "allocated by the API server")
	assert.Nil(t, ac.Spec.Selector)
	assert.Equal(t, corev1.ServiceAffinityClientIP, *ac.Spec.SessionAffinity)
	assert.Equal(t, int32(60), *ac.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
	assert.Equal(t, []corev1.IPFamily{corev1.IPv4Protocol}, ac.Spec.IPFamilies)
	assert.Equal(t, corev1.IPFamilyPolicySingleStack, *ac.Spec.IPFamilyPolicy)
	require.Len(t, ac.Spec.Ports, 1)
	assert.Equal(t, "http", *ac.Spec.Ports[0].Name)
	assert.Equal(t, int32(80), *ac.Spec.Ports[0].Port)
	assert.Equal(t, "http", *ac.Spec.Ports[0].AppProtocol)

	imp.Type = gdsv1.ServiceType_SERVICE_TYPE_HEADLESS
	imp.SessionAffinity = gdsv1.SessionAffinity_SESSION_AFFINITY_NONE
	imp.IpFamilies = []gdsv1.IPFamily{gdsv1.IPFamily_IP_FAMILY_IPV4, gdsv1.IPFamily_IP_FAMILY_IPV6}
	ac = BuildDerivedService(imp, "cs", uid)
	assert.Equal(t, corev1.ClusterIPNone, *ac.Spec.ClusterIP)
	assert.Nil(t, ac.Spec.SessionAffinityConfig)
	assert.Equal(t, corev1.IPFamilyPolicyPreferDualStack, *ac.Spec.IPFamilyPolicy)

	imp.IpFamilies = nil
	ac = BuildDerivedService(imp, "cs", uid)
	assert.Nil(t, ac.Spec.IPFamilyPolicy)
}

func names(slices []*discoveryv1ac.EndpointSliceApplyConfiguration) []string {
	var out []string
	for _, s := range slices {
		out = append(out, *s.Name)
	}
	return out
}

func TestBuildEndpointSlicesPacking(t *testing.T) {
	imp := testImport()
	derived := api.DerivedServiceName(testNS, "frontend")
	// Five more IPv4 endpoints and one IPv6 endpoint in cluster a.
	for i := 3; i < 8; i++ {
		imp.Endpoints[0].Endpoints = append(imp.Endpoints[0].Endpoints, endpoint(fmt.Sprintf("10.0.0.%d", i), true, httpPort))
	}
	imp.Endpoints[0].Endpoints = append(imp.Endpoints[0].Endpoints,
		&gdsv1.Endpoint{Addresses: []string{"fd00::1"}, Ports: []*gdsv1.EndpointPort{httpPort}},
		&gdsv1.Endpoint{}, // no address: skipped
	)
	slices := BuildEndpointSlices(imp, "cs", "a", uid, 3)
	assert.Equal(t, []string{
		derived + "-a-0", derived + "-a-1", derived + "-a-2", derived + "-a-3", derived + "-b-0",
	}, names(slices))

	a0 := slices[0]
	assert.Equal(t, discoveryv1.AddressTypeIPv4, *a0.AddressType)
	assert.Equal(t, map[string]string{
		discoveryv1.LabelServiceName:    derived,
		api.LabelMCSServiceName:         "frontend",
		api.LabelMCSSourceCluster:       "a",
		api.LabelEndpointSliceManagedBy: api.EndpointSliceManagedBy,
		api.LabelManagedBy:              api.ManagedByController,
		api.LabelClusterSet:             "cs",
	}, a0.Labels)
	require.Len(t, a0.OwnerReferences, 1)
	assert.Equal(t, uid, *a0.OwnerReferences[0].UID)
	// Ports are filtered to the merged names (metrics is not exported).
	require.Len(t, a0.Ports, 1)
	assert.Equal(t, "http", *a0.Ports[0].Name)
	assert.Equal(t, int32(8080), *a0.Ports[0].Port)
	require.Len(t, a0.Endpoints, 3)
	assert.Equal(t, []string{"10.0.0.1"}, a0.Endpoints[0].Addresses)
	assert.False(t, *a0.Endpoints[0].Conditions.Ready)
	assert.Equal(t, "node-10.0.0.1", *a0.Endpoints[0].NodeName, "local node names are kept")
	assert.Equal(t, "h-10.0.0.1", *a0.Endpoints[0].Hostname)
	assert.Equal(t, "z1", *a0.Endpoints[0].Zone)
	assert.Len(t, slices[1].Endpoints, 3)
	assert.Len(t, slices[2].Endpoints, 1)
	assert.Equal(t, discoveryv1.AddressTypeIPv6, *slices[3].AddressType)

	b0 := slices[4]
	assert.Equal(t, "b", b0.Labels[api.LabelMCSSourceCluster])
	assert.Nil(t, b0.Endpoints[0].NodeName, "remote node names are dropped")

	// Deterministic whatever the order GDS sends.
	eps := imp.Endpoints[0].Endpoints
	for i, j := 0, len(eps)-1; i < j; i, j = i+1, j-1 {
		eps[i], eps[j] = eps[j], eps[i]
	}
	again := BuildEndpointSlices(imp, "cs", "a", uid, 3)
	require.Equal(t, names(slices), names(again))
	for i := range slices {
		assert.Equal(t, slices[i].Endpoints, again[i].Endpoints, *slices[i].Name)
	}

	// Default size.
	assert.Len(t, BuildEndpointSlices(imp, "cs", "a", uid, 0), 3)
	assert.Equal(t, derived+"-a-12", SliceName(derived, "a", 12))
}

func unstructuredSlice(obj map[string]any, fields ...string) ([]any, bool, error) {
	return unstructured.NestedSlice(obj, fields...)
}

func unstructuredString(obj map[string]any, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj, fields...)
}
