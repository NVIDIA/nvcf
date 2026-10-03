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
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	results, err := probeAllNodes(context.Background(), client, "")
	require.NoError(t, err)
	assert.Empty(t, results, "no nodes → no per-node results")
}

func TestProbeAllNodes_ListNodesError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden: nodes")
	})
	_, err := probeAllNodes(context.Background(), client, "")
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
	results, err := probeAllNodes(context.Background(), client, "")
	require.NoError(t, err, "list succeeded so the overall probe should not return an error")
	require.Len(t, results, 2)
	for _, r := range results {
		require.Error(t, r.Err, "per-node create failure must surface as NodeInotifyLimits.Err")
		assert.Contains(t, r.Err.Error(), "create probe pod")
		assert.Contains(t, r.Err.Error(), r.NodeName)
	}
}

// The probe is bounded as a whole. Nodes are probed probeConcurrency at a
// time, so on a cluster whose probe pods never finish each wave took
// perNodePodTimeout, and the probe cut short the validator that runs after it.
// Nodes the budget did not reach are reported as not probed, and every pod
// created is removed.
func TestProbeAllNodes_StallingPodsAreBoundedByTheProbeBudget(t *testing.T) {
	prev := inotifyProbeBudget
	inotifyProbeBudget = 500 * time.Millisecond
	t.Cleanup(func() { inotifyProbeBudget = prev })
	const extra = 3
	var nodes []runtime.Object
	for i := 0; i < probeConcurrency+extra; i++ {
		nodes = append(nodes, fakeNode(fmt.Sprintf("node-%02d", i)))
	}
	client := fake.NewSimpleClientset(nodes...)
	var created atomic.Int32
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		// The fake clientset does not honour generateName. The pods never
		// reach a terminal phase, like pods that cannot pull or schedule.
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, created.Add(1))
		return false, nil, nil
	})

	start := time.Now()
	results, err := probeAllNodes(context.Background(), client, "")
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the probe outlived its budget")
	require.Len(t, results, probeConcurrency+extra)
	notProbed := 0
	for _, r := range results {
		require.Error(t, r.Err, r.NodeName)
		if strings.Contains(r.Err.Error(), "not probed: the inotify probe's 500ms budget ran out") {
			notProbed++
		}
	}
	assert.Equal(t, extra, notProbed, "the nodes past the first wave are never reached")
	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, pods.Items, "every probe pod created is removed")
	assert.EqualValues(t, probeConcurrency, created.Load())
}

func TestBuildInotifyProbePodShape(t *testing.T) {
	pod := buildInotifyProbePod("node-x", "")

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

	assert.False(t, c.Resources.Requests.Cpu().IsZero(), "a LimitRange or quota needs requests")
	assert.False(t, c.Resources.Requests.Memory().IsZero(), "a LimitRange or quota needs requests")
	assert.False(t, c.Resources.Limits.Memory().IsZero())
	require.NotNil(t, pod.Spec.SecurityContext)
	require.NotNil(t, pod.Spec.SecurityContext.RunAsUser)
	assert.NotZero(t, *pod.Spec.SecurityContext.RunAsUser, "the sysctls are world-readable, so root is not needed")
	require.NotNil(t, c.SecurityContext)
	assert.True(t, *c.SecurityContext.RunAsNonRoot)
	assert.True(t, *c.SecurityContext.ReadOnlyRootFilesystem)
	assert.False(t, *c.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
}

// The configured probe image replaces the Docker Hub default, so a cluster
// that pulls from a mirror can run the probe.
func TestBuildInotifyProbePod_UsesTheConfiguredImage(t *testing.T) {
	pod := buildInotifyProbePod("node-x", "mirror.example/busybox:1.36")
	assert.Equal(t, "mirror.example/busybox:1.36", pod.Spec.Containers[0].Image)
}
