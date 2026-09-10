// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubAPIURL             = "https://api.github.com"
	defaultGitHubOwner              = "NVIDIA"
	defaultGitHubRepo               = "nvcf"
	resolvedStackInventoryAssetName = "nvcf-self-managed-stack-inventory.json"
	resolvedStackInventoryMaxBytes  = 10 << 20
	stackTagPrefix                  = "deploy/stacks/self-managed/v"
	stackRefPrefix                  = "refs/tags/" + stackTagPrefix
)

var (
	stableStackVersionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	stackVersionRE       = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	numericIdentifierRE  = regexp.MustCompile(`^[0-9]+$`)
)

// githubClient reads immutable stack release refs from GitHub.
type githubClient struct {
	baseURL    string
	token      string
	owner      string
	repo       string
	httpClient *http.Client
}

// githubRefObject identifies the Git object addressed by a ref or annotated tag.
type githubRefObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// githubRef is the GitHub representation of a Git ref and its target object.
type githubRef struct {
	Ref    string          `json:"ref"`
	Object githubRefObject `json:"object"`
}

// githubRelease contains the release identity and downloadable assets needed by the updater.
type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// stackSourceRelease identifies an immutable self-managed stack source release.
type stackSourceRelease struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
	Commit  string `json:"commit"`
}

// stableStackVersion is a stable semantic version used for numeric ordering.
type stableStackVersion struct {
	major string
	minor string
	patch string
}

