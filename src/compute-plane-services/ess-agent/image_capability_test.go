// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var imageArchive = flag.String("image", "", "Docker image archive")

func TestImageEntrypointRetainsChown(t *testing.T) {
	archive := *imageArchive
	if !filepath.IsAbs(archive) {
		if _, err := os.Stat(archive); err != nil {
			archive = filepath.Join(os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"), archive)
		}
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	outer := tar.NewReader(f)
	found := false
	for {
		h, err := outer.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(outer)
		if err != nil {
			t.Fatal(err)
		}
		var layer io.Reader = bytes.NewReader(data)
		if gz, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
			defer gz.Close()
			layer = gz
		}
		tr := tar.NewReader(layer)
		for {
			entry, err := tr.Next()
			if err != nil {
				break // Configuration and manifest files are not tar layers.
			}
			if strings.TrimPrefix(entry.Name, "./") != "usr/bin/ess-agent" {
				continue
			}
			found = true
			want := []byte{1, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
			got := []byte(entry.PAXRecords["SCHILY.xattr.security.capability"])
			if !bytes.Equal(got, want) || entry.Mode != 0o755 {
				t.Fatalf("entrypoint capability=%x mode=%o; want effective CHOWN and 0755", got, entry.Mode)
			}
		}
	}
	if !found {
		t.Fatal("image does not contain /usr/bin/ess-agent")
	}
}
