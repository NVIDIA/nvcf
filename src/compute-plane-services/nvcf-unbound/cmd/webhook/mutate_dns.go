// SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// cacheOptOutEnvName is the environment variable a function sets to opt out
	// of the proxy cache. When present with cacheOptOutEnvValue, the DNS redirect
	// is not injected, so the pod uses normal cluster DNS and its traffic never
	// reaches the cache. Must be a literal env value; valueFrom is not resolvable
	// at admission. Kept in sync with the Kyverno add-unbound-dns precondition.
	cacheOptOutEnvName  = "NVCF_DISABLE_PROXY_CACHE"
	cacheOptOutEnvValue = "true"
)

// cacheOptedOut reports whether any container or init container requests the
// per-function proxy-cache opt-out.
func cacheOptedOut(pod *corev1.Pod) bool {
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for i := range containers {
		for _, e := range containers[i].Env {
			// Opt out if ANY container sets it to the canonical value. Keep
			// scanning on a non-matching value so an earlier container with a
			// different value does not mask a later container's opt-out.
			if e.Name == cacheOptOutEnvName && e.Value == cacheOptOutEnvValue {
				return true
			}
		}
	}
	return false
}

// mutateDNS overrides DNS configuration for all containers
func mutateDNS(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	// Check if DNS is already configured
	if pod.Spec.DNSPolicy == corev1.DNSNone && pod.Spec.DNSConfig != nil {
		klog.V(4).InfoS("Pod already has custom DNS configuration, skipping", "pod", pod.Name)
		return patches
	}

	// Honor the per-function cache opt-out: leave DNS untouched so the pod uses
	// normal cluster DNS and bypasses the proxy cache entirely.
	if cacheOptedOut(pod) {
		klog.V(4).InfoS("Pod opted out of proxy cache, skipping DNS mutation", "pod", pod.Name)
		return patches
	}

	// Set DNSPolicy to None
	patches = append(patches, JSONPatch{
		Op:    "replace",
		Path:  "/spec/dnsPolicy",
		Value: corev1.DNSNone,
	})

	// Add DNSConfig
	dnsConfig := &corev1.PodDNSConfig{
		Nameservers: []string{
			unboundIP,      // Unbound DNS
			stubNameserver, // CoreDNS fallback
		},
		Searches: []string{
			fmt.Sprintf("%s.svc.cluster.local", pod.Namespace),
		},
		Options: []corev1.PodDNSConfigOption{
			{
				Name:  "ndots",
				Value: stringPtr("5"),
			},
			{
				Name:  "timeout",
				Value: stringPtr("1"),
			},
		},
	}

	patches = append(patches, JSONPatch{
		Op:    "add",
		Path:  "/spec/dnsConfig",
		Value: dnsConfig,
	})

	klog.V(4).InfoS("Added DNS configuration",
		"pod", pod.Name,
		"nameservers", dnsConfig.Nameservers,
	)

	return patches
}

func stringPtr(s string) *string {
	return &s
}
