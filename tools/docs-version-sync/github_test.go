// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewGitHubClientFromEnvironmentUsesExplicitSettings(t *testing.T) {
	t.Setenv("DOC_VERSION_SYNC_GITHUB_API_URL", "https://github.example.test/api/v3/")
	t.Setenv("DOC_VERSION_SYNC_GITHUB_TOKEN", "explicit-token")
	t.Setenv("GITHUB_TOKEN", "fallback-token")
	t.Setenv("GH_TOKEN", "other-fallback-token")

	client := newGitHubClientFromEnvironment()
	if client.baseURL != "https://github.example.test/api/v3" {
		t.Fatalf("baseURL = %q, want trimmed explicit API URL", client.baseURL)
	}
	if client.token != "explicit-token" {
		t.Fatalf("token = %q, want explicit token", client.token)
	}
	if client.owner != defaultGitHubOwner || client.repo != defaultGitHubRepo {
		t.Fatalf("repository = %s/%s, want %s/%s", client.owner, client.repo, defaultGitHubOwner, defaultGitHubRepo)
	}
}

func TestResolveStackSourceReleaseDiscoversLatestStableTag(t *testing.T) {
	const latestCommit = "ffffffffffffffffffffffffffffffffffffffff"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/NVIDIA/nvcf/git/matching-refs/tags/deploy/stacks/self-managed/v" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer github-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", fmt.Sprintf("<%s%s?per_page=100&page=2>; rel=\"next\"", server.URL, r.URL.Path))
			fmt.Fprint(w, `[
  {"ref":"refs/tags/deploy/stacks/self-managed/v0.9.9","object":{"type":"commit","sha":"0909090909090909090909090909090909090909"}},
  {"ref":"refs/tags/deploy/stacks/self-managed/v9.0.0-rc.1","object":{"type":"commit","sha":"9999999999999999999999999999999999999999"}}
]`)
		case "2":
			fmt.Fprint(w, `[
  {"ref":"refs/tags/deploy/stacks/self-managed/v184467440737095516160.14.10","object":{"type":"commit","sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}},
  {"ref":"refs/tags/deploy/stacks/self-managed/v999999999999999999999.14.10","object":{"type":"commit","sha":"`+latestCommit+`"}},
  {"ref":"refs/tags/deploy/stacks/self-managed/v10.0.0-dev.1","object":{"type":"commit","sha":"1010101010101010101010101010101010101010"}},
  {"ref":"refs/tags/deploy/stacks/self-managed/v0.14.5","object":{"type":"commit","sha":"1414141414141414141414141414141414141405"}}
]`)
		default:
			http.Error(w, "unexpected page "+r.URL.Query().Get("page"), http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := testGitHubClient(server, "github-token")
	release, err := client.resolveStackSourceRelease("")
	if err != nil {
		t.Fatalf("resolveStackSourceRelease failed: %v", err)
	}
	if release.Version != "999999999999999999999.14.10" || release.Tag != stackTagPrefix+"999999999999999999999.14.10" || release.Commit != latestCommit {
		t.Fatalf("release = %#v, want latest overflow-safe stable version at %s", release, latestCommit)
	}
}

func TestResolveStackSourceReleaseAcceptsExplicitSelectors(t *testing.T) {
	const commit = "1515151515151515151515151515151515151515"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/NVIDIA/nvcf/git/ref/tags/deploy/stacks/self-managed/v0.15.0-rc.2" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"ref":"refs/tags/deploy/stacks/self-managed/v0.15.0-rc.2","object":{"type":"commit","sha":"`+commit+`"}}`)
	}))
	defer server.Close()

	selectors := []string{
		"0.15.0-rc.2",
		stackTagPrefix + "0.15.0-rc.2",
		"refs/tags/" + stackTagPrefix + "0.15.0-rc.2",
	}
	for _, selector := range selectors {
		t.Run(selector, func(t *testing.T) {
			client := testGitHubClient(server, "")
			release, err := client.resolveStackSourceRelease(selector)
			if err != nil {
				t.Fatalf("resolveStackSourceRelease(%q) failed: %v", selector, err)
			}
			if release.Version != "0.15.0-rc.2" || release.Commit != commit {
				t.Fatalf("release = %#v, want explicit prerelease at %s", release, commit)
			}
		})
	}
}

func TestResolveStackSourceReleaseDereferencesAnnotatedTag(t *testing.T) {
	const (
		tagObject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		commit    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/NVIDIA/nvcf/git/ref/tags/deploy/stacks/self-managed/v0.14.5":
			fmt.Fprint(w, `{"ref":"refs/tags/deploy/stacks/self-managed/v0.14.5","object":{"type":"tag","sha":"`+tagObject+`"}}`)
		case "/repos/NVIDIA/nvcf/git/tags/" + tagObject:
			fmt.Fprint(w, `{"object":{"type":"commit","sha":"`+commit+`"}}`)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := testGitHubClient(server, "")
	release, err := client.resolveStackSourceRelease("0.14.5")
	if err != nil {
		t.Fatalf("resolveStackSourceRelease failed: %v", err)
	}
	if release.Commit != commit {
		t.Fatalf("commit = %q, want dereferenced commit %q", release.Commit, commit)
	}
}

func TestResolveStackSourceReleaseRejectsMissingExplicitRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer server.Close()

	client := testGitHubClient(server, "")
	_, err := client.resolveStackSourceRelease("0.14.5")
	if err == nil || !strings.Contains(err.Error(), "was not found or could not be read from GitHub") {
		t.Fatalf("resolveStackSourceRelease error = %v, want missing-ref rejection", err)
	}
}