// newGitHubClientFromEnvironment creates a GitHub client from docs-version-sync environment variables.
func newGitHubClientFromEnvironment() *githubClient {
	baseURL := strings.TrimRight(os.Getenv("DOC_VERSION_SYNC_GITHUB_API_URL"), "/")
	if baseURL == "" {
		baseURL = defaultGitHubAPIURL
	}
	token := os.Getenv("DOC_VERSION_SYNC_GITHUB_TOKEN")
	if token == "" {
		for _, envName := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
			if value := os.Getenv(envName); value != "" {
				token = value
				break
			}
		}
	}
	return &githubClient{
		baseURL: baseURL,
		token:   token,
		owner:   defaultGitHubOwner,
		repo:    defaultGitHubRepo,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// resolveStackSourceRelease selects the latest stable release or an explicit stack source ref.
func (client *githubClient) resolveStackSourceRelease(sourceRef string) (stackSourceRelease, error) {
	wantedRef := ""
	wantedVersion := ""
	if strings.TrimSpace(sourceRef) != "" {
		var err error
		wantedRef, wantedVersion, err = normalizeStackSourceRef(sourceRef)
		if err != nil {
			return stackSourceRelease{}, err
		}
	}

	if wantedRef != "" {
		ref, err := client.stackRef(wantedRef)
		if err != nil {
			return stackSourceRelease{}, err
		}
		commit, err := client.resolveCommit(ref.Object)
		if err != nil {
			return stackSourceRelease{}, fmt.Errorf("resolve stack source ref %s: %w", wantedRef, err)
		}
		return stackSourceRelease{Version: wantedVersion, Tag: strings.TrimPrefix(wantedRef, "refs/tags/"), Commit: commit}, nil
	}

	refs, err := client.stackRefs()
	if err != nil {
		return stackSourceRelease{}, err
	}

	selected := -1
	selectedVersion := stableStackVersion{}
	selectedSourceVersion := ""
	for i := range refs {
		version, parsed, ok := parseStableStackRef(refs[i].Ref)
		if !ok {
			continue
		}
		if selected == -1 || selectedVersion.less(parsed) {
			selected = i
			selectedVersion = parsed
			selectedSourceVersion = version
		}
	}
	if selected == -1 {
		return stackSourceRelease{}, fmt.Errorf("no stable %sX.Y.Z stack source refs found in GitHub", stackTagPrefix)
	}
	commit, err := client.resolveCommit(refs[selected].Object)
	if err != nil {
		return stackSourceRelease{}, fmt.Errorf("resolve stack source ref %s: %w", refs[selected].Ref, err)
	}
	return stackSourceRelease{
		Version: selectedSourceVersion,
		Tag:     strings.TrimPrefix(refs[selected].Ref, "refs/tags/"),
		Commit:  commit,
	}, nil
}

// resolvedStackInventory downloads and validates the inventory attached to a selected release.
func (client *githubClient) resolvedStackInventory(source stackSourceRelease) (resolvedStackInventory, error) {
	if err := validateStackSourceRelease(source); err != nil {
		return resolvedStackInventory{}, err
	}
	path := fmt.Sprintf(
		"/repos/%s/%s/releases/tags/%s",
		url.PathEscape(client.owner),
		url.PathEscape(client.repo),
		url.PathEscape(source.Tag),
	)
	var release githubRelease
	if _, err := client.getJSON(path, &release); err != nil {
		return resolvedStackInventory{}, fmt.Errorf("read GitHub release for stack source %s: %w", source.Tag, err)
	}
	if release.TagName != source.Tag {
		return resolvedStackInventory{}, fmt.Errorf("GitHub release tag is %s, want selected stack source %s", release.TagName, source.Tag)
	}

	assetURL := ""
	for _, asset := range release.Assets {
		if asset.Name != resolvedStackInventoryAssetName {
			continue
		}
		if assetURL != "" {
			return resolvedStackInventory{}, fmt.Errorf("GitHub release %s has more than one %s asset", source.Tag, resolvedStackInventoryAssetName)
		}
		assetURL = asset.BrowserDownloadURL
	}
	if assetURL == "" {
		return resolvedStackInventory{}, fmt.Errorf("GitHub release %s has no %s asset", source.Tag, resolvedStackInventoryAssetName)
	}

	raw, err := client.downloadPublicAsset(assetURL)
	if err != nil {
		return resolvedStackInventory{}, fmt.Errorf("download %s from GitHub release %s: %w", resolvedStackInventoryAssetName, source.Tag, err)
	}
	inventory, err := parseResolvedStackInventory(raw)
	if err != nil {
		return resolvedStackInventory{}, fmt.Errorf("parse %s from GitHub release %s: %w", resolvedStackInventoryAssetName, source.Tag, err)
	}
	if inventory.Source != source {
		return resolvedStackInventory{}, fmt.Errorf("resolved inventory source is %+v, want selected release %+v", inventory.Source, source)
	}
	return inventory, nil
}

// stackRef reads one exact stack source ref from GitHub.
func (client *githubClient) stackRef(ref string) (githubRef, error) {
	path := fmt.Sprintf(
		"/repos/%s/%s/git/ref/%s",
		url.PathEscape(client.owner),
		url.PathEscape(client.repo),
		strings.TrimPrefix(ref, "refs/"),
	)
	var result githubRef
	if _, err := client.getJSON(path, &result); err != nil {
		return githubRef{}, fmt.Errorf("stack source ref %s was not found or could not be read from GitHub: %w", ref, err)
	}
	if result.Ref != ref {
		return githubRef{}, fmt.Errorf("GitHub returned stack source ref %s for requested ref %s", result.Ref, ref)
	}
	return result, nil
}

// stackRefs reads every GitHub ref with the self-managed stack tag prefix.
func (client *githubClient) stackRefs() ([]githubRef, error) {
	path := fmt.Sprintf(
		"/repos/%s/%s/git/matching-refs/tags/%s",
		url.PathEscape(client.owner),
		url.PathEscape(client.repo),
		stackTagPrefix,
	)
	var refs []githubRef
	for page := 1; ; {
		query := url.Values{}
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		var pageRefs []githubRef
		headers, err := client.getJSON(path+"?"+query.Encode(), &pageRefs)
		if err != nil {
			return nil, fmt.Errorf("list stack source refs: %w", err)
		}
		refs = append(refs, pageRefs...)
		nextPage, ok := nextPageFromHeaders(headers)
		if !ok {
			break
		}
		if nextPage <= page {
			return nil, fmt.Errorf("GitHub refs pagination did not advance past page %d", page)
		}
		page = nextPage
	}
	return refs, nil
}

// resolveCommit dereferences a GitHub ref object to an immutable commit SHA.
func (client *githubClient) resolveCommit(object githubRefObject) (string, error) {
	for depth := 0; depth < 10; depth++ {
		switch object.Type {
		case "commit":
			if !fullLowercaseCommitSHARe.MatchString(object.SHA) {
				return "", fmt.Errorf("GitHub commit object has invalid SHA %q", object.SHA)
			}
			return object.SHA, nil
		case "tag":
			path := fmt.Sprintf(
				"/repos/%s/%s/git/tags/%s",
				url.PathEscape(client.owner),
				url.PathEscape(client.repo),
				url.PathEscape(object.SHA),
			)
			var tag struct {
				Object githubRefObject `json:"object"`
			}
			if _, err := client.getJSON(path, &tag); err != nil {
				return "", fmt.Errorf("dereference annotated tag %s: %w", object.SHA, err)
			}
			object = tag.Object
		default:
			return "", fmt.Errorf("GitHub ref targets unsupported object type %q", object.Type)
		}
	}
	return "", fmt.Errorf("GitHub annotated tag chain exceeds 10 objects")
}

// getJSON reads and decodes one GitHub API response.
func (client *githubClient) getJSON(path string, target any) (http.Header, error) {
	httpClient := client.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	requestURL := strings.TrimRight(client.baseURL, "/") + path
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return nil, err
	}
	if client.token != "" && !strings.EqualFold(parsedURL.Scheme, "https") {
		return nil, fmt.Errorf("refuse to send GitHub token over non-HTTPS URL %s", parsedURL.Redacted())
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "nvcf-docs-version-sync")
	if client.token != "" {
		req.Header.Set("Authorization", "Bearer "+client.token)
	}
	requestClient := *httpClient
	checkRedirect := requestClient.CheckRedirect
	requestClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("refuse GitHub API redirect from HTTPS to %s", req.URL.Scheme)
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := requestClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub GET %s failed: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return nil, fmt.Errorf("decode GitHub response for %s: %w", path, err)
	}
	return resp.Header.Clone(), nil
}

