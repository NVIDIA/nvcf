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
	"encoding/json"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	// Set test configuration
	unboundIP = "10.96.0.100"
	stubNameserver = "10.96.0.10"
	certificatesImage = "test-registry/nvcf-certs:latest"
}

// ============================================================================
// DNS Mutation Tests
// ============================================================================

func TestMutateDNS_BasicPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateDNS(pod)

	if len(patches) != 2 {
		t.Errorf("Expected 2 patches, got %d", len(patches))
	}

	// Verify DNS policy patch
	foundPolicyPatch := false
	foundConfigPatch := false

	for _, patch := range patches {
		if patch.Path == "/spec/dnsPolicy" {
			foundPolicyPatch = true
			if patch.Op != "replace" {
				t.Errorf("Expected 'replace' op for dnsPolicy, got %s", patch.Op)
			}
			if patch.Value != corev1.DNSNone {
				t.Errorf("Expected DNSNone, got %v", patch.Value)
			}
		}
		if patch.Path == "/spec/dnsConfig" {
			foundConfigPatch = true
			if patch.Op != "add" {
				t.Errorf("Expected 'add' op for dnsConfig, got %s", patch.Op)
			}
		}
	}

	if !foundPolicyPatch {
		t.Error("DNS policy patch not found")
	}
	if !foundConfigPatch {
		t.Error("DNS config patch not found")
	}
}

func TestMutateDNS_VerifyConfig(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "my-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateDNS(pod)

	// Find the dnsConfig patch
	var dnsConfig *corev1.PodDNSConfig
	for _, patch := range patches {
		if patch.Path == "/spec/dnsConfig" {
			dnsConfig = patch.Value.(*corev1.PodDNSConfig)
			break
		}
	}

	if dnsConfig == nil {
		t.Fatal("DNS config patch not found")
	}

	// Verify nameservers
	if len(dnsConfig.Nameservers) != 2 {
		t.Errorf("Expected 2 nameservers, got %d", len(dnsConfig.Nameservers))
	}
	if dnsConfig.Nameservers[0] != unboundIP {
		t.Errorf("Expected first nameserver to be %s, got %s", unboundIP, dnsConfig.Nameservers[0])
	}
	if dnsConfig.Nameservers[1] != stubNameserver {
		t.Errorf("Expected second nameserver to be %s, got %s", stubNameserver, dnsConfig.Nameservers[1])
	}

	// Verify searches - should be only 1 (matches kyverno)
	if len(dnsConfig.Searches) != 1 {
		t.Errorf("Expected 1 search path, got %d", len(dnsConfig.Searches))
	}
	expectedSearch := "my-namespace.svc.cluster.local"
	if dnsConfig.Searches[0] != expectedSearch {
		t.Errorf("Expected search %s, got %s", expectedSearch, dnsConfig.Searches[0])
	}

	// Verify options
	if len(dnsConfig.Options) != 2 {
		t.Errorf("Expected 2 options, got %d", len(dnsConfig.Options))
	}

	foundNdots := false
	foundTimeout := false
	for _, opt := range dnsConfig.Options {
		if opt.Name == "ndots" && opt.Value != nil && *opt.Value == "5" {
			foundNdots = true
		}
		if opt.Name == "timeout" && opt.Value != nil && *opt.Value == "1" {
			foundTimeout = true
		}
	}

	if !foundNdots {
		t.Error("ndots option not found or incorrect")
	}
	if !foundTimeout {
		t.Error("timeout option not found or incorrect")
	}
}

func TestMutateDNS_SkipIfAlreadyConfigured(t *testing.T) {
	ndots := "5"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			DNSPolicy: corev1.DNSNone,
			DNSConfig: &corev1.PodDNSConfig{
				Nameservers: []string{"8.8.8.8"},
				Options: []corev1.PodDNSConfigOption{
					{Name: "ndots", Value: &ndots},
				},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateDNS(pod)

	if len(patches) != 0 {
		t.Errorf("Expected 0 patches for already configured pod, got %d", len(patches))
	}
}

// ============================================================================
// Certificate Mutation Tests
// ============================================================================

func TestMutateCertificates_BasicPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	if len(patches) == 0 {
		t.Error("Expected patches for certificate mutation")
	}

	// Verify patches include volumes, init containers, and container mounts
	hasVolumes := false
	hasInitContainers := false
	hasContainerMounts := false
	hasEnvVars := false

	for _, patch := range patches {
		if patch.Path == "/spec/volumes" {
			hasVolumes = true
		}
		if patch.Path == "/spec/initContainers" {
			hasInitContainers = true
		}
		if patch.Path == "/spec/containers/0/volumeMounts" || patch.Path == "/spec/containers/0/volumeMounts/-" {
			hasContainerMounts = true
		}
		if patch.Path == "/spec/containers/0/env" || patch.Path == "/spec/containers/0/env/-" {
			hasEnvVars = true
		}
	}

	if !hasVolumes {
		t.Error("Volume patches not found")
	}
	if !hasInitContainers {
		t.Error("Init container patches not found")
	}
	if !hasContainerMounts {
		t.Error("Container mount patches not found")
	}
	if !hasEnvVars {
		t.Error("Environment variable patches not found")
	}
}

