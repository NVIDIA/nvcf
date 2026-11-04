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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	toolsVolumeName       = "tools"
	extractedVolumeName   = "extracted"
	mergedCertsVolumeName = "merged-certs"
	finalCertsPath        = "/etc/ssl/certs"
)

// mutateCertificates adds init containers and volumes for certificate management
// This matches the kyverno-cert-mount.yaml logic:
// 1. a-toolbox: copies busybox to /tools + copies NVCF certs to /merged-certs
// 2. b-extract-inference: uses inference image + /tools/busybox to extract certs
// 3. fast-merge-certs: merges extracted inference certs with NVCF certs
func mutateCertificates(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	// Check if pod already has our init containers (avoid double mutation)
	if hasInitContainer(pod, "a-toolbox") {
		klog.V(4).InfoS("Pod already has certificate init containers, skipping", "pod", pod.Name)
		return patches
	}

	// 1. Add volumes
	volumePatches := addCertVolumes(pod)
	patches = append(patches, volumePatches...)

	// 2. Add init containers
	initContainerPatches := addCertInitContainers(pod)
	patches = append(patches, initContainerPatches...)

	// 3. Mount certificates in all existing init containers
	initMountPatches := mountCertsInInitContainers(pod)
	patches = append(patches, initMountPatches...)

	// 4. Mount certificates in all containers
	mountPatches := mountCertsInContainers(pod)
	patches = append(patches, mountPatches...)

	return patches
}

// addCertVolumes adds the 3 volumes matching kyverno base-setup rule
func addCertVolumes(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	volumes := []corev1.Volume{
		{
			Name: toolsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resourceQuantityPtr("25Mi"),
				},
			},
		},
		{
			Name: extractedVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resourceQuantityPtr("25Mi"),
				},
			},
		},
		{
			Name: mergedCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resourceQuantityPtr("25Mi"),
				},
			},
		},
	}

	if len(pod.Spec.Volumes) == 0 {
		patches = append(patches, JSONPatch{
			Op:    "add",
			Path:  "/spec/volumes",
			Value: volumes,
		})
	} else {
		for _, vol := range volumes {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/volumes/%d", len(pod.Spec.Volumes)),
				Value: vol,
			})
		}
	}

	return patches
}

// addCertInitContainers adds init containers matching kyverno setup-all-init-containers rule
func addCertInitContainers(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	hasInference, inferenceImage := getInferenceContainer(pod)

	initContainers := []corev1.Container{}

	// 1. a-toolbox - copies busybox to /tools AND copies NVCF certs to /merged-certs
	//    This ensures we have a shell for the extraction step
	initContainers = append(initContainers, createToolboxContainer())

	// 2. b-extract-inference - Only if inference container exists
	//    Uses inference image + /tools/busybox to extract certs
	if hasInference {
		klog.V(4).InfoS("Found inference container, adding extraction init container",
			"pod", pod.Name, "image", inferenceImage)
		initContainers = append(initContainers, createExtractInferenceContainer(inferenceImage))
	}

	// 3. fast-merge-certs - merges extracted certs with NVCF certs
	initContainers = append(initContainers, createMergeCertsContainer())

	if len(pod.Spec.InitContainers) == 0 {
		patches = append(patches, JSONPatch{
			Op:    "add",
			Path:  "/spec/initContainers",
			Value: initContainers,
		})
	} else {
		// Insert at beginning (reverse order to maintain correct sequence)
		for i := len(initContainers) - 1; i >= 0; i-- {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  "/spec/initContainers/0",
				Value: initContainers[i],
			})
		}
	}

	return patches
}

