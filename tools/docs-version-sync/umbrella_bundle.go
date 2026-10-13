// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type umbrellaFile struct {
	data []byte
	mode int64
}

// umbrellaFileIndex checks the contents and modes of every packaged file except itself.
type umbrellaFileIndex struct {
	SchemaVersion  int                            `json:"schema_version"`
	Version        string                         `json:"version"`
	ManifestSHA256 string                         `json:"manifest_sha256"`
	Files          map[string]umbrellaFileReceipt `json:"files"`
}

type umbrellaFileReceipt struct {
	SHA256 string `json:"sha256"`
	Mode   int64  `json:"mode"`
}

// umbrellaAssetIndex records release assets outside the archive to avoid circular hashes.
type umbrellaAssetIndex struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	Assets        map[string]string `json:"assets"`
}

// umbrellaInventory retains all three source identities in the combined inventory.
type umbrellaInventory struct {
	SchemaVersion  int                           `json:"schema_version"`
	Version        string                        `json:"version"`
	ManifestSHA256 string                        `json:"manifest_sha256"`
	Sources        map[string]stackSourceRelease `json:"sources"`
	Releases       []resolvedInventoryRelease    `json:"releases"`
	Artifacts      []resolvedInventoryArtifact   `json:"artifacts"`
}

func umbrellaStackDirectory(name string) string {
	return map[string]string{
		releaseSetStackControlPlane:  "deploy/stacks/self-managed",
		releaseSetStackComputePlane:  "deploy/stacks/nvcf-compute-plane",
		releaseSetStackObservability: "deploy/stacks/observability",
	}[name]
}

func umbrellaBundleName(version string) string {
	return "nvcf-" + version + ".tar.gz"
}

func assembleUmbrellaBundle(root, inventoryDir, out string, manifest *umbrellaManifest, manifestData []byte, previous *umbrellaManifest) error {
	if err := checkUmbrellaInputs(root, inventoryDir, manifest, manifestData, previous); err != nil {
		return err
	}
	files, assets, err := umbrellaBundleFiles(root, inventoryDir, manifest, manifestData)
	if err != nil {
		return err
	}
	if err := addUmbrellaFileIndex(files, manifest, manifestData); err != nil {
		return err
	}
	archive, err := archiveUmbrellaFiles(files)
	if err != nil {
		return err
	}
	if err := verifyUmbrellaArchive(archive, manifestData, umbrellaDigest(archive)); err != nil {
		return fmt.Errorf("verify assembled umbrella bundle: %w", err)
	}
	assets[umbrellaBundleName(manifest.Version)] = archive
	index := umbrellaAssetIndex{SchemaVersion: 1, Version: manifest.Version, Assets: map[string]string{}}
	for name, data := range assets {
		index.Assets[name] = umbrellaDigest(data)
	}
	assets["release-assets.json"], err = umbrellaJSON(index)
	if err != nil {
		return err
	}
	if err := writeUmbrellaAssets(out, assets); err != nil {
		return err
	}
	fmt.Printf("Assembled umbrella %s at %s, bundle %s\n", manifest.Version, out, umbrellaDigest(archive))
	return nil
}

func umbrellaBundleFiles(root, inventoryDir string, manifest *umbrellaManifest, manifestData []byte) (map[string]umbrellaFile, map[string][]byte, error) {
	files := map[string]umbrellaFile{
		"manifest.json": {data: manifestData, mode: 0o644},
		"VERSION":       {data: []byte(manifest.Version + "\n"), mode: 0o644},
	}
	assets := map[string][]byte{"manifest.json": manifestData}
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		if err := addUmbrellaStackFiles(root, umbrellaStackDirectory(name), stack, files); err != nil {
			return nil, nil, err
		}
		data, err := os.ReadFile(filepath.Join(inventoryDir, stack.InventoryAsset))
		if err != nil {
			return nil, nil, err
		}
		if umbrellaDigest(data) != stack.InventorySHA256 {
			return nil, nil, fmt.Errorf("inventory changed during assembly: %s", stack.InventoryAsset)
		}
		files["inventories/"+stack.InventoryAsset] = umbrellaFile{data: data, mode: 0o644}
		assets[stack.InventoryAsset] = data
	}
	for _, name := range []string{"LICENSE", "NOTICE"} {
		data, err := gitOutput(root, "cat-file", "blob", manifest.SourceCommit+":"+name)
		if err != nil {
			return nil, nil, fmt.Errorf("bundle requires reviewed %s: %w", name, err)
		}
		files[name] = umbrellaFile{data: data, mode: 0o644}
	}
	combined, err := umbrellaCombinedInventory(manifest, manifestData, files)
	if err != nil {
		return nil, nil, err
	}
	assets["inventory.json"] = combined
	files["inventory.json"] = umbrellaFile{data: combined, mode: 0o644}
	files["README.md"] = umbrellaFile{data: []byte(umbrellaBundleReadme(manifest)), mode: 0o644}
	return files, assets, nil
}