func TestMutateCertificates_VerifyVolumes(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find volumes patch
	var volumes []corev1.Volume
	for _, patch := range patches {
		if patch.Path == "/spec/volumes" {
			volumeBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(volumeBytes, &volumes)
			break
		}
	}

	if len(volumes) != 3 {
		t.Errorf("Expected 3 volumes, got %d", len(volumes))
	}

	expectedVolumes := map[string]bool{
		"tools":        false,
		"extracted":    false,
		"merged-certs": false,
	}

	for _, vol := range volumes {
		if _, exists := expectedVolumes[vol.Name]; exists {
			expectedVolumes[vol.Name] = true

			// Verify emptyDir with Memory medium
			if vol.EmptyDir == nil {
				t.Errorf("Volume %s should be emptyDir", vol.Name)
			} else if vol.EmptyDir.Medium != corev1.StorageMediumMemory {
				t.Errorf("Volume %s should use Memory medium", vol.Name)
			}
		}
	}

	for name, found := range expectedVolumes {
		if !found {
			t.Errorf("Volume %s not found", name)
		}
	}
}

func TestMutateCertificates_WithoutInference(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	// Should have 2 init containers: a-toolbox, fast-merge-certs (no b-extract-inference)
	if len(initContainers) != 2 {
		t.Errorf("Expected 2 init containers without inference, got %d", len(initContainers))
	}

	hasToolbox := false
	hasMerge := false
	hasExtract := false

	for _, c := range initContainers {
		switch c.Name {
		case "a-toolbox":
			hasToolbox = true
		case "fast-merge-certs":
			hasMerge = true
		case "b-extract-inference":
			hasExtract = true
		}
	}

	if !hasToolbox {
		t.Error("a-toolbox init container not found")
	}
	if !hasMerge {
		t.Error("fast-merge-certs init container not found")
	}
	if hasExtract {
		t.Error("b-extract-inference should not be present without inference container")
	}
}