// createToolboxContainer creates the setup init container
// Uses Go binary (certmerge) instead of bash for:
// - Copying busybox to /tools/busybox (for use by extraction container)
// - Copying NVCF certificates to /merged-certs
func createToolboxContainer() corev1.Container {
	return corev1.Container{
		Name:            "a-toolbox",
		Image:           certificatesImage,
		Command:         []string{"/certmerge"},
		Args:            []string{"setup", "--dest=/merged-certs", "--tools=/tools"},
		SecurityContext: securityContext(),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      toolsVolumeName,
				MountPath: "/tools",
			},
			{
				Name:      mergedCertsVolumeName,
				MountPath: "/merged-certs",
			},
		},
	}
}

// createExtractInferenceContainer matches kyverno b-extract-inference-certificates rule
// Uses the INFERENCE CONTAINER'S IMAGE (to access its filesystem)
// Uses /tools/busybox (copied by a-toolbox) since inference image may not have shell
func createExtractInferenceContainer(inferenceImage string) corev1.Container {
	return corev1.Container{
		Name:    "b-extract-inference",
		Image:   inferenceImage, // Use inference image to access its certs
		Command: []string{"/tools/busybox", "sh", "-c"},
		Args: []string{
			`BB=/tools/busybox
DEST="/extracted/inference"
echo "[B-EXTRACT-INFERENCE] Starting certificate extraction from inference container..."
$BB mkdir -p "$DEST"

FOUND_CERTS=0
for d in /etc/ssl/certs /etc/pki /etc/certs /app/certs /opt/certs; do
  if [ -d "$d" ]; then
    echo "[B-EXTRACT-INFERENCE] Checking directory: $d"
    COUNT=$($BB find "$d" -maxdepth 2 \( -name "*.crt" -o -name "*.pem" -o -name "*.key" -o -name "*.0" \) 2>/dev/null | $BB wc -l || echo 0)
    if [ "$COUNT" -gt 0 ]; then
      echo "[B-EXTRACT-INFERENCE] Found $COUNT certificate files in $d"
      $BB find "$d" -maxdepth 2 \( -name "*.crt" -o -name "*.pem" -o -name "*.key" -o -name "*.0" \) -exec $BB cp -v {} "$DEST"/ \; 2>/dev/null && FOUND_CERTS=$((FOUND_CERTS + COUNT)) || true
    fi
  else
    echo "[B-EXTRACT-INFERENCE] Directory $d does not exist"
  fi
done

echo "[B-EXTRACT-INFERENCE] Total certificates found: $FOUND_CERTS"
$BB ls -la "$DEST" 2>/dev/null || echo "[B-EXTRACT-INFERENCE] No files in destination"
exit 0`,
		},
		SecurityContext: securityContext(),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      toolsVolumeName,
				MountPath: "/tools",
			},
			{
				Name:      extractedVolumeName,
				MountPath: "/extracted",
			},
		},
	}
}

// createMergeCertsContainer creates the certificate merge init container
// Uses Go binary (certmerge) instead of bash for merging
// extracted inference certificates with NVCF certificates
func createMergeCertsContainer() corev1.Container {
	return corev1.Container{
		Name:            "fast-merge-certs",
		Image:           certificatesImage,
		Command:         []string{"/certmerge"},
		Args:            []string{"merge", "--dest=/merged-certs", "--extracted=/extracted/inference"},
		SecurityContext: securityContext(),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      mergedCertsVolumeName,
				MountPath: "/merged-certs",
			},
			{
				Name:      extractedVolumeName,
				MountPath: "/extracted",
			},
		},
	}
}