func TestResolveStackSourceReleaseRejectsMalformedExplicitRefBeforeRequest(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := testGitHubClient(server, "")
	for _, selector := range []string{"latest", "1.2.3-01"} {
		_, err := client.resolveStackSourceRelease(selector)
		if err == nil || !strings.Contains(err.Error(), "must be a semantic version") {
			t.Errorf("resolveStackSourceRelease(%q) error = %v, want malformed-ref rejection", selector, err)
		}
	}
	if requested {
		t.Fatal("malformed explicit ref made a GitHub request")
	}
}

func TestGitHubClientRejectsTokenOverHTTP(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	client := testGitHubClient(server, "github-token")
	var result any
	_, err := client.getJSON("/test", &result)
	if err == nil || !strings.Contains(err.Error(), "refuse to send GitHub token over non-HTTPS") {
		t.Fatalf("getJSON error = %v, want cleartext-token rejection", err)
	}
	if requested {
		t.Fatal("cleartext token request reached the server")
	}
}

func TestGitHubClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	redirected := false
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		fmt.Fprint(w, `{}`)
	}))
	defer httpServer.Close()
	httpsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL+"/target", http.StatusFound)
	}))
	defer httpsServer.Close()

	client := testGitHubClient(httpsServer, "github-token")
	var result any
	_, err := client.getJSON("/test", &result)
	if err == nil || !strings.Contains(err.Error(), "refuse GitHub API redirect from HTTPS to http") {
		t.Fatalf("getJSON error = %v, want HTTPS downgrade rejection", err)
	}
	if redirected {
		t.Fatal("HTTPS downgrade redirect reached the cleartext server")
	}
}

func TestResolveStackSourceReleaseRejectsOnlyPrereleaseTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
  {"ref":"refs/tags/deploy/stacks/self-managed/v0.15.0-rc.1","object":{"type":"commit","sha":"1515151515151515151515151515151515151515"}},
  {"ref":"refs/tags/deploy/stacks/self-managed/v0.16.0-dev.1","object":{"type":"commit","sha":"1616161616161616161616161616161616161616"}}
]`)
	}))
	defer server.Close()

	client := testGitHubClient(server, "")
	_, err := client.resolveStackSourceRelease("")
	if err == nil || !strings.Contains(err.Error(), "no stable") {
		t.Fatalf("resolveStackSourceRelease error = %v, want no-stable-release rejection", err)
	}
}

func TestResolvedStackInventoryRejectsMissingAsset(t *testing.T) {
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":"other.json","browser_download_url":"https://example.com/other.json"}]}`, source.Tag)
	}))
	defer server.Close()

	_, err := testGitHubClient(server, "").resolvedStackInventory(source)
	if err == nil || !strings.Contains(err.Error(), "has no "+resolvedStackInventoryAssetName+" asset") {
		t.Fatalf("resolvedStackInventory error = %v, want missing asset rejection", err)
	}
}

func TestResolvedStackInventoryRejectsMismatchedSourceIdentity(t *testing.T) {
	source := stackSourceRelease{Version: "1.2.3", Tag: stackTagPrefix + "1.2.3", Commit: strings.Repeat("a", 40)}
	other := source
	other.Commit = strings.Repeat("b", 40)
	raw, err := marshalResolvedStackInventory(testCatalogResolvedInventory(t, other))
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/NVIDIA/nvcf/releases/tags/" + source.Tag:
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q}]}`, source.Tag, resolvedStackInventoryAssetName, server.URL+"/inventory")
		case "/inventory":
			w.Write(raw)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	_, err = testGitHubClient(server, "").resolvedStackInventory(source)
	if err == nil || !strings.Contains(err.Error(), "want selected release") {
		t.Fatalf("resolvedStackInventory error = %v, want source identity rejection", err)
	}
}

func TestDownloadPublicAssetRejectsExternalHTTP(t *testing.T) {
	client := &githubClient{baseURL: defaultGitHubAPIURL, httpClient: http.DefaultClient}
	_, err := client.downloadPublicAsset("http://example.com/inventory.json")
	if err == nil || !strings.Contains(err.Error(), "refuse non-HTTPS GitHub asset URL") {
		t.Fatalf("downloadPublicAsset error = %v, want non-HTTPS rejection", err)
	}
}

// testGitHubClient creates a GitHub client backed by a local test server.
func testGitHubClient(server *httptest.Server, token string) *githubClient {
	return &githubClient{
		baseURL:    server.URL,
		token:      token,
		owner:      defaultGitHubOwner,
		repo:       defaultGitHubRepo,
		httpClient: server.Client(),
	}
}