func TestMutateCertificates_WithInference(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference", Image: "nvcr.io/nvidia/tritonserver:latest"},
				{Name: "sidecar", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	// Should have 3 init containers: a-toolbox, b-extract-inference, fast-merge-certs
	if len(initContainers) != 3 {
		t.Errorf("Expected 3 init containers with inference, got %d", len(initContainers))
	}

	hasToolbox := false
	hasMerge := false
	hasExtract := false
	extractImage := ""

	for _, c := range initContainers {
		switch c.Name {
		case "a-toolbox":
			hasToolbox = true
			if c.Image != certificatesImage {
				t.Errorf("a-toolbox should use certificates image, got %s", c.Image)
			}
		case "fast-merge-certs":
			hasMerge = true
			if c.Image != certificatesImage {
				t.Errorf("fast-merge-certs should use certificates image, got %s", c.Image)
			}
		case "b-extract-inference":
			hasExtract = true
			extractImage = c.Image
		}
	}

	if !hasToolbox {
		t.Error("a-toolbox init container not found")
	}
	if !hasMerge {
		t.Error("fast-merge-certs init container not found")
	}
	if !hasExtract {
		t.Error("b-extract-inference should be present with inference container")
	}

	// b-extract-inference should use the inference container's image
	if extractImage != "nvcr.io/nvidia/tritonserver:latest" {
		t.Errorf("b-extract-inference should use inference image, got %s", extractImage)
	}
}

func TestMutateCertificates_ExtractUsesBusybox(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference", Image: "nvcr.io/nvidia/tritonserver:latest"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	// Find b-extract-inference and verify it uses /tools/busybox
	for _, c := range initContainers {
		if c.Name == "b-extract-inference" {
			// Should use /tools/busybox as command
			if len(c.Command) < 1 || c.Command[0] != "/tools/busybox" {
				t.Errorf("b-extract-inference should use /tools/busybox, got %v", c.Command)
			}

			// Should have tools volume mounted
			hasToolsMount := false
			for _, vm := range c.VolumeMounts {
				if vm.Name == "tools" && vm.MountPath == "/tools" {
					hasToolsMount = true
					break
				}
			}
			if !hasToolsMount {
				t.Error("b-extract-inference should have tools volume mounted at /tools")
			}
			break
		}
	}
}

func TestMutateCertificates_ToolboxUsesCertmerge(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	// Find a-toolbox and verify it uses certmerge binary
	for _, c := range initContainers {
		if c.Name == "a-toolbox" {
			// Should use /certmerge binary
			if len(c.Command) < 1 || c.Command[0] != "/certmerge" {
				t.Errorf("a-toolbox should use /certmerge, got %v", c.Command)
			}

			// Should have setup as first arg
			if len(c.Args) < 1 || c.Args[0] != "setup" {
				t.Errorf("a-toolbox should have 'setup' as first arg, got %v", c.Args)
			}

			hasToolsMount := false
			hasMergedCertsMount := false
			for _, vm := range c.VolumeMounts {
				if vm.Name == "tools" && vm.MountPath == "/tools" {
					hasToolsMount = true
				}
				if vm.Name == "merged-certs" && vm.MountPath == "/merged-certs" {
					hasMergedCertsMount = true
				}
			}
			if !hasToolsMount {
				t.Error("a-toolbox should have tools volume mounted")
			}
			if !hasMergedCertsMount {
				t.Error("a-toolbox should have merged-certs volume mounted")
			}
			break
		}
	}
}

func TestMutateCertificates_MergeUsesCertmerge(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	// Find fast-merge-certs and verify it uses certmerge binary
	for _, c := range initContainers {
		if c.Name == "fast-merge-certs" {
			// Should use /certmerge binary
			if len(c.Command) < 1 || c.Command[0] != "/certmerge" {
				t.Errorf("fast-merge-certs should use /certmerge, got %v", c.Command)
			}

			// Should have merge as first arg
			if len(c.Args) < 1 || c.Args[0] != "merge" {
				t.Errorf("fast-merge-certs should have 'merge' as first arg, got %v", c.Args)
			}
			break
		}
	}
}

func TestMutateCertificates_SkipIfAlreadyMutated(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "a-toolbox", Image: certificatesImage},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	if len(patches) != 0 {
		t.Errorf("Expected 0 patches for already mutated pod, got %d", len(patches))
	}
}

func TestMutateCertificates_VerifyEnvVars(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find env var patches
	var envVars []corev1.EnvVar
	for _, patch := range patches {
		if patch.Path == "/spec/containers/0/env" {
			envBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(envBytes, &envVars)
			break
		}
	}

	expected := map[string]string{
		"REQUESTS_CA_BUNDLE":                  "/etc/ssl/certs/ca-certificates.crt",
		"SSL_CERT_FILE":                       "/etc/ssl/certs/ca-certificates.crt",
		"AWS_CA_BUNDLE":                       "/etc/ssl/certs/ca-certificates.crt",
		"NIM_SDK_USE_NATIVE_TLS":              "1",
		"NIM_SDK_DOWNLOAD_MAX_RETRY_COUNT":    "8",
		"NIM_SDK_DOWNLOAD_BACKOFF_INTERVAL_MS": "500",
	}

	for name, want := range expected {
		found := false
		for _, env := range envVars {
			if env.Name == name {
				found = true
				if env.Value != want {
					t.Errorf("env %s value mismatch: got %q, want %q", name, env.Value, want)
				}
				break
			}
		}
		if !found {
			t.Errorf("expected env var %s not found", name)
		}
	}
}

func TestAddCertEnvVars_AddsAllWhenEnvEmpty(t *testing.T) {
	container := &corev1.Container{Name: "app", Image: "nginx"}
	patches := addCertEnvVars("/spec/containers/0", container)
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].Path != "/spec/containers/0/env" {
		t.Fatalf("expected env patch path, got %s", patches[0].Path)
	}

	envBytes, _ := json.Marshal(patches[0].Value)
	var envVars []corev1.EnvVar
	_ = json.Unmarshal(envBytes, &envVars)

	if len(envVars) != 6 {
		t.Fatalf("expected 6 managed env vars, got %d", len(envVars))
	}
}

