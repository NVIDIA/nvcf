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

package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// CatalogInfo is the rich identity collected at capture time. Same
// data the agent has on hand from pod + node lookups; we surface it
// to nvsnap-server (and onward to the catalog row + API + UI) so
// operators can browse "all vLLM Llama-3.1 checkpoints on H100s" or
// filter restore-compat by driver version.
//
// All fields are best-effort: if a lookup fails or a label is
// absent, the field stays empty and the catalog row just shows
// less. The Hash field is the only one with hard semantics — it's
// the content-addressed identity used as the nvsnap.io/restore-from
// annotation value.
type CatalogInfo struct {
	// Content identity (the restore-from key).
	Hash      string `json:"hash,omitempty"`
	ShortHash string `json:"shortHash,omitempty"`

	// Source identity.
	FunctionName      string `json:"functionName,omitempty"`
	FunctionVersionID string `json:"functionVersionID,omitempty"`

	// Image identity.
	ImageRef    string `json:"imageRef,omitempty"`
	ImageDigest string `json:"imageDigest,omitempty"`

	// Workload identity (best-effort).
	Engine      string   `json:"engine,omitempty"`
	ModelID     string   `json:"modelID,omitempty"`
	EngineFlags []string `json:"engineFlags,omitempty"`

	// Hardware identity (restore-compatibility).
	GPUType              string `json:"gpuType,omitempty"`
	GPUCount             int    `json:"gpuCount,omitempty"`
	GPUComputeCapability string `json:"gpuComputeCapability,omitempty"`
	DriverVersion        string `json:"driverVersion,omitempty"`
	CUDAVersion          string `json:"cudaVersion,omitempty"`
	CPUArchitecture      string `json:"cpuArchitecture,omitempty"`

	// Cluster identity.
	ClusterName    string `json:"clusterName,omitempty"`
	CapturedOnNode string `json:"capturedOnNode,omitempty"`
}

// CollectCatalogInfo gathers the rich identity fields from the live
// pod + node + container spec at capture time, computes the canonical
// content hash, and returns a populated CatalogInfo.
//
// Failures on individual lookups are non-fatal — fields stay empty
// and the catalog row degrades gracefully. The only "hard" output is
// Hash, which we always populate (even if from a partially-empty
// HashInput, so two pods with the same partial spec still dedup to
// the same hash).
//
// The agent calls this exactly once per capture, after the dump
// completes (so we have the right containerImage + pid + nodeName)
// and before writing CheckpointResult.
func (a *Agent) CollectCatalogInfo(ctx context.Context, namespace, podName, containerName, nodeName, clusterName string) CatalogInfo {
	info := CatalogInfo{
		CapturedOnNode: nodeName,
		ClusterName:    clusterName,
	}

	if a.kubeClient == nil {
		info.Hash = computeHash(info)
		info.ShortHash = checkpointstore.ShortHash(info.Hash)
		return info
	}

	pod, err := a.kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err == nil {
		populateFromPod(&info, pod, containerName)
	}

	if nodeName != "" {
		node, err := a.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err == nil {
			populateFromNode(&info, node)
		}
	}

	info.Hash = computeHash(info)
	info.ShortHash = checkpointstore.ShortHash(info.Hash)
	return info
}

// populateFromPod pulls source / image / engine / workload identity
// from the pod's spec, status, labels, and annotations. The inference
// container is selected by name (defaults to "inference" per NVCA
// convention); workload-identity fields come from its env + args.
func populateFromPod(info *CatalogInfo, pod *corev1.Pod, containerName string) {
	info.FunctionName = pod.Annotations["function-name"]
	if info.FunctionName == "" {
		info.FunctionName = pod.Annotations["FUNCTION_NAME"]
	}
	info.FunctionVersionID = pod.Labels["function-version-id"]
	info.Engine = pod.Labels["nvsnap.io/source-engine"]

	// Find the target container (default "inference").
	target := containerName
	if target == "" {
		target = "inference"
	}
	var spec *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == target {
			spec = &pod.Spec.Containers[i]
			break
		}
	}
	if spec != nil {
		info.ImageRef = spec.Image
		info.ModelID = extractModelID(spec)
		info.EngineFlags = canonicalArgs(spec.Args)
		info.GPUCount = gpuRequestCount(spec)
	}

	// imageDigest comes from container status (kubelet resolves :tag → digest).
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name == target {
			info.ImageDigest = digestFromImageID(cs.ImageID)
			break
		}
	}
}