func addUmbrellaStackFiles(root, directory string, stack umbrellaStack, files map[string]umbrellaFile) error {
	tree, err := gitOutput(root, "ls-tree", "-rz", "--full-tree", stack.SourceCommit, "--", directory+"/")
	if err != nil {
		return err
	}
	for _, entry := range bytes.Split(tree, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		metadata, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || !strings.HasPrefix(name, directory+"/") {
			return fmt.Errorf("invalid bundle source tree entry %q", entry)
		}
		relative := strings.TrimPrefix(name, directory+"/")
		if !umbrellaDistributionFile(relative) {
			continue
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return fmt.Errorf("bundle source %s must be a regular file, got mode %s", name, fields[0])
		}
		data, err := gitOutput(root, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		mode, err := strconv.ParseInt(fields[0], 8, 64)
		if err != nil {
			return err
		}
		files[name] = umbrellaFile{data: data, mode: mode & 0o777}
	}
	for _, required := range []string{"environments/base.yaml"} {
		if _, ok := files[directory+"/"+required]; !ok {
			return fmt.Errorf("bundle stack %s is missing %s", directory, required)
		}
	}
	// Old tags may omit VERSION; maintenance branches may keep the train's value.
	// The verified tag and manifest own the distributable's component version.
	files[directory+"/VERSION"] = umbrellaFile{data: []byte(stack.Version + "\n"), mode: 0o644}
	if distributionMakefile, ok := files[directory+"/Makefile.dist"]; ok {
		files[directory+"/Makefile"] = distributionMakefile
	}
	foundState := false
	for name := range files {
		if strings.HasPrefix(name, directory+"/helmfile.d/") {
			foundState = true
		}
	}
	if !foundState {
		return fmt.Errorf("bundle stack %s has no Helmfile states", directory)
	}
	return nil
}

func umbrellaDistributionFile(name string) bool {
	if !safeUmbrellaArchivePath(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") {
			return false
		}
	}
	if name == "environments/base.yaml" || name == "VERSION" || name == "README.md" || name == "LICENSE" || name == "NOTICE" || name == "Makefile.dist" || name == "release-inventory.yaml" {
		return true
	}
	if !strings.Contains(name, "/") && strings.HasSuffix(name, ".gotmpl") {
		return true
	}
	if strings.HasPrefix(name, "secrets/") {
		return strings.HasSuffix(name, ".template") || strings.HasSuffix(name, ".example")
	}
	for _, directory := range []string{"helmfile.d/", "charts/", "values/", "scripts/"} {
		if strings.HasPrefix(name, directory) {
			return true
		}
	}
	return false
}

