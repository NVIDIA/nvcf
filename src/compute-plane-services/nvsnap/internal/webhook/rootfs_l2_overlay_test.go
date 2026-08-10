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
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// buildRootfsManifest returns a rootfs manifest with nPaths extract paths
// (to exercise the O(1) invariant) plus one non-rootfs volume and a
// recorded entrypoint.
func buildRootfsManifest(nPaths int) checkpointstore.Manifest {
	m := checkpointstore.Manifest{
		CaptureMethod: "rootfs",
		EntryArgv:     []string{"/opt/nim/start_server.sh", "--port", "9000"},
		EntryCwd:      "/opt/nim",
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "rootfs", MountPath: "/", Type: "rootfs", FileCount: 1, SizeBytes: 1},
			{Name: "model-data", MountPath: "/config/models", Type: "emptyDir", FileCount: 1, SizeBytes: 1},
		},
	}
	for i := 0; i < nPaths; i++ {
		m.RootfsExtractPaths = append(m.RootfsExtractPaths, checkpointstore.ExtractPath{
			Path:      fmt.Sprintf("/usr/local/lib/python3.12/dist-packages/pkg%d/__pycache__", i),
			Category:  "python-dist-packages",
			SizeBytes: 1 << 20,
			FileCount: 10,
		})
	}
	return m
}

func newRootfsOverlayMutator(t *testing.T, manifest checkpointstore.Manifest) (*Mutator, *corev1.Pod) {
	t.Helper()
	b := newBackend(t)
	hash := "c1656e2e36d50f26b5dad10f151faef45ced9cc80f9ff6cdb4f8140eb03860da"
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: t.TempDir()}}, manifest); err != nil {
		t.Fatal(err)
	}
	l2 := &stubL2Backend{mountResult: checkpointstore.PodMount{
		Volume: corev1.Volume{
			Name: "nvsnap-rox",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-c1656e2e"},
			},
		},
	}}
	m := &Mutator{Backend: b, L2Backend: l2, MainContainer: 0}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "inference", Image: "nvcr.io/nim/nvidia/whisper-large-v3:1.5.1",
	}}}}
	pod.Annotations = map[string]string{RestoreFromAnnotation: hash}
	return m, pod
}

