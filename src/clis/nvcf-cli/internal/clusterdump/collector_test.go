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

package clusterdump

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var fixedNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func nowFunc() time.Time { return fixedNow }

// fakeHelm is a test double for HelmRunner.
type fakeHelm struct {
	releases []HelmRelease
	err      error
}

func (f *fakeHelm) ListReleases(_ context.Context, _ string) ([]HelmRelease, error) {
	return f.releases, f.err
}

func newDynamicFake(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	// Register every GVR the compute-plane collector may LIST (the always-on
	// ICMS/GPU summaries list icmsrequests; the fake panics on an unregistered
	// resource, whereas the real client returns a handled NotFound).
	gvrToListKind := map[schema.GroupVersionResource]string{
		nvcfBackendGVR: "NVCFBackendList",
		icmsRequestGVR: "ICMSRequestList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
}

func readyNode(name string, gpu int64) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if gpu > 0 {
		n.Status.Capacity = corev1.ResourceList{
			gpuResourceName: *resource.NewQuantity(gpu, resource.DecimalSI),
		}
	}
	return n
}

func runningPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-2 * time.Hour)),
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}},
	}
}

func crashingPod(ns, name string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-10 * time.Minute)),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Ready:        false,
				RestartCount: restarts,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}},
		},
	}
}

func warningEvent(ns, name, reason, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name + "-evt", Namespace: ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: name},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        msg,
		LastTimestamp:  metav1.NewTime(fixedNow.Add(-1 * time.Minute)),
	}
}

func nvcfBackend(name, agentStatus, version string, gpuCapacity int64) *unstructured.Unstructured {
	status := map[string]interface{}{"agentStatus": agentStatus}
	if gpuCapacity > 0 {
		status["gpuUsage"] = map[string]interface{}{
			"H100": map[string]interface{}{"capacity": gpuCapacity},
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvcf.nvidia.io/v1",
		"kind":       "NVCFBackend",
		"metadata": map[string]interface{}{
			"name":              name,
			"namespace":         "nvca-operator",
			"creationTimestamp": fixedNow.Add(-48 * time.Hour).Format(time.RFC3339),
		},
		"spec":   map[string]interface{}{"version": version},
		"status": status,
	}}
}

func TestPlaneCollector_ControlPlane(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(
		readyNode("cp-node-1", 0),
		runningPod("nvcf", "nvcf-api-aaa"),
		crashingPod("nvcf", "reval-bbb", 5),
		warningEvent("nvcf", "reval-bbb", "BackOff", "Back-off restarting failed container"),
	)
	pc := &PlaneCollector{
		Role:       RoleControlPlane,
		Context:    "k3d-ncp-local-cp",
		Kube:       kube,
		Helm:       &fakeHelm{releases: []HelmRelease{{Name: "nats", Namespace: "nats-system", Chart: "nats-1.2.3", AppVersion: "1.2.3", Status: "deployed"}}},
		Namespaces: ControlPlaneNamespaces(),
		NowFunc:    nowFunc,
	}

	snap := pc.collect(context.Background())

	assert.Equal(t, RoleControlPlane, snap.Role)
	assert.Equal(t, 1, snap.Nodes.Ready)
	assert.Equal(t, 1, snap.Nodes.Total)
	assert.Equal(t, int64(0), snap.Nodes.GPUCount)
	assert.Len(t, snap.HelmReleases, 1)
	assert.Len(t, snap.Pods, 2, "both pods collected regardless of health")
	assert.Len(t, snap.Events, 1)
	assert.Empty(t, snap.NVCFBackends, "no NVCFBackend on control plane")
	assert.Empty(t, snap.Warnings)
}

func TestPlaneCollector_HelmNotInstalled(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(readyNode("cp-node-1", 0))
	pc := &PlaneCollector{
		Role:       RoleControlPlane,
		Context:    "k3d-ncp-local-cp",
		Kube:       kube,
		Helm:       &fakeHelm{err: errors.New("helm not found in PATH")},
		Namespaces: ControlPlaneNamespaces(),
		NowFunc:    nowFunc,
	}

	snap := pc.collect(context.Background())

	assert.Empty(t, snap.HelmReleases)
	require.NotEmpty(t, snap.Warnings)
	assert.Contains(t, strings.Join(snap.Warnings, "\n"), "helm: helm not found in PATH")
}

func TestPlaneCollector_ComputePlane(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(readyNode("gpu-node-1", 8))
	dyn := newDynamicFake(nvcfBackend("ncp-local-compute-1", "Healthy", "2.1.0", 8))
	pc := &PlaneCollector{
		Role:       RoleComputePlane,
		Context:    "k3d-ncp-local-compute-1",
		Kube:       kube,
		Dynamic:    dyn,
		Helm:       &fakeHelm{releases: []HelmRelease{{Name: "nvca-operator", Namespace: "nvca-operator", Status: "deployed"}}},
		Namespaces: ComputePlaneNamespaces(),
		NowFunc:    nowFunc,
	}

	snap := pc.collect(context.Background())

	assert.Equal(t, int64(8), snap.Nodes.GPUCount)
	require.Len(t, snap.NVCFBackends, 1)
	b := snap.NVCFBackends[0]
	assert.Equal(t, "ncp-local-compute-1", b.Name)
	assert.Equal(t, "Healthy", b.AgentStatus)
	assert.Equal(t, "2.1.0", b.NVCAVersion)
	assert.Equal(t, int64(8), b.GPUCapacity)
	assert.Equal(t, "2d", b.Age.String())
	assert.Empty(t, snap.Warnings)
}

