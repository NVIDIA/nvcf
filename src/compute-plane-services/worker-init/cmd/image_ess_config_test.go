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

package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var imageTar = flag.String("image_tar", "", "path to the worker-init image tarball")

func TestImageContainsESSConfig(t *testing.T) {
	if *imageTar == "" {
		t.Fatal("-image_tar is required")
	}

	imagePath := resolveRunfile(*imageTar)
	file, err := os.Open(imagePath)
	if err != nil {
		t.Fatalf("open image tarball %q: %v", imagePath, err)
	}
	defer file.Close()

	required := []string{
		"etc/ess/config.hcl",
		"etc/ess/secrets.tmpl",
		"etc/ess/accounts-secrets.tmpl",
	}
	found := make(map[string]bool, len(required))

	image := tar.NewReader(file)
	for {
		header, err := image.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read image tarball: %v", err)
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}

		// Docker image tarballs contain metadata and layer archives. Non-tar
		// entries are expected and ignored.
		if err := scanLayer(io.LimitReader(image, header.Size), found); err != nil {
			continue
		}
		if containsAll(found, required) {
			return
		}
	}

	var missing []string
	for _, path := range required {
		if !found[path] {
			missing = append(missing, "/"+path)
		}
	}
	t.Fatalf("worker-init image is missing required ESS files: %s", strings.Join(missing, ", "))
}

func resolveRunfile(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}

	runfilesDir := os.Getenv("TEST_SRCDIR")
	workspace := os.Getenv("TEST_WORKSPACE")
	if runfilesDir != "" && workspace != "" {
		return filepath.Join(runfilesDir, workspace, path)
	}
	return path
}

func scanLayer(reader io.Reader, found map[string]bool) error {
	buffered := bufio.NewReader(reader)
	layerReader := io.Reader(buffered)

	magic, err := buffered.Peek(2)
	if err != nil {
		return err
	}
	if magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err := gzip.NewReader(buffered)
		if err != nil {
			return err
		}
		defer compressed.Close()
		layerReader = compressed
	}

	layer := tar.NewReader(layerReader)
	for {
		header, err := layer.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read layer: %w", err)
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}

		path := strings.TrimPrefix(strings.TrimPrefix(header.Name, "./"), "/")
		found[path] = true
	}
}

func containsAll(found map[string]bool, required []string) bool {
	for _, path := range required {
		if !found[path] {
			return false
		}
	}
	return true
}
