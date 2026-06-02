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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// ---------------------------------------------------------------------------
// checkPrerequisites – additional cases
// ---------------------------------------------------------------------------

func TestCheckPrerequisites_NoNodes(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	err := checkPrerequisites(context.Background(), client, state)
	require.NoError(t, err)
	assert.Equal(t, "0", state.TotalNodes)
	assert.NotEmpty(t, state.K8sVersion)
}

// ---------------------------------------------------------------------------
// checkControlPlaneHealth – additional cases
// ---------------------------------------------------------------------------

func TestCheckControlPlaneHealth_NoPods(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
	checkControlPlaneHealth(context.Background(), client, state)
	// Missing control plane pods mark the cluster as unhealthy.
	assert.False(t, state.ControlPlaneHealthy)
}

func TestCheckControlPlaneHealth_MixedPodPhases(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("node-1", true, 0),
		makePod("kube-apiserver-node-1", "kube-system", corev1.PodRunning),
		makePod("kube-scheduler-node-1", "kube-system", corev1.PodPending),
		makePod("etcd-node-1", "kube-system", corev1.PodFailed),
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
	checkControlPlaneHealth(context.Background(), client, state)
	// Non-running pods (Pending/Failed) and missing pods mark cluster unhealthy.
	assert.False(t, state.ControlPlaneHealthy)
}

func TestCheckControlPlaneHealth_MultipleNotReadyNodes(t *testing.T) {
	// NotReady worker nodes alone should NOT flip the cluster verdict —
	// they emit a Warning but cluster readiness is unaffected. Only
	// control-plane pod failures cause Critical.
	client := fake.NewSimpleClientset(
		makeNode("node-1", false, 0),
		makeNode("node-2", false, 0),
		makeNode("node-3", true, 0),
		makePod("kube-apiserver-node-3", "kube-system", corev1.PodRunning),
		makePod("kube-controller-manager-node-3", "kube-system", corev1.PodRunning),
		makePod("kube-scheduler-node-3", "kube-system", corev1.PodRunning),
		makePod("etcd-node-3", "kube-system", corev1.PodRunning),
		makePod("coredns-node-3", "kube-system", corev1.PodRunning),
		makePod("kube-proxy-node-3", "kube-system", corev1.PodRunning),
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true, NodesAllReady: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.True(t, state.ControlPlaneHealthy,
		"NotReady nodes alone should not mark Control Plane unhealthy")
	assert.False(t, state.NodesAllReady,
		"NodesAllReady should reflect that not all worker nodes are Ready")
	assert.Equal(t, 2, state.NotReadyNodes,
		"NotReadyNodes count should match the actual number of NotReady nodes")
	assert.NotEmpty(t, state.Warnings,
		"a Warning entry should be appended for surface in printSummary")
	assert.Empty(t, state.Recommendations,
		"no recommendations expected when only nodes are NotReady (no pod failures)")
}

func TestCheckControlPlaneHealth_PodsUnhealthyButNodesReady(t *testing.T) {
	// Missing data-plane pods (coredns, kube-proxy) should still flip the
	// verdict to Critical, even when all nodes are Ready.
	client := fake.NewSimpleClientset(
		makeNode("node-1", true, 0),
		// no data-plane pods → podsHealthy = false
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.False(t, state.ControlPlaneHealthy,
		"missing coredns/kube-proxy should mark cluster unhealthy")
	assert.NotEmpty(t, state.Recommendations)
}

func TestCheckControlPlaneHealth_ManagedControlPlane(t *testing.T) {
	// EKS-like cluster: control-plane pods (apiserver/etcd/scheduler/cm) are
	// hidden because the cloud provider manages them, but coredns and
	// kube-proxy run as visible workloads. /readyz isn't reachable from the
	// fake client, so we fall back to ServerVersion which succeeds. Verdict
	// must be healthy.
	client := fake.NewSimpleClientset(
		makeNode("ip-10-0-1-15", true, 0),
		makeNode("ip-10-0-1-22", true, 0),
		makePod("coredns-abc123", "kube-system", corev1.PodRunning),
		makePod("coredns-xyz789", "kube-system", corev1.PodRunning),
		makePod("kube-proxy-aaa", "kube-system", corev1.PodRunning),
		makePod("kube-proxy-bbb", "kube-system", corev1.PodRunning),
		// no apiserver / etcd / scheduler / controller-manager pods
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true, NodesAllReady: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.True(t, state.ControlPlaneHealthy,
		"managed control plane (apiserver/etc. hidden) should still be healthy "+
			"when coredns/kube-proxy are running")
	assert.True(t, state.NodesAllReady)
	assert.Empty(t, state.Recommendations,
		"no recommendations expected on a healthy managed cluster")
}

func TestCheckControlPlaneHealth_DataPlanePodMissingOnManaged(t *testing.T) {
	// Even on a managed cluster (no apiserver pod visible), missing coredns
	// is still Critical because workloads depend on it.
	client := fake.NewSimpleClientset(
		makeNode("ip-10-0-1-15", true, 0),
		makePod("kube-proxy-aaa", "kube-system", corev1.PodRunning),
		// no coredns
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true, NodesAllReady: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.False(t, state.ControlPlaneHealthy,
		"missing coredns should flip the verdict even on a managed cluster")
	assert.NotEmpty(t, state.Recommendations)
}

// ---------------------------------------------------------------------------
// checkSMBCSIDriver – additional cases
// ---------------------------------------------------------------------------

func TestCheckSMBCSIDriver_OldVersion(t *testing.T) {
	client := fake.NewSimpleClientset(
		&storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "smb.csi.k8s.io"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "csi-smb-controller", Namespace: "kube-system"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "csi-smb"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "csi-smb"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "smb", Image: "registry.k8s.io/sig-storage/smbplugin:v1.14.0"},
						},
					},
				},
			},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkSMBCSIDriver(context.Background(), client, state)
	assert.False(t, state.SMBCSIDriverOK)
	assert.NotEmpty(t, state.Recommendations)
}