func TestAddCertEnvVars_AddsOnlyMissingManagedVars(t *testing.T) {
	container := &corev1.Container{
		Name:  "app",
		Image: "nginx",
		Env: []corev1.EnvVar{
			{Name: "REQUESTS_CA_BUNDLE", Value: "/custom/path"},
			{Name: "NIM_SDK_USE_NATIVE_TLS", Value: "0"},
		},
	}

	patches := addCertEnvVars("/spec/containers/0", container)
	if len(patches) != 4 {
		t.Fatalf("expected 4 add patches for missing vars, got %d", len(patches))
	}

	added := map[string]bool{}
	for _, p := range patches {
		if p.Path != "/spec/containers/0/env/-" {
			t.Fatalf("unexpected patch path: %s", p.Path)
		}
		envBytes, _ := json.Marshal(p.Value)
		var ev corev1.EnvVar
		_ = json.Unmarshal(envBytes, &ev)
		added[ev.Name] = true
	}

	for _, name := range []string{"SSL_CERT_FILE", "AWS_CA_BUNDLE", "NIM_SDK_DOWNLOAD_MAX_RETRY_COUNT", "NIM_SDK_DOWNLOAD_BACKOFF_INTERVAL_MS"} {
		if !added[name] {
			t.Errorf("expected missing managed env var %s to be added", name)
		}
	}
	if added["REQUESTS_CA_BUNDLE"] {
		t.Error("REQUESTS_CA_BUNDLE should not be re-added when already present")
	}
	if added["NIM_SDK_USE_NATIVE_TLS"] {
		t.Error("NIM_SDK_USE_NATIVE_TLS should not be re-added when already present")
	}
}

func TestAddCertEnvVars_NoopWhenAllManagedVarsPresent(t *testing.T) {
	container := &corev1.Container{
		Name:  "app",
		Image: "nginx",
		Env: []corev1.EnvVar{
			{Name: "REQUESTS_CA_BUNDLE", Value: "/etc/ssl/certs/ca-certificates.crt"},
			{Name: "SSL_CERT_FILE", Value: "/etc/ssl/certs/ca-certificates.crt"},
			{Name: "AWS_CA_BUNDLE", Value: "/etc/ssl/certs/ca-certificates.crt"},
			{Name: "NIM_SDK_USE_NATIVE_TLS", Value: "1"},
			{Name: "NIM_SDK_DOWNLOAD_MAX_RETRY_COUNT", Value: "8"},
			{Name: "NIM_SDK_DOWNLOAD_BACKOFF_INTERVAL_MS", Value: "500"},
		},
	}

	patches := addCertEnvVars("/spec/containers/0", container)
	if len(patches) != 0 {
		t.Fatalf("expected no patches when all managed env vars exist, got %d", len(patches))
	}
}

func TestMutateCertificates_VerifySecurityContext(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	for _, c := range initContainers {
		if c.SecurityContext == nil {
			t.Errorf("Init container %s missing security context", c.Name)
			continue
		}

		sc := c.SecurityContext

		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation != false {
			t.Errorf("Init container %s: allowPrivilegeEscalation should be false", c.Name)
		}

		if sc.ReadOnlyRootFilesystem == nil || *sc.ReadOnlyRootFilesystem != true {
			t.Errorf("Init container %s: readOnlyRootFilesystem should be true", c.Name)
		}

		if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Errorf("Init container %s: seccompProfile should be RuntimeDefault", c.Name)
		}

		if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
			t.Errorf("Init container %s: should drop ALL capabilities", c.Name)
		} else {
			foundDropAll := false
			for _, cap := range sc.Capabilities.Drop {
				if cap == "ALL" {
					foundDropAll = true
					break
				}
			}
			if !foundDropAll {
				t.Errorf("Init container %s: should drop ALL capabilities", c.Name)
			}
		}
	}
}