// populateFromNode pulls hardware identity from node labels stamped
// by the GPU operator + GKE.
func populateFromNode(info *CatalogInfo, node *corev1.Node) {
	if v, ok := node.Labels["nvidia.com/gpu.product"]; ok {
		info.GPUType = v
	} else if v, ok := node.Labels["cloud.google.com/gke-accelerator"]; ok {
		info.GPUType = v
	}
	if v, ok := node.Labels["nvidia.com/cuda.driver-version.full"]; ok {
		info.DriverVersion = v
	} else if v, ok := node.Labels["nvidia.com/cuda.driver.major"]; ok {
		// Compose from the major/minor/rev labels the GPU operator stamps.
		info.DriverVersion = v
		if minor, ok := node.Labels["nvidia.com/cuda.driver.minor"]; ok {
			info.DriverVersion += "." + minor
		}
		if rev, ok := node.Labels["nvidia.com/cuda.driver.rev"]; ok {
			info.DriverVersion += "." + rev
		}
	}
	if v, ok := node.Labels["nvidia.com/cuda.runtime-version.full"]; ok {
		info.CUDAVersion = v
	}
	// Compute capability "<major>.<minor>" (e.g. "9.0" for H100). Part of
	// the content hash — arch-compiled state must not be reused across it.
	if major, ok := node.Labels["nvidia.com/gpu.compute.major"]; ok {
		info.GPUComputeCapability = major
		if minor, ok := node.Labels["nvidia.com/gpu.compute.minor"]; ok {
			info.GPUComputeCapability += "." + minor
		}
	}
	info.CPUArchitecture = node.Labels["kubernetes.io/arch"]
}

// extractModelID inspects the container's env vars + args for the
// well-known model-identifier keys used by NIM, vLLM, SGLang, TRT-LLM.
// Returns empty when none match — the catalog row just doesn't display
// a model in that case.
func extractModelID(c *corev1.Container) string {
	envKeys := []string{
		"NIM_MODEL_NAME",
		"MODEL_NAME",
		"MODEL_REPO",
		"HF_MODEL_ID",
		"HUGGINGFACE_MODEL",
	}
	for _, e := range c.Env {
		for _, key := range envKeys {
			if e.Name == key && e.Value != "" {
				return e.Value
			}
		}
	}
	// vLLM / SGLang style: --model <id> in args.
	for i, arg := range c.Args {
		if arg == "--model" || arg == "--model-path" {
			if i+1 < len(c.Args) {
				return c.Args[i+1]
			}
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
	}
	return ""
}

// canonicalArgs returns the container args with --model* stripped
// (recorded separately as ModelID) and sorted. The sort makes the
// list order-stable across pods that pass flags in different orders,
// which is what ComputeHash expects for EngineCompatFlags.
func canonicalArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if a == "--model" || a == "--model-path" {
			skip = true
			continue
		}
		if strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "--model-path=") {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// gpuRequestCount sums the `nvidia.com/gpu` resource requests on the
// container. Falls back to limits if requests is unset.
func gpuRequestCount(c *corev1.Container) int {
	for _, src := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
		if n, ok := src["nvidia.com/gpu"]; ok {
			if v, err := strconv.Atoi(n.String()); err == nil {
				return v
			}
		}
	}
	return 0
}

// digestFromImageID strips kubelet's "docker-pullable://" prefix that
// sometimes wraps the digest form. Returns the bare "sha256:..." or
// the original string if no prefix is present.
func digestFromImageID(imageID string) string {
	const prefix = "docker-pullable://"
	imageID = strings.TrimPrefix(imageID, prefix)
	if i := strings.Index(imageID, "@"); i >= 0 {
		return imageID[i+1:]
	}
	return imageID
}

// computeHash routes the collected fields into the canonical HashInput
// and returns the sha256 hex digest. Driver-major is parsed best-effort
// from DriverVersion; failures default to 0 (so two pods with no
// driver info still dedup together).
func computeHash(info CatalogInfo) string {
	major := 0
	if info.DriverVersion != "" {
		// Take the first dot-separated segment.
		parts := strings.SplitN(info.DriverVersion, ".", 2)
		if v, err := strconv.Atoi(parts[0]); err == nil {
			major = v
		}
	}
	return checkpointstore.ComputeHash(checkpointstore.HashInput{
		ImageDigest:          info.ImageDigest,
		ModelID:              info.ModelID,
		EngineCompatFlags:    info.EngineFlags,
		CUDADriverMajor:      major,
		GPUComputeCapability: info.GPUComputeCapability,
		CaptureFormatVersion: catalogFormatVersion,
	})
}

// catalogFormatVersion is bumped when this file's collection /
// hashing logic changes in a way that would shift the hash for an
// otherwise-identical pod. The version is folded into the HashInput
// so cross-version checkpoints don't collide.
const catalogFormatVersion = 1

// buildCheckpointID derives the catalog-stable on-disk + API id for
// a captured checkpoint. Format: <shortHash>__<timestamp>.
//
// The shortHash makes co-located captures of different functions
// sort distinctly and gives the operator a hint of content identity
// just from the id. The timestamp suffix keeps repeat-captures of
// the same canonical content addressable (CRIU's later mtime gives
// the fresher artifact) and avoids collisions when the same
// workload is re-captured.
//
// nvsnap#58 replaces the legacy "<podName>__<namespace>__<timestamp>"
// format, which leaked ephemeral pod identity into the catalog and
// made content-addressed lookup impossible. PodName / Namespace
// remain as separate filterable fields in metadata.json.
func buildCheckpointID(catalog CatalogInfo, t time.Time) string {
	return fmt.Sprintf("%s__%s", catalog.ShortHash, t.Format("20060102-150405"))
}
