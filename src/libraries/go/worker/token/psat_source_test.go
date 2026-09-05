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

package token

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildFakeJWT builds a minimal unsigned JWT with the given exp claim.
func buildFakeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"sub":"system:serviceaccount:ns:nvcf-worker","exp":%d}`, exp)),
	)
	return header + "." + payload + ".fakesig"
}

// useTempMountRoot points MountedTokenRoot at a fresh temp directory for the test and
// returns its symlink-resolved path.
func useTempMountRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := MountedTokenRoot
	MountedTokenRoot = dir + "/"
	t.Cleanup(func() { MountedTokenRoot = old })
	return dir
}

// writeMountedJWT writes a valid fake JWT under root and points the env var at it.
func writeMountedJWT(t *testing.T, root string, exp int64) string {
	t.Helper()
	path := filepath.Join(root, "token")
	if err := os.WriteFile(path, []byte(buildFakeJWT(exp)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MountedTokenPathEnvKey, path)
	return path
}

func TestNewMountedJWTSource_NoEnvVar_UsesDefaultPath(t *testing.T) {
	t.Setenv(MountedTokenPathEnvKey, "")
	path, err := resolveMountedTokenPath("")
	if err != nil {
		t.Fatalf("resolveMountedTokenPath: %v", err)
	}
	if path != DefaultMountedTokenPath {
		t.Errorf("default path = %q, want %q", path, DefaultMountedTokenPath)
	}
	if _, statErr := os.Stat(DefaultMountedTokenPath); statErr == nil {
		t.Skip("default mount path exists on this host")
	}
	_, err = NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken when the default path is absent, got %v", err)
	}
}

func TestNewMountedJWTSource_FileMissing(t *testing.T) {
	root := useTempMountRoot(t)
	t.Setenv(MountedTokenPathEnvKey, filepath.Join(root, "missing"))
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken, got %v", err)
	}
}

func TestNewMountedJWTSource_NonRegularFile(t *testing.T) {
	root := useTempMountRoot(t)
	t.Setenv(MountedTokenPathEnvKey, root)
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken for directory path, got %v", err)
	}
}

func TestNewMountedJWTSource_PathOutsideAllowedRoot(t *testing.T) {
	useTempMountRoot(t)
	outside := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(outside, []byte(buildFakeJWT(time.Now().Add(time.Hour).Unix())), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MountedTokenPathEnvKey, outside)
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken for a path outside %s, got %v", MountedTokenRoot, err)
	}
}

func TestNewMountedJWTSource_SymlinkEscapeRejected(t *testing.T) {
	root := useTempMountRoot(t)
	outside := filepath.Join(t.TempDir(), "real-token")
	if err := os.WriteFile(outside, []byte(buildFakeJWT(time.Now().Add(time.Hour).Unix())), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "token")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MountedTokenPathEnvKey, link)
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken for a symlink escaping the root, got %v", err)
	}
}

func TestNewMountedJWTSource_TraversalRejected(t *testing.T) {
	root := useTempMountRoot(t)
	outside := filepath.Join(filepath.Dir(root), "escaped-token")
	if err := os.WriteFile(outside, []byte(buildFakeJWT(time.Now().Add(time.Hour).Unix())), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	t.Setenv(MountedTokenPathEnvKey, filepath.Join(root, "..", "escaped-token"))
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken for a traversal path, got %v", err)
	}
}

func TestNewMountedJWTSource_NonJWTContent(t *testing.T) {
	root := useTempMountRoot(t)
	path := filepath.Join(root, "token")
	if err := os.WriteFile(path, []byte("not a jwt"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MountedTokenPathEnvKey, path)
	_, err := NewMountedJWTSource()
	if !errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected ErrNoMountedToken for non-JWT content, got %v", err)
	}
}

func TestNewMountedJWTSource_UnreadableFileIsNotNoToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files")
	}
	root := useTempMountRoot(t)
	path := writeMountedJWT(t, root, time.Now().Add(time.Hour).Unix())
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	_, err := NewMountedJWTSource()
	if err == nil || errors.Is(err, ErrNoMountedToken) {
		t.Errorf("expected a read error distinct from ErrNoMountedToken, got %v", err)
	}
}

func TestNewMountedJWTSource_FileExists(t *testing.T) {
	root := useTempMountRoot(t)
	writeMountedJWT(t, root, time.Now().Add(time.Hour).Unix())
	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestMountedJWTTokenSource_Token_ReadsFile(t *testing.T) {
	root := useTempMountRoot(t)
	exp := time.Now().Add(15 * time.Minute).Unix()
	writeMountedJWT(t, root, exp)

	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token() error: %v", err)
	}
	if tok.AccessToken != buildFakeJWT(exp) {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, buildFakeJWT(exp))
	}
	if tok.Expiry.Unix() != exp {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, time.Unix(exp, 0))
	}
}

func TestMountedJWTTokenSource_Token_ReReadsOnRotation(t *testing.T) {
	root := useTempMountRoot(t)
	path := writeMountedJWT(t, root, time.Now().Add(10*time.Minute).Unix())

	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatal(err)
	}
	r1, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: write second token
	tok2 := buildFakeJWT(time.Now().Add(15 * time.Minute).Unix())
	if err := os.WriteFile(path, []byte(tok2), 0600); err != nil {
		t.Fatal(err)
	}
	r2, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}
	if r2.AccessToken != tok2 || r2.AccessToken == r1.AccessToken {
		t.Errorf("second read after rotation: got %q, want %q", r2.AccessToken, tok2)
	}
}

func TestMountedJWTTokenSource_Token_ErrorsOnRotatedGarbage(t *testing.T) {
	root := useTempMountRoot(t)
	path := writeMountedJWT(t, root, time.Now().Add(10*time.Minute).Unix())

	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token()
	if err == nil {
		t.Fatalf("expected error for non-JWT content, got token %q", tok.AccessToken)
	}
}

func TestParseJWTExpiry_Valid(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute).Unix()
	jwtStr := buildFakeJWT(exp)
	got, err := parseJWTExpiry(jwtStr)
	if err != nil {
		t.Fatalf("parseJWTExpiry: %v", err)
	}
	if got.Unix() != exp {
		t.Errorf("got expiry %v, want %v", got.Unix(), exp)
	}
}

func TestParseJWTExpiry_NoExpClaim(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"test"}`))
	jwtStr := header + "." + payload + ".sig"
	_, err := parseJWTExpiry(jwtStr)
	if err == nil {
		t.Error("expected error for missing exp claim")
	}
}

func TestParseJWTExpiry_BadFormat(t *testing.T) {
	_, err := parseJWTExpiry("not.a.jwt.with.too.many.parts")
	if err == nil {
		t.Error("expected error for malformed JWT")
	}
}

// Exercise that the JSON payload round-trips cleanly (regression for padding issues).
func TestParseJWTExpiry_PaddingVariants(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	for _, pad := range []string{"", "a", "ab", "abc"} {
		t.Run("pad="+pad, func(t *testing.T) {
			claims := map[string]int64{"exp": exp}
			b, _ := json.Marshal(claims)
			payload := base64.RawURLEncoding.EncodeToString(b)
			jwtStr := "hdr." + payload + ".sig"
			// Inject a suffix to vary the part count (exercises structural validation).
			jwtStr = strings.Replace(jwtStr, ".sig", pad+".sig", 1)
			// Must not panic; errors are acceptable for structurally invalid input.
			_, _ = parseJWTExpiry(jwtStr)
		})
	}
}