// A rootfs capture restores via the whole-rootfs overlay shim: rox PVC
// mounted ONCE (RO), a scratch emptyDir, the /nvsnap bundle, command
// rewritten to the shim, runAsUser=0 + DAC_OVERRIDE + SYS_ADMIN (NOT
// privileged), and NO per-path mounts / init container.
func TestMutate_RootfsWholeOverlay(t *testing.T) {
	m, pod := newRootfsOverlayMutator(t, buildRootfsManifest(20))
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	var sawCaptured, sawScratch, sawTools, sawPriv, sawRoot, sawDac, sawSysAdmin, sawInit, sawSeccomp, sawAppArmor bool
	var command []string
	env := map[string]string{}
	mainMounts := map[string]corev1.VolumeMount{}
	for _, p := range patches {
		switch p.Path {
		case "/spec/volumes/-":
			if v, ok := p.Value.(corev1.Volume); ok {
				switch {
				case v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "rox-c1656e2e":
					sawCaptured = true
					if !v.PersistentVolumeClaim.ReadOnly {
						t.Error("captured rox volume must be readOnly")
					}
				case v.Name == scratchVolumeName && v.EmptyDir != nil:
					sawScratch = true
				case v.Name == nvsnapToolsVolumeName && v.HostPath != nil:
					sawTools = true
				}
			}
		case "/spec/initContainers", "/spec/initContainers/-":
			sawInit = true
		case "/spec/containers/0/volumeMounts/-":
			if vm, ok := p.Value.(corev1.VolumeMount); ok {
				mainMounts[vm.MountPath] = vm
			}
		case "/spec/containers/0/command":
			if c, ok := p.Value.([]string); ok {
				command = c
			}
		case "/spec/containers/0/securityContext/seccompProfile":
			if sp, ok := p.Value.(corev1.SeccompProfile); ok && sp.Type == corev1.SeccompProfileTypeUnconfined {
				sawSeccomp = true
			}
		case "/metadata/annotations/container.apparmor.security.beta.kubernetes.io~1inference":
			if v, ok := p.Value.(string); ok && v == "unconfined" {
				sawAppArmor = true
			}
		case "/spec/containers/0/env/-":
			if e, ok := p.Value.(corev1.EnvVar); ok {
				env[e.Name] = e.Value
			}
		case "/spec/containers/0/securityContext":
			if sc, ok := p.Value.(corev1.SecurityContext); ok {
				if sc.Privileged != nil && *sc.Privileged {
					sawPriv = true
				}
				if sc.RunAsUser != nil && *sc.RunAsUser == 0 {
					sawRoot = true
				}
				if sc.Capabilities != nil {
					sawDac = hasCapability(sc.Capabilities.Add, "DAC_OVERRIDE")
					sawSysAdmin = hasCapability(sc.Capabilities.Add, "SYS_ADMIN")
				}
			}
		}
	}

	if !sawCaptured || !sawScratch || !sawTools {
		t.Errorf("missing O(1) volumes: captured=%v scratch=%v tools=%v", sawCaptured, sawScratch, sawTools)
	}
	if sawInit {
		t.Error("whole-overlay must NOT inject an init container (shim does the mount in the workload)")
	}
	if len(command) != 1 || !strings.HasSuffix(command[0], "/nvsnap-rootfs-restore") {
		t.Errorf("command must be the shim; got %v", command)
	}
	if env["NVSNAP_CAPTURED_DIR"] != capturedMountPath || env["NVSNAP_SCRATCH_DIR"] != scratchMountPath {
		t.Errorf("shim env dirs wrong: %v", env)
	}
	if env["NVSNAP_ORIG_COMMAND"] == "" || !strings.Contains(env["NVSNAP_ORIG_COMMAND"], "start_server.sh") {
		t.Errorf("NVSNAP_ORIG_COMMAND must carry the recorded EntryArgv; got %q", env["NVSNAP_ORIG_COMMAND"])
	}
	if env["NVSNAP_ORIG_CWD"] != "/opt/nim" {
		t.Errorf("NVSNAP_ORIG_CWD = %q, want /opt/nim", env["NVSNAP_ORIG_CWD"])
	}
	if !strings.Contains(env["NVSNAP_ROOTFS_VOLUMES"], "/config/models") {
		t.Errorf("NVSNAP_ROOTFS_VOLUMES must carry non-rootfs volumes; got %q", env["NVSNAP_ROOTFS_VOLUMES"])
	}
	// Exactly the three O(1) mounts — never one-per-extract-path.
	for _, want := range []string{capturedMountPath, scratchMountPath, nvsnapToolsMountPath} {
		if _, ok := mainMounts[want]; !ok {
			t.Errorf("missing main mount at %q; got %v", want, mainMounts)
		}
	}
	if len(mainMounts) != 3 {
		t.Errorf("expected exactly 3 main volumeMounts (O(1)); got %d: %v", len(mainMounts), mainMounts)
	}
	if !sawRoot {
		t.Error("main container must get runAsUser=0")
	}
	if sawPriv {
		t.Error("main container must NOT be privileged (breaks single-GPU isolation)")
	}
	if !sawDac || !sawSysAdmin {
		t.Errorf("main caps must include DAC_OVERRIDE + SYS_ADMIN; dac=%v sysadmin=%v", sawDac, sawSysAdmin)
	}
	if !sawSeccomp {
		t.Error("main container must set seccompProfile=Unconfined (mount/pivot_root blocked by RuntimeDefault otherwise)")
	}
	if !sawAppArmor {
		t.Error("must annotate AppArmor unconfined for the main container (COS default profile blocks mount)")
	}
}

// injectVllmLoadStrategy forces parallel safetensors loading on vLLM warm
// restores (vLLM disables auto-prefetch on the overlay fs → 5x slower read).
// It must fire ONLY for vLLM `serve` and never override an explicit choice
// or touch other engines.
func TestInjectVllmLoadStrategy(t *testing.T) {
	const flag = "--safetensors-load-strategy=prefetch"
	has := func(a []string, s string) bool {
		for _, x := range a {
			if x == s {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name     string
		argv     []string
		wantFlag bool
		wantSame bool // output identical to input (no-op)
	}{
		{"vllm via python path", []string{"/usr/bin/python3", "/usr/local/bin/vllm", "serve", "--model", "x"}, true, false},
		{"vllm bare", []string{"vllm", "serve", "--model", "x"}, true, false},
		{"already has load-strategy", []string{"vllm", "serve", "--safetensors-load-strategy=lazy"}, false, true},
		{"already has load-format", []string{"vllm", "serve", "--load-format", "runai_streamer"}, false, true},
		{"vllm without serve", []string{"vllm", "--help"}, false, true},
		{"non-vllm NIM entrypoint", []string{"/opt/nim/start_server.sh", "--port", "9000"}, false, true},
		{"empty", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := injectVllmLoadStrategy(tc.argv)
			if tc.wantFlag && !has(got, flag) {
				t.Errorf("expected %s appended; got %v", flag, got)
			}
			if !tc.wantFlag && has(got, flag) {
				t.Errorf("must NOT append flag; got %v", got)
			}
			if tc.wantSame && len(got) != len(tc.argv) {
				t.Errorf("expected no-op; got %v from %v", got, tc.argv)
			}
		})
	}
}

// The webhook appends the prefetch flag into NVSNAP_ORIG_COMMAND for a vLLM
// rootfs restore (end-to-end through Mutate), so the shim execs vLLM with
// parallel loading.
func TestMutate_RootfsOverlay_InjectsVllmPrefetch(t *testing.T) {
	mf := buildRootfsManifest(5)
	mf.EntryArgv = []string{"/usr/bin/python3", "/usr/local/bin/vllm", "serve", "--model", "openai/gpt-oss-120b"}
	m, pod := newRootfsOverlayMutator(t, mf)
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/env/-" {
			if e, ok := p.Value.(corev1.EnvVar); ok && e.Name == "NVSNAP_ORIG_COMMAND" {
				if !strings.Contains(e.Value, "--safetensors-load-strategy=prefetch") {
					t.Errorf("NVSNAP_ORIG_COMMAND must carry the injected prefetch flag; got %q", e.Value)
				}
				return
			}
		}
	}
	t.Error("NVSNAP_ORIG_COMMAND env not found in patches")
}