func TestCheckSMBCSIDriver_VersionUndetectable(t *testing.T) {
	client := fake.NewSimpleClientset(
		&storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "smb.csi.k8s.io"}},
		// No deployment with version in image tag.
	)
	state := &ValidationState{Log: testLog()}
	checkSMBCSIDriver(context.Background(), client, state)
	// Undetectable version → mark OK but add recommendation.
	assert.True(t, state.SMBCSIDriverOK)
	assert.NotEmpty(t, state.Recommendations)
}

// ---------------------------------------------------------------------------
// detectSMBVersion – additional cases
// ---------------------------------------------------------------------------

func TestDetectSMBVersion_InSMBCSINamespace(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "csi-smb-controller", Namespace: "smb-csi"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "csi-smb"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "csi-smb"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "smb", Image: "registry.k8s.io/sig-storage/smbplugin:v1.17.2"},
						},
					},
				},
			},
		},
	)
	result := detectSMBVersion(context.Background(), client)
	assert.Equal(t, "1.17.2", result)
}

func TestDetectSMBVersion_NoDeployment(t *testing.T) {
	client := fake.NewSimpleClientset()
	result := detectSMBVersion(context.Background(), client)
	assert.Empty(t, result)
}

func TestDetectSMBVersion_NoVersionInImage(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "csi-smb-controller", Namespace: "kube-system"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "csi-smb"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "csi-smb"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "smb", Image: "custom-registry/smb:latest"},
						},
					},
				},
			},
		},
	)
	result := detectSMBVersion(context.Background(), client)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// checkGPUResources – additional cases
// ---------------------------------------------------------------------------

func TestCheckGPUResources_MultipleGPUNodes(t *testing.T) {
	client := fake.NewSimpleClientset(
		gpuNodeHelper("gpu-1", 4, 2),
		gpuNodeHelper("gpu-2", 8, 8),
		makeNode("cpu-1", true, 0),
	)
	state := &ValidationState{Log: testLog()}
	checkGPUResources(context.Background(), client, state)
	assert.True(t, state.GPUAvailable)
	assert.Empty(t, state.Recommendations)
}

