// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func bundleAgent(t *testing.T) *Agent {
	t.Helper()
	root := t.TempDir()
	return &Agent{log: logrus.New(), config: Config{CheckpointDir: t.TempDir(), Webhook: WebhookConfig{HostBundleRoot: root}}}
}

func stageVersion(t *testing.T, a *Agent, id string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(a.bundleVersionsDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "libnvsnap_gpushare.so"), []byte("lib-"+id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../libnvsnap_gpushare.so", filepath.Join(dir, "lib", "link")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCurrentBundle(t *testing.T) {
	a := bundleAgent(t)
	if a.currentBundle() != "" {
		t.Fatal("no staging yet: no current version")
	}
	stageVersion(t, a, "8ab0d751c1f6277b", 0)
	for content, want := range map[string]string{"8ab0d751c1f6277b\n": "8ab0d751c1f6277b", "../etc": "", "v9-unstaged": ""} {
		if err := os.WriteFile(filepath.Join(a.bundleRoot(), "current-bundle"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := a.currentBundle(); got != want {
			t.Errorf("current-bundle %q: got %q, want %q (an unstaged or invalid id is none)", content, got, want)
		}
	}
}

// A version served by one agent arrives whole, modes and links kept.
func TestBundleTarRoundTrip(t *testing.T) {
	src := bundleAgent(t)
	stageVersion(t, src, "aaaa", 0)
	r := mux.NewRouter()
	r.HandleFunc("/v1/bundles/{id}", src.bundleHandler)
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp := get(t, srv.URL+"/v1/bundles/aaaa")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serve: %v", resp.Status)
	}
	defer func() { _ = resp.Body.Close() }()
	dst := t.TempDir()
	if err := readBundleTar(resp.Body, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "libnvsnap_gpushare.so")); err != nil || string(b) != "lib-aaaa" {
		t.Fatalf("library: %q %v", b, err)
	}
	if st, _ := os.Stat(filepath.Join(dst, "libnvsnap_gpushare.so")); st.Mode().Perm()&0o100 == 0 {
		t.Error("exec bit lost")
	}
	if l, err := os.Readlink(filepath.Join(dst, "lib", "link")); err != nil || l != "../libnvsnap_gpushare.so" {
		t.Errorf("symlink: %q %v", l, err)
	}
	for _, bad := range []string{"/v1/bundles/zzzz", "/v1/bundles/0123"} {
		resp := get(t, srv.URL+bad)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s served", bad)
		}
	}
}

// A fetched version digests the same as the served one under the agent's
// umask: modes it would strip (group write on 0775) are kept, so the
// fetch passes the recorded digest.
func TestBundleTarKeepsModesPastUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	src := bundleAgent(t)
	stageVersion(t, src, "bbbb", 0)
	dir := filepath.Join(src.bundleVersionsDir(), "bbbb")
	if err := os.Mkdir(filepath.Join(dir, "plugins"), 0o775); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"cuda-checkpoint", "plugins/cuda_plugin.so"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"plugins", "cuda-checkpoint", "plugins/cuda_plugin.so"} {
		if err := os.Chmod(filepath.Join(dir, p), 0o775); err != nil {
			t.Fatal(err)
		}
	}
	want, err := bundleDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeBundleTar(&buf, dir); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := readBundleTar(&buf, dst); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"plugins", "cuda-checkpoint", "plugins/cuda_plugin.so"} {
		if st, err := os.Stat(filepath.Join(dst, p)); err != nil || st.Mode().Perm() != 0o775 {
			t.Errorf("%s: mode %v %v, want 0775", p, st.Mode().Perm(), err)
		}
	}
	if got, err := bundleDigest(dst); err != nil || got != want {
		t.Errorf("fetched digest %s %v, want the served one %s", got, err, want)
	}
}

