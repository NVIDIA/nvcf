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

// mount_prep_init.go — emits the nvsnap-mount-prep init container patch.
// See docs/proposals/init-container-mount-prep.md.

package webhook

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// AgentTokenSecretName / AgentTokenSecretKey locate the shared agent API
// bearer token (GH #486). The chart creates this Secret only when auth is
// enabled, so every reference to it is marked optional -- a pod admitted
// before the operator turns auth on must still start.
const (
	AgentTokenSecretName = "nvsnap-agent-token"
	AgentTokenSecretKey  = "token"
)

const (
	// MountPrepContainerName is the canonical name of the injected
	// init container; surfaces in `kubectl describe pod` and logs.
	MountPrepContainerName = "nvsnap-mount-prep"

	// MountPrepDefaultAgentPort is the default agent HTTP port
	// (matches the agent's --listen=:8081 default).
	MountPrepDefaultAgentPort = 8081

	// MountPrepDeadline caps polling. Init container exits 2 if
	// the agent doesn't report ready by then. 15 min covers the
	// largest captures we've benchmarked with headroom.
	MountPrepDeadline = "15m"
)

// emitMountPrepInitContainer appends the nvsnap-mount-prep init
// container patches to *patches. Caller has already collected
// initMounts (the VolumeMeta list the init container should POST to
// /v1/restore/prep) during the section-1/2 loops, and has already
// emitted the hostPath volumes referencing the deterministic merged
// mountpoints those mounts will eventually expose.
//
// needInitArray / bootstrappedInit mirror the same bootstrap pattern
// used elsewhere in Mutate (emitMount): when pod.Spec.InitContainers
// is nil we have to emit /spec/initContainers as an empty array
// BEFORE we can /spec/initContainers/- into it. *bootstrappedInit
// tracks whether the bootstrap has been emitted already by a
// previous emitter (e.g. the L2-wait init container or a hostPath
// staging container).
func (m *Mutator) emitMountPrepInitContainer(
	patches *[]PatchOp,
	bootstrappedInit *bool,
	needInitArray bool,
	podUID, captureHash, captureNode string,
	initMounts []checkpointstore.VolumeMeta,
) error {
	if m.MountPrepInitImage == "" {
		return fmt.Errorf("MountPrepInitImage is empty; init-container strategy requires an image ref")
	}

	mountsJSON, err := json.Marshal(initMounts)
	if err != nil {
		return fmt.Errorf("encode initMounts JSON: %w", err)
	}

	agentPort := m.AgentHostPort
	if agentPort == 0 {
		agentPort = MountPrepDefaultAgentPort
	}
	// Both forms reach the agent on the pod's OWN node; cross-node peer
	// routing is the agent's job, driven by captureNode in the POST body.
	//
	// Default is the downward-API host IP, which depends on the agent
	// binding hostPort. AgentBaseURL replaces it with the
	// internalTrafficPolicy:Local Service under pod networking, which routes
	// to the node-local endpoint with no node-wide listener (GH #490).
	// Addressable true for the Secret's Optional field. Inlined rather than
	// pulling in k8s.io/utils/ptr for a single call: bazel enforces strict
	// deps, so one import here means a new external dependency in the build
	// graph for something the language expresses in a line.
	secretOptional := true

	agentURL := fmt.Sprintf("http://$(NVSNAP_HOST_IP):%d", agentPort)
	if m.AgentBaseURL != "" {
		agentURL = strings.TrimRight(m.AgentBaseURL, "/")
	}

	c := corev1.Container{
		Name:            MountPrepContainerName,
		Image:           m.MountPrepInitImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/nvsnap-mount-prep"},
		Env: []corev1.EnvVar{
			{
				Name: "NVSNAP_HOST_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
				},
			},
			{Name: "NVSNAP_POD_UID", Value: podUID},
			{Name: "NVSNAP_RESTORE_HASH", Value: captureHash},
			{Name: "NVSNAP_AGENT_URL", Value: agentURL},
			{Name: "NVSNAP_CAPTURE_NODE", Value: captureNode},
			{Name: "NVSNAP_PREP_MOUNTS", Value: string(mountsJSON)},
			{Name: "NVSNAP_PREP_DEADLINE", Value: MountPrepDeadline},
			// Optional: the Secret only exists once the operator enables
			// auth, so the reference is marked optional and the init
			// container simply sends no header until then (GH #486).
			{Name: "NVSNAP_AGENT_TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: AgentTokenSecretName},
					Key:                  AgentTokenSecretKey,
					Optional:             &secretOptional,
				},
			}},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}

	if needInitArray && !*bootstrappedInit {
		*patches = append(*patches, PatchOp{
			Op: "add", Path: "/spec/initContainers", Value: []any{},
		})
		*bootstrappedInit = true
	}
	*patches = append(*patches, PatchOp{
		Op: "add", Path: "/spec/initContainers/-", Value: c,
	})
	return nil
}