func TestCheckGPUResources_NoNodes(t *testing.T) {
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog()}
	checkGPUResources(context.Background(), client, state)
	assert.False(t, state.GPUAvailable)
	assert.NotEmpty(t, state.Recommendations)
}

// ---------------------------------------------------------------------------
// checkGPUOperator – additional cases
// ---------------------------------------------------------------------------

func TestCheckGPUOperator_InAlternateNamespace(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gpu-operator-xyz",
				Namespace: "custom-gpu-ns",
				Labels:    map[string]string{"app": "gpu-operator"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	state := &ValidationState{Log: testLog()}
	checkGPUOperator(context.Background(), client, state)
	assert.True(t, state.GPUOperatorInstalled)
}

func TestCheckGPUOperator_NamespaceExistsButNoPods(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gpu-operator"}},
	)
	state := &ValidationState{Log: testLog()}
	checkGPUOperator(context.Background(), client, state)
	assert.False(t, state.GPUOperatorInstalled)
	assert.NotEmpty(t, state.Recommendations)
}

func TestCheckGPUOperator_MixedPodPhases(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gpu-operator"}},
		makePod("gpu-operator-main", "gpu-operator", corev1.PodRunning),
		makePod("gpu-operator-init", "gpu-operator", corev1.PodSucceeded),
		makePod("gpu-operator-fail", "gpu-operator", corev1.PodFailed),
	)
	state := &ValidationState{Log: testLog()}
	checkGPUOperator(context.Background(), client, state)
	assert.True(t, state.GPUOperatorInstalled)
}

func TestCheckGPUOperator_NotInstalledWithGPUsAvailable(t *testing.T) {
	// Manual Instance Configuration: GPUs are already discoverable on the
	// node (e.g. nvidia.com/gpu in capacity) without GPU Operator. The
	// validator must report this as a Warning rather than an Error and
	// must NOT add the "Install GPU Operator" recommendation.
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog(), GPUAvailable: true}
	checkGPUOperator(context.Background(), client, state)
	assert.False(t, state.GPUOperatorInstalled)
	assert.Empty(t, state.Recommendations,
		"should not recommend installing GPU Operator when GPUs are already discoverable")
	assert.NotEmpty(t, state.Warnings,
		"should append a Warning entry so the summary surfaces it under 'with warnings'")
}

func TestCheckGPUOperator_NotInstalledWithoutGPUs(t *testing.T) {
	// When neither GPU Operator nor GPUs are present, keep the existing
	// behavior: install recommendation surfaces. (The Critical verdict
	// itself is driven by GPU Resources, not this check.)
	client := fake.NewSimpleClientset()
	state := &ValidationState{Log: testLog(), GPUAvailable: false}
	checkGPUOperator(context.Background(), client, state)
	assert.False(t, state.GPUOperatorInstalled)
	assert.NotEmpty(t, state.Recommendations,
		"should still recommend installing GPU Operator when no GPUs are visible")
	assert.Empty(t, state.Warnings,
		"no manual-mode warning when there's no alternative GPU mechanism")
}

// ---------------------------------------------------------------------------
// parseVersion – additional cases
// ---------------------------------------------------------------------------

func TestParseVersion_WithPreRelease(t *testing.T) {
	result := parseVersion("1.17.0-rc1")
	assert.Equal(t, []int{1, 17, 0}, result)
}

func TestParseVersion_WithBuildMetadata(t *testing.T) {
	result := parseVersion("1.16.0+build.1")
	assert.Equal(t, []int{1, 16, 0}, result)
}

func TestParseVersion_TwoPartsOnly(t *testing.T) {
	result := parseVersion("1.16")
	assert.Nil(t, result)
}

func TestParseVersion_NonNumeric(t *testing.T) {
	result := parseVersion("a.b.c")
	assert.Nil(t, result)
}