// A symlink the stream creates cannot carry a later entry outside the
// bundle.
func TestReadBundleTarConfinesSymlinks(t *testing.T) {
	outside := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: outside}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "lib/planted.so", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := readBundleTar(&buf, t.TempDir()); err == nil {
		t.Error("a file was written through a symlink out of the bundle")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.so")); err == nil {
		t.Fatal("a file landed outside the bundle")
	}
}

// A link the stream makes inside the bundle is not followed by a later
// entry: "x -> ." and then a directory "x" would otherwise change the mode
// of the extraction root itself. A link no later entry goes through is
// kept.
func TestReadBundleTarRefusesEntriesThroughLinks(t *testing.T) {
	for name, entries := range map[string][]tar.Header{
		"directory through a link": {{Name: "x", Typeflag: tar.TypeSymlink, Linkname: "."}, {Name: "x", Mode: 0o777, Typeflag: tar.TypeDir}},
		"file under a link":        {{Name: "d", Mode: 0o755, Typeflag: tar.TypeDir}, {Name: "x", Typeflag: tar.TypeSymlink, Linkname: "d"}, {Name: "x/f", Mode: 0o644, Typeflag: tar.TypeReg}},
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i := range entries {
			if err := tw.WriteHeader(&entries[i]); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		if err := os.Chmod(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := readBundleTar(&buf, dst); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: %v, want it refused", name, err)
		}
		if st, _ := os.Stat(dst); st.Mode().Perm() != 0o700 {
			t.Errorf("%s: the extraction root became %v", name, st.Mode().Perm())
		}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []tar.Header{{Name: "lib", Mode: 0o755, Typeflag: tar.TypeDir}, {Name: "lib/link", Typeflag: tar.TypeSymlink, Linkname: "../x.so"}} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := readBundleTar(&buf, dst); err != nil {
		t.Fatalf("a plain link was refused: %v", err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "lib", "link")); err != nil || l != "../x.so" {
		t.Errorf("link %q %v", l, err)
	}
}

func TestReadBundleTarRejectsEscapes(t *testing.T) {
	for _, name := range []string{"../../escape.so", "/etc/escape.so", ".."} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		err := readBundleTar(&buf, dst)
		if err == nil || !strings.Contains(err.Error(), "leaves the bundle") {
			t.Errorf("%q: %v, want it refused for leaving the bundle", name, err)
		}
	}
}

// Unused versions go; the current one, those pods mount, those
// checkpoints recorded and young ones stay.
func TestReapBundleVersions(t *testing.T) {
	a := bundleAgent(t)
	for _, id := range []string{"c0", "d0", "e0", "f0"} {
		stageVersion(t, a, id, 2*time.Hour)
	}
	stageVersion(t, a, "a0", time.Minute)
	stageVersion(t, a, ".a1.fetch-x", 2*time.Hour)
	if err := os.WriteFile(filepath.Join(a.bundleRoot(), "current-bundle"), []byte("c0"), 0o644); err != nil {
		t.Fatal(err)
	}
	ck := filepath.Join(a.config.CheckpointDir, "ck1")
	if err := os.MkdirAll(ck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ck, "metadata.json"), []byte(`{"bundle":"e0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pod := corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "nvsnap-gpushare-lib",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: a.bundleRoot() + "/versions/d0"}}}}}}
	a.reapBundleVersions([]corev1.Pod{pod}, time.Now())
	for id, want := range map[string]bool{"c0": true, "d0": true, "e0": true, "a0": true, "f0": false, ".a1.fetch-x": false} {
		_, err := os.Stat(filepath.Join(a.bundleVersionsDir(), id))
		if got := err == nil; got != want {
			t.Errorf("%s kept = %v, want %v", id, got, want)
		}
	}
}

func TestValidBundleID(t *testing.T) {
	for id, want := range map[string]bool{"8ab0d751c1f6277b": true, "v0.2.76-mv152": true, "": false, "../x": false, ".hidden": false, "a/b": false, "a b": false, "..": false} {
		if validBundleID(id) != want {
			t.Errorf("%q: want %v", id, want)
		}
	}
	_ = webhook.BundleMeta
}
