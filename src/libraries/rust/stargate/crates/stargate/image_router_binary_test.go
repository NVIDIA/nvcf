// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package stargateimage_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"testing"
)

var (
	imageLayout = flag.String("image-layout", "", "OCI image layout to inspect")
	imagePath   = flag.String("image-path", "", "absolute path required in every image manifest")
)

type descriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int    `json:"size,omitempty"`
}

type document struct {
	Manifests json.RawMessage `json:"manifests,omitempty"`
	Layers    json.RawMessage `json:"layers,omitempty"`
}

func TestStargateImageContainsRouter(t *testing.T) {
	if *imageLayout == "" || *imagePath == "" {
		t.Skip("Bazel supplies the assembled OCI image layout and required path")
	}

	if err := imageContainsPath(*imageLayout, *imagePath); err != nil {
		t.Fatal(err)
	}
}

func TestImageContainsPathUsesReachableManifests(t *testing.T) {
	const target = "/usr/local/bin/stargate-k8s-router"

	t.Run("ignores unreferenced layer", func(t *testing.T) {
		layout := newLayoutBuilder(t)
		missing := layout.addManifest(layout.addLayer("usr/local/bin/stargate"))
		layout.addLayer(strings.TrimPrefix(target, "/"))
		layout.writeIndex(missing)

		if err := imageContainsPath(layout.root, target); err == nil {
			t.Fatal("unreferenced target layer made the check pass")
		}
	})

	t.Run("requires path in every nested manifest", func(t *testing.T) {
		layout := newLayoutBuilder(t)
		present := layout.addManifest(layout.addLayer(
			strings.TrimPrefix(target, "/"),
			"platform/amd64",
		))
		missing := layout.addManifest(layout.addLayer(
			"usr/local/bin/stargate",
			"platform/arm64",
		))
		nested := layout.addIndex(present, missing)
		layout.writeIndex(nested)

		if err := imageContainsPath(layout.root, target); err == nil {
			t.Fatal("one manifest missing the target made the check pass")
		}
	})

	t.Run("accepts path in every nested manifest", func(t *testing.T) {
		layout := newLayoutBuilder(t)
		first := layout.addManifest(layout.addLayer(
			strings.TrimPrefix(target, "/"),
			"platform/amd64",
		))
		second := layout.addManifest(layout.addLayer(
			strings.TrimPrefix(target, "/"),
			"platform/arm64",
		))
		nested := layout.addIndex(first, second)
		layout.writeIndex(nested)

		if err := imageContainsPath(layout.root, target); err != nil {
			t.Fatal(err)
		}
	})
}

func imageContainsPath(layout, requiredPath string) error {
	requiredPath = normalizeTarPath(requiredPath)
	if requiredPath == "." || requiredPath == "" {
		return fmt.Errorf("required image path is empty")
	}

	indexBytes, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		return fmt.Errorf("read OCI index: %w", err)
	}

	var index struct {
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return fmt.Errorf("parse OCI index: %w", err)
	}
	if len(index.Manifests) == 0 {
		return fmt.Errorf("OCI index does not reference an image manifest")
	}

	visited := make(map[string]bool)
	manifestCount := 0
	var walk func(descriptor) error
	walk = func(desc descriptor) error {
		if visited[desc.Digest] {
			return nil
		}
		visited[desc.Digest] = true

		contents, err := readBlob(layout, desc.Digest)
		if err != nil {
			return err
		}
		var doc document
		if err := json.Unmarshal(contents, &doc); err != nil {
			return fmt.Errorf("parse reachable descriptor %s: %w", desc.Digest, err)
		}

		switch {
		case doc.Manifests != nil:
			var manifests []descriptor
			if err := json.Unmarshal(doc.Manifests, &manifests); err != nil {
				return fmt.Errorf("parse manifests in %s: %w", desc.Digest, err)
			}
			for _, manifest := range manifests {
				if err := walk(manifest); err != nil {
					return err
				}
			}
			return nil
		case doc.Layers != nil:
			manifestCount++
			var layers []descriptor
			if err := json.Unmarshal(doc.Layers, &layers); err != nil {
				return fmt.Errorf("parse layers in %s: %w", desc.Digest, err)
			}
			for _, layer := range layers {
				found, err := layerContainsPath(layout, layer.Digest, requiredPath)
				if err != nil {
					return fmt.Errorf("inspect layer %s from manifest %s: %w", layer.Digest, desc.Digest, err)
				}
				if found {
					return nil
				}
			}
			return fmt.Errorf("missing /%s in OCI image manifest %s", requiredPath, desc.Digest)
		default:
			return fmt.Errorf("reachable descriptor %s is not an image index or manifest", desc.Digest)
		}
	}

	for _, desc := range index.Manifests {
		if err := walk(desc); err != nil {
			return err
		}
	}
	if manifestCount == 0 {
		return fmt.Errorf("OCI index does not reach an image manifest")
	}
	return nil
}

