// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// NVCA runs pods of its own next to every function it deploys: a utils pod
// that proxies the function's requests and an SMB server for its shared
// storage. They belong to the platform, not to the function's instance, so
// an instance's capture never takes them, whatever connections they hold.
// These are NVCA's own definitions of them (IsUtilsPod in
// nvca/internal/util/k8sutil and SMBServerPodName in nvca/pkg/storage).
const (
	nvcaUtilsPodName       = "utils"
	nvcaUtilsContainerName = "utils"
	nvcaLLMWorkerContainer = "llm-worker"
	nvcaInstanceIDEnv      = "INSTANCE_ID"
	nvcaFunctionNSPrefix   = "sr-"
	nvcaSMBServerPodName   = "nvcf-smb-server"
)

// nvcaPlatformPod reports a pod NVCA runs for a function rather than one of
// the function's own: its utils pod (by its name in a function namespace,
// or by its worker container, which alone carries the instance ID) or its
// SMB server.
func nvcaPlatformPod(p *corev1.Pod) bool {
	if p.Name == nvcaSMBServerPodName {
		return true
	}
	if p.Name == nvcaUtilsPodName && strings.HasPrefix(p.Namespace, nvcaFunctionNSPrefix) {
		return true
	}
	for _, c := range p.Spec.Containers {
		if c.Name != nvcaUtilsContainerName && c.Name != nvcaLLMWorkerContainer {
			continue
		}
		for _, e := range c.Env {
			if e.Name == nvcaInstanceIDEnv {
				return true
			}
		}
		return false
	}
	return false
}
