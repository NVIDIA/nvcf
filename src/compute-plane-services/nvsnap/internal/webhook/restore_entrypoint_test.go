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

// Tests for the restore-entrypoint hostPath injection (nvsnap#147
// second half, redesigned in nvsnap#184). End-to-end through Mutate
// so we exercise the integration with tryL2Mount — the injection
// only runs when an L2 mount succeeds.

package webhook

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// l2RestoreFixture builds a Mutator + stub L2 backend pre-wired for
// the happy "L2 mount succeeds" path. HostBundleRoot left empty so
// the default ("/var/lib/nvsnap/bundle") is exercised — tests override
// per case when needed.
func l2RestoreFixture(t *testing.T) (*Mutator, *corev1.Pod) {
	t.Helper()
	l2 := &stubL2Backend{
		mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{
				Name: "nvsnap-checkpoint",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "rox-abc12345",
						ReadOnly:  true,
					},
				},
			},
			VolumeMount: corev1.VolumeMount{
				Name:      "nvsnap-checkpoint",
				MountPath: "/nvsnap-checkpoint",
				ReadOnly:  true,
			},
		},
	}
	m := &Mutator{
		Backend:       newBackend(t),
		L2Backend:     l2,
		MainContainer: 0,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "nvcf-backend",
			Name:        "vllm-restored",
			Annotations: map[string]string{RestoreFromAnnotation: "abc12345"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "inference",
				Image:   "vllm/vllm-openai:v0.11.2",
				Command: []string{"vllm"},
				Args:    []string{"serve", "--model", "tinyllama"},
			}},
		},
	}
	return m, pod
}

