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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestResolveWithinRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file inside the root.
	inside := filepath.Join(root, "sub", "ok.img")
	if err := os.WriteFile(inside, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret OUTSIDE the root, and a symlink inside the root pointing at it.
	secret := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(secret, []byte("password"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	// A sibling dir sharing the root's name prefix (the bare-HasPrefix bug).
	if err := os.MkdirAll(root+"-evil", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"-evil/x", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("file inside root resolves", func(t *testing.T) {
		got, err := resolveWithinRoot(root, "sub/ok.img")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := filepath.EvalSymlinks(inside)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("symlink escaping root is rejected", func(t *testing.T) {
		if _, err := resolveWithinRoot(root, "escape"); err == nil {
			t.Fatal("symlink to outside-root file must be rejected, got nil error")
		}
	})

	t.Run("dot-dot traversal is rejected", func(t *testing.T) {
		// Anchored clean turns "../secret.txt" into "/secret.txt" → joined
		// under root → does not exist → error (never escapes).
		if _, err := resolveWithinRoot(root, "../secret.txt"); err == nil {
			t.Fatal("../ traversal must be rejected")
		}
	})

	t.Run("prefix-sibling directory is rejected", func(t *testing.T) {
		// Requesting a path that would land in "<root>-evil" must not pass a
		// naive HasPrefix check. We can't express it via relPath (Join keeps
		// it under root), but verify the boundary directly: a target whose
		// resolved path is root+"-evil/x" is outside.
		if _, err := resolveWithinRoot(root+"-evil", "x"); err != nil {
			t.Fatalf("sanity: serving the evil dir as its own root should work: %v", err)
		}
		// The real guarantee: "<root>-evil/x" is NOT served when root is `root`.
		got, err := resolveWithinRoot(root, "../root-evil/x")
		if err == nil {
			t.Fatalf("must not serve sibling-prefix path, got %q", got)
		}
	})

	t.Run("missing file maps to not-exist", func(t *testing.T) {
		_, err := resolveWithinRoot(root, "sub/nope.img")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("missing file should wrap os.ErrNotExist, got %v", err)
		}
	})
}

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