func TestMutateCertificates_VerifyResources(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference", Image: "nvcr.io/nvidia/tritonserver:latest"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	expectedResources := map[string]struct {
		reqCPU string
		reqMem string
		limCPU string
		limMem string
	}{
		"a-toolbox":           {"50m", "32Mi", "50m", "32Mi"},
		"b-extract-inference": {"5m", "8Mi", "50m", "32Mi"},
		"fast-merge-certs":    {"50m", "32Mi", "50m", "32Mi"}, // Lower with Go binary
	}

	for _, c := range initContainers {
		expected, ok := expectedResources[c.Name]
		if !ok {
			continue
		}

		// Check requests
		if reqCPU := c.Resources.Requests[corev1.ResourceCPU]; reqCPU.String() != expected.reqCPU {
			t.Errorf("Init container %s: expected request CPU %s, got %s", c.Name, expected.reqCPU, reqCPU.String())
		}
		if reqMem := c.Resources.Requests[corev1.ResourceMemory]; reqMem.String() != expected.reqMem {
			t.Errorf("Init container %s: expected request memory %s, got %s", c.Name, expected.reqMem, reqMem.String())
		}

		// Check limits
		if limCPU := c.Resources.Limits[corev1.ResourceCPU]; limCPU.String() != expected.limCPU {
			t.Errorf("Init container %s: expected limit CPU %s, got %s", c.Name, expected.limCPU, limCPU.String())
		}
		if limMem := c.Resources.Limits[corev1.ResourceMemory]; limMem.String() != expected.limMem {
			t.Errorf("Init container %s: expected limit memory %s, got %s", c.Name, expected.limMem, limMem.String())
		}
	}
}

func TestMutateCertificates_InitContainerOrder(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference", Image: "nvcr.io/nvidia/tritonserver:latest"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Find init containers patch
	var initContainers []corev1.Container
	for _, patch := range patches {
		if patch.Path == "/spec/initContainers" {
			containerBytes, _ := json.Marshal(patch.Value)
			_ = json.Unmarshal(containerBytes, &initContainers)
			break
		}
	}

	if len(initContainers) != 3 {
		t.Fatalf("Expected 3 init containers, got %d", len(initContainers))
	}

	// Order should be: a-toolbox, b-extract-inference, fast-merge-certs
	expectedOrder := []string{"a-toolbox", "b-extract-inference", "fast-merge-certs"}
	for i, expected := range expectedOrder {
		if initContainers[i].Name != expected {
			t.Errorf("Init container at position %d should be %s, got %s", i, expected, initContainers[i].Name)
		}
	}
}

func TestMutateCertificates_MountToInitContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "my-init-container", Image: "busybox"},
				{Name: "download-ngc-model", Image: "nvidia/cli"},
				{Name: "setup", Image: "busybox"},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// All 3 existing init containers should get the cert mount.
	// After our 2 init containers are prepended, indices are 2, 3, 4.
	foundMounts := map[int]bool{}
	for _, patch := range patches {
		for _, idx := range []int{2, 3, 4} {
			path := fmt.Sprintf("/spec/initContainers/%d/volumeMounts", idx)
			if patch.Path == path || patch.Path == path+"/-" {
				foundMounts[idx] = true
			}
		}
	}

	for _, idx := range []int{2, 3, 4} {
		if !foundMounts[idx] {
			t.Errorf("Expected mount patch for init container at index %d", idx)
		}
	}
}

func TestMutateCertificates_MultipleContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
				{Name: "sidecar", Image: "envoy"},
				{Name: "logger", Image: "fluentd"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Should have mount patches for all 3 containers
	containerMounts := make(map[int]bool)
	containerEnvs := make(map[int]bool)

	for _, patch := range patches {
		for i := 0; i < 3; i++ {
			mountPath := "/spec/containers/" + string(rune('0'+i)) + "/volumeMounts"
			envPath := "/spec/containers/" + string(rune('0'+i)) + "/env"
			if patch.Path == mountPath || patch.Path == mountPath+"/-" {
				containerMounts[i] = true
			}
			if patch.Path == envPath || patch.Path == envPath+"/-" {
				containerEnvs[i] = true
			}
		}
	}

	for i := 0; i < 3; i++ {
		if !containerMounts[i] {
			t.Errorf("Container %d missing volume mount patch", i)
		}
		if !containerEnvs[i] {
			t.Errorf("Container %d missing env var patch", i)
		}
	}
}

func TestMutateCertificates_ExistingVolumes(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "existing-volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Should append volumes, not replace
	volumeAddPatches := 0
	for _, patch := range patches {
		if patch.Op == "add" && patch.Path == "/spec/volumes/1" {
			volumeAddPatches++
		}
	}

	if volumeAddPatches < 1 {
		t.Error("Expected volume append patches")
	}
}

