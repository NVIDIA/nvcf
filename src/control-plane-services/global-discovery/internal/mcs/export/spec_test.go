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

package export

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

func ptr[T any](v T) *T { return &v }

func TestBuildSpec(t *testing.T) {
	created := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	timeout := int32(300)
	local := corev1.ServiceInternalTrafficPolicyLocal
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "llm", Name: "frontend",
			Labels: map[string]string{
				api.LabelExport: api.ExportEnabled, api.LabelRole: api.RolePoolFrontend, api.LabelPool: "p1",
				api.LabelManagedBy: "someone", "app": "frontend",
			},
			Annotations: map[string]string{api.AnnotationPoolModels: "m1,m2", "kubectl.kubernetes.io/last-applied-configuration": "{}"},
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeLoadBalancer,
			ClusterIP:             "10.0.0.1",
			SessionAffinity:       corev1.ServiceAffinityClientIP,
			SessionAffinityConfig: &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: &timeout}},
			IPFamilies:            []corev1.IPFamily{corev1.IPv4Protocol},
			InternalTrafficPolicy: &local,
			TrafficDistribution:   ptr("PreferClose"),
			Ports: []corev1.ServicePort{
				{Name: "http", Protocol: corev1.ProtocolTCP, Port: 8000, AppProtocol: ptr("http")},
				{Name: "metrics", Port: 9090},
			},
		},
	}
	se := &mcsv1beta1.ServiceExport{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
		Spec: mcsv1beta1.ServiceExportSpec{
			ExportedLabels:      map[string]string{"team": "inference", api.LabelPool: "explicit"},
			ExportedAnnotations: map[string]string{"note": "x"},
		},
	}
	spec := BuildSpec(svc, se)
	want := &gdsv1.ServiceExportSpec{
		Labels: map[string]string{
			api.LabelExport: api.ExportEnabled, api.LabelRole: api.RolePoolFrontend, api.LabelPool: "explicit", "team": "inference",
		},
		Annotations:           map[string]string{api.AnnotationPoolModels: "m1,m2", "note": "x"},
		Type:                  gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		SessionAffinity:       gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP,
		SessionAffinityConfig: &gdsv1.SessionAffinityConfig{ClientIp: &gdsv1.ClientIPConfig{TimeoutSeconds: 300}},
		IpFamilies:            []gdsv1.IPFamily{gdsv1.IPFamily_IP_FAMILY_IPV4},
		InternalTrafficPolicy: gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_LOCAL,
		TrafficDistribution:   "PreferClose",
		Ports: []*gdsv1.ServicePort{
			{Name: "http", Protocol: gdsv1.Protocol_PROTOCOL_TCP, AppProtocol: "http", Port: 8000},
			{Name: "metrics", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 9090},
		},
	}
	want.ExportCreationTimestamp = spec.GetExportCreationTimestamp()
	assert.True(t, proto.Equal(want, spec), "got %v", spec)
	assert.Equal(t, created, spec.GetExportCreationTimestamp().AsTime())

	// Headless, no affinity: the affinity config is dropped; no labels.
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.SessionAffinity = corev1.ServiceAffinityNone
	svc.Labels, svc.Annotations = nil, nil
	spec = BuildSpec(svc, &mcsv1beta1.ServiceExport{})
	assert.Equal(t, gdsv1.ServiceType_SERVICE_TYPE_HEADLESS, spec.GetType())
	assert.Nil(t, spec.GetSessionAffinityConfig())
	assert.Nil(t, spec.GetLabels())
	assert.Nil(t, spec.GetExportCreationTimestamp())
}

func TestSpecHashIsStable(t *testing.T) {
	a := &gdsv1.ServiceExportSpec{Labels: map[string]string{}, Annotations: map[string]string{}}
	b := &gdsv1.ServiceExportSpec{Labels: map[string]string{}, Annotations: map[string]string{}}
	for i := range 50 {
		a.Labels[fmt.Sprintf("k%d", i)] = "v"
		b.Labels[fmt.Sprintf("k%d", 49-i)] = "v"
	}
	assert.Equal(t, SpecHash(a), SpecHash(b))
	b.Labels["k0"] = "changed"
	assert.NotEqual(t, SpecHash(a), SpecHash(b))
}

func slice(name string, addressType discoveryv1.AddressType, ports []discoveryv1.EndpointPort, eps ...discoveryv1.Endpoint) discoveryv1.EndpointSlice {
	return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: name}, AddressType: addressType, Ports: ports, Endpoints: eps}
}

