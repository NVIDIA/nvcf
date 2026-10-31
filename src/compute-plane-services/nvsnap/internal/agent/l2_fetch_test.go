// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

const l2TestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// l2Catalog serves the two catalog reads of an L2 fetch.
func l2Catalog(t *testing.T, id, state string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/checkpoints/" + id:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "hash": l2TestHash, "podNamespace": "fn-ns"})
		case "/api/v1/checkpoints/by-hash/" + l2TestHash + "/pvc-state":
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": l2TestHash, "state": state, "pvc_name": "rox-" + checkpointstore.ShortHash(l2TestHash)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func l2TestAgent(catalogURL string, claims ...*corev1.PersistentVolumeClaim) *Agent {
	kc := fake.NewSimpleClientset()
	for _, c := range claims {
		_ = kc.Tracker().Add(c)
	}
	return &Agent{
		log:       logrus.New(),
		config:    Config{CatalogURL: catalogURL, NodeName: "node-b"},
		l2Backend: &checkpointstore.PerCapturePVCBackend{KubeClient: kc, Namespace: "nvsnap-system"},
	}
}

func TestFetchFromL2NotReadyIsUnavailable(t *testing.T) {
	srv := l2Catalog(t, "ck1", "writing")
	a := l2TestAgent(srv.URL)
	err := a.fetchFromL2(context.Background(), "ck1", t.TempDir())
	if !errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want errL2Unavailable", err)
	}
}

func TestFetchFromL2UnknownCheckpointIsUnavailable(t *testing.T) {
	srv := l2Catalog(t, "ck1", "ready")
	a := l2TestAgent(srv.URL)
	if err := a.fetchFromL2(context.Background(), "other", t.TempDir()); !errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want errL2Unavailable", err)
	}
}

func TestFetchFromL2NoCatalogURLIsUnavailable(t *testing.T) {
	a := l2TestAgent("")
	if err := a.fetchFromL2(context.Background(), "ck1", t.TempDir()); !errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want errL2Unavailable", err)
	}
}

func TestFetchFromL2NoBackendIsUnavailable(t *testing.T) {
	a := &Agent{log: logrus.New(), config: Config{CatalogURL: "http://catalog", NodeName: "n"}}
	if err := a.fetchFromL2(context.Background(), "ck1", t.TempDir()); !errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want errL2Unavailable", err)
	}
}

func TestFetchFromL2CatalogErrorIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := l2TestAgent(srv.URL)
	err := a.fetchFromL2(context.Background(), "ck1", t.TempDir())
	if err == nil || errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want a failure other than errL2Unavailable", err)
	}
}

func TestFetchFromL2MissingClaimIsUnavailable(t *testing.T) {
	srv := l2Catalog(t, "ck1", "ready")
	a := l2TestAgent(srv.URL)
	if err := a.fetchFromL2(context.Background(), "ck1", t.TempDir()); !errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want errL2Unavailable", err)
	}
}

