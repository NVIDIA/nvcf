// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// A cachedir restore is a volume mount plus a seeded cache shadow, and the
// pod runs its own command untouched. The shim that used to sit in front of
// the entrypoint exec'd the CAPTURED pod's argv -- for the bash-wrapper
// convention that was the idle sleep, so the restored pod seeded 2.2GB and
// exited without serving. This pins the no-shim contract end to end through
// the real patch builder.
func TestTryL2CacheDir_NoShim_PodCommandUntouched(t *testing.T) {
	m := &Mutator{
		CacheDir:      "/opt/nvsnap",
		MainContainer: 0,
		L2Backend: &stubL2Backend{mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-abc"}}},
			VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
		}},
	}
	wrapper := []string{"/bin/bash", "-lc", "nohup setsid vllm serve & while true; do sleep 30; done"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "reuse", Namespace: "ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "vllm", Image: "vllm/vllm-openai:v0.20.0", Command: wrapper[:2], Args: wrapper[2:],
		}}},
	}
	manifest := checkpointstore.Manifest{Hash: "abc", EntryArgv: []string{"sleep", "30"}, CaptureMethod: "cachedir"}

	patches, err := m.tryL2CacheDir(context.Background(), pod, "abc", manifest)
	if err != nil {
		t.Fatal(err)
	}

	var sawRoxMount, sawSeedInit, sawCacheEnv bool
	var inits []corev1.Container
	for _, p := range patches {
		// Nothing may touch the entrypoint.
		if strings.HasSuffix(p.Path, "/command") || strings.HasSuffix(p.Path, "/args") {
			t.Fatalf("cachedir restore must not rewrite the pod command, got patch %s %s", p.Op, p.Path)
		}
		switch v := p.Value.(type) {
		case corev1.EnvVar:
			switch v.Name {
			case "NVSNAP_ORIG_COMMAND", "NVSNAP_NO_OVERLAY", "NVSNAP_PREWARM_DIR", "NVSNAP_ORIG_CWD", envRuntimeDirs:
				t.Fatalf("shim env %s leaked into a cachedir restore", v.Name)
			case "HF_HOME":
				sawCacheEnv = true
			}
		case corev1.Volume:
			if v.Name == nvsnapToolsVolumeName {
				t.Fatal("the nvsnap-tools shim bundle must not be mounted into a cachedir restore")
			}
		case corev1.VolumeMount:
			if v.MountPath == "/opt/nvsnap" {
				sawRoxMount = true
			}
		case corev1.Container:
			inits = append(inits, v)
			if v.Name == "nvsnap-seed-cache" {
				sawSeedInit = true
			}
		}
		if vols, ok := p.Value.([]corev1.Volume); ok {
			for _, v := range vols {
				if v.Name == nvsnapToolsVolumeName {
					t.Fatal("the nvsnap-tools shim bundle must not be mounted into a cachedir restore")
				}
			}
		}
	}
	if !sawRoxMount {
		t.Error("expected the rox cache mounted at /opt/nvsnap")
	}
	if !sawSeedInit {
		t.Error("expected the nvsnap-seed-cache init container")
	}
	if !sawCacheEnv {
		t.Error("expected the cache env (HF_HOME) replayed from the manifest")
	}
	// The prewarm must come back as an init container, after the seed, reading
	// the rox read-only as root, and never as an entrypoint wrapper. It is the
	// one piece of the retired shim that measurably helps large models.
	if len(inits) != 2 || inits[0].Name != "nvsnap-seed-cache" || inits[1].Name != "nvsnap-prewarm" {
		names := []string{}
		for _, c := range inits {
			names = append(names, c.Name)
		}
		t.Fatalf("want init containers [nvsnap-seed-cache nvsnap-prewarm] in that order, got %v", names)
	}
	pw := inits[1]
	if pw.Image != pod.Spec.Containers[0].Image {
		t.Errorf("prewarm should reuse the workload image (already pulled), got %q", pw.Image)
	}
	if len(pw.VolumeMounts) != 1 || !pw.VolumeMounts[0].ReadOnly || pw.VolumeMounts[0].Name != cacheDirVolumeName {
		t.Errorf("prewarm must mount only the rox, read-only, got %+v", pw.VolumeMounts)
	}
	if pw.SecurityContext == nil || pw.SecurityContext.RunAsUser == nil || *pw.SecurityContext.RunAsUser != 0 {
		t.Error("prewarm must run as root: the rox files are root-owned")
	}
	cmd := strings.Join(pw.Command, " ")
	if !strings.Contains(cmd, cacheSeedSrcPath) || !strings.Contains(cmd, "|| true") {
		t.Errorf("prewarm must read the rox tree and be best-effort, got %q", cmd)
	}
}

