/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package selfhosted

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveStack_LocalPath_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	resolved, err := ResolveStack(context.Background(), StackOptions{Source: dir})
	require.NoError(t, err)
	assert.Equal(t, dir, resolved.Path)
	assert.Equal(t, "local", resolved.Source)
}

func TestResolveStack_LocalPath_MissingHelmfileD(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveStack(context.Background(), StackOptions{Source: dir})
	assert.ErrorContains(t, err, "helmfile.d")
}

// TestResolveStack_DefaultsToBuiltIn verifies that an empty Source falls back
// to BuiltInOCIRef and routes to the OCI resolver. Requires oras on PATH
// because resolveOCI now performs a real fetch.
func TestResolveStack_DefaultsToBuiltIn(t *testing.T) {
	if _, err := exec.LookPath("oras"); err != nil {
		t.Skip("oras not on PATH; skipping OCI dispatch test (run on mcamp-dev-vm)")
	}
	cache := t.TempDir()
	resolved, err := ResolveStack(context.Background(), StackOptions{
		Source:        "",
		BuiltInOCIRef: "oci://nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0",
		CacheDir:      cache,
	})
	require.NoError(t, err)
	assert.Equal(t, "oci", resolved.Source)
	assert.Equal(t, "oci://nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0", resolved.OCIRef)
	assert.NotEmpty(t, resolved.Path)
}

func TestResolveStack_ErrorWhenNoSourceAndNoDefault(t *testing.T) {
	_, err := ResolveStack(context.Background(), StackOptions{})
	assert.ErrorContains(t, err, "no --stack")
}

// TestResolveOCI_FetchesArtifactToCacheDir is an integration test that requires
// 'oras' on PATH and network access to nvcr.io. It is automatically skipped on
// hosts without oras (e.g. macOS CI). Run it on mcamp-dev-vm where oras is
// installed and nvcr.io credentials are available.
//
// Test artifact: nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0
// Rationale: this is a known-good artifact in the ncp-dev registry that is
// accessible without interactive login (docker config.json credential already
// present on the dev VM). The nvcf-stack bundle artifact doesn't exist yet
// (M1 Task 3 publish deferred to first release). The helm chart tar.gz
// serves as a stand-in: it is a real OCI artifact with a gzip layer blob,
// exercises the full oras manifest fetch → blob fetch → tar extract path.
// We assert the extracted directory is non-empty rather than asserting
// helmfile.d/ (which a helm chart won't have).
func TestResolveOCI_FetchesArtifactToCacheDir(t *testing.T) {
	if _, err := exec.LookPath("oras"); err != nil {
		t.Skip("oras not on PATH; skipping OCI integration test (run on mcamp-dev-vm)")
	}

	cache := t.TempDir()
	r, err := ResolveStack(context.Background(), StackOptions{
		Source:   "oci://nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0",
		CacheDir: cache,
	})
	require.NoError(t, err)
	assert.Equal(t, "oci", r.Source)
	assert.Equal(t, "oci://nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0", r.OCIRef)
	assert.NotEmpty(t, r.Path)

	// Verify the sentinel file was written.
	assert.FileExists(t, filepath.Join(r.Path, ".extraction-complete"))

	// Second call must be a cache hit (no oras invocation needed).
	r2, err := ResolveStack(context.Background(), StackOptions{
		Source:   "oci://nvcr.io/0651155215864979/ncp-dev/helm-nvcf-nats:0.6.0",
		CacheDir: cache,
	})
	require.NoError(t, err)
	assert.Equal(t, r.Path, r2.Path, "cache hit should return same path")
}

// TestExtractTarGzDigest covers the pure-function parser with no oras dependency.
func TestExtractTarGzDigest(t *testing.T) {
	type layer struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	}
	type manifest struct {
		Layers []layer `json:"layers"`
	}
	makeJSON := func(layers []layer) []byte {
		b, err := json.Marshal(manifest{Layers: layers})
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		return b
	}

	tests := []struct {
		name       string
		layers     []layer
		wantDigest string
		wantErrSub string
	}{
		{
			name: "application/gzip matches",
			layers: []layer{
				{MediaType: "application/gzip", Digest: "sha256:aaa"},
			},
			wantDigest: "sha256:aaa",
		},
		{
			name: "nvcf tar+gzip mediatype matches",
			layers: []layer{
				{MediaType: "application/vnd.nvidia.nvcf.stack.v1.tar+gzip", Digest: "sha256:bbb"},
			},
			wantDigest: "sha256:bbb",
		},
		{
			name: "empty digest on matching layer returns error",
			layers: []layer{
				{MediaType: "application/gzip", Digest: ""},
			},
			wantErrSub: "empty digest",
		},
		{
			name:       "no layers returns error",
			layers:     []layer{},
			wantErrSub: "no tar.gz layer found",
		},
		{
			name: "non-matching mediatypes return error",
			layers: []layer{
				{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:ccc"},
				{MediaType: "application/json", Digest: "sha256:ddd"},
			},
			wantErrSub: "no tar.gz layer found",
		},
		{
			name: "multi-layer: second matches, returns first match",
			layers: []layer{
				{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:skip"},
				{MediaType: "application/gzip", Digest: "sha256:eee"},
			},
			wantDigest: "sha256:eee",
		},
		{
			name: "application/octet-stream is not matched",
			layers: []layer{
				{MediaType: "application/octet-stream", Digest: "sha256:fff"},
			},
			wantErrSub: "no tar.gz layer found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractTarGzDigest(makeJSON(tc.layers))
			if tc.wantErrSub != "" {
				assert.ErrorContains(t, err, tc.wantErrSub)
				assert.Empty(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantDigest, got)
			}
		})
	}
}

// TestResolveGit_ClonesShallowToCache verifies that resolveGit shallow-clones
// a local repo and writes the extraction sentinel.
func TestResolveGit_ClonesShallowToCache(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	srcDir := t.TempDir()
	// Build a minimal stack tree + commit it so we have a local "remote".
	// Note: git does not track empty directories, so helmfile.d/ must contain a file.
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "helmfile.d", "values.yaml"), []byte("# stub\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"-c", "user.email=a@b", "-c", "user.name=t", "commit", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = srcDir
		require.NoError(t, c.Run(), "git %v", args)
	}

	cache := t.TempDir()
	r, err := ResolveStack(context.Background(), StackOptions{
		// file:// URLs dispatch to the git resolver and are cloneable locally.
		Source:   "file://" + srcDir + "@main",
		CacheDir: cache,
	})
	require.NoError(t, err)
	assert.Equal(t, "git", r.Source)
	assert.DirExists(t, filepath.Join(r.Path, "helmfile.d"))
	assert.FileExists(t, filepath.Join(r.Path, ".extraction-complete"))
}

// TestResolveStack_GitURI_DispatchesToGitResolver verifies that git@ URIs
// dispatch to resolveGit (not resolveLocal or resolveOCI). The clone will
// fail (nonexistent repo), but the error must come from the git path.
func TestResolveStack_GitURI_DispatchesToGitResolver(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	_, err := ResolveStack(context.Background(), StackOptions{
		Source: "git@github.com:nonexistent/nonexistent.git",
	})
	// Test expects the resolver to be called and to fail at the git clone
	// stage (since the URL doesn't exist). The point is to verify dispatch,
	// not network reachability.
	assert.Error(t, err)
	// A sentinel substring from the resolveGit error format so we know it
	// went through the git path, not local-path Stat or OCI.
	assert.Contains(t, err.Error(), "git ")
}
