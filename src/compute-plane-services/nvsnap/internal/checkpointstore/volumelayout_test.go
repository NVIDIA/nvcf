/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Tests for the captured-tree layout contract.
//
// The capture and restore sides used to derive a volume's on-disk location
// independently -- the writer from CaptureSource.DstSubpath, every reader by
// inferring from VolumeMeta.Type. cachedir mode writes its bytes to the tree
// root, but its Type is the underlying emptyDir, so readers looked under
// volumes/cachedir/ and the restore pod waited out its readiness timeout on a
// mount that could never appear. These tests pin the contract that replaced
// the inference.
package checkpointstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVolumeSubpathPrefersRecordedSubpath(t *testing.T) {
	// The recorded subpath wins over whatever Type would have implied.
	// This is the cachedir case: type emptyDir, bytes at the tree root.
	vol := VolumeMeta{
		Name:      "cachedir",
		MountPath: "/opt/nvsnap",
		Type:      "emptyDir",
		Subpath:   SubpathAt(""),
	}
	got, ok := VolumeSubpath(vol)
	if !ok {
		t.Fatal("VolumeSubpath: ok = false for a volume with a recorded subpath")
	}
	if got != "" {
		t.Errorf("VolumeSubpath = %q, want %q (the tree root)", got, "")
	}
}

// The empty subpath is a location, not an absence. Before VolumeSubpath
// returned a bool, "" doubled as "cannot derive" and Local.Mount rejected
// it -- so a root-mounted capture could not be expressed at all.
func TestVolumeSubpathDistinguishesRootFromUnresolvable(t *testing.T) {
	root, ok := VolumeSubpath(VolumeMeta{Name: "cachedir", Type: "emptyDir", Subpath: SubpathAt("")})
	if !ok || root != "" {
		t.Errorf("tree root: got (%q, %v), want (%q, true)", root, ok, "")
	}
	if _, ok := VolumeSubpath(VolumeMeta{Name: "x", Type: "somethingElse"}); ok {
		t.Error("unknown volume type: ok = true, want false")
	}
	// A user-data volume with no name has nothing to index by.
	if _, ok := VolumeSubpath(VolumeMeta{Type: "emptyDir"}); ok {
		t.Error("nameless emptyDir: ok = true, want false")
	}
}

// Manifests written before Subpath existed must keep resolving. Only the
// layouts that inference ever described correctly are covered: rootfs,
// rootfs-extract and user-data volumes.
func TestVolumeSubpathLegacyInference(t *testing.T) {
	for _, tc := range []struct {
		name string
		vol  VolumeMeta
		want string
	}{
		{"rootfs", VolumeMeta{Name: "rootfs", Type: "rootfs"}, "rootfs"},
		{"rootfs-extract", VolumeMeta{Type: "rootfs-extract", MountPath: "/root/.cache/huggingface"},
			"rootfs/root/.cache/huggingface"},
		{"emptyDir", VolumeMeta{Name: "scratch", Type: "emptyDir"}, "volumes/scratch"},
		{"hostPath", VolumeMeta{Name: "models", Type: "hostPath"}, "volumes/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := VolumeSubpath(tc.vol)
			if !ok {
				t.Fatalf("ok = false for legacy %s", tc.name)
			}
			if got != tc.want {
				t.Errorf("VolumeSubpath = %q, want %q", got, tc.want)
			}
		})
	}
}

// A capture whose tree contradicts its manifest must not commit. Without
// this the bytes land on disk, the capture reports success, and the error
// only surfaces as a restore that never becomes ready.
func TestPutRejectsManifestTreeMismatch(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "weights.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	l := &Local{root: root}
	// Writer puts the bytes at the tree root; the manifest claims the
	// volume lives under volumes/cachedir/. Exactly the cachedir bug.
	_, err := l.Put(context.Background(), "deadbeef",
		[]CaptureSource{{SrcPath: srcDir, DstSubpath: ""}},
		Manifest{Volumes: []VolumeMeta{{
			Name:      "cachedir",
			MountPath: "/opt/nvsnap",
			Type:      "emptyDir", // no Subpath -> infers volumes/cachedir
		}}},
	)
	if err == nil {
		t.Fatal("Put committed a capture whose tree contradicts its manifest")
	}
	if !strings.Contains(err.Error(), "cachedir") {
		t.Errorf("error should name the offending volume, got: %v", err)
	}
	// And nothing may be left behind for a restore to find.
	if _, statErr := os.Stat(filepath.Join(root, "deadbeef")); statErr == nil {
		t.Error("rejected capture was still committed to the store")
	}
}

// The corrected cachedir shape commits and round-trips to the tree root.
func TestPutAcceptsCacheDirAtTreeRoot(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(filepath.Join(srcDir, "model"), 0o755); err != nil {
		t.Fatal(err)
	}

	l := &Local{root: root}
	m, err := l.Put(context.Background(), "cafebabe",
		[]CaptureSource{{SrcPath: srcDir, DstSubpath: ""}},
		Manifest{Volumes: []VolumeMeta{{
			Name:      "cachedir",
			MountPath: "/opt/nvsnap",
			Type:      "emptyDir",
			Subpath:   SubpathAt(""),
		}}},
	)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := VolumeSubpath(m.Volumes[0]); !ok || got != "" {
		t.Errorf("round-tripped subpath = (%q, %v), want (%q, true)", got, ok, "")
	}
	// The bytes must be reachable at the location the manifest advertises.
	if _, err := os.Stat(filepath.Join(root, "cafebabe", "tree", "model")); err != nil {
		t.Errorf("cachedir contents not at the tree root: %v", err)
	}
}
