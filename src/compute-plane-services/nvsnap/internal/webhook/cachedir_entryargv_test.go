// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// The captured pod used the bash-wrapper convention, so the pid resolver
// recorded the wrapper's idle loop as the entry command. A restoring pod
// must run ITS OWN command, never the captured one.
var wrapperArgv = []string{"/bin/bash", "-lc", "nohup setsid vllm serve --model x & tail -F /vllm.out & while true; do sleep 30; done"}

func TestRestoreEntryArgv_PrefersPodOwnCommand(t *testing.T) {
	main := corev1.Container{Command: wrapperArgv[:2], Args: wrapperArgv[2:]}
	got, err := restoreEntryArgv(main, checkpointstore.Manifest{EntryArgv: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\x00") != strings.Join(wrapperArgv, "\x00") {
		t.Fatalf("shim would exec %v, want the pod's own command %v", got, wrapperArgv)
	}
}

func TestRestoreEntryArgv_FallsBackToManifestForImageEntrypoint(t *testing.T) {
	got, err := restoreEntryArgv(corev1.Container{}, checkpointstore.Manifest{EntryArgv: []string{"vllm", "serve"}})
	if err != nil || strings.Join(got, " ") != "vllm serve" {
		t.Fatalf("pod with no command should fall back to the manifest, got %v err=%v", got, err)
	}
}

func TestRestoreEntryArgv_ErrorsWhenNothingToExec(t *testing.T) {
	if _, err := restoreEntryArgv(corev1.Container{}, checkpointstore.Manifest{Hash: "abc"}); err == nil {
		t.Fatal("no pod command and no recorded EntryArgv must be an error, not a silent empty exec")
	}
}

// Composition: the whole restore patch set, through the real tryL2CacheDir
// with a stubbed L2 backend, must hand the shim the pod's own argv. This is
// the exact shape that exited on dev1 with the captured ["sleep","30"].
func TestTryL2CacheDir_ShimExecsPodOwnCommand(t *testing.T) {
	m := &Mutator{
		CacheDir:      "/opt/nvsnap",
		MainContainer: 0,
		L2Backend: &stubL2Backend{mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-abc"}}},
			VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
		}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "reuse", Namespace: "ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "vllm", Image: "vllm/vllm-openai:v0.20.0",
			Command: wrapperArgv[:2], Args: wrapperArgv[2:],
		}}},
	}
	manifest := checkpointstore.Manifest{Hash: "abc", EntryArgv: []string{"sleep", "30"}, CaptureMethod: "cachedir"}

	patches, err := m.tryL2CacheDir(context.Background(), pod, "abc", manifest)
	if err != nil {
		t.Fatal(err)
	}
	var orig string
	for _, p := range patches {
		if e, ok := p.Value.(corev1.EnvVar); ok && e.Name == "NVSNAP_ORIG_COMMAND" {
			orig = e.Value
		}
	}
	if orig == "" {
		t.Fatal("no NVSNAP_ORIG_COMMAND env patch emitted")
	}
	var argv []string
	if err := json.Unmarshal([]byte(orig), &argv); err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, "\x00") != strings.Join(wrapperArgv, "\x00") {
		t.Fatalf("shim would exec %v; the captured [sleep 30] leaked through instead of the pod's own command", argv)
	}
}
