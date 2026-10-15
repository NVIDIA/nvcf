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

package selfhosted

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// fakeNode is a minimal corev1.Node for the fake clientset's tracker.
func fakeNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestProbeAllNodes_EmptyCluster(t *testing.T) {
	client := fake.NewSimpleClientset()
	results, err := probeAllNodes(context.Background(), client)
	require.NoError(t, err)
	assert.Empty(t, results, "no nodes → no per-node results")
}

func TestProbeAllNodes_ListNodesError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden: nodes")
	})
	_, err := probeAllNodes(context.Background(), client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing nodes")
	assert.Contains(t, err.Error(), "forbidden")
}

func TestProbeAllNodes_PodCreateErrorSurfacesPerNode(t *testing.T) {
	unschedulableNode := fakeNode("node-c")
	unschedulableNode.Spec.Unschedulable = true
	client := fake.NewSimpleClientset(fakeNode("node-a"), fakeNode("node-b"), unschedulableNode)
	client.PrependReactor("create", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden: pod create denied")
	})
	results, err := probeAllNodes(context.Background(), client)
	require.NoError(t, err, "list succeeded so the overall probe should not return an error")
	require.Len(t, results, 2)
	for _, r := range results {
		require.Error(t, r.Err, "per-node create failure must surface as NodeInotifyLimits.Err")
		assert.Contains(t, r.Err.Error(), "create probe pod")
		assert.Contains(t, r.Err.Error(), r.NodeName)
	}
}

func TestBuildInotifyProbePodShape(t *testing.T) {
	pod := buildInotifyProbePod("node-x")

	assert.Equal(t, inotifyProbeNamespace, pod.Namespace)
	assert.Equal(t, "nvcf-inotify-probe-", pod.GenerateName, "GenerateName lets the API server assign a unique suffix")
	assert.Empty(t, pod.Name, "explicit Name conflicts with GenerateName")
	assert.Equal(t, "node-x", pod.Spec.NodeName, "must pin to the target node")
	assert.False(t, pod.Spec.HostPID,
		"hostPID is not needed; inotify sysctls are world-readable via the /host hostPath mount, and dropping hostPID reduces the privilege footprint for PSA baseline/restricted clusters")
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	require.Len(t, pod.Spec.Tolerations, 1)
	assert.Equal(t, corev1.TolerationOpExists, pod.Spec.Tolerations[0].Operator,
		"tolerate-all so tainted nodes (control-plane, GPU) are probed")

	require.Len(t, pod.Spec.Containers, 1)
	c := pod.Spec.Containers[0]
	assert.Equal(t, inotifyProbeContainer, c.Name)
	assert.Equal(t, inotifyProbeImage, c.Image)
	assert.Equal(t, []string{"sh", "-c", inotifyProbeShellCmd}, c.Command)
	require.Len(t, c.VolumeMounts, 1)
	assert.Equal(t, "/host", c.VolumeMounts[0].MountPath)
	assert.True(t, c.VolumeMounts[0].ReadOnly, "no writes to host fs from a probe pod")

	require.Len(t, pod.Spec.Volumes, 1)
	require.NotNil(t, pod.Spec.Volumes[0].HostPath)
	assert.Equal(t, "/", pod.Spec.Volumes[0].HostPath.Path)
}
