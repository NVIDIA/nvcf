// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const umbrellaBundleMaxBytes = 256 << 20

func readUmbrellaArchive(data []byte) (map[string]umbrellaFile, error) {
	if len(data) > umbrellaBundleMaxBytes {
		return nil, fmt.Errorf("umbrella archive exceeds %d bytes", umbrellaBundleMaxBytes)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: umbrellaBundleMaxBytes + 1}
	reader := tar.NewReader(limited)
	files := map[string]umbrellaFile{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg || !safeUmbrellaArchivePath(header.Name) || (header.Mode != 0o644 && header.Mode != 0o755) {
			return nil, fmt.Errorf("unsupported umbrella archive entry %q", header.Name)
		}
		if _, exists := files[header.Name]; exists {
			return nil, fmt.Errorf("duplicate umbrella archive entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > umbrellaBundleMaxBytes || limited.N <= header.Size {
			return nil, fmt.Errorf("umbrella archive is too large")
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[header.Name] = umbrellaFile{data: contents, mode: header.Mode}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return nil, fmt.Errorf("umbrella gzip checksum: %w", err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("umbrella archive is too large")
	}
	return files, nil
}

func verifyUmbrellaArchive(archive, manifestData []byte, expectedDigest string) error {
	if !lowercaseSHA256DigestRe.MatchString(expectedDigest) || umbrellaDigest(archive) != expectedDigest {
		return fmt.Errorf("umbrella bundle checksum mismatch")
	}
	manifest, err := parseUmbrellaManifest(manifestData)
	if err != nil {
		return err
	}
	files, err := readUmbrellaArchive(archive)
	if err != nil {
		return err
	}
	if !bytes.Equal(files["manifest.json"].data, manifestData) {
		return fmt.Errorf("bundled manifest differs from the selected product manifest")
	}
	if err := verifyUmbrellaFileIndex(files, manifest, manifestData); err != nil {
		return err
	}
	if !bytes.Equal(files["VERSION"].data, []byte(manifest.Version+"\n")) {
		return fmt.Errorf("bundled umbrella VERSION mismatch")
	}
	for _, name := range releaseSetStackNames {
		directory := umbrellaStackDirectory(name)
		if !bytes.Equal(files[directory+"/VERSION"].data, []byte(manifest.Stacks[name].Version+"\n")) {
			return fmt.Errorf("bundled %s VERSION mismatch", name)
		}
		if _, ok := files[directory+"/environments/base.yaml"]; !ok {
			return fmt.Errorf("bundled %s is missing environments/base.yaml", name)
		}
	}
	combined, err := umbrellaCombinedInventory(manifest, manifestData, files)
	if err != nil {
		return err
	}
	if !bytes.Equal(combined, files["inventory.json"].data) {
		return fmt.Errorf("bundled combined inventory does not match the constituent inventories")
	}
	for _, name := range []string{"LICENSE", "NOTICE", "README.md"} {
		if len(files[name].data) == 0 {
			return fmt.Errorf("bundle is missing %s", name)
		}
	}
	return nil
}

func verifyUmbrellaFileIndex(files map[string]umbrellaFile, manifest *umbrellaManifest, manifestData []byte) error {
	var index umbrellaFileIndex
	decoder := json.NewDecoder(bytes.NewReader(files["bundle-files.json"].data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return fmt.Errorf("bundle file index: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if index.SchemaVersion != 1 || index.Version != manifest.Version || index.ManifestSHA256 != umbrellaDigest(manifestData) || len(index.Files)+1 != len(files) {
		return fmt.Errorf("bundle file index identity or file count mismatch")
	}
	for name, receipt := range index.Files {
		file, ok := files[name]
		if !ok || name == "bundle-files.json" || receipt.Mode != file.mode || receipt.SHA256 != umbrellaDigest(file.data) {
			return fmt.Errorf("bundled file %s is missing or changed", name)
		}
	}
	return nil
}

func verifyUmbrellaBundleFile(bundlePath, manifestPath, digest string) error {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return err
	}
	if info.Size() > umbrellaBundleMaxBytes {
		return fmt.Errorf("umbrella archive exceeds %d bytes", umbrellaBundleMaxBytes)
	}
	archive, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	manifest, data, err := loadUmbrellaManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := verifyUmbrellaArchive(archive, data, digest); err != nil {
		return err
	}
	fmt.Printf("Verified umbrella %s, bundle %s\n", manifest.Version, digest)
	return nil
}
