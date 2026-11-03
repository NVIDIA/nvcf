// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUmbrellaPublisherE2E(t *testing.T) {
	testUmbrellaPublisherE2E(t, false)
}

func TestUmbrellaPublisherReleaseLocalE2E(t *testing.T) {
	testUmbrellaPublisherE2E(t, true)
}

func testUmbrellaPublisherE2E(t *testing.T, releaseLocal bool) {
	t.Helper()
	fixture := umbrellaBundleFixture(t)
	args := fixture.prepareArgs()
	if releaseLocal {
		divergeUmbrellaDevelopment(t, &fixture)
		args = fixture.releaseLocalArgs()
	}
	if err := runUmbrella(args[1:]); err != nil {
		t.Fatal(err)
	}
	manifest, data, err := loadUmbrellaManifest(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = ReleaseSetQualified
	manifest.Qualification = "https://github.com/NVIDIA/nvcf/issues/2483"
	data, err = marshalUmbrellaManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "assets")
	if err := assembleUmbrellaBundle(fixture.root, fixture.inventories, directory, manifest, data, nil); err != nil {
		t.Fatal(err)
	}
	manifest, assets, err := readUmbrellaReleaseAssets(directory)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixture.root, umbrellaManifestPath(manifest.Version)), string(data))
	writeFile(t, filepath.Join(fixture.root, "deploy/releases", manifest.Version, "notes.md"), "Reviewed fixture combination.\n")
	editionGit(t, fixture.root, "add", ".")
	editionGit(t, fixture.root, "commit", "-m", "review product release")
	target := editionGit(t, fixture.root, "rev-parse", "HEAD")
	if err := runUmbrellaPublish([]string{"--repo", fixture.root, "--target", target, "--assets", directory}); err != nil {
		t.Fatal(err)
	}

	var release *umbrellaRelease
	uploaded := map[int64][]byte{}
	metadata := map[string]umbrellaReleaseAsset{}
	var writes, failed int
	next := int64(100)
	interrupt := true
	tagExists := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/NVIDIA/nvcf")
		send := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		if r.Method != "GET" {
			writes++
		}
		if r.Method == "GET" {
			for index, name := range releaseSetStackNames {
				stack := manifest.Stacks[name]
				id := int64(index + 1)
				if path == "/releases/tags/"+stack.SourceTag {
					if r.Header.Get("Authorization") != "" {
						t.Error("source visibility check used credentials")
					}
					send(umbrellaRelease{ID: id, Tag: stack.SourceTag})
					return
				}
				if path == "/git/ref/tags/"+stack.SourceTag {
					send(map[string]any{"object": map[string]string{"type": "commit", "sha": stack.SourceCommit}})
					return
				}
				if path == fmt.Sprintf("/releases/%d/assets", id) {
					send([]umbrellaReleaseAsset{{ID: id, Name: stack.InventoryAsset, State: "uploaded"}})
					return
				}
				if path == fmt.Sprintf("/releases/assets/%d", id) {
					w.Write(assets[stack.InventoryAsset])
					return
				}
			}
			if path == "/git/ref/tags/"+umbrellaTag(manifest.Version) {
				if !tagExists {
					w.WriteHeader(404)
					return
				}
				send(map[string]any{"object": map[string]string{"type": "commit", "sha": target}})
				return
			}
			if path == "/releases" {
				result := []umbrellaRelease{}
				if release != nil {
					result = append(result, *release)
				}
				send(result)
				return
			}
			if path == "/releases/99/assets" {
				result := []umbrellaReleaseAsset{}
				for _, v := range metadata {
					result = append(result, v)
				}
				send(result)
				return
			}
			if strings.HasPrefix(path, "/releases/assets/") {
				id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/releases/assets/"), 10, 64)
				if b, ok := uploaded[id]; ok {
					w.Write(b)
					return
				}
			}
		}
		if r.Method == "POST" && path == "/releases" {
			release = &umbrellaRelease{}
			json.NewDecoder(r.Body).Decode(release)
			release.ID = 99
			if !release.Draft {
				t.Error("created non-draft")
			}
			send(release)
			return
		}
		if r.Method == "POST" && path == "/releases/99/assets" {
			if interrupt && len(metadata) == 2 {
				interrupt = false
				failed++
				w.WriteHeader(503)
				return
			}
			b, _ := io.ReadAll(r.Body)
			name := r.URL.Query().Get("name")
			if _, exists := metadata[name]; exists {
				t.Error("replaced existing asset")
			}
			a := umbrellaReleaseAsset{ID: next, Name: name, State: "uploaded"}
			next++
			uploaded[a.ID] = b
			metadata[name] = a
			send(a)
			return
		}
		if r.Method == "PATCH" && path == "/releases/99" {
			if len(metadata) != len(assets) {
				t.Error("promoted incomplete release")
			}
			for name, a := range metadata {
				if !bytes.Equal(uploaded[a.ID], assets[name]) {
					t.Error("promoted corrupt release")
				}
			}
			release.Draft = false
			tagExists = true
			send(release)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, path)
		w.WriteHeader(404)
	}))
	defer server.Close()
	publisher := umbrellaPublisher{client: server.Client(), api: server.URL, uploads: server.URL, repository: "NVIDIA/nvcf", token: "fixture"}
	notes := "Reviewed fixture combination.\n"
	if err := publisher.publish(manifest, target, notes, assets, true); err == nil {
		t.Fatal("expected interrupted upload")
	}
	if failed != 1 || release == nil || !release.Draft || tagExists {
		t.Fatal("failed upload exposed a stable release")
	}
	if err := publisher.publish(manifest, target, notes, assets, true); err != nil {
		t.Fatal(err)
	}
	before := writes
	if err := publisher.publish(manifest, target, notes, assets, true); err != nil {
		t.Fatal(err)
	}
	if writes != before {
		t.Fatal("identical retry mutated stable release")
	}
	a := metadata["manifest.json"]
	uploaded[a.ID] = []byte("corruption")
	if err := publisher.publish(manifest, target, notes, assets, true); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("corrupt readback accepted: %v", err)
	}
	if writes != before {
		t.Fatal("tried to repair stable release")
	}
}

