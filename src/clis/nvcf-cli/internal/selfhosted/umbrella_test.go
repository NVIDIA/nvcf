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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func umbrellaFixtureFiles(t *testing.T) map[string]umbrellaFile {
	t.Helper()
	manifest := umbrellaManifest{SchemaVersion: 1, Version: "2.0.3", Change: "initial", Status: "qualified", Qualification: "https://github.com/NVIDIA/nvcf/issues/2483", SourceCommit: strings.Repeat("a", 40), Stacks: map[string]UmbrellaStack{}}
	manifest.Toolchain.Helm = "3.19.1"
	manifest.Toolchain.Helmfile = "1.1.9"
	versions := map[string]string{"control-plane": "1.0.1", "compute-plane": "1.0.2", "observability": "1.0.0"}
	files := map[string]umbrellaFile{"VERSION": {data: []byte(manifest.Version + "\n"), mode: 0o644}}
	for plane, directory := range umbrellaDirectories {
		inventory := []byte("fixture " + plane)
		asset := "nvcf-" + directory + "-inventory.json"
		peers := map[string]string{}
		for peer, version := range versions {
			if peer != plane {
				peers[peer] = version
			}
		}
		manifest.Stacks[plane] = UmbrellaStack{Version: versions[plane], SourceCommit: strings.Repeat("b", 40), SourceTag: "deploy/stacks/" + directory + "/v" + versions[plane], InventoryAsset: asset, InventorySHA256: umbrellaHash(inventory), CompatibleWith: peers}
		prefix := "deploy/stacks/" + directory + "/"
		files[prefix+"VERSION"] = umbrellaFile{data: []byte(versions[plane] + "\n"), mode: 0o644}
		files[prefix+"environments/base.yaml"] = umbrellaFile{data: []byte("fixture: true\n"), mode: 0o644}
		files[prefix+"helmfile.d/01-stack.yaml"] = umbrellaFile{data: []byte("releases: []\n"), mode: 0o644}
		files["inventories/"+asset] = umbrellaFile{data: inventory, mode: 0o644}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = umbrellaFile{data: data, mode: 0o644}
	return files
}

func umbrellaFixtureArchive(t *testing.T, files map[string]umbrellaFile) []byte {
	t.Helper()
	var manifest umbrellaManifest
	if err := json.Unmarshal(files["manifest.json"].data, &manifest); err != nil {
		t.Fatal(err)
	}
	index := umbrellaFileIndex{SchemaVersion: 1, Version: manifest.Version, ManifestSHA256: umbrellaHash(files["manifest.json"].data), Files: map[string]umbrellaFileReceipt{}}
	for name, file := range files {
		if name != "bundle-files.json" {
			index.Files[name] = umbrellaFileReceipt{SHA256: umbrellaHash(file.data), Mode: file.mode}
		}
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	files["bundle-files.json"] = umbrellaFile{data: data, mode: 0o644}
	var buffer bytes.Buffer
	zip := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zip)
	for name, file := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: file.mode, Size: int64(len(file.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestUmbrellaSelectionValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		opts   *UmbrellaOptions
		source string
		fail   bool
	}{
		{name: "legacy defaults"},
		{name: "release", opts: &UmbrellaOptions{Release: "1.0.3"}},
		{name: "ambiguous", opts: &UmbrellaOptions{Release: "1.0.3", Bundle: "local"}, fail: true},
		{name: "component override", opts: &UmbrellaOptions{Release: "1.0.3"}, source: "oci://existing", fail: true},
		{name: "latest", opts: &UmbrellaOptions{Release: "latest"}, fail: true},
		{name: "missing local checksum", opts: &UmbrellaOptions{Bundle: "local"}, fail: true},
		{name: "checksum without selector", opts: &UmbrellaOptions{SHA256: umbrellaHash(nil)}, fail: true},
		{name: "invalid checksum", opts: &UmbrellaOptions{Release: "1.0.3", SHA256: "bad"}, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateUmbrellaOptions(test.opts, test.source)
			if (err != nil) != test.fail {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestResolveUmbrellaColdWarmAndReceipt(t *testing.T) {
	files := umbrellaFixtureFiles(t)
	files["deploy/stacks/self-managed/scripts/hook.sh"] = umbrellaFile{data: []byte("#!/bin/sh\nexit 0\n"), mode: 0o755}
	archive := umbrellaFixtureArchive(t, files)
	digest := umbrellaHash(archive)
	local := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(local, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	ctx := context.Background()
	var root string
	for plane, directory := range umbrellaDirectories {
		opts := StackOptions{Umbrella: &UmbrellaOptions{Bundle: local, SHA256: digest}, Plane: plane, CacheDir: cache}
		first, err := ResolveStack(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		second, err := ResolveStack(ctx, opts)
		if err != nil || first.Path != second.Path {
			t.Fatalf("warm cache: %v", err)
		}
		if first.Source != "umbrella" || filepath.Base(first.Path) != directory || first.Umbrella.SHA256 != digest {
			t.Fatalf("wrong identity: %+v", first)
		}
		actual, err := os.ReadFile(filepath.Join(first.Path, "VERSION"))
		if err != nil || string(actual) != first.Umbrella.Stacks[plane].Version+"\n" {
			t.Fatal("component receipt version replaced by umbrella version")
		}
		root = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(first.Path))))
		receipt, err := os.ReadFile(filepath.Join(root, "selection.json"))
		if err != nil {
			t.Fatal(err)
		}
		var identity UmbrellaIdentity
		if err := json.Unmarshal(receipt, &identity); err != nil || identity.SHA256 != digest || len(identity.Stacks) != 3 {
			t.Fatal("selection receipt round trip")
		}
	}
	// Pinned release selection can reuse the verified cache without network.
	warm, err := ResolveStack(ctx, StackOptions{Umbrella: &UmbrellaOptions{Release: "2.0.3", SHA256: digest}, Plane: "control-plane", CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(warm.Path, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(warm.Path, "out/generated-values.yaml"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCachedUmbrella(root, "self-managed", digest, "2.0.3"); err != nil {
		t.Fatalf("generated install output invalidated cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(warm.Path, "VERSION"), []byte("9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCachedUmbrella(root, "self-managed", digest, "2.0.3"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("accepted corrupt warm cache: %v", err)
	}
}

func TestUmbrellaArchiveRejectsInvalidContents(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]umbrellaFile)
	}{
		{"missing stack", func(files map[string]umbrellaFile) {
			delete(files, "deploy/stacks/nvcf-compute-plane/helmfile.d/01-stack.yaml")
		}},
		{"wrong component receipt", func(files map[string]umbrellaFile) {
			files["deploy/stacks/self-managed/VERSION"] = umbrellaFile{data: []byte("2.0.3\n"), mode: 0o644}
		}},
		{"path traversal", func(files map[string]umbrellaFile) {
			files["../outside"] = umbrellaFile{data: []byte("unsafe"), mode: 0o644}
		}},
		{"bad inventory", func(files map[string]umbrellaFile) {
			for name := range files {
				if strings.HasPrefix(name, "inventories/") {
					delete(files, name)
					break
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := umbrellaFixtureFiles(t)
			test.mutate(files)
			archive := umbrellaFixtureArchive(t, files)
			if _, _, err := verifyUmbrellaPayload(archive, umbrellaHash(archive), ""); err == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
	archive := umbrellaFixtureArchive(t, umbrellaFixtureFiles(t))
	if _, _, err := verifyUmbrellaPayload(archive, umbrellaHash([]byte("wrong")), ""); err == nil {
		t.Fatal("accepted bad download checksum")
	}
	if _, _, err := verifyUmbrellaPayload(archive, umbrellaHash(archive), "99.0.0"); err == nil {
		t.Fatal("accepted wrong release version")
	}
}

func TestDownloadUmbrellaRelease(t *testing.T) {
	archive := umbrellaFixtureArchive(t, umbrellaFixtureFiles(t))
	digest := umbrellaHash(archive)
	missing, prerelease := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "nvcf/2.0.3", "draft": false, "prerelease": prerelease})
		case strings.HasSuffix(r.URL.Path, "release-assets.json"):
			json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "version": "2.0.3", "assets": map[string]string{"nvcf-2.0.3.tar.gz": digest}})
		default:
			if missing {
				w.WriteHeader(404)
			} else {
				w.Write(archive)
			}
		}
	}))
	defer server.Close()
	actual, got, err := downloadUmbrellaRelease(context.Background(), "2.0.3", "", server.Client(), server.URL, server.URL)
	if err != nil || got != digest || !bytes.Equal(actual, archive) {
		t.Fatalf("download: %v", err)
	}
	if _, _, err := verifyUmbrellaPayload(actual, got, "2.0.3"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := downloadUmbrellaRelease(context.Background(), "2.0.3", umbrellaHash(nil), server.Client(), server.URL, server.URL); err == nil {
		t.Fatal("accepted conflicting trusted pin")
	}
	missing = true
	if _, _, err := downloadUmbrellaRelease(context.Background(), "2.0.3", "", server.Client(), server.URL, server.URL); err == nil {
		t.Fatal("accepted missing bundle")
	}
	missing = false
	prerelease = true
	if _, _, err := downloadUmbrellaRelease(context.Background(), "2.0.3", "", server.Client(), server.URL, server.URL); err == nil {
		t.Fatal("accepted prerelease as stable")
	}
}

func TestResolveUmbrellaRestrictiveUmask(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []string{"027", "077"} {
		t.Run(mask, func(t *testing.T) {
			// Change the umask only in a subprocess; it is process-wide state.
			cmd := exec.Command("sh", "-c", `umask "$1"; exec "$2" -test.run '^TestResolveUmbrellaColdWarmAndReceipt$'`, "umbrella-cache-test", mask, executable)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cold/warm cache with umask %s: %v\n%s", mask, err, output)
			}
		})
	}
}