// The Pod footprint must be independent of capture path count: a 5-path
// and a 5000-path capture inject the SAME number of volumes + mounts.
// This is the etcd object-size invariant (gpt-oss 1055 paths blew the
// ~1.5 MiB ceiling under the old per-path injection).
func TestMutate_RootfsWholeOverlay_ConstantSize(t *testing.T) {
	count := func(nPaths int) (volumes, mounts, envs int) {
		m, pod := newRootfsOverlayMutator(t, buildRootfsManifest(nPaths))
		patches, err := m.Mutate(context.Background(), pod)
		if err != nil {
			t.Fatalf("Mutate(%d): %v", nPaths, err)
		}
		for _, p := range patches {
			switch p.Path {
			case "/spec/volumes/-":
				volumes++
			case "/spec/containers/0/volumeMounts/-":
				mounts++
			case "/spec/containers/0/env/-":
				envs++
			}
		}
		return
	}
	v5, m5, e5 := count(5)
	v5k, m5k, e5k := count(5000)
	if v5 != v5k || m5 != m5k || e5 != e5k {
		t.Errorf("Pod footprint scales with path count (NOT O(1)): "+
			"5-path={vol:%d mnt:%d env:%d} 5000-path={vol:%d mnt:%d env:%d}",
			v5, m5, e5, v5k, m5k, e5k)
	}
	if m5 != 3 {
		t.Errorf("expected 3 mounts regardless of path count; got %d", m5)
	}
}

// A capture with no recorded EntryArgv (pre-feature) must NOT inject the
// shim — there's no complete entrypoint to exec post-pivot_root, and we
// must NOT fall back to the pod's command/args (which lacks the image's
// ENTRYPOINT binary, silently launching the wrong thing). Mutate falls
// through to the legacy L1 per-path injector instead — no shim command,
// even when the pod sets a command.
func TestMutate_RootfsWholeOverlay_NoEntryArgvFallsThroughToL1(t *testing.T) {
	mf := buildRootfsManifest(3)
	mf.EntryArgv = nil
	mf.EntryCwd = ""
	m, pod := newRootfsOverlayMutator(t, mf)
	// Even with a pod command set, the shim must NOT be injected — the
	// fallback is deliberately gone (it produced incomplete argv).
	pod.Spec.Containers[0].Command = []string{"vllm"}
	pod.Spec.Containers[0].Args = []string{"serve", "--tp", "4"}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/command" {
			if c, ok := p.Value.([]string); ok && len(c) == 1 && strings.HasSuffix(c[0], "/nvsnap-rootfs-restore") {
				t.Errorf("no-EntryArgv capture must not inject the shim; got command %v", c)
			}
		}
	}
}

func TestValidateMountPath(t *testing.T) {
	ok := []string{"/data", "/var/run/nvcf/info", "/root/.cache/huggingface"}
	for _, p := range ok {
		if err := validateMountPath(p); err != nil {
			t.Errorf("validateMountPath(%q) = %v, want nil", p, err)
		}
	}
	bad := map[string]string{
		"":                    "empty",
		"relative/path":       "not absolute",
		"/a/../b":             "unclean",
		"/a//b":               "unclean",
		"/data,upperdir=/etc": "comma breaks overlay opts",
		"/data:/etc/passwd":   "colon breaks lowerdir list",
		"/data\nrm -rf":       "newline",
		"/data\x00x":          "NUL",
	}
	for p, why := range bad {
		if err := validateMountPath(p); err == nil {
			t.Errorf("validateMountPath(%q) = nil, want error (%s)", p, why)
		}
	}
}