// NVSNAP_PREWARM=0 on the workload skips the prewarm init, matching the knob
// the retired shim honoured, and touches nothing else.
func TestTryL2CacheDir_PrewarmOptOut(t *testing.T) {
	m := &Mutator{
		CacheDir: "/opt/nvsnap", MainContainer: 0,
		L2Backend: &stubL2Backend{mountResult: checkpointstore.PodMount{
			Volume:      corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-abc"}}},
			VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
		}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "reuse", Namespace: "ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "vllm", Image: "img", Command: []string{"serve"},
			Env: []corev1.EnvVar{{Name: "NVSNAP_PREWARM", Value: "0"}},
		}}},
	}
	patches, err := m.tryL2CacheDir(context.Background(), pod, "abc", checkpointstore.Manifest{Hash: "abc", CaptureMethod: "cachedir"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		if c, ok := p.Value.(corev1.Container); ok && c.Name == "nvsnap-prewarm" {
			t.Fatal("NVSNAP_PREWARM=0 must skip the prewarm init container")
		}
		if strings.HasSuffix(p.Path, "/command") {
			t.Fatal("opt-out must not rewrite the command either")
		}
	}
}

// The storage profile of the L2 StorageClass decides the prewarm default
// and its reader count; the pod's own NVSNAP_PREWARM still overrides it.
func TestTryL2CacheDir_PrewarmFollowsStorageProfile(t *testing.T) {
	newMutator := func(p *checkpointstore.StorageProfile) *Mutator {
		return &Mutator{
			CacheDir: "/opt/nvsnap", MainContainer: 0, StorageProfile: p,
			L2Backend: &stubL2Backend{mountResult: checkpointstore.PodMount{
				Volume:      corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-abc"}}},
				VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
			}},
		}
	}
	newPod := func(env ...corev1.EnvVar) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "reuse", Namespace: "ns"},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "vllm", Image: "img", Command: []string{"serve"}, Env: env,
			}}},
		}
	}
	prewarmCmd := func(t *testing.T, m *Mutator, pod *corev1.Pod) (string, bool) {
		t.Helper()
		patches, err := m.tryL2CacheDir(context.Background(), pod, "abc", checkpointstore.Manifest{Hash: "abc", CaptureMethod: "cachedir"})
		if err != nil {
			t.Fatal(err)
		}
		var sawSeed bool
		for _, p := range patches {
			c, ok := p.Value.(corev1.Container)
			if !ok {
				continue
			}
			switch c.Name {
			case "nvsnap-seed-cache":
				sawSeed = true
			case "nvsnap-prewarm":
				return strings.Join(c.Command, " "), true
			}
		}
		if !sawSeed {
			t.Fatal("the seed init must be present regardless of the prewarm policy")
		}
		return "", false
	}
	off, on := false, true

	if cmd, ok := prewarmCmd(t, newMutator(nil), newPod()); !ok || !strings.Contains(cmd, "-P 6 ") {
		t.Errorf("no profile: want the prewarm with the default 6 readers, got ok=%v cmd=%q", ok, cmd)
	}
	if cmd, ok := prewarmCmd(t, newMutator(&checkpointstore.StorageProfile{PrewarmParallelism: 16}), newPod()); !ok || !strings.Contains(cmd, "-P 16 ") {
		t.Errorf("profile parallelism 16 not applied, got ok=%v cmd=%q", ok, cmd)
	}
	if _, ok := prewarmCmd(t, newMutator(&checkpointstore.StorageProfile{Prewarm: &off}), newPod()); ok {
		t.Error("profile prewarm: false must drop the prewarm init")
	}
	if _, ok := prewarmCmd(t, newMutator(&checkpointstore.StorageProfile{Prewarm: &off}), newPod(corev1.EnvVar{Name: "NVSNAP_PREWARM", Value: "1"})); !ok {
		t.Error("NVSNAP_PREWARM=1 on the pod must override a profile that turns the prewarm off")
	}
	if _, ok := prewarmCmd(t, newMutator(&checkpointstore.StorageProfile{Prewarm: &on}), newPod(corev1.EnvVar{Name: "NVSNAP_PREWARM", Value: "0"})); ok {
		t.Error("NVSNAP_PREWARM=0 on the pod must override a profile that turns the prewarm on")
	}
}
