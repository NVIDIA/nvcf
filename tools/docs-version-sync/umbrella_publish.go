// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type umbrellaHTTPError struct {
	method, path string
	status       int
}

func (e *umbrellaHTTPError) Error() string {
	return fmt.Sprintf("release API %s %s: HTTP %d", e.method, e.path, e.status)
}

// The publisher only adds missing draft assets. Existing bytes are never replaced.
type umbrellaPublisher struct {
	client                          *http.Client
	api, uploads, repository, token string
}
type umbrellaReleaseAsset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}
type umbrellaRelease struct {
	ID         int64  `json:"id"`
	Tag        string `json:"tag_name"`
	Target     string `json:"target_commitish"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (p umbrellaPublisher) request(method, endpoint, contentType string, data []byte) ([]byte, error) {
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if contentType == "download" {
		req.Header.Set("Accept", "application/octet-stream")
	} else if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, umbrellaBundleMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > umbrellaBundleMaxBytes {
		return nil, fmt.Errorf("release response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &umbrellaHTTPError{method: method, path: req.URL.Path, status: response.StatusCode}
	}
	return contents, nil
}

func (p umbrellaPublisher) json(method, path string, body, result any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	data, err = p.request(method, p.api+"/repos/"+p.repository+path, "application/json", data)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func (p umbrellaPublisher) findRelease(tag string) (*umbrellaRelease, error) {
	var found *umbrellaRelease
	for page := 1; ; page++ {
		var releases []umbrellaRelease
		if err := p.json("GET", fmt.Sprintf("/releases?per_page=100&page=%d", page), nil, &releases); err != nil {
			return nil, err
		}
		for _, release := range releases {
			if release.Tag == tag {
				if found != nil {
					return nil, fmt.Errorf("duplicate umbrella release %s", tag)
				}
				copy := release
				found = &copy
			}
		}
		if len(releases) < 100 {
			return found, nil
		}
	}
}

func (p umbrellaPublisher) assets(id int64) (map[string]umbrellaReleaseAsset, error) {
	result := map[string]umbrellaReleaseAsset{}
	for page := 1; ; page++ {
		var assets []umbrellaReleaseAsset
		if err := p.json("GET", fmt.Sprintf("/releases/%d/assets?per_page=100&page=%d", id, page), nil, &assets); err != nil {
			return nil, err
		}
		for _, asset := range assets {
			if _, exists := result[asset.Name]; exists {
				return nil, fmt.Errorf("duplicate release asset %s", asset.Name)
			}
			result[asset.Name] = asset
		}
		if len(assets) < 100 {
			return result, nil
		}
	}
}

func (p umbrellaPublisher) verifyAssets(release umbrellaRelease, expected map[string][]byte, upload bool) error {
	existing, err := p.assets(release.ID)
	if err != nil {
		return err
	}
	for name := range existing {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("unexpected release asset %s", name)
		}
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		asset, ok := existing[name]
		if !ok {
			if !release.Draft || !upload {
				return fmt.Errorf("release is missing asset %s", name)
			}
			endpoint := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", p.uploads, p.repository, release.ID, url.QueryEscape(name))
			data, err := p.request("POST", endpoint, "application/octet-stream", expected[name])
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &asset); err != nil {
				return err
			}
		}
		if asset.State != "uploaded" || asset.Name != name {
			return fmt.Errorf("release asset %s is incomplete; inspect the draft before retrying", name)
		}
		endpoint := fmt.Sprintf("%s/repos/%s/releases/assets/%d", p.api, p.repository, asset.ID)
		actual, err := p.request("GET", endpoint, "download", nil)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected[name]) {
			return fmt.Errorf("immutable release asset differs: %s", name)
		}
	}
	return nil
}

func (p umbrellaPublisher) verifyTag(tag, target string, allowMissing bool) error {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	err := p.json("GET", "/git/ref/tags/"+tag, nil, &ref)
	var status *umbrellaHTTPError
	if err != nil {
		if allowMissing && errors.As(err, &status) && status.status == 404 {
			return nil
		}
		return err
	}
	for depth := 0; ref.Object.Type == "tag" && depth < 5; depth++ {
		if err := p.json("GET", "/git/tags/"+ref.Object.SHA, nil, &ref); err != nil {
			return err
		}
	}
	if ref.Object.Type != "commit" || ref.Object.SHA != target {
		return fmt.Errorf("tag %s does not select expected commit %s", tag, target)
	}
	return nil
}

func (p umbrellaPublisher) verifyPublicSources(manifest *umbrellaManifest, assets map[string][]byte) error {
	public := p
	public.token = ""
	for _, name := range releaseSetStackNames {
		stack := manifest.Stacks[name]
		var release umbrellaRelease
		if err := public.json("GET", "/releases/tags/"+stack.SourceTag, nil, &release); err != nil {
			return err
		}
		if release.Draft || release.Prerelease || release.Tag != stack.SourceTag {
			return fmt.Errorf("%s must be a public stable component release", stack.SourceTag)
		}
		if err := public.verifyTag(stack.SourceTag, stack.SourceCommit, false); err != nil {
			return err
		}
		remote, err := public.assets(release.ID)
		if err != nil {
			return err
		}
		asset, ok := remote[stack.InventoryAsset]
		if !ok || asset.State != "uploaded" {
			return fmt.Errorf("public inventory missing: %s", stack.InventoryAsset)
		}
		data, err := public.request("GET", fmt.Sprintf("%s/repos/%s/releases/assets/%d", p.api, p.repository, asset.ID), "download", nil)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, assets[stack.InventoryAsset]) {
			return fmt.Errorf("public inventory differs: %s", stack.InventoryAsset)
		}
	}
	return nil
}

func (p umbrellaPublisher) publish(manifest *umbrellaManifest, target, notes string, assets map[string][]byte, promote bool) error {
	if promote && manifest.Status != ReleaseSetQualified {
		return fmt.Errorf("stable promotion requires whole-combination qualification")
	}
	if err := p.verifyPublicSources(manifest, assets); err != nil {
		return err
	}
	if err := p.verifyTag(umbrellaTag(manifest.Version), target, true); err != nil {
		return err
	}
	release, err := p.findRelease(umbrellaTag(manifest.Version))
	if err != nil {
		return err
	}
	if release == nil {
		release = &umbrellaRelease{}
		body := map[string]any{"tag_name": umbrellaTag(manifest.Version), "target_commitish": target, "name": "NVCF " + manifest.Version, "body": notes, "draft": true, "prerelease": false, "make_latest": "false"}
		if err := p.json("POST", "/releases", body, release); err != nil {
			return err
		}
	}
	if release.Tag != umbrellaTag(manifest.Version) || release.Target != target || release.Body != notes || release.Prerelease {
		return fmt.Errorf("existing umbrella release identity or notes differ")
	}
	if err := p.verifyAssets(*release, assets, true); err != nil {
		return err
	}
	if release.Draft && promote {
		if err := p.verifyTag(umbrellaTag(manifest.Version), target, true); err != nil {
			return err
		}
		// Re-read all bytes before exposing the release. No mutation of a stable release.
		if err := p.verifyAssets(*release, assets, false); err != nil {
			return err
		}
		if err := p.json("PATCH", fmt.Sprintf("/releases/%d", release.ID), map[string]any{"draft": false, "make_latest": "false"}, release); err != nil {
			return err
		}
		if release.Draft {
			return fmt.Errorf("release was not promoted")
		}
	}
	if !release.Draft {
		if err := p.verifyTag(umbrellaTag(manifest.Version), target, false); err != nil {
			return err
		}
	}
	return p.verifyAssets(*release, assets, false)
}

func readUmbrellaReleaseAssets(directory string) (*umbrellaManifest, map[string][]byte, error) {
	manifest, manifestData, err := loadUmbrellaManifest(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "release-assets.json"))
	if err != nil {
		return nil, nil, err
	}
	var index umbrellaAssetIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return nil, nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, nil, err
	}
	required := map[string]bool{"manifest.json": true, "inventory.json": true, umbrellaBundleName(manifest.Version): true}
	for _, stack := range manifest.Stacks {
		required[stack.InventoryAsset] = true
	}
	if index.SchemaVersion != 1 || index.Version != manifest.Version || len(index.Assets) != len(required) {
		return nil, nil, fmt.Errorf("release asset index identity or count mismatch")
	}
	assets := map[string][]byte{"release-assets.json": data}
	for name, digest := range index.Assets {
		if !required[name] || !lowercaseSHA256DigestRe.MatchString(digest) {
			return nil, nil, fmt.Errorf("unexpected indexed release asset %s", name)
		}
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil {
			return nil, nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > umbrellaBundleMaxBytes {
			return nil, nil, fmt.Errorf("invalid release asset %s", name)
		}
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, nil, err
		}
		if umbrellaDigest(content) != digest {
			return nil, nil, fmt.Errorf("release asset checksum mismatch: %s", name)
		}
		assets[name] = content
	}
	if !bytes.Equal(assets["manifest.json"], manifestData) {
		return nil, nil, fmt.Errorf("manifest changed while loading")
	}
	if err := verifyUmbrellaArchive(assets[umbrellaBundleName(manifest.Version)], manifestData, index.Assets[umbrellaBundleName(manifest.Version)]); err != nil {
		return nil, nil, err
	}
	files, err := readUmbrellaArchive(assets[umbrellaBundleName(manifest.Version)])
	if err != nil {
		return nil, nil, err
	}
	for name, content := range assets {
		embedded := name
		if required[name] && name != "manifest.json" && name != "inventory.json" && name != umbrellaBundleName(manifest.Version) {
			embedded = "inventories/" + name
		}
		if file, ok := files[embedded]; ok && !bytes.Equal(file.data, content) {
			return nil, nil, fmt.Errorf("asset differs from bundle: %s", name)
		}
	}
	return manifest, assets, nil
}

func runUmbrellaPublish(args []string) error {
	flags := flag.NewFlagSet("umbrella publish", flag.ContinueOnError)
	root := flags.String("repo", ".", "local reviewed repository")
	directory := flags.String("assets", "", "assembled release assets directory")
	target := flags.String("target", "", "full commit containing the reviewed manifest and release notes")
	write := flags.Bool("write", false, "create or resume a draft release; default is local validation only")
	promote := flags.Bool("promote", false, "promote a fully verified qualified draft to stable (requires --write)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || !fullLowercaseCommitSHARe.MatchString(*target) || (*promote && !*write) {
		return fmt.Errorf("publish requires --assets and full --target; --promote also requires --write")
	}
	manifest, assets, err := readUmbrellaReleaseAssets(*directory)
	if err != nil {
		return err
	}
	committed, err := gitOutput(*root, "show", *target+":"+umbrellaManifestPath(manifest.Version))
	if err != nil {
		return err
	}
	if !bytes.Equal(committed, assets["manifest.json"]) {
		return fmt.Errorf("release target must contain the exact reviewed manifest")
	}
	if _, err := gitOutput(*root, "merge-base", "--is-ancestor", manifest.SourceCommit, *target); err != nil {
		return fmt.Errorf("release target must descend from reviewed catalog source: %w", err)
	}
	if err := validatePublishedUmbrella(*root, manifest, assets["manifest.json"]); err != nil {
		return err
	}
	previousPath := ""
	if manifest.PreviousVersion != "" {
		previousPath = umbrellaManifestPath(manifest.PreviousVersion)
	}
	previous, err := loadPreviousUmbrella(*root, previousPath)
	if err != nil {
		return err
	}
	if err := validateUmbrellaTransition(previous, manifest); err != nil {
		return err
	}
	if _, err := validateUmbrellaSources(*root, manifest, *directory); err != nil {
		return err
	}
	notes, err := gitOutput(*root, "show", *target+":deploy/releases/"+manifest.Version+"/notes.md")
	if err != nil || strings.TrimSpace(string(notes)) == "" {
		return fmt.Errorf("release target requires customer notes at deploy/releases/%s/notes.md", manifest.Version)
	}
	if *promote && manifest.Status != ReleaseSetQualified {
		return fmt.Errorf("stable promotion requires reviewed whole-combination qualification")
	}
	if !*write {
		fmt.Printf("Validated umbrella %s publication; no remote writes\n", manifest.Version)
		return nil
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("publication requires GITHUB_TOKEN")
	}
	p := umbrellaPublisher{client: &http.Client{Timeout: 2 * time.Minute}, api: "https://api.github.com", uploads: "https://uploads.github.com", repository: "NVIDIA/nvcf", token: token}
	return p.publish(manifest, *target, string(notes), assets, *promote)
}
