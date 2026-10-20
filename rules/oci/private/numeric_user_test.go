// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package image_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var imageIndex = flag.String("image_index", "", "OCI image index directory")
var expectedUser = flag.String("expected_user", "", "numeric UID:GID expected on both architectures")

type descriptor struct {
	Digest   string
	Platform struct{ OS, Architecture string }
}

// TestNumericUserForEachPlatform checks the runtime identity on both supported architectures.
func TestNumericUserForEachPlatform(t *testing.T) {
	if *imageIndex == "" || *expectedUser == "" {
		t.Fatal("-image_index and -expected_user are required")
	}
	root := *imageIndex
	if !filepath.IsAbs(root) {
		if _, err := os.Stat(root); err != nil {
			root = filepath.Join(os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"), root)
		}
	}
	readJSON := func(path string, value any) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	blob := func(d descriptor) string {
		t.Helper()
		algorithm, digest, ok := strings.Cut(d.Digest, ":")
		if !ok || algorithm != "sha256" || len(digest) != 64 {
			t.Fatalf("unexpected OCI digest: %q", d.Digest)
		}
		return filepath.Join(root, "blobs", algorithm, digest)
	}
	var index struct{ Manifests []descriptor }
	readJSON(filepath.Join(root, "index.json"), &index)
	seen := map[string]bool{}
	// An OCI layout can wrap the multi-platform index in another descriptor.
	pending := index.Manifests
	visited := map[string]bool{}
	for len(pending) > 0 {
		platform := pending[0]
		pending = pending[1:]
		if visited[platform.Digest] {
			t.Fatalf("repeated OCI descriptor %q", platform.Digest)
		}
		visited[platform.Digest] = true
		var manifest struct {
			Config    descriptor
			Manifests []descriptor
		}
		readJSON(blob(platform), &manifest)
		if len(manifest.Manifests) > 0 {
			pending = append(pending, manifest.Manifests...)
			continue
		}
		var config struct {
			Architecture string
			OS           string
			Config       struct{ User string }
		}
		readJSON(blob(manifest.Config), &config)
		name := config.OS + "/" + config.Architecture
		if name != "linux/amd64" && name != "linux/arm64" {
			t.Fatalf("unexpected platform %q", name)
		}
		if seen[name] {
			t.Fatalf("duplicate platform %q", name)
		}
		seen[name] = true
		if config.Config.User != *expectedUser {
			t.Errorf("%s OCI user = %q, want %q", name, config.Config.User, *expectedUser)
		}
		if platform.Platform.OS != config.OS || platform.Platform.Architecture != config.Architecture {
			t.Errorf("%s manifest platform does not match its image configuration", name)
		}
	}
	if !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Errorf("image index is missing a supported platform: %v", seen)
	}
}
