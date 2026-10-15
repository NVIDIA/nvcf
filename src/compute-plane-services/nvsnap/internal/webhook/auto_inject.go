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

package webhook

import (
	corev1 "k8s.io/api/core/v1"
)

// AutoInjectAnnotation opts a pod into auto-injection of the NvSnap
// sitecustomize plumbing — emptyDir volume, one init container
// running nvsnap-agent's auto-inject-init.sh (which lays down the
// uvloop wheels + patched libuv/libzmq + libnvsnap_intercept.so +
// sitecustomize.py), and three env vars on the main container
// (PYTHONPATH, LD_LIBRARY_PATH, LD_PRELOAD).
//
// Set "nvsnap.io/auto-inject": "true" on a customer pod and the
// webhook stamps everything in. Lets BYOC pods (anyone's inference
// pod, NVCA tenants, third-party customers) participate in NvSnap
// checkpoint/restore without touching their pod yaml.
//
// Idempotent: if the pod already has a "nvsnap-lib" volume, the
// webhook assumes the operator wired things manually and skips
// auto-injection.
const AutoInjectAnnotation = "nvsnap.io/auto-inject"

// AutoInjectImages was the four-image config used when the webhook
// fanned out across four init containers. The unified
// auto-inject-init.sh in the nvsnap-agent image now does everything in
// one container — only Agent is consulted.
//
// Kept as a struct (rather than a single string) so future additions
// (e.g., a debug-tools image override) can land without breaking the
// flag surface again. Existing flag plumbing on the agent still
// fills Uvloop/LibUV/LibZMQ but they're ignored.
type AutoInjectImages struct {
	// Agent is the nvsnap-agent image ref the webhook injects as the
	// single init container. Must match the agent runtime image so
	// the libnvsnap_intercept.so build-ID lines up at restore time.
	Agent string

	// Deprecated: kept for backward-compat with existing flag plumbing.
	// The unified init container in nvsnap-agent already carries these
	// payloads, so these fields are not read.
	Uvloop string
	LibUV  string
	LibZMQ string
}

// Valid reports whether the Agent image is set. The other fields are
// deprecated and ignored — only Agent matters. Webhook fails open
// (skips auto-injection) when Agent is empty.
func (i AutoInjectImages) Valid() bool {
	return i.Agent != ""
}

// autoInjectPatches returns the JSON Patch ops needed to inject the
// NvSnap sitecustomize plumbing into pod. Returns nil if auto-inject
// is not requested, already done, or the agent image is unconfigured.
func (m *Mutator) autoInjectPatches(pod *corev1.Pod) []PatchOp {
	if pod.Annotations[AutoInjectAnnotation] != "true" {
		return nil
	}
	if !m.AutoInject.Valid() {
		m.logger().Warn("auto-inject requested but Agent image unset; admitting pod unchanged")
		return nil
	}
	// If the pod already has a nvsnap-lib volume, the operator wired
	// it manually. Don't double-inject.
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == nvsnapLibVolumeName {
			return nil
		}
	}
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		m.logger().Warn("auto-inject: MainContainer index out of range; admitting pod unchanged")
		return nil
	}

	var patches []PatchOp

	// 1. emptyDir volume.
	nvsnapLibVol := map[string]any{
		"name":     nvsnapLibVolumeName,
		"emptyDir": map[string]any{},
	}
	if len(pod.Spec.Volumes) == 0 {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{nvsnapLibVol}})
	} else {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: nvsnapLibVol})
	}

	// 2. One init container running nvsnap-agent's auto-inject-init.sh.
	// Replaces the previous four-init-container fan-out (get-uvloop,
	// get-libuv, get-libzmq, get-nvsnap) — same effect, fewer container
	// startups + one image pull on cold nodes.
	initContainer := autoInjectInitContainer(m.AutoInject.Agent)
	if len(pod.Spec.InitContainers) == 0 {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{initContainer}})
	} else {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: initContainer})
	}

	// 3. Mount nvsnap-lib on the main container.
	mount := map[string]any{
		"name":      nvsnapLibVolumeName,
		"mountPath": nvsnapLibMountPath,
	}
	mainPath := func(field string) string {
		return "/spec/containers/" + intToStr(m.MainContainer) + "/" + field
	}
	if len(pod.Spec.Containers[m.MainContainer].VolumeMounts) == 0 {
		patches = append(patches, PatchOp{Op: "add", Path: mainPath("volumeMounts"), Value: []any{mount}})
	} else {
		patches = append(patches, PatchOp{Op: "add", Path: mainPath("volumeMounts/-"), Value: mount})
	}

	// 4. PYTHONPATH / LD_LIBRARY_PATH / LD_PRELOAD on the main
	// container. We skip any env var the customer already set so an
	// explicit override (e.g. PATH-style LD_LIBRARY_PATH with extra
	// dirs) wins. Customer can always opt out per-var.
	existingEnv := map[string]bool{}
	for _, e := range pod.Spec.Containers[m.MainContainer].Env {
		existingEnv[e.Name] = true
	}
	envAdds := []map[string]any{
		{"name": "PYTHONPATH", "value": nvsnapLibMountPath + "/sitecustomize"},
		{"name": "LD_LIBRARY_PATH", "value": nvsnapLibMountPath + ":/usr/local/nvidia/lib64:/usr/local/cuda/lib64"},
		{"name": "LD_PRELOAD", "value": nvsnapLibMountPath + "/libnvsnap_intercept.so"},
	}
	for i, e := range envAdds {
		if existingEnv[e["name"].(string)] {
			continue
		}
		if len(pod.Spec.Containers[m.MainContainer].Env) == 0 && i == 0 {
			patches = append(patches, PatchOp{Op: "add", Path: mainPath("env"), Value: []any{e}})
		} else {
			patches = append(patches, PatchOp{Op: "add", Path: mainPath("env/-"), Value: e})
		}
	}

	return patches
}

// autoInjectInitContainer returns the single init container spec that
// the webhook stamps into pods carrying nvsnap.io/auto-inject. The
// container runs the agent image's bundled auto-inject-init.sh which
// lays the four payloads under /nvsnap-lib.
func autoInjectInitContainer(agentImage string) map[string]any {
	return map[string]any{
		"name":            "nvsnap-init",
		"image":           agentImage,
		"imagePullPolicy": "IfNotPresent",
		// Override the agent ENTRYPOINT — this image's default is to
		// start the nvsnap-agent process; here we just need the
		// payload-install script to run and exit.
		"command": []any{"/criu-bundle/auto-inject-init.sh"},
		"volumeMounts": []any{
			map[string]any{
				"name":      nvsnapLibVolumeName,
				"mountPath": nvsnapLibMountPath,
			},
		},
	}
}

const (
	nvsnapLibVolumeName = "nvsnap-lib"
	nvsnapLibMountPath  = "/nvsnap-lib"
)

// intToStr is a tiny strconv.Itoa alias so we don't pull strconv into
// every callsite for the one-time MainContainer index format.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	// Single-digit cases cover us — MainContainer is almost always 0,
	// rarely above 9. Format defensively for two-digit indices.
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
