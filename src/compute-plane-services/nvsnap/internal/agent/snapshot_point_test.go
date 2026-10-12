// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestSnapshotContextNamesEveryVariable(t *testing.T) {
	environ := []byte("POD_NAME=new-0\x00DYN_NAMESPACE=ns-b\x00HF_TOKEN=secret\x00PATH=/bin\x00")
	var doc struct {
		Env map[string]*string `json:"env"`
	}
	if err := json.Unmarshal(snapshotContext(environ), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Env) != len(snapshotContextEnv) {
		t.Fatalf("env has %d names, want %d", len(doc.Env), len(snapshotContextEnv))
	}
	if v := doc.Env["POD_NAME"]; v == nil || *v != "new-0" {
		t.Errorf("POD_NAME = %v, want new-0", v)
	}
	if v := doc.Env["DYN_NAMESPACE"]; v == nil || *v != "ns-b" {
		t.Errorf("DYN_NAMESPACE = %v, want ns-b", v)
	}
	// Unset names are null, so the engine clears what the capture had.
	if v, ok := doc.Env["NATS_SERVER"]; !ok || v != nil {
		t.Errorf("NATS_SERVER = %v (present %v), want null", v, ok)
	}
	for _, n := range []string{"HF_TOKEN", "PATH"} {
		if _, ok := doc.Env[n]; ok {
			t.Errorf("%s is in the context; only the names the engine reads belong there", n)
		}
	}
}

func TestWriteSnapshotReleaseWritesContextThenSentinel(t *testing.T) {
	dir := t.TempDir()
	if err := writeSnapshotRelease(dir, []byte(`{"env":{}}`+"\n")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{snapshotContextFile, snapshotRestoreFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("dir holds %d entries, want the two files only", len(ents))
	}
}

// fakeEngineProc lays out procBase/<pid>/{status,comm,root}.
func fakeEngineProc(t *testing.T, procs map[int]struct {
	ppid int
	comm string
}) (procBase string, roots map[int]string) {
	t.Helper()
	procBase = t.TempDir()
	roots = map[int]string{}
	for pid, p := range procs {
		d := filepath.Join(procBase, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "status"), []byte("Name:\t"+p.comm+"\nPPid:\t"+strconv.Itoa(p.ppid)+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "comm"), []byte(p.comm+"\n"), 0o644)
		roots[pid] = t.TempDir()
		if err := os.Symlink(roots[pid], filepath.Join(d, "root")); err != nil {
			t.Fatal(err)
		}
	}
	return procBase, roots
}

func TestSnapshotPointRootPrefersTheEngine(t *testing.T) {
	procBase, roots := fakeEngineProc(t, map[int]struct {
		ppid int
		comm string
	}{10: {1, "nvsnap-idle"}, 11: {10, "python3"}})
	for _, pid := range []int{10, 11} {
		if err := os.MkdirAll(filepath.Join(roots[pid], "snapshot-control"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, ok := snapshotPointRoot(procBase, 10, "/snapshot-control", false)
	if !ok || root != filepath.Join(procBase, "11", "root") {
		t.Fatalf("root = %q %v, want the restored engine's", root, ok)
	}
	if _, ok := snapshotPointRoot(procBase, 10, "/snapshot-control", true); ok {
		t.Fatal("found a ready file nobody wrote")
	}
	_ = os.WriteFile(filepath.Join(roots[11], "snapshot-control", snapshotReadyFile), []byte("ready"), 0o644)
	if _, ok := snapshotPointRoot(procBase, 10, "/snapshot-control", true); !ok {
		t.Fatal("did not see the ready file")
	}
}

func TestSnapshotPointDir(t *testing.T) {
	gpu := corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}
	pod := func(env ...corev1.EnvVar) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Env: []corev1.EnvVar{{Name: snapshotControlDirEnv, Value: "/elsewhere"}}},
			{Name: "main", Resources: gpu, Env: env},
		}}}
	}
	if c, d := snapshotPointDir(pod(corev1.EnvVar{Name: snapshotControlDirEnv, Value: "/snapshot-control/"})); c != "main" || d != "/snapshot-control" {
		t.Errorf("got %q %q, want main /snapshot-control", c, d)
	}
	for name, env := range map[string][]corev1.EnvVar{
		"none":     nil,
		"emptied":  {{Name: snapshotControlDirEnv}},
		"relative": {{Name: snapshotControlDirEnv, Value: "control"}},
	} {
		if _, d := snapshotPointDir(pod(env...)); d != "" {
			t.Errorf("%s: dir %q, want none", name, d)
		}
	}
}