// securityContext returns the standard security context for init containers
func securityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// mountCertsInInitContainers mounts certs to existing init containers whose
// name matches *init* or *download* (mirrors the Kyverno chart's anchor).
func mountCertsInInitContainers(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	// We need to account for the init containers we're adding
	numAddedInitContainers := 2 // a-toolbox + fast-merge-certs
	if hasInference, _ := getInferenceContainer(pod); hasInference {
		numAddedInitContainers = 3 // + b-extract-inference
	}

	for i, container := range pod.Spec.InitContainers {
		// Skip our own init containers
		if container.Name == "a-toolbox" || container.Name == "b-extract-inference" || container.Name == "fast-merge-certs" {
			continue
		}

		// Only consumer init containers get the merged bundle: match *init* or
		// *download* substrings, matching the Kyverno chart's anchor pattern.
		lname := strings.ToLower(container.Name)
		if !strings.Contains(lname, "init") && !strings.Contains(lname, "download") {
			continue
		}

		actualIndex := i + numAddedInitContainers

		mount := corev1.VolumeMount{
			Name:      mergedCertsVolumeName,
			MountPath: finalCertsPath,
			ReadOnly:  true,
		}

		if len(container.VolumeMounts) == 0 {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/initContainers/%d/volumeMounts", actualIndex),
				Value: []corev1.VolumeMount{mount},
			})
		} else {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/initContainers/%d/volumeMounts/-", actualIndex),
				Value: mount,
			})
		}

		envPatches := addCertEnvVars(fmt.Sprintf("/spec/initContainers/%d", actualIndex), &container)
		patches = append(patches, envPatches...)
	}

	return patches
}

// mountCertsInContainers mounts certs to all containers (matches mount-bundle rule)
func mountCertsInContainers(pod *corev1.Pod) []JSONPatch {
	patches := []JSONPatch{}

	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]

		mount := corev1.VolumeMount{
			Name:      mergedCertsVolumeName,
			MountPath: finalCertsPath,
			ReadOnly:  true,
		}

		if len(container.VolumeMounts) == 0 {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", i),
				Value: []corev1.VolumeMount{mount},
			})
		} else {
			patches = append(patches, JSONPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", i),
				Value: mount,
			})
		}

		envPatches := addCertEnvVars(fmt.Sprintf("/spec/containers/%d", i), container)
		patches = append(patches, envPatches...)
	}

	return patches
}

// addCertEnvVars adds certificate and NIM SDK env vars (matches kyverno exactly)
func addCertEnvVars(basePath string, container *corev1.Container) []JSONPatch {
	patches := []JSONPatch{}

	envVars := []corev1.EnvVar{
		{
			Name:  "REQUESTS_CA_BUNDLE",
			Value: fmt.Sprintf("%s/ca-certificates.crt", finalCertsPath),
		},
		{
			Name:  "SSL_CERT_FILE",
			Value: fmt.Sprintf("%s/ca-certificates.crt", finalCertsPath),
		},
		{
			Name:  "AWS_CA_BUNDLE",
			Value: fmt.Sprintf("%s/ca-certificates.crt", finalCertsPath),
		},
		{
			Name:  "NIM_SDK_USE_NATIVE_TLS",
			Value: "1",
		},
		{
			Name:  "NIM_SDK_DOWNLOAD_MAX_RETRY_COUNT",
			Value: "8",
		},
		{
			Name:  "NIM_SDK_DOWNLOAD_BACKOFF_INTERVAL_MS",
			Value: "500",
		},
	}

	if len(container.Env) == 0 {
		patches = append(patches, JSONPatch{
			Op:    "add",
			Path:  fmt.Sprintf("%s/env", basePath),
			Value: envVars,
		})
	} else {
		for _, env := range envVars {
			if !hasEnvVar(container, env.Name) {
				patches = append(patches, JSONPatch{
					Op:    "add",
					Path:  fmt.Sprintf("%s/env/-", basePath),
					Value: env,
				})
			}
		}
	}

	return patches
}

// Helper functions

func getInferenceContainer(pod *corev1.Pod) (bool, string) {
	for _, c := range pod.Spec.Containers {
		if c.Name == "inference" {
			return true, c.Image
		}
	}
	return false, ""
}

func hasInitContainer(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name || strings.HasPrefix(c.Name, name) {
			return true
		}
	}
	return false
}

func hasEnvVar(container *corev1.Container, name string) bool {
	for _, env := range container.Env {
		if env.Name == name {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool {
	return &b
}

func resourceQuantityPtr(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}
