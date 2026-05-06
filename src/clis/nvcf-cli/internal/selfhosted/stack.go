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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StackOptions controls how ResolveStack locates the helmfile bundle.
type StackOptions struct {
	// Source is the user-supplied --stack= value, or empty to fall through
	// to BuiltInOCIRef.
	Source string
	// BuiltInOCIRef is the CLI-version-pinned default (digest-suffixed),
	// used when Source is empty.
	BuiltInOCIRef string
	// CacheDir is the on-disk cache root (~/.cache/nvcf-cli/stacks/ by default).
	// Used for git and OCI extractions.
	CacheDir string
}

// ResolvedStack is the on-disk location of a resolved helmfile bundle and
// metadata about how it was obtained.
type ResolvedStack struct {
	// Path is a directory on disk containing helmfile.d/, environments/, etc.
	Path string
	// Source is the URI scheme used to resolve this stack: "local" | "git" | "oci".
	Source string
	// OCIRef is populated when Source == "oci". Empty otherwise.
	OCIRef string
}

// ResolveStack dispatches on the shape of opts.Source (or opts.BuiltInOCIRef
// when Source is empty) and returns a ResolvedStack pointing to a directory
// that contains at minimum a helmfile.d/ subdirectory.
//
// URI shape dispatch:
//
//	oci://...            → resolveOCI  (Task 9)
//	git@... / https://*.git / file:// → resolveGit (Task 10)
//	anything else        → resolveLocal (fully implemented)
//
// Dispatch heuristic: the prefix-and-substring rules below are "best effort"
// for v1. Users who pin to https://… without a .git suffix should use a
// trailing slash or .git suffix to opt into the git resolver explicitly.
// Users who want a non-git https URL treated as not-git can either supply
// a local path or an oci:// reference. (Tracked: spec §12 follow-up.)
func ResolveStack(ctx context.Context, opts StackOptions) (*ResolvedStack, error) {
	src := opts.Source
	if src == "" {
		if opts.BuiltInOCIRef == "" {
			return nil, fmt.Errorf("no --stack provided and no built-in OCI default configured")
		}
		src = opts.BuiltInOCIRef
	}

	switch {
	case strings.HasPrefix(src, "oci://"):
		return resolveOCI(ctx, src, opts.CacheDir)
	case strings.HasPrefix(src, "git@") ||
		strings.HasPrefix(src, "file://") ||
		(strings.HasPrefix(src, "https://") && strings.Contains(src, ".git")):
		return resolveGit(ctx, src, opts.CacheDir)
	default:
		return resolveLocal(ctx, src)
	}
}

func resolveLocal(ctx context.Context, path string) (*ResolvedStack, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving --stack=%s: %w", path, err)
	}
	if _, err := os.Stat(filepath.Join(abs, "helmfile.d")); err != nil {
		return nil, fmt.Errorf("--stack=%s: missing helmfile.d/ directory", abs)
	}
	return &ResolvedStack{Path: abs, Source: "local"}, nil
}

// resolveGit shallow-clones ref to cacheDir/git-<hash>/ and returns the path.
// ref is a URL with an optional "@branch" suffix (e.g. "https://host/repo.git@v1.0").
// On a cache hit the branch is refreshed best-effort (offline runs tolerated).
func resolveGit(ctx context.Context, ref, cacheDir string) (*ResolvedStack, error) {
	if cacheDir == "" {
		home, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolving cache dir: %w", err)
		}
		cacheDir = filepath.Join(home, "nvcf-cli", "stacks")
	}
	url, branch := splitGitRef(ref)
	cacheKey := fmt.Sprintf("%x", sha256.Sum256([]byte(url+"@"+branch)))[:12]
	dst := filepath.Join(cacheDir, "git-"+cacheKey)

	if _, err := os.Stat(filepath.Join(dst, ".extraction-complete")); err == nil {
		// Cache hit; refresh the branch to pick up new pushes (best-effort).
		c := exec.CommandContext(ctx, "git", "-C", dst, "fetch", "origin", branch, "--depth=1")
		_ = c.Run() // best-effort; cache stays valid even on offline runs.
		return &ResolvedStack{Path: dst, Source: "git"}, nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	args := []string{"clone", "--depth=1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, url, dst)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git %v: %w (%s)", args, err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, "helmfile.d")); err != nil {
		return nil, fmt.Errorf("cloned %s but no helmfile.d/ found", url)
	}
	// Write sentinel so a future cache-hit is unambiguous.
	if err := os.WriteFile(filepath.Join(dst, ".extraction-complete"), []byte{}, 0o644); err != nil {
		return nil, fmt.Errorf("writing extraction sentinel: %w", err)
	}
	return &ResolvedStack{Path: dst, Source: "git"}, nil
}

