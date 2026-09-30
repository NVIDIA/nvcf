/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func seedTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, mode int64, body string, link string) {
		h := &tar.Header{Name: name, Mode: mode, Size: int64(len(body))}
		switch {
		case strings.HasSuffix(name, "/"):
			h.Typeflag = tar.TypeDir
		case link != "":
			h.Typeflag = tar.TypeSymlink
			h.Linkname = link
			h.Size = 0
		default:
			h.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	add("torchinductor/", 0o755, "", "")
	add("torchinductor/kernel.so", 0o755, "binary", "")
	add("autotune.json", 0o644, "{}", "")
	add("latest.json", 0o777, "", "autotune.json")
	add("../escape", 0o644, "nope", "")
	add("abs", 0o777, "", "/etc/passwd")
	tw.Close()
	return buf.Bytes()
}

// The seed init waits through 503s, unpacks the tar world-writable, and
// exits 0. It also exits 0 when the agent never becomes ready or refuses,
// because a missing seed only costs a compile.
func TestRunSeed(t *testing.T) {
	var calls int32
	body := seedTar(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(seedTokenHeader) != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if atomic.AddInt32(&calls, 1) < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "cache")
	var out bytes.Buffer
	cfg := seedConfig{URL: srv.URL + "/v1/cache-seed/k", Dest: dest, Token: "tok", Timeout: 20 * time.Second, Interval: 10 * time.Millisecond}
	if code := runSeed(context.Background(), cfg, &out); code != exitReady {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("polled through two 503s, got %d calls", calls)
	}
	b, err := os.ReadFile(filepath.Join(dest, "torchinductor", "kernel.so"))
	if err != nil || string(b) != "binary" {
		t.Fatalf("kernel.so: %v %q", err, b)
	}
	if fi, _ := os.Stat(filepath.Join(dest, "torchinductor", "kernel.so")); fi.Mode().Perm() != 0o777 {
		t.Errorf("executables are world-writable and executable for whichever user the engine runs as: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dest, "autotune.json")); fi.Mode().Perm() != 0o666 {
		t.Errorf("files are world-writable: %v", fi.Mode())
	}
	if l, err := os.Readlink(filepath.Join(dest, "latest.json")); err != nil || l != "autotune.json" {
		t.Errorf("relative symlink kept: %v %q", err, l)
	}
	for _, absent := range []string{filepath.Join(filepath.Dir(dest), "escape"), filepath.Join(dest, "abs")} {
		if _, err := os.Lstat(absent); err == nil {
			t.Errorf("%s must not be created", absent)
		}
	}
	if !strings.Contains(out.String(), "unpacked 2 files") {
		t.Errorf("log: %s", out.String())
	}

	// Never ready within the deadline: still exit 0.
	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer never.Close()
	out.Reset()
	if code := runSeed(context.Background(), seedConfig{URL: never.URL, Dest: t.TempDir(), Timeout: 50 * time.Millisecond, Interval: 10 * time.Millisecond}, &out); code != exitReady || !strings.Contains(out.String(), "not available within the deadline") {
		t.Errorf("deadline: exit %d log %s", code, out.String())
	}
	// Refused: exit 0 at once.
	out.Reset()
	if code := runSeed(context.Background(), seedConfig{URL: srv.URL, Dest: t.TempDir(), Token: "wrong", Timeout: time.Second, Interval: 10 * time.Millisecond}, &out); code != exitReady || !strings.Contains(out.String(), "refused") {
		t.Errorf("refused: exit %d log %s", code, out.String())
	}
}
