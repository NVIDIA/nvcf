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
)

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