func TestParseVersion_Empty(t *testing.T) {
	result := parseVersion("")
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// versionGTE – additional edge cases
// ---------------------------------------------------------------------------

func TestVersionGTE_TwoPartVersion(t *testing.T) {
	assert.False(t, versionGTE("1.16", "1.16.0"))
}

func TestVersionGTE_BothWithVPrefix(t *testing.T) {
	assert.True(t, versionGTE("v2.0.0", "v1.16.0"))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func gpuNodeHelper(name string, capacity, allocatable int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
				"nvidia.com/gpu":      *resource.NewQuantity(capacity, resource.DecimalSI),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
				"nvidia.com/gpu":      *resource.NewQuantity(allocatable, resource.DecimalSI),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// detectManagedClusterProvider
// ---------------------------------------------------------------------------

func nodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestDetectManagedClusterProvider(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"EKS", map[string]string{"eks.amazonaws.com/nodegroup": "ng-1"}, "EKS"},
		{"GKE", map[string]string{"cloud.google.com/gke-nodepool": "default"}, "GKE"},
		{"AKS", map[string]string{"kubernetes.azure.com/agentpool": "agentpool"}, "AKS"},
		{"self-hosted (no label)", map[string]string{"kubernetes.io/hostname": "node-1"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(nodeWithLabels("node-1", tt.labels))
			got := detectManagedClusterProvider(context.Background(), client)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// isEmbeddedKubeProxyDistro / k3s-rke2 handling
// ---------------------------------------------------------------------------

func TestIsEmbeddedKubeProxyDistro(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"vanilla", "v1.30.2", false},
		{"EKS", "v1.32.13-eks-bbe087e", false},
		{"GKE", "v1.30.5-gke.1014001", false},
		{"k3s", "v1.30.2+k3s2", true},
		{"k3s with dot", "v1.30.2+k3s.1", true},
		{"rke2", "v1.30.5+rke2r1", true},
		{"k3s uppercase", "v1.30.2+K3S2", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isEmbeddedKubeProxyDistro(tt.version))
		})
	}
}

func TestCheckControlPlaneHealth_K3sSkipsKubeProxy(t *testing.T) {
	// k3s/k3d cluster: coredns present, no kube-proxy pod (embedded in k3s
	// server binary). Verdict must still be healthy.
	client := fake.NewSimpleClientset(
		makeNode("k3d-server-0", true, 0),
		makePod("coredns-abc", "kube-system", corev1.PodRunning),
	)
	state := &ValidationState{
		Log:                 testLog(),
		ControlPlaneHealthy: true,
		NodesAllReady:       true,
		K8sVersion:          "v1.30.2+k3s2",
	}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.True(t, state.ControlPlaneHealthy,
		"k3s clusters without a kube-proxy pod should still be healthy")
	assert.Empty(t, state.Recommendations)
}

func TestCheckControlPlaneHealth_K3sCorednsStillRequired(t *testing.T) {
	// On k3s/k3d, coredns still runs as a workload and is required. Missing
	// coredns is still Critical even when kube-proxy is skipped.
	client := fake.NewSimpleClientset(
		makeNode("k3d-server-0", true, 0),
		// no coredns
	)
	state := &ValidationState{
		Log:                 testLog(),
		ControlPlaneHealthy: true,
		NodesAllReady:       true,
		K8sVersion:          "v1.30.2+k3s2",
	}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.False(t, state.ControlPlaneHealthy,
		"missing coredns on k3s should still mark cluster unhealthy")
}

// ---------------------------------------------------------------------------
// probeReadyz error classification (Greptile P2 follow-up)
// ---------------------------------------------------------------------------

// Test5xxStatusErrorPredicate documents the predicate probeReadyz uses to
// classify errors from the REST client. HTTP 5xx StatusErrors (Kubernetes
// signals /readyz unreadiness via 503) must be routed to reached=true,
// ready=false. Anything else (transport errors, non-5xx StatusErrors,
// context cancellation) must NOT match the predicate.
//
// This is a unit test on the predicate expression in isolation. The
// end-to-end behaviour of probeReadyz is exercised by
// TestProbeReadyz_AgainstHTTPServer below, which drives a real REST
// client against an httptest server.
func Test5xxStatusErrorPredicate(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want5xx bool
	}{
		{"503 ServiceUnavailable", apierrors.NewServiceUnavailable("api not ready"), true},
		{"500 InternalError", apierrors.NewInternalError(errors.New("boom")), true},
		{"generic transport error", errors.New("dial tcp: connection refused"), false},
		{"context canceled", context.Canceled, false},
		{"404 NotFound (not 5xx)", apierrors.NewNotFound(corev1.Resource("nodes"), "x"), false},
		{"401 Unauthorized (not 5xx)", apierrors.NewUnauthorized("nope"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var se *apierrors.StatusError
			got := errors.As(tt.err, &se) && se.ErrStatus.Code >= 500 && se.ErrStatus.Code < 600
			assert.Equal(t, tt.want5xx, got,
				"probeReadyz routes HTTP 5xx StatusErrors to reached=true,ready=false")
		})
	}
}