func TestUmbrellaPublisherTriggerIsolation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github/workflows/publish-umbrella-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	triggers := workflow["on"].(map[string]any)
	if len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("umbrella publication must only be manually dispatched")
	}
	jobs := workflow["jobs"].(map[string]any)
	publish := jobs["publish"].(map[string]any)
	if publish["if"] != "github.ref == 'refs/heads/main'" {
		t.Fatal("publication must use reviewed main")
	}
	if strings.Contains(string(data), "fern generate") || strings.Contains(string(data), "git push") {
		t.Fatal("artifact publisher must not publish docs or push branches")
	}
}

func TestUmbrellaPublisherRetainsReviewedTarget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github/workflows/publish-umbrella-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	inputs := workflow["on"].(map[string]any)["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
	if inputs["target"].(map[string]any)["required"] != true {
		t.Fatal("release target must be required")
	}
	job := workflow["jobs"].(map[string]any)["publish"].(map[string]any)
	if job["env"].(map[string]any)["RELEASE_TARGET"] != "${{ inputs.target }}" {
		t.Fatal("publisher must use the explicit target")
	}
	var selection string
	publishCalls := 0
	for _, raw := range job["steps"].([]any) {
		step := raw.(map[string]any)
		script, _ := step["run"].(string)
		if step["name"] == "Select reviewed release target" {
			selection = script
		}
		if strings.Contains(script, "umbrella publish") {
			publishCalls++
			if !strings.Contains(script, `--target "$RELEASE_TARGET"`) || strings.Contains(script, "$GITHUB_SHA") {
				t.Fatal("publication does not retain the reviewed target")
			}
		}
	}
	if selection == "" || publishCalls != 2 {
		t.Fatal("missing target selection or publish invocation")
	}
	repo := initTestGitRepo(t)
	git := func(args ...string) string {
		t.Helper()
		output, err := gitOutput(repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(output))
	}
	git("commit", "--allow-empty", "-m", "reviewed release")
	target := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "advance main")
	main := git("rev-parse", "HEAD")
	git("update-ref", "refs/remotes/origin/main", main)
	selectTarget := func(target string, wantSuccess bool) {
		t.Helper()
		command := exec.Command("bash", "-c", selection)
		command.Dir = repo
		command.Env = append(os.Environ(), "RELEASE_TARGET="+target)
		output, err := command.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("target %q: %v\n%s", target, err, output)
		}
		if wantSuccess && git("rev-parse", "HEAD") != target {
			t.Fatal("selected moving main instead of reviewed target")
		}
	}
	selectTarget(target, true)
	git("checkout", "--detach", main)
	git("commit", "--allow-empty", "-m", "advance main again before promotion")
	git("update-ref", "refs/remotes/origin/main", git("rev-parse", "HEAD"))
	selectTarget(target, true)
	git("commit", "--allow-empty", "-m", "unreviewed side commit")
	side := git("rev-parse", "HEAD")
	for _, rejected := range []string{"main", target[:12], strings.Repeat("f", 40), side} {
		selectTarget(rejected, false)
	}
}

func TestUmbrellaValidationCoversPublisherWorkflows(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github/workflows/umbrella-release-validation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	paths := workflow["on"].(map[string]any)["pull_request"].(map[string]any)["paths"].([]any)
	for _, required := range []string{".github/workflows/publish-umbrella-release.yml", ".github/workflows/release-tags.yml"} {
		found := false
		for _, path := range paths {
			if path == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("umbrella validation does not run for %s", required)
		}
	}
}
