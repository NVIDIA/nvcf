// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// DebugEnv adds environment variables to the GPU containers of the pods
// whose Label has one of Values: debug switches for a workload whose spec
// nvsnap does not own (an NVCF function's pods are built by NVCA). A
// variable the container already sets is left as it is.
type DebugEnv struct {
	Label  string            `json:"label"`
	Values []string          `json:"values"`
	Env    map[string]string `json:"env"`
}

func (d DebugEnv) matches(pod *corev1.Pod) bool {
	v, ok := pod.Labels[d.Label]
	if !ok {
		return false
	}
	for _, want := range d.Values {
		if v == want {
			return true
		}
	}
	return false
}

// debugEnvPatches sets the matching DebugEnv variables on the pod's GPU
// containers.
func (m *Mutator) debugEnvPatches(pod *corev1.Pod) []PatchOp {
	vars := map[string]string{}
	for _, d := range m.DebugEnv {
		if d.matches(pod) {
			for k, v := range d.Env {
				vars[k] = v
			}
		}
	}
	if len(vars) == 0 {
		return nil
	}
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var patches []PatchOp
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok {
			continue
		}
		var add []corev1.EnvVar
		for _, n := range names {
			if !hasEnv(c, n) {
				add = append(add, corev1.EnvVar{Name: n, Value: vars[n]})
			}
		}
		patches = append(patches, setContainerEnv(i, c, add)...)
	}
	if len(patches) > 0 {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("vars", names).Info("debug env: set on the GPU containers")
	}
	return patches
}
