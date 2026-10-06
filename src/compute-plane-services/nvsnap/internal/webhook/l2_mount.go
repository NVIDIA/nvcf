/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// L2 per-capture PVC mount path for the mutating webhook (nvsnap#63).
//
// When the nvsnap-server-driven L2 promote has succeeded for a hash
// (rox-<short-hash> PVC is Bound), the webhook injects:
//
//   - a Volume on pod.spec.volumes referencing the PVC (readOnly)
//   - a VolumeMount on the main container at L2MountPath (readOnly)
//   - CHECKPOINT_PATH env var pointing at L2MountPath
//
// And explicitly does NOT inject a nodeAffinity constraint — the PVC
// is RWX so the K8s scheduler can place the pod on any node.
//
// The L1 hostPath + nodeAffinity path stays as the fallback (called
// from Mutate when this returns ErrNotFound).
//
// Design: docs/L2-PVC-CRIU-DESIGN.md §"Restore-side resolver".

package webhook

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// buildL2WaitContainer assembles the nvsnap-l2-wait init container.
// Resources are sized for the binary's actual runtime profile (Go
// HTTP client + small JSON decoder; sub-50 MB RSS in steady state).
// Limits are 4x requests to avoid OOM on slow GC under high load.
// The webhook factory keeps this in one place so test assertions
// can pin the exact shape without scattering literals.
func buildL2WaitContainer(image, serverURL, hash, timeout string) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "NVSNAP_SERVER_URL", Value: serverURL},
		{Name: "NVSNAP_CHECKPOINT_HASH", Value: hash},
	}
	if timeout != "" {
		env = append(env, corev1.EnvVar{Name: "NVSNAP_WAIT_TIMEOUT", Value: timeout})
	}
	return corev1.Container{
		Name:  "nvsnap-l2-wait",
		Image: image,
		Env:   env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
}