func TestCollector_SplitCluster(t *testing.T) {
	control := PlaneCollector{
		Role: RoleControlPlane, Context: "cp", Kube: k8sfake.NewSimpleClientset(readyNode("cp", 0)),
		Helm: &fakeHelm{}, Namespaces: ControlPlaneNamespaces(), NowFunc: nowFunc,
	}
	compute := &PlaneCollector{
		Role: RoleComputePlane, Context: "compute", Kube: k8sfake.NewSimpleClientset(readyNode("gpu", 4)),
		Dynamic: newDynamicFake(), Helm: &fakeHelm{}, Namespaces: ComputePlaneNamespaces(), NowFunc: nowFunc,
	}
	c := &Collector{Control: &control, Compute: compute}

	d, err := c.Collect(context.Background())
	require.NoError(t, err)

	assert.Equal(t, fixedNow, d.CollectedAt)
	require.NotNil(t, d.ControlPlane)
	require.NotNil(t, d.ComputePlane)
	assert.Equal(t, RoleComputePlane, d.ComputePlane.Role)
	assert.Equal(t, int64(4), d.ComputePlane.Nodes.GPUCount)
}

func TestCollector_SingleCluster(t *testing.T) {
	control := PlaneCollector{
		Role: RoleControlPlane, Context: "cp", Kube: k8sfake.NewSimpleClientset(readyNode("cp", 0)),
		Helm: &fakeHelm{}, Namespaces: ControlPlaneNamespaces(), NowFunc: nowFunc,
	}
	c := &Collector{Control: &control}

	d, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, d.ControlPlane)
	assert.Nil(t, d.ComputePlane, "compute plane omitted in single-cluster mode")
}

func TestCollector_ComputeOnly(t *testing.T) {
	compute := &PlaneCollector{
		Role: RoleComputePlane, Context: "compute", Kube: k8sfake.NewSimpleClientset(readyNode("gpu", 4)),
		Dynamic: newDynamicFake(), Helm: &fakeHelm{}, Namespaces: ComputePlaneNamespaces(), NowFunc: nowFunc,
	}
	c := &Collector{Compute: compute}

	d, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fixedNow, d.CollectedAt)
	assert.Nil(t, d.ControlPlane, "control plane omitted in compute-only mode")
	require.NotNil(t, d.ComputePlane)
	assert.Equal(t, RoleComputePlane, d.ComputePlane.Role)
}

func TestRenderText_ShowsAllPods(t *testing.T) {
	d := Dump{
		CollectedAt: fixedNow,
		ControlPlane: &PlaneSnapshot{
			Role:    RoleControlPlane,
			Context: "k3d-ncp-local-cp",
			Pods: []PodRow{
				{Namespace: "nvcf", Name: "nvcf-api-aaa", Ready: "1/1", Status: "Running", Age: HumanDuration(2 * time.Hour)},
				{Namespace: "nvcf", Name: "reval-bbb", Ready: "0/1", Status: "CrashLoopBackOff", Restarts: 5, Age: HumanDuration(10 * time.Minute)},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, RenderText(&buf, d))
	out := buf.String()

	assert.Contains(t, out, "Pods  2 total, 1 unhealthy")
	assert.Contains(t, out, "reval-bbb", "unhealthy pod shown")
	assert.Contains(t, out, "CrashLoopBackOff")
	assert.Contains(t, out, "nvcf-api-aaa", "all pods shown, including healthy")
}

func TestRenderText_NoPods(t *testing.T) {
	d := Dump{
		CollectedAt:  fixedNow,
		ControlPlane: &PlaneSnapshot{Role: RoleControlPlane},
	}

	var buf bytes.Buffer
	require.NoError(t, RenderText(&buf, d))
	out := buf.String()

	idx := strings.Index(out, "Pods  0 total, 0 unhealthy")
	require.GreaterOrEqual(t, idx, 0)
	assert.Contains(t, out[idx:], "(none)")
}

func TestRenderJSON_IncludesAllPods(t *testing.T) {
	d := Dump{
		CollectedAt: fixedNow,
		ControlPlane: &PlaneSnapshot{
			Role: RoleControlPlane,
			Pods: []PodRow{
				{Namespace: "nvcf", Name: "healthy", Ready: "1/1", Status: "Running", Age: HumanDuration(time.Hour)},
				{Namespace: "nvcf", Name: "broken", Ready: "0/1", Status: "CrashLoopBackOff", Age: HumanDuration(time.Minute)},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, RenderJSON(&buf, d))
	out := buf.String()

	assert.Contains(t, out, "\"healthy\"", "JSON carries all pods including healthy ones")
	assert.Contains(t, out, "\"broken\"")
	assert.Contains(t, out, "\"age\": \"1h\"", "duration renders as human string in JSON")
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{10 * time.Minute, "10m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
		{-5 * time.Second, "0s"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, humanizeDuration(tc.d))
	}
}

func TestPodReason(t *testing.T) {
	row := podRow(crashingPod("nvcf", "x", 3), fixedNow)
	assert.Equal(t, "CrashLoopBackOff", row.Status)
	assert.Equal(t, int32(3), row.Restarts)
	assert.Equal(t, "0/1", row.Ready)
}