func TestMutateCertificates_ExistingEnvVars(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
					Env: []corev1.EnvVar{
						{Name: "REQUESTS_CA_BUNDLE", Value: "/custom/path"},
					},
				},
			},
		},
	}

	patches := mutateCertificates(pod)

	// Should not re-add any managed env vars that already exist.
	managedNames := map[string]bool{
		"REQUESTS_CA_BUNDLE":                   true,
		"SSL_CERT_FILE":                        true,
		"AWS_CA_BUNDLE":                        true,
		"NIM_SDK_USE_NATIVE_TLS":               true,
		"NIM_SDK_DOWNLOAD_MAX_RETRY_COUNT":     true,
		"NIM_SDK_DOWNLOAD_BACKOFF_INTERVAL_MS": true,
	}

	addedManaged := map[string]bool{}
	for _, patch := range patches {
		if patch.Path == "/spec/containers/0/env/-" {
			envBytes, _ := json.Marshal(patch.Value)
			var envVar corev1.EnvVar
			_ = json.Unmarshal(envBytes, &envVar)
			if managedNames[envVar.Name] {
				addedManaged[envVar.Name] = true
			}
		}
	}

	if addedManaged["REQUESTS_CA_BUNDLE"] {
		t.Error("should not add REQUESTS_CA_BUNDLE when it already exists")
	}
}

// ============================================================================
// Helper function tests
// ============================================================================

func TestGetInferenceContainer(t *testing.T) {
	tests := []struct {
		name          string
		containers    []corev1.Container
		expectFound   bool
		expectedImage string
	}{
		{
			name: "with inference container",
			containers: []corev1.Container{
				{Name: "inference", Image: "triton:latest"},
			},
			expectFound:   true,
			expectedImage: "triton:latest",
		},
		{
			name: "without inference container",
			containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
			expectFound:   false,
			expectedImage: "",
		},
		{
			name: "multiple containers with inference",
			containers: []corev1.Container{
				{Name: "sidecar", Image: "envoy"},
				{Name: "inference", Image: "custom-inference:v1"},
				{Name: "logger", Image: "fluentd"},
			},
			expectFound:   true,
			expectedImage: "custom-inference:v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: tt.containers,
				},
			}

			found, image := getInferenceContainer(pod)

			if found != tt.expectFound {
				t.Errorf("Expected found=%v, got %v", tt.expectFound, found)
			}
			if image != tt.expectedImage {
				t.Errorf("Expected image=%s, got %s", tt.expectedImage, image)
			}
		})
	}
}

func TestHasInitContainer(t *testing.T) {
	tests := []struct {
		name           string
		initContainers []corev1.Container
		searchName     string
		expectFound    bool
	}{
		{
			name: "exact match",
			initContainers: []corev1.Container{
				{Name: "a-toolbox"},
			},
			searchName:  "a-toolbox",
			expectFound: true,
		},
		{
			name: "prefix match",
			initContainers: []corev1.Container{
				{Name: "a-toolbox-v2"},
			},
			searchName:  "a-toolbox",
			expectFound: true,
		},
		{
			name: "not found",
			initContainers: []corev1.Container{
				{Name: "other-init"},
			},
			searchName:  "a-toolbox",
			expectFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: tt.initContainers,
				},
			}

			found := hasInitContainer(pod, tt.searchName)

			if found != tt.expectFound {
				t.Errorf("Expected found=%v, got %v", tt.expectFound, found)
			}
		})
	}
}

func TestHasEnvVar(t *testing.T) {
	container := &corev1.Container{
		Env: []corev1.EnvVar{
			{Name: "FOO", Value: "bar"},
			{Name: "SSL_CERT_FILE", Value: "/path"},
		},
	}

	if !hasEnvVar(container, "FOO") {
		t.Error("Should find FOO env var")
	}
	if !hasEnvVar(container, "SSL_CERT_FILE") {
		t.Error("Should find SSL_CERT_FILE env var")
	}
	if hasEnvVar(container, "NOT_EXISTS") {
		t.Error("Should not find NOT_EXISTS env var")
	}
}

func TestResourceQuantityPtr(t *testing.T) {
	qty := resourceQuantityPtr("5Mi")
	if qty == nil {
		t.Fatal("Expected non-nil quantity")
	}
	if qty.String() != "5Mi" {
		t.Errorf("Expected 5Mi, got %s", qty.String())
	}
}

func TestBoolPtr(t *testing.T) {
	truePtr := boolPtr(true)
	if truePtr == nil || *truePtr != true {
		t.Error("Expected true pointer")
	}

	falsePtr := boolPtr(false)
	if falsePtr == nil || *falsePtr != false {
		t.Error("Expected false pointer")
	}
}

// Ensure resource.MustParse is available
var _ = resource.MustParse("1Mi")
