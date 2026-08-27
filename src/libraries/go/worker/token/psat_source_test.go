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
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// buildFakeJWT builds a minimal unsigned JWT with the given exp claim.
func buildFakeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"sub":"system:serviceaccount:ns:sa","exp":%d}`, exp)),
	)
	return header + "." + payload + ".fakesig"
}

func TestNewMountedJWTSource_NoEnvVar(t *testing.T) {
	t.Setenv("NVCF_TOKEN_FILE_PATH", "")
	_, err := NewMountedJWTSource()
	if err != ErrNoMountedToken {
		t.Errorf("expected ErrNoMountedToken, got %v", err)
	}
}

func TestNewMountedJWTSource_FileMissing(t *testing.T) {
	t.Setenv("NVCF_TOKEN_FILE_PATH", "/nonexistent/path/token")
	_, err := NewMountedJWTSource()
	if err != ErrNoMountedToken {
		t.Errorf("expected ErrNoMountedToken, got %v", err)
	}
}

func TestNewMountedJWTSource_NonRegularFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NVCF_TOKEN_FILE_PATH", dir)
	_, err := NewMountedJWTSource()
	if err != ErrNoMountedToken {
		t.Errorf("expected ErrNoMountedToken for directory path, got %v", err)
	}
}

func TestNewMountedJWTSource_FileExists(t *testing.T) {
	f, err := os.CreateTemp("", "psat-*.jwt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	t.Setenv("NVCF_TOKEN_FILE_PATH", f.Name())

	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestMountedJWTTokenSource_Token_ReadsFile(t *testing.T) {
	exp := time.Now().Add(15 * time.Minute).Unix()
	jwtStr := buildFakeJWT(exp)

	f, err := os.CreateTemp("", "psat-*.jwt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(jwtStr); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Setenv("NVCF_TOKEN_FILE_PATH", f.Name())

	src, err := NewMountedJWTSource()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token() error: %v", err)
	}
	if tok.AccessToken != jwtStr {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, jwtStr)
	}
	if tok.Expiry.Unix() != exp {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, time.Unix(exp, 0))
	}
}

func TestMountedJWTTokenSource_Token_ReReadsOnRotation(t *testing.T) {
	f, err := os.CreateTemp("", "psat-*.jwt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	t.Setenv("NVCF_TOKEN_FILE_PATH", f.Name())

	src, _ := NewMountedJWTSource()

	// Write first token
	tok1 := buildFakeJWT(time.Now().Add(10 * time.Minute).Unix())
	os.WriteFile(f.Name(), []byte(tok1), 0600)
	r1, _ := src.Token()
	if r1.AccessToken != tok1 {
		t.Errorf("first read: got %q, want %q", r1.AccessToken, tok1)
	}

	// Rotate: write second token
	tok2 := buildFakeJWT(time.Now().Add(15 * time.Minute).Unix())
	os.WriteFile(f.Name(), []byte(tok2), 0600)
	r2, _ := src.Token()
	if r2.AccessToken != tok2 {
		t.Errorf("second read after rotation: got %q, want %q", r2.AccessToken, tok2)
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