// TestRestoreBundle_HostPathInjected — happy path. Webhook emits two
// hostPath volumes pointing at /var/lib/nvsnap/bundle/{nvsnap,nvsnap-lib},
// read-only mounts on the main container, /nvsnap/restore-entrypoint
// command rewrite, env preservation, and securityContext patches.
func TestRestoreBundle_HostPathInjected(t *testing.T) {
	m, pod := l2RestoreFixture(t)

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(patches) == 0 {
		t.Fatalf("expected restore-bundle patches, got none")
	}

	saw := struct {
		volumeTools       bool
		volumeLib         bool
		mountTools        bool
		mountLib          bool
		commandRewrite    bool
		argsRemoved       bool
		origCommandEnv    bool
		origArgsEnv       bool
		criuBundlePathEnv bool
		checkpointIDEnv   bool
	}{}

	collectVolume := func(v corev1.Volume) {
		if v.HostPath == nil {
			return
		}
		if v.Name == nvsnapToolsVolumeName {
			saw.volumeTools = true
			if v.HostPath.Path != "/var/lib/nvsnap/bundle/nvsnap" {
				t.Errorf("nvsnap-tools hostPath = %q, want /var/lib/nvsnap/bundle/nvsnap", v.HostPath.Path)
			}
			if v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectory {
				t.Errorf("nvsnap-tools hostPath.type = %v, want Directory (fail-fast if agent hasn't staged)", v.HostPath.Type)
			}
		}
		if v.Name == nvsnapLibVolumeName {
			saw.volumeLib = true
			if v.HostPath.Path != "/var/lib/nvsnap/bundle/nvsnap-lib" {
				t.Errorf("nvsnap-lib hostPath = %q, want /var/lib/nvsnap/bundle/nvsnap-lib", v.HostPath.Path)
			}
			if v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectory {
				t.Errorf("nvsnap-lib hostPath.type = %v, want Directory", v.HostPath.Type)
			}
		}
	}

	for _, p := range patches {
		switch {
		case p.Path == "/spec/volumes/-" || p.Path == "/spec/volumes":
			raw, _ := json.Marshal(p.Value)
			var single corev1.Volume
			if json.Unmarshal(raw, &single) == nil {
				collectVolume(single)
			}
			var list []corev1.Volume
			if json.Unmarshal(raw, &list) == nil {
				for _, v := range list {
					collectVolume(v)
				}
			}
		case p.Path == "/spec/containers/0/volumeMounts/-":
			raw, _ := json.Marshal(p.Value)
			var vm corev1.VolumeMount
			if json.Unmarshal(raw, &vm) == nil {
				if vm.Name == nvsnapToolsVolumeName && vm.MountPath == nvsnapToolsMountPath {
					saw.mountTools = true
					if !vm.ReadOnly {
						t.Errorf("/nvsnap mount must be readOnly (function pod is consumer, agent is sole writer)")
					}
				}
				if vm.Name == nvsnapLibVolumeName && vm.MountPath == nvsnapLibMountPath {
					saw.mountLib = true
					if !vm.ReadOnly {
						t.Errorf("/nvsnap-lib mount must be readOnly")
					}
				}
			}
		case p.Path == "/spec/containers/0/command":
			raw, _ := json.Marshal(p.Value)
			var cmd []string
			if json.Unmarshal(raw, &cmd) == nil && len(cmd) == 1 && cmd[0] == "/nvsnap/restore-entrypoint" {
				saw.commandRewrite = true
				if p.Op != "replace" {
					t.Errorf("command op = %q, want replace (pod had main.Command set)", p.Op)
				}
			}
		case p.Path == "/spec/containers/0/args" && p.Op == "remove":
			saw.argsRemoved = true
		case p.Path == "/spec/containers/0/env/-":
			raw, _ := json.Marshal(p.Value)
			var e corev1.EnvVar
			if json.Unmarshal(raw, &e) == nil {
				switch e.Name {
				case "NVSNAP_ORIG_COMMAND":
					saw.origCommandEnv = true
					var decoded []string
					if err := json.Unmarshal([]byte(e.Value), &decoded); err != nil {
						t.Errorf("NVSNAP_ORIG_COMMAND value not valid JSON array: %v", err)
					}
					if len(decoded) != 1 || decoded[0] != "vllm" {
						t.Errorf("NVSNAP_ORIG_COMMAND decoded = %v, want [vllm]", decoded)
					}
				case "NVSNAP_ORIG_ARGS":
					saw.origArgsEnv = true
				case "CRIU_BUNDLE_PATH":
					saw.criuBundlePathEnv = true
					if e.Value != "/nvsnap" {
						t.Errorf("CRIU_BUNDLE_PATH = %q, want /nvsnap", e.Value)
					}
				case "CHECKPOINT_ID":
					saw.checkpointIDEnv = true
					if e.Value != "" {
						t.Errorf("CHECKPOINT_ID = %q, want \"\"", e.Value)
					}
				}
			}
		}
	}

	if !saw.volumeTools {
		t.Errorf("missing nvsnap-tools hostPath volume patch")
	}
	if !saw.volumeLib {
		t.Errorf("missing nvsnap-lib hostPath volume patch")
	}
	if !saw.mountTools {
		t.Errorf("missing /nvsnap volumeMount on main container")
	}
	if !saw.mountLib {
		t.Errorf("missing /nvsnap-lib volumeMount on main container")
	}
	if !saw.commandRewrite {
		t.Errorf("main container command not rewritten to /nvsnap/restore-entrypoint")
	}
	if !saw.argsRemoved {
		t.Errorf("main container args not removed")
	}
	if !saw.origCommandEnv {
		t.Errorf("NVSNAP_ORIG_COMMAND env var not set")
	}
	if !saw.origArgsEnv {
		t.Errorf("NVSNAP_ORIG_ARGS env var not set")
	}
	if !saw.criuBundlePathEnv {
		t.Errorf("CRIU_BUNDLE_PATH env var not set")
	}
	if !saw.checkpointIDEnv {
		t.Errorf("CHECKPOINT_ID env var not explicitly set to \"\"")
	}
}

// TestRestoreBundle_HostBundleRoot_Override — operator overrides the
// default /var/lib/nvsnap/bundle by setting Mutator.HostBundleRoot.
// HostPath volumes should reflect the override.
func TestRestoreBundle_HostBundleRoot_Override(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	m.HostBundleRoot = "/mnt/nvsnap-staging"

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	var toolsPath, libPath string
	for _, p := range patches {
		raw, _ := json.Marshal(p.Value)
		var v corev1.Volume
		if json.Unmarshal(raw, &v) == nil && v.HostPath != nil {
			if v.Name == nvsnapToolsVolumeName {
				toolsPath = v.HostPath.Path
			}
			if v.Name == nvsnapLibVolumeName {
				libPath = v.HostPath.Path
			}
		}
	}
	if toolsPath != "/mnt/nvsnap-staging/nvsnap" {
		t.Errorf("nvsnap-tools hostPath = %q, want /mnt/nvsnap-staging/nvsnap", toolsPath)
	}
	if libPath != "/mnt/nvsnap-staging/nvsnap-lib" {
		t.Errorf("nvsnap-lib hostPath = %q, want /mnt/nvsnap-staging/nvsnap-lib", libPath)
	}
}

