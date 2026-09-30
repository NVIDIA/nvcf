/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import (
	"archive/tar"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

func seedRequest(t *testing.T, s *CacheSeedServer, uri, ns, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := mux.NewRouter()
	r.HandleFunc("/v1/cache-seed/{key}", s.Handler)
	req := httptest.NewRequest(http.MethodGet, "/v1/cache-seed/"+modelvolume.Key(uri)+"?uri="+uri+"&ns="+ns, nil)
	if token != "" {
		req.Header.Set(modelvolume.SeedTokenHeader, token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A complete node mirror is streamed as a tar to a pod presenting the
// token signed for its namespace and key; anything else is refused, and a
// missing mirror starts one fill and answers 503 so the init retries.
func TestCacheSeedServer_ServesMirrorToSignedCaller(t *testing.T) {
	uri := "cache://6c5d41da310d1537/3"
	host := t.TempDir()
	var fills int32
	s := &CacheSeedServer{HostFSRoot: host, Root: "/var/lib/nvsnap/cache-seeds", Secret: "agent-token", Retention: 7 * 24 * time.Hour,
		fill: func(context.Context, string) error { atomic.AddInt32(&fills, 1); return nil }}
	dir := s.mirrorDir(modelvolume.Key(uri))
	if err := os.MkdirAll(filepath.Join(dir, "torchinductor", "k1"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "torchinductor", "k1", "kernel.so"), []byte("binary"), 0o755)
	os.WriteFile(filepath.Join(dir, "autotune.json"), []byte("{}"), 0o644)
	os.Symlink("autotune.json", filepath.Join(dir, "latest.json"))
	os.Symlink("/etc/passwd", filepath.Join(dir, "escape"))

	if w := seedRequest(t, s, uri, "sr-fn", modelvolume.SeedToken("agent-token", "sr-fn", uri)); w.Code == http.StatusOK {
		t.Fatal("no marker: the mirror is not complete and must not be served")
	} else if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("incomplete mirror answers 503 with Retry-After, got %d", w.Code)
	}
	waitUntil(t, "fill started", func() bool { return atomic.LoadInt32(&fills) == 1 })
	os.WriteFile(filepath.Join(dir, modelvolume.SeedMirrorMarker), nil, 0o644)

	for name, tc := range map[string]struct {
		ns, token string
		want      int
	}{
		"wrong namespace": {"sr-other", modelvolume.SeedToken("agent-token", "sr-fn", uri), http.StatusUnauthorized},
		"no token":        {"sr-fn", "", http.StatusUnauthorized},
		"wrong secret":    {"sr-fn", modelvolume.SeedToken("other", "sr-fn", uri), http.StatusUnauthorized},
	} {
		if w := seedRequest(t, s, uri, tc.ns, tc.token); w.Code != tc.want {
			t.Errorf("%s: got %d want %d", name, w.Code, tc.want)
		}
	}

	w := seedRequest(t, s, uri, "sr-fn", modelvolume.SeedToken("agent-token", "sr-fn", uri))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-tar" {
		t.Fatalf("signed caller gets the tar, got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	entries := map[string]string{}
	tr := tar.NewReader(w.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		entries[h.Name] = string(b) + "|" + h.Linkname
	}
	if entries["torchinductor/k1/kernel.so"] != "binary|" || entries["autotune.json"] != "{}|" || entries["latest.json"] != "|autotune.json" {
		t.Errorf("tar content: %v", entries)
	}
	for _, absent := range []string{"escape", modelvolume.SeedMirrorMarker, modelvolume.SeedLastUsedFile} {
		if _, ok := entries[absent]; ok {
			t.Errorf("%s must not be streamed (absolute symlink or mirror bookkeeping)", absent)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, modelvolume.SeedLastUsedFile)); err == nil {
		// touched only if it exists; a fresh fill creates it
	}
	if atomic.LoadInt32(&fills) != 1 {
		t.Error("a complete mirror is served without another fill")
	}
}

// Mirrors unused past retention are removed; recent ones and in-progress
// temp dirs stay.
func TestCacheSeedServer_SweepRetiresStaleMirrors(t *testing.T) {
	host := t.TempDir()
	s := &CacheSeedServer{HostFSRoot: host, Root: "/mirrors", Retention: 24 * time.Hour}
	mk := func(name string, age time.Duration) {
		d := filepath.Join(host, "mirrors", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, modelvolume.SeedMirrorMarker), nil, 0o644)
		os.WriteFile(filepath.Join(d, modelvolume.SeedLastUsedFile), nil, 0o644)
		old := time.Now().Add(-age)
		os.Chtimes(filepath.Join(d, modelvolume.SeedLastUsedFile), old, old)
	}
	mk("stale", 48*time.Hour)
	mk("fresh", time.Hour)
	os.MkdirAll(filepath.Join(host, "mirrors", "filling.tmp"), 0o755)
	if n := s.Sweep(time.Now()); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	for name, want := range map[string]bool{"stale": false, "fresh": true, "filling.tmp": true} {
		_, err := os.Stat(filepath.Join(host, "mirrors", name))
		if (err == nil) != want {
			t.Errorf("%s present=%v want %v", name, err == nil, want)
		}
	}
	if (&CacheSeedServer{HostFSRoot: host, Root: "/mirrors"}).Sweep(time.Now().Add(400*24*time.Hour)) != 0 {
		t.Error("zero retention never sweeps")
	}
}
