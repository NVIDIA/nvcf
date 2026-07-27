/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestValidPathSegment(t *testing.T) {
	ok := []string{
		"a1b2c3d4__20260724-120000",            // what buildCheckpointID emits
		"deadbeef",                             // capture hash
		"3f2a1c9e-0b45-4d8f-9a11-77c0de1234ab", // pod UID
		"v0.2.2",
	}
	for _, s := range ok {
		if err := validPathSegment("id", s); err != nil {
			t.Errorf("validPathSegment(%q) = %v, want nil", s, err)
		}
	}

	// Each of these reaches filepath.Join on a hostPath directory in an
	// agent that runs privileged, so each is a read or write outside the
	// checkpoint tree as root on the node.
	bad := []string{
		"",
		"..",
		"../../etc",
		"../../../var/lib/kubelet",
		"a/b",
		"/etc/passwd",
		".hidden",
		"id\x00truncate",
		"id with spaces",
		"id;rm -rf /",
		strings.Repeat("a", 200),
	}
	for _, s := range bad {
		if err := validPathSegment("id", s); err == nil {
			t.Errorf("validPathSegment(%q) = nil, want error", s)
		}
	}
}

func TestPathVarGuardRejectsTraversal(t *testing.T) {
	r := mux.NewRouter()
	r.Use(pathVarGuard)
	reached := false
	r.HandleFunc("/v1/checkpoints/{id}/manifest", func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// Drive the guard with the vars already bound. Going through a URL
	// would only prove mux's own path normalization works: it 301s
	// "/v1/checkpoints/../x" before any middleware runs. That redirect is
	// not what protects us -- it is route-matching behavior we do not
	// control, and the same handlers are reachable with vars set from
	// other sources. Assert the guard rejects a bad var on its own.
	guarded := pathVarGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	for _, id := range []string{"..", "../../etc", "a/b", ""} {
		reached = false
		w := httptest.NewRecorder()
		req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/v1/checkpoints/x/manifest", http.NoBody),
			map[string]string{"id": id})
		guarded.ServeHTTP(w, req)
		if reached {
			t.Errorf("id=%q: handler ran; guard did not reject it", id)
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q: status %d, want 400", id, w.Code)
		}
	}

	reached = false
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/checkpoints/abc__20260724-120000/manifest", http.NoBody))
	if !reached || w.Code != http.StatusOK {
		t.Errorf("legitimate id rejected: reached=%v status=%d", reached, w.Code)
	}
}

func TestJoinWithinRoot(t *testing.T) {
	root := t.TempDir()

	got, err := joinWithinRoot(root, "sub/dir/pages-1.img")
	if err != nil {
		t.Fatalf("legitimate relative path rejected: %v", err)
	}
	if want := filepath.Join(root, "sub", "dir", "pages-1.img"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A peer manifest entry that climbs out of the destination.
	for _, rel := range []string{"../escape", "../../etc/cron.d/x", "/etc/passwd"} {
		got, err := joinWithinRoot(root, rel)
		if err == nil && !strings.HasPrefix(got, root+string(os.PathSeparator)) {
			t.Errorf("joinWithinRoot(%q) = %q, escaped root", rel, got)
		}
	}

	// The two-step attack the lexical check alone misses: the peer sends a
	// symlink first, then a file "under" it.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "cache")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := joinWithinRoot(root, "cache/payload"); err == nil {
		t.Error("write through a symlink pointing outside the root was allowed")
	}
}
