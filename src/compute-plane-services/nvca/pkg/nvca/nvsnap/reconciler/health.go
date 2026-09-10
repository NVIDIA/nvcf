/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package reconciler

import (
	corev1 "k8s.io/api/core/v1"
)

// podReady reports whether the pod has condition Ready=True.
//
// Hook B reconciler trusts this as the "workload is warm enough" signal.
// K8s computes pod.Ready by aggregating each container's readinessProbe;
// for NVCF function pods the utils sidecar runs a httpGet probe against
// the inference container's /health endpoint, so PodReady=true implies
// inference /health returned 200. Re-polling /health from the
// reconciler would duplicate that exact RTT against the exact same
// endpoint — and would fail when the inference container has no
// containerPort declared (NVCA's default, since INFERENCE_PORT is
// stamped only on the utils sidecar's env).
//
// Duplicated from controller.IsPodReady to avoid the cyclic
// controller→reconciler→controller import.
func podReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
