// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The helper comes from the node bundle, read-only, and its volume is
// added once however many steps run it.
func TestWithHelperVolume_BundleReadOnlyOnce(t *testing.T) {
	m := &Mutator{HostBundleRoot: "/bundle"}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine", Image: "img"}}}}
	step := corev1.Container{Name: "step", Image: "img", Command: []string{IdleBin}, VolumeMounts: []corev1.VolumeMount{helperMount()}}
	patches := m.withHelperVolume(pod, appendInits(pod, nil, step))
	patches = m.withHelperVolume(pod, appendInits(pod, patches, step))
	got := applyPatches(t, pod, mergePatchPlan(patches))
	var vols []corev1.Volume
	for _, v := range got.Spec.Volumes {
		if v.Name == helperVolumeName {
			vols = append(vols, v)
		}
	}
	if len(vols) != 1 || vols[0].HostPath == nil || vols[0].HostPath.Path != "/bundle/nvsnap" || *vols[0].HostPath.Type != corev1.HostPathDirectory {
		t.Fatalf("one helper volume from the node bundle, got %+v", vols)
	}
	if vm := helperMount(); !vm.ReadOnly || vm.MountPath != helperMountPath {
		t.Errorf("steps mount the bundle read-only: %+v", vm)
	}
}

// The download Job requests no GPU: it gets the agent's placement on top
// of the pod's nodeSelector, so it runs where the helper is staged. The
// JSON is what the chart renders from its default values.
func TestOnAgentNodes(t *testing.T) {
	var p AgentPlacement
	if err := json.Unmarshal([]byte(`{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"nvidia.com/gpu.present","operator":"In","values":["true"]}]},{"matchExpressions":[{"key":"cloud.google.com/gke-gpu","operator":"In","values":["true"]}]},{"matchExpressions":[{"key":"feature.node.kubernetes.io/pci-10de.present","operator":"In","values":["true"]}]}]}},"nodeSelector":{}}`), &p); err != nil {
		t.Fatalf("chart placement JSON: %v", err)
	}
	if p.NodeAffinity == nil || p.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("the chart's default node affinity did not parse: %+v", p)
	}
	p.NodeSelector = map[string]string{"pool": "gpu", "zone": "a"}
	m := &Mutator{AgentPlacement: p}
	sel, aff := m.onAgentNodes(map[string]string{"zone": "b", "type": "h100"})
	if len(sel) != 3 || sel["pool"] != "gpu" || sel["zone"] != "b" || sel["type"] != "h100" {
		t.Errorf("selector = %v, want the agent's keys with the pod's taking precedence", sel)
	}
	if aff == nil || aff.NodeAffinity == p.NodeAffinity || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Errorf("affinity must be a copy of the agent's: %+v", aff)
	}
	if sel, aff := (&Mutator{}).onAgentNodes(nil); sel != nil || aff != nil {
		t.Errorf("no placement, nothing added: %v %v", sel, aff)
	}
}