func TestBuildEndpoints(t *testing.T) {
	ports := []discoveryv1.EndpointPort{
		{Name: ptr("metrics"), Protocol: ptr(corev1.ProtocolTCP), Port: ptr[int32](9090)},
		{Name: ptr("http"), Port: ptr[int32](8000), AppProtocol: ptr("http")},
	}
	pod := &corev1.ObjectReference{Kind: "Pod", Name: "frontend-0", UID: types.UID("u0")}
	slices := []discoveryv1.EndpointSlice{
		slice("s1", discoveryv1.AddressTypeIPv4, ports,
			discoveryv1.Endpoint{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(false), Serving: ptr(true), Terminating: ptr(true)},
				Hostname: ptr("frontend-1"), NodeName: ptr("node-1"), Zone: ptr("us-east-1a")},
			discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, TargetRef: pod},
			discoveryv1.Endpoint{Addresses: nil},
		),
		// The EndpointSlice controller may list an endpoint twice while it
		// moves between slices.
		slice("s2", discoveryv1.AddressTypeIPv4, ports, discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, TargetRef: pod}),
		slice("s3", discoveryv1.AddressTypeIPv6, nil, discoveryv1.Endpoint{Addresses: []string{"fd00::1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(true)}}),
	}
	deleting := slice("s4", discoveryv1.AddressTypeIPv4, ports, discoveryv1.Endpoint{Addresses: []string{"10.9.9.9"}})
	deleting.DeletionTimestamp = ptr(metav1.Now())
	slices = append(slices, deleting)

	eps, dropped := BuildEndpoints(slices, 0)
	require.Zero(t, dropped)
	wantPorts := []*gdsv1.EndpointPort{
		{Name: "http", Protocol: gdsv1.Protocol_PROTOCOL_TCP, AppProtocol: "http", Port: 8000},
		{Name: "metrics", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: 9090},
	}
	want := []*gdsv1.Endpoint{
		{Addresses: []string{"10.0.0.1"}, AddressType: gdsv1.AddressType_ADDRESS_TYPE_IPV4,
			Conditions: &gdsv1.EndpointConditions{Ready: true, Serving: true}, Ports: wantPorts,
			Attributes: map[string]string{AttrTargetKind: "Pod", AttrTargetName: "frontend-0", AttrTargetUID: "u0"}},
		{Addresses: []string{"10.0.0.2"}, AddressType: gdsv1.AddressType_ADDRESS_TYPE_IPV4,
			Conditions: &gdsv1.EndpointConditions{Ready: false, Serving: true, Terminating: true}, Ports: wantPorts,
			Hostname: "frontend-1", NodeName: "node-1", Zone: "us-east-1a"},
		{Addresses: []string{"fd00::1"}, AddressType: gdsv1.AddressType_ADDRESS_TYPE_IPV6,
			Conditions: &gdsv1.EndpointConditions{Ready: true, Serving: true}},
	}
	require.Len(t, eps, len(want))
	for i := range want {
		assert.True(t, proto.Equal(want[i], eps[i]), "endpoint %d: got %v", i, eps[i])
	}

	// The hash does not depend on the order of slices or endpoints.
	reversed := []discoveryv1.EndpointSlice{slices[3], slices[2], slices[1], slices[0]}
	reversed[3].Endpoints = []discoveryv1.Endpoint{slices[0].Endpoints[2], slices[0].Endpoints[1], slices[0].Endpoints[0]}
	again, _ := BuildEndpoints(reversed, 0)
	assert.Equal(t, EndpointsHash(eps), EndpointsHash(again))

	// A ready flip changes it.
	slices[2].Endpoints[0].Conditions.Ready = ptr(false)
	changed, _ := BuildEndpoints(slices, 0)
	assert.NotEqual(t, EndpointsHash(eps), EndpointsHash(changed))
}

func TestBuildEndpointsTruncatesReadyFirst(t *testing.T) {
	var eps []discoveryv1.Endpoint
	for i := range 10 {
		eps = append(eps, discoveryv1.Endpoint{
			Addresses:  []string{fmt.Sprintf("10.0.0.%d", i)},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr(i%2 == 1)},
		})
	}
	got, dropped := BuildEndpoints([]discoveryv1.EndpointSlice{slice("s", discoveryv1.AddressTypeIPv4, nil, eps...)}, 4)
	assert.Equal(t, 6, dropped)
	require.Len(t, got, 4)
	for i, ep := range got {
		assert.True(t, ep.GetConditions().GetReady(), ep.GetAddresses())
		if i > 0 {
			assert.Less(t, got[i-1].GetAddresses()[0], ep.GetAddresses()[0], "sorted")
		}
	}
}

func TestConditionFromGDS(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	c := ConditionFromGDS(&gdsv1.Condition{Type: api.ConditionReady, Status: gdsv1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Ready", Message: "ok"})
	assert.Equal(t, metav1.Condition{Type: api.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ok"}, c)
	c = ConditionFromGDS(&gdsv1.Condition{Type: api.ConditionConflict, Status: gdsv1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: "PortConflict,TypeConflict"})
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "PortConflict,TypeConflict", c.Reason)
	c = ConditionFromGDS(&gdsv1.Condition{Type: "X", Reason: "not valid!", Message: string(make([]byte, 40000))})
	assert.Equal(t, metav1.ConditionUnknown, c.Status)
	assert.Equal(t, "Unknown", c.Reason)
	assert.Len(t, c.Message, 32768)
	c = ConditionFromGDS(&gdsv1.Condition{Type: "X", Reason: "R", LastTransitionAt: timestampOf(at)})
	assert.True(t, c.LastTransitionTime.Time.Equal(at))
}

func timestampOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }
