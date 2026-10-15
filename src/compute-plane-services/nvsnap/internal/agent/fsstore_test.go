/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

// makeFakeCheckpointDir builds a minimal CRIU-shaped dump tree at root.
// inventory.img is the "completion marker" hasCheckpoint probes for.
func makeFakeCheckpointDir(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	files := map[string][]byte{
		"inventory.img":     []byte("CRIU INVENTORY"),
		"pages-1.img":       randomBytes(t, 1*1024*1024),
		"core-1.img":        []byte("core data"),
		"subdir/nested.img": []byte("nested"),
	}
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(root, rel), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestFSStore_NilSafe(t *testing.T) {
	var f *fsStore
	if f.hasCheckpoint("anything") {
		t.Errorf("nil fsStore.hasCheckpoint should be false")
	}
	if err := f.fetchToLocal(context.Background(), "id", "/tmp/x"); err == nil {
		t.Errorf("nil fsStore.fetchToLocal should error, got nil")
	}
	if err := f.publish(context.Background(), "id", "/tmp/x"); err == nil {
		t.Errorf("nil fsStore.publish should error, got nil")
	}
}

func TestFSStore_EmptyBasePathReturnsNil(t *testing.T) {
	if got := newFSStore("", logrus.New()); got != nil {
		t.Errorf("newFSStore(\"\") should return nil, got %+v", got)
	}
}

func TestFSStore_HasCheckpoint(t *testing.T) {
	base := t.TempDir()
	f := newFSStore(base, logrus.New())
	if f.hasCheckpoint("ckpt-1") {
		t.Errorf("missing checkpoint should be absent")
	}
	// Stand up a fake checkpoint inside FSStore.
	if err := os.MkdirAll(filepath.Join(base, "ckpt-1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// inventory.img is the completion marker — must exist AND be non-empty.
	if err := os.WriteFile(filepath.Join(base, "ckpt-1", "inventory.img"), []byte{}, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if f.hasCheckpoint("ckpt-1") {
		t.Errorf("empty inventory.img should not count as present")
	}
	if err := os.WriteFile(filepath.Join(base, "ckpt-1", "inventory.img"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write non-empty: %v", err)
	}
	if !f.hasCheckpoint("ckpt-1") {
		t.Errorf("non-empty inventory.img should count as present")
	}
}

func TestFSStore_PublishFetchRoundtrip(t *testing.T) {
	fsBase := t.TempDir()
	srcDir := t.TempDir() // pretends to be /var/lib/nvsnap/checkpoints/<id> on capture node
	dstDir := t.TempDir() // pretends to be /var/lib/nvsnap/checkpoints/<id> on a different node

	makeFakeCheckpointDir(t, srcDir)
	f := newFSStore(fsBase, logrus.New())

	if err := f.publish(context.Background(), "ckpt-rt", srcDir); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !f.hasCheckpoint("ckpt-rt") {
		t.Fatalf("hasCheckpoint after publish should be true")
	}

	// Staging dir should not linger after a successful publish.
	if _, err := os.Stat(filepath.Join(fsBase, "ckpt-rt.partial")); !os.IsNotExist(err) {
		t.Errorf("staging dir should be cleaned up, got: %v", err)
	}

	if err := f.fetchToLocal(context.Background(), "ckpt-rt", dstDir); err != nil {
		t.Fatalf("fetchToLocal: %v", err)
	}

	// Every file in src should now exist in dst with identical bytes.
	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		want, _ := os.ReadFile(path)
		got, gerr := os.ReadFile(filepath.Join(dstDir, rel))
		if gerr != nil {
			t.Errorf("missing in dst: %s (%v)", rel, gerr)
			return nil
		}
		if !equalBytes(got, want) {
			t.Errorf("content mismatch for %s", rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Errorf("walk src for verification: %v", walkErr)
	}
}

func TestFSStore_PublishOverwrites(t *testing.T) {
	fsBase := t.TempDir()
	src1 := t.TempDir()
	src2 := t.TempDir()
	makeFakeCheckpointDir(t, src1)
	makeFakeCheckpointDir(t, src2)
	// Tweak one file in src2 so we can tell the two apart.
	if err := os.WriteFile(filepath.Join(src2, "inventory.img"), []byte("VERSION 2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	f := newFSStore(fsBase, logrus.New())
	if err := f.publish(context.Background(), "ckpt-ovr", src1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if err := f.publish(context.Background(), "ckpt-ovr", src2); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	// Latest publish should win.
	got, err := os.ReadFile(filepath.Join(fsBase, "ckpt-ovr", "inventory.img"))
	if err != nil {
		t.Fatalf("read after re-publish: %v", err)
	}
	if string(got) != "VERSION 2" {
		t.Errorf("re-publish did not replace prior copy: got %q", string(got))
	}
}

func TestFSStore_FetchMissingReturnsError(t *testing.T) {
	f := newFSStore(t.TempDir(), logrus.New())
	dst := t.TempDir()
	err := f.fetchToLocal(context.Background(), "nonexistent", dst)
	if err == nil {
		t.Errorf("fetch of missing checkpoint should error, got nil")
	}
}

func TestFSStore_PublishBadSourceErrors(t *testing.T) {
	f := newFSStore(t.TempDir(), logrus.New())
	if err := f.publish(context.Background(), "ckpt-x", "/nonexistent/source/path"); err == nil {
		t.Errorf("publish from nonexistent source should error, got nil")
	}
}