// writeL2Tree lays out a checkpoint as the promote leaves it on the volume.
func writeL2Tree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for p, body := range map[string]string{
		"inventory.img":         "inv",
		"pages-1.img":           strings.Repeat("p", 4096),
		"gpushare/chunk-000000": strings.Repeat("c", 8192),
	} {
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(src, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	return src
}

// checkL2Copy asserts the tree landed in localDir and gpushare is the
// directory that was there before.
func checkL2Copy(t *testing.T, localDir string, before os.FileInfo) {
	t.Helper()
	after, err := os.Stat(filepath.Join(localDir, "gpushare"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("gpushare was replaced, want the existing directory filled in place")
	}
	for p, size := range map[string]int64{"inventory.img": 3, "pages-1.img": 4096, "gpushare/chunk-000000": 8192} {
		fi, err := os.Stat(filepath.Join(localDir, p))
		if err != nil || fi.Size() != size {
			t.Fatalf("%s: size %v err %v, want %d", p, fi, err, size)
		}
	}
	if _, err := os.Stat(filepath.Join(localDir, "lost+found")); !os.IsNotExist(err) {
		t.Fatalf("lost+found copied (err %v)", err)
	}
}

func TestCopyL2TreeFillsExistingDirs(t *testing.T) {
	src := writeL2Tree(t)
	localDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(localDir, "gpushare"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale file from an earlier attempt is overwritten, not appended to.
	if err := os.WriteFile(filepath.Join(localDir, "pages-1.img"), []byte(strings.Repeat("x", 9000)), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(localDir, "gpushare"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := copyL2Tree(context.Background(), src, localDir, logrus.NewEntry(logrus.New()))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3+4096+8192 {
		t.Fatalf("bytes = %d", n)
	}
	checkL2Copy(t, localDir, before)
}

func TestFetchFromL2ReadyCopiesAndReleases(t *testing.T) {
	srv := l2Catalog(t, "ck1", "ready")
	claim := "rox-" + checkpointstore.ShortHash(l2TestHash)
	a := l2TestAgent(srv.URL, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: "fn-ns"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}})
	src := writeL2Tree(t)

	var gotNS, gotClaim, gotName string
	released := false
	orig := l2Attach
	l2Attach = func(_ context.Context, _ *checkpointstore.PerCapturePVCBackend, _ *logrus.Entry, ns, c, name, _ string) (string, func(context.Context) error, error) {
		gotNS, gotClaim, gotName = ns, c, name
		return src, func(context.Context) error { released = true; return nil }, nil
	}
	t.Cleanup(func() { l2Attach = orig })

	localDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(localDir, "gpushare"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(localDir, "gpushare"))
	if err := a.fetchFromL2(context.Background(), "ck1", localDir); err != nil {
		t.Fatal(err)
	}
	if gotNS != "fn-ns" || gotClaim != claim {
		t.Fatalf("attached %s/%s, want fn-ns/%s", gotNS, gotClaim, claim)
	}
	if len(gotName) > 63 || gotName != l2HolderName(l2TestHash, "node-b") {
		t.Fatalf("holder name %q", gotName)
	}
	if !released {
		t.Fatal("holder not released")
	}
	checkL2Copy(t, localDir, before)
}

func TestFetchFromL2ReleasesOnCopyFailure(t *testing.T) {
	srv := l2Catalog(t, "ck1", "ready")
	claim := "rox-" + checkpointstore.ShortHash(l2TestHash)
	a := l2TestAgent(srv.URL, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: "fn-ns"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}})
	released := false
	orig := l2Attach
	l2Attach = func(context.Context, *checkpointstore.PerCapturePVCBackend, *logrus.Entry, string, string, string, string) (string, func(context.Context) error, error) {
		return filepath.Join(t.TempDir(), "missing"), func(context.Context) error { released = true; return nil }, nil
	}
	t.Cleanup(func() { l2Attach = orig })
	err := a.fetchFromL2(context.Background(), "ck1", t.TempDir())
	if err == nil || errors.Is(err, errL2Unavailable) {
		t.Fatalf("err = %v, want a copy failure", err)
	}
	if !released {
		t.Fatal("holder not released after a failed copy")
	}
}

func TestL2HolderNameUniquePerNode(t *testing.T) {
	a, b := l2HolderName(l2TestHash, "node-a"), l2HolderName(l2TestHash, "node-b")
	if a == b || len(a) > 63 {
		t.Fatalf("names %q %q", a, b)
	}
}

func TestL2HolderSpecReadOnly(t *testing.T) {
	h := checkpointstore.NewMountHolder(fake.NewSimpleClientset(), nil, "ns", "h", "n", "rox-x", "", "img", "/host", nil).ReadOnly()
	spec := h.Spec()
	if !spec.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly || !spec.Spec.Containers[0].VolumeMounts[0].ReadOnly {
		t.Fatal("holder does not mount read-only")
	}
}

// The capture's namespace is gone (its function undeployed): the claim is
// minted in the L2 namespace instead.
func TestFetchFromL2CaptureNamespaceGone(t *testing.T) {
	srv := l2Catalog(t, "ck1", "ready")
	a := l2TestAgent(srv.URL)
	src := writeL2Tree(t)
	var gotNS string
	orig := l2Attach
	l2Attach = func(_ context.Context, _ *checkpointstore.PerCapturePVCBackend, _ *logrus.Entry, ns, _, _, _ string) (string, func(context.Context) error, error) {
		gotNS = ns
		return src, func(context.Context) error { return nil }, nil
	}
	t.Cleanup(func() { l2Attach = orig })
	if err := a.fetchFromL2(context.Background(), "ck1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if want := "nvsnap-system"; gotNS != want {
		t.Errorf("attached in %q, want the L2 namespace %q", gotNS, want)
	}
}