// TestProbeReadyz_AgainstHTTPServer drives probeReadyz end-to-end against a
// real REST client pointed at httptest servers. This catches regressions in
// the function's wiring that a predicate-only unit test would miss
// (e.g. if the errors.As branch were moved, removed, or the response body
// parsing changed). Three scenarios:
//   - 200 "ok"       → reached=true,  ready=true
//   - 503 + Status   → reached=true,  ready=false (Greptile P2 fix)
//   - port nothing's listening on → reached=false, ready=false
func TestProbeReadyz_AgainstHTTPServer(t *testing.T) {
	t.Run("200 ok body returns reached and ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
		require.NoError(t, err)

		reached, ready := probeReadyz(context.Background(), client)
		assert.True(t, reached, "200 OK must be classified as reached")
		assert.True(t, ready, "body 'ok' must be classified as ready")
	})

	t.Run("503 with Status body returns reached but not ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"api server not ready","code":503}`))
		}))
		defer srv.Close()

		client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
		require.NoError(t, err)

		reached, ready := probeReadyz(context.Background(), client)
		assert.True(t, reached, "503 from /readyz must be classified as reached")
		assert.False(t, ready, "503 must be classified as not ready")
	})

	t.Run("transport failure returns not reached", func(t *testing.T) {
		// Port 1 is reserved (tcpmux); nothing listens. Connect should
		// fail with a non-StatusError transport error, which probeReadyz
		// must classify as reached=false.
		client, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
		require.NoError(t, err)

		reached, ready := probeReadyz(context.Background(), client)
		assert.False(t, reached, "transport failure must NOT be classified as reached")
		assert.False(t, ready)
	})
}

// ---------------------------------------------------------------------------
// checkControlPlaneHealth: node-list failure (Greptile P2 follow-up)
// ---------------------------------------------------------------------------

// TestCheckControlPlaneHealth_NodeListErrorClearsNodesAllReady verifies that
// when the API server fails the Nodes().List() call, both the cluster
// verdict flips to unhealthy AND NodesAllReady is set to false — so the
// summary row reads "Worker Nodes: 0 NotReady" rather than the misleading
// "Worker Nodes: All Ready" that the prior code would have produced.
func TestCheckControlPlaneHealth_NodeListErrorClearsNodesAllReady(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("coredns-abc", "kube-system", corev1.PodRunning),
		makePod("kube-proxy-xyz", "kube-system", corev1.PodRunning),
	)
	client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated nodes-list error")
	})

	state := &ValidationState{
		Log:                 testLog(),
		ControlPlaneHealthy: true,
		NodesAllReady:       true,
	}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.False(t, state.ControlPlaneHealthy,
		"node listing failure must flip the cluster verdict")
	assert.False(t, state.NodesAllReady,
		"NodesAllReady must reflect that node status was not verified")
}
