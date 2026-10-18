// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A reader that exits early fails the pipe instead of leaving its writer
// blocked on a full pipe forever.
func TestRunTarPipe_ReaderExitsEarly(t *testing.T) {
	src := exec.Command("sh", "-c", "yes nvsnap")
	dst := exec.Command("sh", "-c", "head -c 100 >/dev/null; echo extract died >&2; exit 7")
	done := make(chan error, 1)
	go func() { done <- runTarPipe(src, dst, "src", "dst") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "dst") || !strings.Contains(err.Error(), "extract died") {
			t.Errorf("err = %v; want the reader's failure with its stderr", err)
		}
	case <-time.After(20 * time.Second):
		_ = src.Process.Kill()
		t.Fatal("the pipe hung after its reader exited")
	}
}

func TestRunTarPipe_Copies(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	src := exec.Command("sh", "-c", "printf 'a%.0s' $(seq 100000)")
	dst := exec.Command("sh", "-c", "cat > "+out)
	if err := runTarPipe(src, dst, "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() != 100000 {
		t.Errorf("copied %v, %v; want 100000 bytes", fi, err)
	}
}

// A captured diff that carries the agent's bundle (captures before it was
// left out) replays without it.
func TestReplayDiffSourceArgs_LeavesOutTheAgentsDirs(t *testing.T) {
	diff := t.TempDir()
	for _, f := range []string{"criu-bundle/lib/libc.so.6", "opt/nvsnap-imgs/x.img", "opt/app/cache.bin", "etc/app.conf"} {
		p := filepath.Join(diff, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tarOut, err := exec.Command("tar", replayDiffSourceArgs(diff, nil)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	list := exec.Command("tar", "-tf", "-")
	list.Stdin = strings.NewReader(string(tarOut))
	b, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasSuffix(l, "/") {
			files = append(files, l)
		}
	}
	slices.Sort(files)
	if want := []string{"./etc/app.conf", "./opt/app/cache.bin"}; !slices.Equal(files, want) {
		t.Errorf("replayed %v, want %v", files, want)
	}
}