func (client *githubClient) downloadPublicAsset(downloadURL string) ([]byte, error) {
	parsedURL, err := url.Parse(downloadURL)
	if err != nil {
		return nil, err
	}
	if !parsedURL.IsAbs() || parsedURL.Host == "" {
		return nil, fmt.Errorf("asset URL must be absolute")
	}
	baseURL, err := url.Parse(client.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub API base URL: %w", err)
	}
	if !strings.EqualFold(parsedURL.Scheme, "https") && !sameURLOrigin(parsedURL, baseURL) {
		return nil, fmt.Errorf("refuse non-HTTPS GitHub asset URL %s", parsedURL.Redacted())
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "nvcf-docs-version-sync")
	httpClient := client.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	requestClient := *httpClient
	checkRedirect := requestClient.CheckRedirect
	requestClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("refuse GitHub asset redirect from HTTPS to %s", req.URL.Scheme)
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := requestClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub asset GET failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, resolvedStackInventoryMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > resolvedStackInventoryMaxBytes {
		return nil, fmt.Errorf("GitHub asset exceeds %d bytes", resolvedStackInventoryMaxBytes)
	}
	return body, nil
}

func sameURLOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

// normalizeStackSourceRef converts a version, tag, or full ref to a canonical tag ref.
func normalizeStackSourceRef(sourceRef string) (string, string, error) {
	value := strings.TrimSpace(sourceRef)
	value = strings.TrimPrefix(value, "refs/tags/")
	if !strings.HasPrefix(value, stackTagPrefix) {
		value = stackTagPrefix + value
	}
	version := strings.TrimPrefix(value, stackTagPrefix)
	if !validStackVersion(version) {
		return "", "", fmt.Errorf("stack source %q must be a semantic version, %s tag, or refs/tags/%s ref", sourceRef, stackTagPrefix, stackTagPrefix)
	}
	return "refs/tags/" + value, version, nil
}

// validStackVersion reports whether a value follows the SemVer 2.0.0 grammar used by stack tags.
func validStackVersion(version string) bool {
	match := stackVersionRE.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	if match[1] == "" {
		return true
	}
	for _, identifier := range strings.Split(match[1], ".") {
		if len(identifier) > 1 && identifier[0] == '0' && numericIdentifierRE.MatchString(identifier) {
			return false
		}
	}
	return true
}

// parseStableStackRef parses a stable stack tag ref for numeric ordering.
func parseStableStackRef(ref string) (string, stableStackVersion, bool) {
	if !strings.HasPrefix(ref, stackRefPrefix) {
		return "", stableStackVersion{}, false
	}
	version := strings.TrimPrefix(ref, stackRefPrefix)
	match := stableStackVersionRE.FindStringSubmatch(version)
	if match == nil {
		return "", stableStackVersion{}, false
	}
	return version, stableStackVersion{major: match[1], minor: match[2], patch: match[3]}, true
}

// less reports whether a stable stack version precedes another version.
func (version stableStackVersion) less(other stableStackVersion) bool {
	if comparison := compareNumericIdentifier(version.major, other.major); comparison != 0 {
		return comparison < 0
	}
	if comparison := compareNumericIdentifier(version.minor, other.minor); comparison != 0 {
		return comparison < 0
	}
	return compareNumericIdentifier(version.patch, other.patch) < 0
}

// compareNumericIdentifier compares arbitrarily large decimal SemVer components.
func compareNumericIdentifier(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}