// TestRestoreBundle_Skipped_WhenAlreadyMounted — operator pre-wired
// the workload with /nvsnap by hand. Mutator must not double-inject.
func TestRestoreBundle_Skipped_WhenAlreadyMounted(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "nvsnap-tools-existing", MountPath: nvsnapToolsMountPath},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/command" {
			t.Errorf("already-mounted /nvsnap but command was rewritten")
		}
	}
}

// TestRestoreBundle_Skipped_WhenLibAlreadyMounted — operator pre-wired
// /nvsnap-lib too.
func TestRestoreBundle_Skipped_WhenLibAlreadyMounted(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "nvsnap-lib", MountPath: nvsnapLibMountPath},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/command" {
			t.Errorf("/nvsnap-lib already mounted but command was rewritten")
		}
	}
}

// TestRestoreBundle_CommandAddedWhenAbsent — pods that rely on the
// image's ENTRYPOINT have main.Command == nil. JSON Patch "replace"
// on a missing path errors at apply time; the mutator must use "add"
// in that case.
func TestRestoreBundle_CommandAddedWhenAbsent(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	pod.Spec.Containers[0].Command = nil
	pod.Spec.Containers[0].Args = nil

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	sawAdd := false
	sawArgsRemove := false
	for _, p := range patches {
		if p.Path == "/spec/containers/0/command" {
			if p.Op != "add" {
				t.Errorf("command op = %q, want add (pod main.Command was nil)", p.Op)
			}
			sawAdd = true
		}
		if p.Path == "/spec/containers/0/args" && p.Op == "remove" {
			sawArgsRemove = true
		}
	}
	if !sawAdd {
		t.Errorf("expected an add op for command; got patches=%+v", patches)
	}
	if sawArgsRemove {
		t.Errorf("Args was nil but a remove op was emitted (would fail at apply time)")
	}
}

// TestRestoreBundle_SecurityContext_FromNil — pod has no
// securityContext on the main container; webhook adds the whole
// object with privileged=true + runAsUser=0.
func TestRestoreBundle_SecurityContext_FromNil(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	pod.Spec.Containers[0].SecurityContext = nil

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	sawWholeObject := false
	for _, p := range patches {
		if p.Path != "/spec/containers/0/securityContext" {
			continue
		}
		raw, _ := json.Marshal(p.Value)
		var sc corev1.SecurityContext
		if json.Unmarshal(raw, &sc) != nil {
			continue
		}
		if sc.Privileged == nil || !*sc.Privileged {
			t.Errorf("securityContext.privileged not true")
		}
		if sc.RunAsUser == nil || *sc.RunAsUser != 0 {
			t.Errorf("securityContext.runAsUser not 0")
		}
		sawWholeObject = true
	}
	if !sawWholeObject {
		t.Errorf("expected whole securityContext add patch")
	}
}

// TestRestoreBundle_SecurityContext_FromExisting — pod already has
// securityContext. Webhook adds only privileged + runAsUser, leaving
// other fields untouched.
func TestRestoreBundle_SecurityContext_FromExisting(t *testing.T) {
	m, pod := l2RestoreFixture(t)
	falsePtr := false
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &falsePtr,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	sawPriv, sawUser := false, false
	for _, p := range patches {
		switch p.Path {
		case "/spec/containers/0/securityContext/privileged":
			if v, ok := p.Value.(bool); ok && v {
				sawPriv = true
			}
		case "/spec/containers/0/securityContext/runAsUser":
			if v, ok := p.Value.(int64); ok && v == 0 {
				sawUser = true
			}
		case "/spec/containers/0/securityContext":
			t.Errorf("should NOT replace whole securityContext when one exists; patch=%+v", p)
		}
	}
	if !sawPriv {
		t.Errorf("missing privileged=true add under existing securityContext")
	}
	if !sawUser {
		t.Errorf("missing runAsUser=0 add under existing securityContext")
	}
}
