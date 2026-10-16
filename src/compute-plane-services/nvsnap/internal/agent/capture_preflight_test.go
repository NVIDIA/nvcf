// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func findingFor(fs []PreflightFinding, check string) (PreflightFinding, bool) {
	for _, f := range fs {
		if f.Check == check {
			return f, true
		}
	}
	return PreflightFinding{}, false
}

func TestPodPreflight(t *testing.T) {
	a := &Agent{config: Config{Webhook: WebhookConfig{CaptureLivenessBudget: 300}}}
	share, always := true, corev1.ContainerRestartPolicyAlways
	execProbe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}, PeriodSeconds: 10, FailureThreshold: 30}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w"}, Spec: corev1.PodSpec{
		ShareProcessNamespace: &share,
		InitContainers:        []corev1.Container{{Name: "beacon", RestartPolicy: &always}},
		Containers: []corev1.Container{
			{Name: "main", ReadinessProbe: execProbe, Env: []corev1.EnvVar{{Name: "LD_PRELOAD", Value: webhook.GPUShareLibPath}}},
			{Name: "sidecar", LivenessProbe: execProbe},
		},
	}}
	fs := a.podPreflight(pod, "main", true)
	if len(blocking(fs)) != 0 {
		t.Fatalf("no blocking findings expected: %+v", fs)
	}
	for check, sev := range map[string]string{"shared-pid-namespace": preflightInfo, "native-sidecar": preflightInfo} {
		if f, ok := findingFor(fs, check); !ok || f.Severity != sev {
			t.Errorf("%s: %+v %v", check, f, ok)
		}
	}
	var mainExec, sideExec string
	for _, f := range fs {
		if f.Check == "exec-probe" && f.Container == "main" {
			mainExec = f.Severity
		}
		if f.Check == "exec-probe" && f.Container == "sidecar" {
			sideExec = f.Severity
		}
	}
	if mainExec != preflightWarn || sideExec != preflightInfo {
		t.Errorf("exec probes: dumped container %q, sidecar %q; want warn, info", mainExec, sideExec)
	}

	// A rank without gpushare, a probe too tight for the capture.
	tight := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w2"}, Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "main", LivenessProbe: &corev1.Probe{PeriodSeconds: 10, FailureThreshold: 6}},
	}}}
	b := blocking(a.podPreflight(tight, "main", true))
	if len(b) != 2 {
		t.Fatalf("want liveness and gpushare to block, got %+v", b)
	}
}

func TestGhostFindings(t *testing.T) {
	ghosts := map[string]ghostFile{
		"1:1": {path: "/dev/shm/kv.mmap", allocated: 600 << 30},
		"1:2": {path: "/dev/shm/small", allocated: 4096},
		"1:3": {path: "/dev/shm/tier", allocated: 2 << 30},
	}
	fs := ghostFindings("w", "main", ghosts, 20<<30, 512<<30)
	var block, warn int
	for _, f := range fs {
		switch {
		case f.Check == "ghost-file" && f.Severity == preflightBlock:
			block++
			if !strings.Contains(f.Detail, "/dev/shm/kv.mmap") {
				t.Errorf("block names the file: %s", f.Detail)
			}
		case f.Check == "ghost-file" && f.Severity == preflightWarn:
			warn++
		}
	}
	if block != 1 || warn != 1 {
		t.Errorf("block=%d warn=%d, want 1 and 1: %+v", block, warn, fs)
	}
	if f, ok := findingFor(fs, "memory"); !ok || !strings.Contains(f.Detail, "20.0 GiB") {
		t.Errorf("memory finding: %+v", f)
	}
}

// A deleted file this process holds open is found, keyed by its inode,
// with its allocated size.
func TestDeletedFiles(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "held"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	ghosts := map[string]ghostFile{}
	deletedFiles("/proc", os.Getpid(), ghosts)
	found := false
	for _, g := range ghosts {
		if g.path == f.Name() {
			found = true
			if g.apparently != 1<<20 || g.allocated <= 0 {
				t.Errorf("sizes: %+v", g)
			}
		}
	}
	if !found {
		t.Fatalf("held deleted file not found among %v", ghosts)
	}
}

func TestCgroupPIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("45\n591\n\n675\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := cgroupPIDs(dir); err != nil || len(got) != 3 || got[0] != 45 || got[2] != 675 {
		t.Errorf("got %v %v", got, err)
	}
}

// The webhook's pid floor is what the agent writes.
func TestPIDReserveFloorMatchesTheAgent(t *testing.T) {
	if webhook.PIDReserveFloor != reservedPIDFloor*2 {
		t.Fatalf("webhook.PIDReserveFloor %d, agent writes %d", webhook.PIDReserveFloor, reservedPIDFloor*2)
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
		{Name: webhook.PIDReserveInit, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if !initRunning(pod, webhook.PIDReserveInit) || initRunning(pod, "other") {
		t.Error("initRunning")
	}
}