func readBlob(layout, digest string) ([]byte, error) {
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm != "sha256" || len(encoded) != sha256.Size*2 {
		return nil, fmt.Errorf("unsupported OCI digest %q", digest)
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return nil, fmt.Errorf("invalid OCI digest %q: %w", digest, err)
	}

	contents, err := os.ReadFile(filepath.Join(layout, "blobs", algorithm, encoded))
	if err != nil {
		return nil, fmt.Errorf("read referenced OCI blob %s: %w", digest, err)
	}
	return contents, nil
}

func layerContainsPath(layout, digest, requiredPath string) (bool, error) {
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm != "sha256" || len(encoded) != sha256.Size*2 {
		return false, fmt.Errorf("unsupported OCI digest %q", digest)
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return false, fmt.Errorf("invalid OCI digest %q: %w", digest, err)
	}

	file, err := os.Open(filepath.Join(layout, "blobs", algorithm, encoded))
	if err != nil {
		return false, err
	}
	defer file.Close()

	buffered := bufio.NewReader(file)
	reader := io.Reader(buffered)
	magic, err := buffered.Peek(2)
	if err != nil && err != io.EOF {
		return false, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err := gzip.NewReader(buffered)
		if err != nil {
			return false, err
		}
		defer compressed.Close()
		reader = compressed
	}

	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if normalizeTarPath(header.Name) == requiredPath {
			return true, nil
		}
	}
}

func normalizeTarPath(value string) string {
	return pathpkg.Clean(strings.TrimLeft(value, "/"))
}

type layoutBuilder struct {
	t    *testing.T
	root string
}

func newLayoutBuilder(t *testing.T) *layoutBuilder {
	t.Helper()
	return &layoutBuilder{t: t, root: t.TempDir()}
}

func (b *layoutBuilder) addBlob(contents []byte, mediaType string) descriptor {
	b.t.Helper()
	digest := sha256.Sum256(contents)
	encoded := hex.EncodeToString(digest[:])
	path := filepath.Join(b.root, "blobs", "sha256", encoded)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		b.t.Fatal(err)
	}
	return descriptor{
		Digest:    "sha256:" + encoded,
		MediaType: mediaType,
		Size:      len(contents),
	}
}

func (b *layoutBuilder) addLayer(paths ...string) descriptor {
	b.t.Helper()
	var contents bytes.Buffer
	archive := tar.NewWriter(&contents)
	for _, name := range paths {
		body := []byte(name)
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := archive.WriteHeader(header); err != nil {
			b.t.Fatal(err)
		}
		if _, err := archive.Write(body); err != nil {
			b.t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		b.t.Fatal(err)
	}
	return b.addBlob(contents.Bytes(), "application/vnd.oci.image.layer.v1.tar")
}

func (b *layoutBuilder) addManifest(layers ...descriptor) descriptor {
	b.t.Helper()
	return b.addJSON(document{Layers: mustJSON(b.t, layers)}, "application/vnd.oci.image.manifest.v1+json")
}

func (b *layoutBuilder) addIndex(manifests ...descriptor) descriptor {
	b.t.Helper()
	return b.addJSON(document{Manifests: mustJSON(b.t, manifests)}, "application/vnd.oci.image.index.v1+json")
}

func (b *layoutBuilder) addJSON(value any, mediaType string) descriptor {
	b.t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.addBlob(contents, mediaType)
}

func (b *layoutBuilder) writeIndex(manifests ...descriptor) {
	b.t.Helper()
	contents, err := json.Marshal(struct {
		SchemaVersion int          `json:"schemaVersion"`
		Manifests     []descriptor `json:"manifests"`
	}{SchemaVersion: 2, Manifests: manifests})
	if err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.root, "index.json"), contents, 0o644); err != nil {
		b.t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
