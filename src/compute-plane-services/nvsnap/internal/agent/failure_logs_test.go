// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestKeepFailureLogs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "abc__20261016-171936")
	for name, body := range map[string]string{"dump.log": "Error (criu/x.c:1): boom", "pages-1.img": "image", "sub/restore.log": "r"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keepFailureLogs(root, dir, logrus.NewEntry(logrus.New()))
	kept := filepath.Join(root, failureLogsDir, "abc__20261016-171936")
	if b, err := os.ReadFile(filepath.Join(kept, "dump.log")); err != nil || string(b) != "Error (criu/x.c:1): boom" {
		t.Fatalf("dump.log not kept: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(kept, "sub", "restore.log")); err != nil {
		t.Errorf("a nested log was not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(kept, "pages-1.img")); !os.IsNotExist(err) {
		t.Errorf("an image was kept with the logs: %v", err)
	}
}

func TestPruneFailureLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := range 5 {
		p := filepath.Join(dir, fmt.Sprintf("c%d", i))
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pruneFailureLogs(dir, 3)
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 || ents[0].Name() != "c2" || ents[2].Name() != "c4" {
		t.Errorf("kept %v, want the three newest", ents)
	}
}
