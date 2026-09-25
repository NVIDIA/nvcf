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
}
