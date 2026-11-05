// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeBedrockPeer is a stand-in for bedrock-peer: "serve" runs until killed,
// "fetch" records its arguments and prints a result line, or, with a
// source of "hang", waits to be stopped.
const fakeBedrockPeer = `#!/bin/sh
case "$1" in
serve) exec sleep 600 ;;
fetch)
  shift
  printf '%s\n' "$@" > "$FAKE_ARGS"
  case "$*" in *hang*) exec sleep 600 ;; esac
  echo "progress line"
  echo '{"ok":true,"exit_code":0,"files":2,"bytes":2048,"seconds":0.1,"version":"fake"}'
  ;;
esac`

// The rails pod's real script, run with a stand-in bedrock-peer: a job
// handed over through the jobs directory runs beside the server with its
// arguments intact, its result comes back through the files the agent
// reads, and a cancelled job is stopped while the server keeps running.
func TestRailsPodScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "bedrock-peer"), []byte(fakeBedrockPeer), 0o755); err != nil {
		t.Fatal(err)
	}
	ckpt := filepath.Join(dir, "ckpt")
	jobs := filepath.Join(ckpt, railsJobsDir)
	argsFile := filepath.Join(dir, "fetch-args")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", railsPodScript(jobs), "bedrock-peer", "serve", "--root", ckpt)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_ARGS="+argsFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()

	kc := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "rails", Namespace: "nvsnap-system"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	a := &Agent{log: logrus.New(), kubeClient: kc}
	a.config.CheckpointDir = ckpt
	a.config.Rails.Namespace = "nvsnap-system"
	defer func(d time.Duration) { railsJobPoll = d }(railsJobPoll)
	railsJobPoll = 20 * time.Millisecond

	args := []string{"fetch", "--source", "10.0.0.1:9890", "--prefix", "ck1", "--dest", "/data/ck1", "--json"}
	j, err := a.submitRailsJob("ck1", args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.waitRailsJob(t.Context(), "rails", j, 20*time.Second)
	if err != nil || res.Files != 2 || res.Version != "fake" {
		t.Fatalf("job result %+v, %v", res, err)
	}
	got, _ := os.ReadFile(argsFile)
	if strings.TrimSpace(string(got)) != strings.Join(args[1:], "\n") {
		t.Errorf("bedrock-peer got arguments %q, want %q", got, args[1:])
	}
	j.remove()

	hang, err := a.submitRailsJob("ck2", []string{"fetch", "--source", "hang"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.waitRailsJob(t.Context(), "rails", hang, 500*time.Millisecond); err == nil {
		t.Fatal("a fetch that never ends reported a result")
	}
	// The loop stops it: its exit code lands, and the server runs on.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(hang.path(".exit")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancelled fetch was not stopped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the rails pod's script ended with the cancelled fetch: %v", err)
	}
	hang.remove()
}

// An argument the loop could not pass through intact is refused.
func TestSubmitRailsJobRefusesBadArguments(t *testing.T) {
	a := &Agent{}
	a.config.CheckpointDir = t.TempDir()
	for _, bad := range [][]string{{"fetch", ""}, {"fetch", "a\nb"}} {
		if _, err := a.submitRailsJob("ck", bad); err == nil {
			t.Errorf("arguments %q accepted", bad)
		}
	}
}
