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

package checkpointstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// nimVol is the typical NIM-shape volume injected after capture.
var nimVol = VolumeMeta{Name: "nim-cache", MountPath: "/opt/nim/.cache", Type: "emptyDir"}

func TestLocal_Mount_Basic(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)
	if _, err := store.Put(context.Background(), "abc123", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}

	m, err := store.Mount(context.Background(), "abc123", nimVol)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	if m.Volume.Name != m.VolumeMount.Name {
		t.Fatalf("Volume.Name (%q) != VolumeMount.Name (%q)", m.Volume.Name, m.VolumeMount.Name)
	}
	if m.VolumeMount.MountPath != "/opt/nim/.cache" {
		t.Fatalf("MountPath = %q, want /opt/nim/.cache", m.VolumeMount.MountPath)
	}
	if !m.VolumeMount.ReadOnly {
		t.Fatalf("VolumeMount should be read-only")
	}
	if m.Volume.HostPath == nil {
		t.Fatalf("Volume.HostPath is nil; got %+v", m.Volume.VolumeSource)
	}
	tree, err := store.PathFor("abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tree, "volumes", "nim-cache")
	if m.Volume.HostPath.Path != want {
		t.Fatalf("HostPath.Path = %q, want %q (per-volume subdir, not whole tree)", m.Volume.HostPath.Path, want)
	}
	if len(m.InitContainers) != 0 {
		t.Fatalf("Local backend should not need init containers; got %d", len(m.InitContainers))
	}
}

func TestLocal_Mount_RootfsVolumeMapsToRootfsSubdir(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)
	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	rootfsVol := VolumeMeta{Name: "rootfs", MountPath: "/", Type: "rootfs"}
	m, err := store.Mount(context.Background(), "h", rootfsVol)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := store.PathFor("h")
	want := filepath.Join(tree, "rootfs")
	if m.Volume.HostPath.Path != want {
		t.Fatalf("HostPath.Path = %q, want %q (rootfs subdir)", m.Volume.HostPath.Path, want)
	}
}

func TestLocal_Mount_HashNotLocalOK(t *testing.T) {
	// Mount must succeed for a hash whose bytes are NOT on this agent's
	// disk yet — the webhook is answered by ANY agent in the nvsnap-webhook
	// Service, but the data lives only on Manifest.CapturedOnNodes. The
	// Mutator's nodeAffinity injection guarantees the pod schedules
	// onto a node that DOES have the data; kubelet there resolves the
	// emitted hostPath at pod start. Verifying existence in Mount would
	// produce a false negative whenever the webhook lands on the "wrong"
	// agent. See internal/checkpointstore/local.go for the production note.
	store, _ := NewLocal(t.TempDir())
	pm, err := store.Mount(context.Background(), "missinghash", nimVol)
	if err != nil {
		t.Fatalf("Mount of non-local hash returned err: %v (expected nil)", err)
	}
	if pm.Volume.HostPath == nil || pm.Volume.HostPath.Path == "" {
		t.Fatalf("Mount of non-local hash returned empty hostPath: %+v", pm)
	}
}

func TestLocal_Mount_VolumeNameInVolumeID(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)

	longHash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if _, err := store.Put(context.Background(), longHash, []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	m, err := store.Mount(context.Background(), longHash, nimVol)
	if err != nil {
		t.Fatal(err)
	}
	// Name = "nvsnap-" + 16-char hash prefix + "-" + sanitized vol name.
	// 16-hex prefix (64 bits) keeps the total under the RFC1123 label
	// 63-char cap even with long volume suffixes (rootfs-extract-vllm-cache).
	// Multiple Mount calls for the same hash produce non-conflicting
	// Volume.Names via the per-volume suffix.
	want := "nvsnap-abcdef0123456789-nim-cache"
	if m.Volume.Name != want {
		t.Fatalf("Volume.Name = %q, want %q", m.Volume.Name, want)
	}
}

func TestLocal_Mount_TwoVolumesProduceDifferentNames(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)
	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}

	a, err := store.Mount(context.Background(), "h",
		VolumeMeta{Name: "nim-cache", MountPath: "/a", Type: "emptyDir"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Mount(context.Background(), "h",
		VolumeMeta{Name: "hf-cache", MountPath: "/b", Type: "hostPath"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Volume.Name == b.Volume.Name {
		t.Fatalf("two distinct volumes produced same Volume.Name: %q", a.Volume.Name)
	}
}

func TestLocal_Mount_UnsupportedVolumeTypeErrors(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)
	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	_, err := store.Mount(context.Background(), "h",
		VolumeMeta{Name: "x", MountPath: "/x", Type: "unknown"})
	if err == nil {
		t.Fatal("expected error for unsupported volume type")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nim-cache", "nim-cache"},
		{"hf_cache", "hf-cache"},
		{"Cap_Mixed/Case", "cap-mixed-case"},
		{"_leading_under_score_", "leading-under-score"},
		{"...", "v"}, // all non-alphanumeric → fallback
		{"a", "a"},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := sanitizeName(strings.Repeat("a", 100)); got != strings.Repeat("a", 100) {
		t.Errorf("sanitizeName preserved 100 a's, got %q", got)
	}
}

// TestMountVolumeName_StripsPriorNvSnapPrefix covers the warm-recapture
// case (caught 2026-06-05 on GCP-H100-a): a re-capture sees a pod
// whose volumes were named by a *prior* restore — `nvsnap-<oldhash>-X`
// or legacy `nvsnap-cache-<oldhash>-X`. Naively re-wrapping pyramids
// past the 63-char RFC1123 limit and K8s rejects the pod. The fix:
// peel the prior nvsnap-prefix before re-wrapping so the result is
// always `nvsnap-<new-hash>-<short>`.
func TestMountVolumeName_StripsPriorNvSnapPrefix(t *testing.T) {
	newHash := "d104cd457664f84a674a5f59fc4fc182"
	cases := []struct {
		name    string
		volName string
		want    string
	}{
		{"plain", "hf-cache", "nvsnap-d104cd457664f84a-hf-cache"},
		{"with-current-prefix", "nvsnap-7eb6a326fb36b6d6-hf-cache", "nvsnap-d104cd457664f84a-hf-cache"},
		{"with-legacy-prefix", "nvsnap-cache-7eb6a326fb36b6d6-hf-cache", "nvsnap-d104cd457664f84a-hf-cache"},
		// User-named "nvsnap-foo-bar" without a hex segment: don't strip.
		{"not-a-nvsnap-injected-name", "nvsnap-mything-cache", "nvsnap-d104cd457664f84a-nvsnap-mything-cache"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mountVolumeName(newHash, VolumeMeta{Name: tc.volName})
			if got != tc.want {
				t.Errorf("mountVolumeName(%q) = %q, want %q", tc.volName, got, tc.want)
			}
			if len(got) > 63 {
				t.Errorf("name %q is %d chars, exceeds RFC1123 limit (63)", got, len(got))
			}
		})
	}
}
