// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

// The tools run inside a placeholder's mount namespace are staged from the
// bundle when it carries them, so a restore never depends on the workload
// image's own tar or mkdir.
func TestStageV2BundleStagesPlaceholderTools(t *testing.T) {
	bundle := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(bundle, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"criu", "cuda-checkpoint.real", "plugins/cuda_plugin.so", "lib/libc.so.6", "tar", "gzip"} {
		write(f)
	}
	a := &Agent{config: Config{CRIUPath: filepath.Join(bundle, "criu")}}
	root := t.TempDir()
	if err := a.stageV2Bundle(root, logrus.NewEntry(logrus.New())); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "criu-bundle")
	for _, name := range []string{"criu", "tar", "gzip", "lib/libc.so.6"} {
		if !fileExists(filepath.Join(staged, name)) {
			t.Errorf("%s not staged", name)
		}
	}
	// A bundle without mkdir (an older base image) stages the rest.
	if fileExists(filepath.Join(staged, "mkdir")) {
		t.Error("mkdir staged although the bundle has none")
	}
}

func TestInContainerToolFallsBackToPATH(t *testing.T) {
	// The test process has no staged bundle at /criu-bundle.
	if fileExists(v2BinDirInContainer + "/tar") {
		t.Skip("this host has a staged bundle")
	}
	if got := inContainerTool(os.Getpid(), "tar"); got != "tar" {
		t.Errorf("inContainerTool = %q, want tar", got)
	}
}