func umbrellaCombinedInventory(manifest *umbrellaManifest, manifestData []byte, files map[string]umbrellaFile) ([]byte, error) {
	inventories := map[string]resolvedStackInventory{}
	sources := map[string]stackSourceRelease{}
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		data := files["inventories/"+stack.InventoryAsset].data
		if umbrellaDigest(data) != stack.InventorySHA256 {
			return nil, fmt.Errorf("bundled %s inventory checksum mismatch", name)
		}
		inventory, err := parseResolvedStackInventory(data)
		if err != nil {
			return nil, err
		}
		if inventory.Source != stack.source() {
			return nil, fmt.Errorf("bundled %s inventory source mismatch", name)
		}
		key, _ := stackKeyForPlane(name)
		inventories[key] = inventory
		sources[name] = inventory.Source
	}
	combined, err := mergeResolvedStackInventories(inventories)
	if err != nil {
		return nil, err
	}
	return umbrellaJSON(umbrellaInventory{SchemaVersion: 1, Version: manifest.Version,
		ManifestSHA256: umbrellaDigest(manifestData), Sources: sources,
		Releases: combined.Releases, Artifacts: combined.Artifacts})
}

func addUmbrellaFileIndex(files map[string]umbrellaFile, manifest *umbrellaManifest, data []byte) error {
	index := umbrellaFileIndex{SchemaVersion: 1, Version: manifest.Version, ManifestSHA256: umbrellaDigest(data), Files: map[string]umbrellaFileReceipt{}}
	for name, file := range files {
		index.Files[name] = umbrellaFileReceipt{SHA256: umbrellaDigest(file.data), Mode: file.mode}
	}
	encoded, err := umbrellaJSON(index)
	if err != nil {
		return err
	}
	files["bundle-files.json"] = umbrellaFile{data: encoded, mode: 0o644}
	return nil
}

func umbrellaJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func archiveUmbrellaFiles(files map[string]umbrellaFile) ([]byte, error) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := files[name]
		if !safeUmbrellaArchivePath(name) {
			return nil, fmt.Errorf("unsafe umbrella file path %q", name)
		}
		header := &tar.Header{Name: name, Mode: file.mode, Size: int64(len(file.data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func safeUmbrellaArchivePath(name string) bool {
	return name != "" && name != "." && name != ".." && !path.IsAbs(name) && path.Clean(name) == name &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n\t:")
}

func writeUmbrellaAssets(out string, assets map[string][]byte) error {
	if entries, err := os.ReadDir(out); err == nil {
		if len(entries) != len(assets) {
			return fmt.Errorf("existing umbrella output has a different asset set: %s", out)
		}
		for _, entry := range entries {
			data, ok := assets[entry.Name()]
			if !ok {
				return fmt.Errorf("unexpected existing umbrella asset: %s", entry.Name())
			}
			if err := writeImmutableUmbrellaFile(filepath.Join(out, entry.Name()), data); err != nil {
				return err
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(filepath.Dir(out), ".umbrella-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	for name, data := range assets {
		if err := writeImmutableUmbrellaFile(filepath.Join(staged, name), data); err != nil {
			return err
		}
	}
	return os.Rename(staged, out)
}

func umbrellaBundleReadme(manifest *umbrellaManifest) string {
	return fmt.Sprintf(`# NVCF %s

This bundle contains the exact stack combination in manifest.json.
Use Helm %s and Helmfile %s. Keep the deploy/stacks sibling layout intact.
Charts and images remain external dependencies listed in inventory.json.

Configure each plane's environments/<name>.yaml and required secrets from the
included templates. Select the appropriate kubeconfig/context for each target.
Render from the extracted bundle before applying:

    HELMFILE_ENV=<name> helmfile -f deploy/stacks/self-managed/helmfile.d template
    HELMFILE_ENV=<name> helmfile -f deploy/stacks/nvcf-compute-plane/helmfile.d template

Use the corresponding sync operation only after reviewing the rendered result.
The self-managed stack delegates enabled observability to the sibling stack;
do not separately install the same observability resources on that cluster.
For a separate compute cluster, configure and run the observability stack there:

    HELMFILE_ENV=<name> helmfile -f deploy/stacks/observability/helmfile.d template

For compute-plane Makefile registration, supply NVCF_CLI=/path/to/nvcf-cli.
The bundle contains no CLI source or customer secrets. Root VERSION identifies
the umbrella release; each stack's VERSION identifies its own artifact release.
Documentation corrections do not rebuild this immutable bundle.
`, manifest.Version, manifest.Toolchain.Helm, manifest.Toolchain.Helmfile)
}
