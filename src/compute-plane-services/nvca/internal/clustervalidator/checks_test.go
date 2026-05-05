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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
	client := fake.NewSimpleClientset(
		makeNode("node-1", false, 0),
		makeNode("node-2", false, 0),
		makeNode("node-3", true, 0),
	)
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.False(t, state.ControlPlaneHealthy)
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
