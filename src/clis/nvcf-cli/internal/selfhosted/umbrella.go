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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const umbrellaMaxBytes = 256 << 20

var umbrellaDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var umbrellaCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
var umbrellaDirectories = map[string]string{"control-plane": "self-managed", "compute-plane": "nvcf-compute-plane", "observability": "observability"}

// UmbrellaOptions is opt-in. A local bundle requires a separately trusted checksum.
type UmbrellaOptions struct {
	CacheDir string
	Release  string
	Bundle   string
	SHA256   string
}

// UmbrellaIdentity describes selected content, not an atomic installation of all planes.
type UmbrellaIdentity struct {
	Version        string                   `json:"version"`
	SHA256         string                   `json:"sha256"`
	ManifestSHA256 string                   `json:"manifest_sha256"`
	Status         string                   `json:"status"`
	Stacks         map[string]UmbrellaStack `json:"stacks"`
}

type UmbrellaStack struct {
	Version         string            `json:"version"`
	SourceTag       string            `json:"source_tag"`
	SourceCommit    string            `json:"source_commit"`
	InventoryAsset  string            `json:"inventory_asset"`
	InventorySHA256 string            `json:"inventory_sha256"`
	CompatibleWith  map[string]string `json:"compatible_with"`
}

type umbrellaManifest struct {
	SchemaVersion   int    `json:"schema_version"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	Change          string `json:"change"`
	Status          string `json:"status"`
	Qualification   string `json:"qualification,omitempty"`
	SourceCommit    string `json:"source_commit"`
	Toolchain       struct {
		Helm     string `json:"helm"`
		Helmfile string `json:"helmfile"`
	} `json:"toolchain"`
	Stacks map[string]UmbrellaStack `json:"stacks"`
}

type umbrellaFileReceipt struct {
	SHA256 string `json:"sha256"`
	Mode   int64  `json:"mode"`
}
type umbrellaFileIndex struct {
	SchemaVersion  int                            `json:"schema_version"`
	Version        string                         `json:"version"`
	ManifestSHA256 string                         `json:"manifest_sha256"`
	Files          map[string]umbrellaFileReceipt `json:"files"`
}
type umbrellaFile struct {
	data []byte
	mode int64
}

func umbrellaHash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
func stableUmbrellaVersion(version string) bool {
	v, err := semver.StrictNewVersion(version)
	return err == nil && v.Prerelease() == "" && v.Metadata() == "" && v.String() == version
}
func decodeUmbrellaJSON(data []byte, result any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(result); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func ValidateUmbrellaOptions(opts *UmbrellaOptions, componentSource string) error {
	if opts == nil {
		return nil
	}
	if componentSource != "" {
		return fmt.Errorf("umbrella selection cannot be combined with explicit component stack sources")
	}
	if (opts.Release == "") == (opts.Bundle == "") {
		return fmt.Errorf("select exactly one of --umbrella-release or --umbrella-bundle")
	}
	if opts.Release != "" && !stableUmbrellaVersion(opts.Release) {
		return fmt.Errorf("--umbrella-release requires an exact stable X.Y.Z version")
	}
	if opts.SHA256 != "" && !umbrellaDigestRE.MatchString(opts.SHA256) {
		return fmt.Errorf("--umbrella-sha256 requires sha256: followed by 64 lowercase hex digits")
	}
	if opts.Bundle != "" && opts.SHA256 == "" {
		return fmt.Errorf("--umbrella-bundle requires a trusted --umbrella-sha256")
	}
	return nil
}

func resolveUmbrellaStack(ctx context.Context, opts StackOptions) (*ResolvedStack, error) {
	if err := ValidateUmbrellaOptions(opts.Umbrella, opts.Source); err != nil {
		return nil, err
	}
	directory, ok := umbrellaDirectories[opts.Plane]
	if !ok {
		return nil, fmt.Errorf("umbrella selection requires a control-plane, compute-plane, or observability target")
	}
	cache := opts.CacheDir
	if cache == "" {
		cache = opts.Umbrella.CacheDir
	}
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		cache = filepath.Join(base, "nvcf-cli/stacks")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(cache)
	if err != nil {
		return nil, err
	}
	cache, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	digest := opts.Umbrella.SHA256
	// An explicitly pinned warm cache is usable offline. Every indexed file is checked.
	if digest != "" {
		root := filepath.Join(cache, "umbrella-"+strings.TrimPrefix(digest, "sha256:"))
		if _, err := os.Stat(filepath.Join(root, "bundle.tar.gz")); err == nil {
			return loadCachedUmbrella(root, directory, digest, opts.Umbrella.Release)
		}
	}
	var archive []byte
	if opts.Umbrella.Bundle != "" {
		info, e := os.Stat(opts.Umbrella.Bundle)
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() || info.Size() > umbrellaMaxBytes {
			return nil, fmt.Errorf("invalid umbrella bundle file")
		}
		archive, err = os.ReadFile(opts.Umbrella.Bundle)
	} else {
		archive, digest, err = downloadUmbrellaRelease(ctx, opts.Umbrella.Release, digest, &http.Client{Timeout: 2 * time.Minute, CheckRedirect: refuseInsecureRedirect}, "https://api.github.com/repos/NVIDIA/nvcf", "https://github.com/NVIDIA/nvcf/releases/download")
	}
	if err != nil {
		return nil, err
	}
	identity, files, err := verifyUmbrellaPayload(archive, digest, opts.Umbrella.Release)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(cache, "umbrella-"+strings.TrimPrefix(digest, "sha256:"))
	if _, err := os.Stat(root); err == nil {
		return loadCachedUmbrella(root, directory, digest, opts.Umbrella.Release)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(cache, ".umbrella-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	for name, file := range files {
		target := filepath.Join(temporary, "source", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, file.data, os.FileMode(file.mode)); err != nil {
			return nil, fmt.Errorf("write umbrella cache file %s: %w", name, err)
		}
		// Restore the verified archive mode after the process umask is applied.
		if err := os.Chmod(target, os.FileMode(file.mode)); err != nil {
			return nil, fmt.Errorf("set umbrella cache file mode %s: %w", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(temporary, "bundle.tar.gz"), archive, 0o644); err != nil {
		return nil, err
	}
	receipt, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "selection.json"), append(receipt, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, root); err != nil {
		if _, check := os.Stat(root); check != nil {
			return nil, err
		}
		return loadCachedUmbrella(root, directory, digest, opts.Umbrella.Release)
	}
	return &ResolvedStack{Path: filepath.Join(root, "source/deploy/stacks", directory), Source: "umbrella", Umbrella: identity}, nil
}

func loadCachedUmbrella(root, directory, digest, version string) (*ResolvedStack, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(root, "bundle.tar.gz"))
	if err != nil || !info.Mode().IsRegular() || info.Size() > umbrellaMaxBytes {
		return nil, fmt.Errorf("invalid cached umbrella archive")
	}
	archive, err := os.ReadFile(filepath.Join(root, "bundle.tar.gz"))
	if err != nil {
		return nil, err
	}
	identity, files, err := verifyUmbrellaPayload(archive, digest, version)
	if err != nil {
		return nil, fmt.Errorf("cached umbrella: %w", err)
	}
	for name, file := range files {
		target := filepath.Join(root, "source", filepath.FromSlash(name))
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() || int64(info.Mode().Perm()) != file.mode {
			return nil, fmt.Errorf("cached umbrella file is missing or changed: %s", name)
		}
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			return nil, err
		}
		expected, err := filepath.Abs(target)
		if err != nil {
			return nil, err
		}
		if resolved != expected {
			return nil, fmt.Errorf("cached umbrella path contains a symlink: %s", name)
		}
		actual, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(actual, file.data) {
			return nil, fmt.Errorf("cached umbrella file checksum mismatch: %s", name)
		}
	}
	return &ResolvedStack{Path: filepath.Join(root, "source/deploy/stacks", directory), Source: "umbrella", Umbrella: identity}, nil
}

func downloadUmbrellaRelease(ctx context.Context, version, want string, client *http.Client, api, downloads string) ([]byte, string, error) {
	get := func(endpoint string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("umbrella release download returned HTTP %d", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, umbrellaMaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > umbrellaMaxBytes {
			return nil, fmt.Errorf("umbrella release asset exceeds size limit")
		}
		return data, nil
	}
	releaseData, err := get(api + "/releases/tags/nvcf/" + version)
	if err != nil {
		return nil, "", err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(releaseData, &release); err != nil {
		return nil, "", err
	}
	if release.Tag != "nvcf/"+version || release.Draft || release.Prerelease {
		return nil, "", fmt.Errorf("umbrella release is not publicly stable")
	}
	base := downloads + "/nvcf/" + version + "/"
	data, err := get(base + "release-assets.json")
	if err != nil {
		return nil, "", err
	}
	var index struct {
		SchemaVersion int               `json:"schema_version"`
		Version       string            `json:"version"`
		Assets        map[string]string `json:"assets"`
	}
	if err := decodeUmbrellaJSON(data, &index); err != nil {
		return nil, "", err
	}
	name := "nvcf-" + version + ".tar.gz"
	digest := index.Assets[name]
	if index.SchemaVersion != 1 || index.Version != version || !umbrellaDigestRE.MatchString(digest) || (want != "" && want != digest) {
		return nil, "", fmt.Errorf("umbrella release identity or checksum mismatch")
	}
	archive, err := get(base + name)
	return archive, digest, err
}

func verifyUmbrellaPayload(archive []byte, digest, version string) (*UmbrellaIdentity, map[string]umbrellaFile, error) {
	if len(archive) > umbrellaMaxBytes || !umbrellaDigestRE.MatchString(digest) || umbrellaHash(archive) != digest {
		return nil, nil, fmt.Errorf("umbrella bundle checksum mismatch")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, nil, err
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: umbrellaMaxBytes + 1}
	reader := tar.NewReader(limited)
	files := map[string]umbrellaFile{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if header.Typeflag != tar.TypeReg || !safeUmbrellaPath(header.Name) || (header.Mode != 0o644 && header.Mode != 0o755) || header.Size < 0 || header.Size >= limited.N {
			return nil, nil, fmt.Errorf("unsafe umbrella archive entry %q", header.Name)
		}
		if _, exists := files[header.Name]; exists {
			return nil, nil, fmt.Errorf("duplicate umbrella archive entry %q", header.Name)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, nil, err
		}
		files[header.Name] = umbrellaFile{data: data, mode: header.Mode}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return nil, nil, err
	}
	if limited.N <= 0 {
		return nil, nil, fmt.Errorf("umbrella archive exceeds size limit")
	}
	var manifest umbrellaManifest
	if err := decodeUmbrellaJSON(files["manifest.json"].data, &manifest); err != nil {
		return nil, nil, fmt.Errorf("umbrella manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || !stableUmbrellaVersion(manifest.Version) || (version != "" && version != manifest.Version) || !umbrellaCommitRE.MatchString(manifest.SourceCommit) || len(manifest.Stacks) != 3 {
		return nil, nil, fmt.Errorf("invalid umbrella manifest identity")
	}
	if manifest.Status != "development" && manifest.Status != "qualified" {
		return nil, nil, fmt.Errorf("invalid umbrella qualification status")
	}
	if version != "" && manifest.Status != "qualified" {
		return nil, nil, fmt.Errorf("published umbrella release lacks qualification")
	}
	var index umbrellaFileIndex
	if err := decodeUmbrellaJSON(files["bundle-files.json"].data, &index); err != nil {
		return nil, nil, err
	}
	if index.SchemaVersion != 1 || index.Version != manifest.Version || index.ManifestSHA256 != umbrellaHash(files["manifest.json"].data) || len(index.Files)+1 != len(files) {
		return nil, nil, fmt.Errorf("umbrella file index identity mismatch")
	}
	for name, receipt := range index.Files {
		file, ok := files[name]
		if !ok || name == "bundle-files.json" || receipt.Mode != file.mode || receipt.SHA256 != umbrellaHash(file.data) {
			return nil, nil, fmt.Errorf("umbrella file checksum mismatch: %s", name)
		}
	}
	if string(files["VERSION"].data) != manifest.Version+"\n" {
		return nil, nil, fmt.Errorf("umbrella VERSION mismatch")
	}
	for plane, directory := range umbrellaDirectories {
		stack, ok := manifest.Stacks[plane]
		if !ok || !stableUmbrellaVersion(stack.Version) || !umbrellaCommitRE.MatchString(stack.SourceCommit) || stack.SourceTag != "deploy/stacks/"+directory+"/v"+stack.Version || len(stack.CompatibleWith) != 2 {
			return nil, nil, fmt.Errorf("invalid umbrella %s identity", plane)
		}
		prefix := "deploy/stacks/" + directory + "/"
		if string(files[prefix+"VERSION"].data) != stack.Version+"\n" {
			return nil, nil, fmt.Errorf("umbrella %s VERSION mismatch", plane)
		}
		if _, ok := files[prefix+"environments/base.yaml"]; !ok {
			return nil, nil, fmt.Errorf("umbrella %s environment is missing", plane)
		}
		found := false
		for name := range files {
			if strings.HasPrefix(name, prefix+"helmfile.d/") {
				found = true
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("umbrella %s Helmfile is missing", plane)
		}
		if !safeUmbrellaPath(stack.InventoryAsset) || strings.Contains(stack.InventoryAsset, "/") || !umbrellaDigestRE.MatchString(stack.InventorySHA256) || umbrellaHash(files["inventories/"+stack.InventoryAsset].data) != stack.InventorySHA256 {
			return nil, nil, fmt.Errorf("umbrella %s inventory checksum mismatch", plane)
		}
		for peer := range umbrellaDirectories {
			if peer == plane {
				continue
			}
			required, minimum := strings.CutSuffix(stack.CompatibleWith[peer], "+")
			if !stableUmbrellaVersion(required) {
				return nil, nil, fmt.Errorf("invalid %s compatibility", plane)
			}
			selected, err := semver.StrictNewVersion(manifest.Stacks[peer].Version)
			if err != nil {
				return nil, nil, err
			}
			bound, _ := semver.StrictNewVersion(required)
			if (minimum && selected.LessThan(bound)) || (!minimum && !selected.Equal(bound)) {
				return nil, nil, fmt.Errorf("umbrella %s requires %s %s", plane, peer, stack.CompatibleWith[peer])
			}
		}
	}
	return &UmbrellaIdentity{Version: manifest.Version, SHA256: digest, ManifestSHA256: index.ManifestSHA256, Status: manifest.Status, Stacks: manifest.Stacks}, files, nil
}

func safeUmbrellaPath(name string) bool {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:") {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