// splitGitRef parses "url[@ref]" forms. The ref may follow either the URL or
// be omitted entirely (default branch).
func splitGitRef(ref string) (url, branch string) {
	if i := strings.LastIndex(ref, "@"); i > len("git@") {
		// Avoid splitting on the @ in 'git@host:path' SSH URLs.
		tail := ref[i+1:]
		if !strings.Contains(tail, "/") && !strings.Contains(tail, ":") {
			return ref[:i], tail
		}
	}
	return ref, ""
}

// resolveOCI fetches an OCI artifact via oras, extracts the tar.gz payload,
// and caches the result under cacheDir/oci-<digest>/. On cache hit the fetch
// is skipped entirely.
//
// The OCI artifact format expected by NVCF stack bundles is a single-layer
// manifest where the layer blob is a gzipped tarball (application/gzip or
// application/vnd.nvidia.nvcf.stack.v1.tar+gzip). The implementation fetches
// the manifest via "oras manifest fetch", selects the first tar/gzip layer,
// and retrieves it via "oras blob fetch". This handles artifacts published
// without the org.opencontainers.image.title layer annotation (which plain
// "oras pull" requires to save files to disk).
func resolveOCI(ctx context.Context, ref, cacheDir string) (*ResolvedStack, error) {
	if cacheDir == "" {
		home, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolving cache dir: %w", err)
		}
		cacheDir = filepath.Join(home, "nvcf-cli", "stacks")
	}

	digest := digestFromRef(ref)
	dst := filepath.Join(cacheDir, "oci-"+digest)

	// Cache hit: sentinel file exists (written only after successful extraction).
	if _, err := os.Stat(filepath.Join(dst, ".extraction-complete")); err == nil {
		return &ResolvedStack{Path: dst, Source: "oci", OCIRef: ref}, nil
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir %s: %w", dst, err)
	}

	tmp, err := os.MkdirTemp("", "oras-pull-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// Strip the oci:// scheme prefix — oras CLI uses bare registry refs.
	bareRef := strings.TrimPrefix(ref, "oci://")

	// Fetch the OCI manifest to find the tar.gz blob digest.
	// Use a stderr buffer so failures include diagnostics in the wrapped error.
	cmd := exec.CommandContext(ctx, "oras", "manifest", "fetch", bareRef)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	manifestJSON, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("oras manifest fetch %s: %w\n%s", bareRef, err, stderr.String())
	}

	blobDigest, err := extractTarGzDigest(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("parsing manifest for %s: %w", bareRef, err)
	}

	// Fetch the blob directly (works regardless of layer title annotation).
	tarball := filepath.Join(tmp, "bundle.tar.gz")
	fetchArgs := []string{"blob", "fetch", "--output", tarball, bareRef + "@" + blobDigest}
	if out, err := exec.CommandContext(ctx, "oras", fetchArgs...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("oras blob fetch %s@%s: %w\n%s", bareRef, blobDigest, err, out)
	}

	if out, err := exec.CommandContext(ctx, "tar", "-xzf", tarball, "-C", dst).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("extracting %s: %w\n%s", tarball, err, out)
	}

	// Write sentinel so partial extractions don't poison the cache.
	if err := os.WriteFile(filepath.Join(dst, ".extraction-complete"), []byte{}, 0o644); err != nil {
		return nil, fmt.Errorf("writing extraction sentinel: %w", err)
	}

	return &ResolvedStack{Path: dst, Source: "oci", OCIRef: ref}, nil
}

// ociManifest is a minimal OCI manifest used to extract layer descriptors.
type ociManifest struct {
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

// extractTarGzDigest parses an OCI manifest JSON and returns the digest of the
// first layer whose mediaType indicates a gzipped tarball. Returns an error if
// no suitable layer is found.
//
// Matched mediatypes: those containing "gzip" or "tar" (e.g. application/gzip,
// application/vnd.nvidia.nvcf.stack.v1.tar+gzip). application/octet-stream is
// intentionally excluded: it could match non-tar blobs that would fail
// cryptically at the tar extraction step.
func extractTarGzDigest(manifestJSON []byte) (string, error) {
	var m ociManifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return "", fmt.Errorf("unmarshal manifest: %w", err)
	}
	for _, layer := range m.Layers {
		mt := layer.MediaType
		if strings.Contains(mt, "gzip") || strings.Contains(mt, "tar") {
			if layer.Digest == "" {
				return "", fmt.Errorf("layer has empty digest")
			}
			return layer.Digest, nil
		}
	}
	return "", fmt.Errorf("no tar.gz layer found in manifest (layers: %d)", len(m.Layers))
}

// digestFromRef derives a short cache key from an OCI reference.
//
// "oci://host/repo:tag@sha256:abcdef" → "abcdef" (first 12 chars of digest).
// When no digest is present the ref is SHA-256 hashed so cache keys remain
// unique per ref. The full ref is preserved in ResolvedStack.OCIRef.
func digestFromRef(ref string) string {
	if i := strings.Index(ref, "@sha256:"); i >= 0 {
		d := ref[i+len("@sha256:"):]
		if len(d) > 12 {
			d = d[:12]
		}
		return d
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(ref)))[:12]
}
