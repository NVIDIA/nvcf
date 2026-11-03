// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// NamesOwnInstance returns the env variable through which a pod's GPU
// container names its own namespace in a literal value, or "". Such a
// container carries the identity of its instance in its configuration
// (Dynamo's DYN_NAMESPACE, a Grove headless service name). A restored
// process keeps the environment it was started with: restored into a later
// instance, it would run as the instance it was captured from, which a
// registration made under that identity does not survive (NVIDIA/nvcf#2517).
// Such pods are not CRIU-captured or restored; the model volume and the
// compile caches still apply.
func NamesOwnInstance(pod *corev1.Pod) string {
	if pod.Namespace == "" {
		return ""
	}
	// As a whole name or a part of one (<namespace>-<name>, <name>.<namespace>).
	// A short namespace may match an unrelated word; the pod then only
	// starts from the caches.
	token := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(pod.Namespace) + `([^a-z0-9]|$)`)
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok {
			continue
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil && strings.Contains(e.Value, pod.Namespace) && token.MatchString(e.Value) {
				return e.Name
			}
		}
	}
	return ""
}
