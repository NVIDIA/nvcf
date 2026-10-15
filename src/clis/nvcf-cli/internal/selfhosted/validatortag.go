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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	// Wall-clock budget for cache freshness. Preflight is rarely re-run more
	// often than this, and the cluster-validator's rc cadence is multi-day.
	validatorTagCacheTTL = 1 * time.Hour

	// Hard upper bound on the registry round-trip so a slow NGC can't stall
	// preflight; we fall back to the const if this trips.
	validatorTagFetchTimeout = 5 * time.Second
)

// Restrict to X.Y.Z or X.Y.Z-rc.N tags; excludes sigstore metadata
// (sha256-*.sig/sbom/vex) and commit-SHA pre-releases (X.Y.Z-vSHA).
var validatorTagPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-rc\.\d+)?$`)

// ResolveLatestValidatorTag returns baseImage's repo with the latest tag
// substituted, sourced from the OCI registry hosting baseImage. Prefers
// stable releases over rc; falls back to the highest rc when no stable
// exists. Returns ("", false) on any failure (network, auth, parse, no
// matching tags) so the caller can fall back to the input value as-is.
//
// Discovery only runs when baseImage has no tag. A pinned tag
// (image:vX.Y.Z) is honored verbatim — operators who specify a tag
// must get exactly that tag, never a registry-side override.
//
// Reads and writes a 1h-TTL cache at ~/.cache/nvcf-cli/validator-tag.json
// so back-to-back preflight runs share the result without re-hitting the
// registry.
func ResolveLatestValidatorTag(ctx context.Context, baseImage string) (string, bool) {
	registry, repo, tag, ok := parseImageRef(baseImage)
	if !ok {
		return "", false
	}
	// Operator pinned an explicit tag — respect it, no discovery. The
	// caller's fallback path uses baseImage unchanged in this case.
	if tag != "" {
		return "", false
	}

	if cached, ok := readValidatorTagCache(baseImage); ok {
		return fmt.Sprintf("%s/%s:%s", registry, repo, cached), true
	}

	fetchCtx, cancel := context.WithTimeout(ctx, validatorTagFetchTimeout)
	defer cancel()
	tags, err := fetchValidatorTags(fetchCtx, registry, repo)
	if err != nil || len(tags) == 0 {
		return "", false
	}
	best := pickBestValidatorTag(tags)
	if best == "" {
		return "", false
	}
	_ = writeValidatorTagCache(baseImage, best)
	return fmt.Sprintf("%s/%s:%s", registry, repo, best), true
}

// parseImageRef splits "registry/repo:tag" or "registry/repo@digest" into
// its parts. The registry must contain a '.' or ':' to distinguish a
// real hostname from a Docker Hub library shorthand. Returns ok=false on
// any shape we don't recognize.
func parseImageRef(image string) (registry, repo, tag string, ok bool) {
	image = strings.TrimSpace(image)
	slash := strings.Index(image, "/")
	if slash <= 0 {
		return "", "", "", false
	}
	head := image[:slash]
	if !strings.ContainsAny(head, ".:") && head != "localhost" {
		return "", "", "", false
	}
	rest := image[slash+1:]
	if at := strings.Index(rest, "@"); at != -1 {
		return head, rest[:at], rest[at+1:], true
	}
	if colon := strings.LastIndex(rest, ":"); colon != -1 {
		return head, rest[:colon], rest[colon+1:], true
	}
	return head, rest, "", true
}

// fetchValidatorTags walks the OCI tag-list endpoint for a single repo.
// Handles the standard NGC bearer-token exchange: try anonymous, on 401
// re-auth using credentials from ~/.docker/config.json or NGC_API_KEY.
func fetchValidatorTags(ctx context.Context, registry, repo string) ([]string, error) {
	tagsURL := fmt.Sprintf("https://%s/v2/%s/tags/list", registry, repo)
	body, err := fetchWithBearer(ctx, tagsURL, registry, repo)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode tags response: %w", err)
	}
	return doc.Tags, nil
}

func fetchWithBearer(ctx context.Context, url, registry, repo string) ([]byte, error) {
	client := &http.Client{Timeout: validatorTagFetchTimeout}

	// First attempt without auth so anonymous-pullable registries work.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		return nil, fmt.Errorf("registry returned %s", resp.Status)
	}
	resp.Body.Close()

	// Bearer-token exchange. Realm and scope come from the Www-Authenticate
	// header; for NGC the realm is /proxy_auth and scope is repository:<repo>:pull.
	token, err := exchangeBearerToken(ctx, client, registry, repo)
	if err != nil {
		return nil, err
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s after bearer auth", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// exchangeBearerToken does the NGC token exchange using credentials
// resolved from ~/.docker/config.json or NGC env vars.
//
// NGC-specific: this constructs the token URL using NGC's /proxy_auth
// realm rather than parsing the Www-Authenticate header from the 401
// response (the generic OAuth2 distribution flow). Non-NGC registries
// (GCR, ECR, GHCR, Harbor, etc.) will return a non-200 here and the
// caller falls back to using the configured image reference as-is.
// Acceptable for v1 since the validator image only ships from NGC.
func exchangeBearerToken(ctx context.Context, client *http.Client, registry, repo string) (string, error) {
	user, pass, ok := ngcCredentials(registry)
	if !ok {
		return "", fmt.Errorf("no NGC credentials for %s", registry)
	}
	tokenURL := fmt.Sprintf("https://%s/proxy_auth?service=%s&scope=repository:%s:pull", registry, registry, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, pass)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange returned %s", resp.Status)
	}
	var doc struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.Token == "" {
		return "", fmt.Errorf("empty token in response")
	}
	return doc.Token, nil
}

// ngcCredentials resolves (username, password) for an NGC-hosted registry.
// Checks ~/.docker/config.json first; falls back to NGC_API_KEY env vars
// with the literal "$oauthtoken" sentinel username NGC expects.
func ngcCredentials(registry string) (string, string, bool) {
	if u, p, ok := credsFromDockerConfig(registry); ok {
		return u, p, true
	}
	if key := firstNonEmptyEnv(ngcAPIKeyEnvNames...); key != "" {
		return "$oauthtoken", key, true
	}
	return "", "", false
}

func credsFromDockerConfig(registry string) (string, string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	body, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	if err != nil {
		return "", "", false
	}
	var doc struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", false
	}
	entry, ok := doc.Auths[registry]
	if !ok {
		return "", "", false
	}
	if entry.Username != "" && entry.Password != "" {
		return entry.Username, entry.Password, true
	}
	if entry.Auth != "" {
		raw, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err != nil {
			return "", "", false
		}
		colon := strings.IndexByte(string(raw), ':')
		if colon == -1 {
			return "", "", false
		}
		return string(raw[:colon]), string(raw[colon+1:]), true
	}
	return "", "", false
}

// pickBestValidatorTag filters to recognized validator tags and returns
// the best one:
//
//  1. Highest stable X.Y.Z (no pre-release) if any exist.
//  2. Otherwise highest X.Y.Z-rc.N.
//
// Returns "" when nothing matches.
func pickBestValidatorTag(tags []string) string {
	var stable, prerelease []*semver.Version
	for _, t := range tags {
		if !validatorTagPattern.MatchString(t) {
			continue
		}
		v, err := semver.NewVersion(t)
		if err != nil {
			continue
		}
		if v.Prerelease() == "" {
			stable = append(stable, v)
		} else {
			prerelease = append(prerelease, v)
		}
	}
	if len(stable) > 0 {
		sort.Sort(sort.Reverse(semver.Collection(stable)))
		return stable[0].Original()
	}
	if len(prerelease) > 0 {
		sort.Sort(sort.Reverse(semver.Collection(prerelease)))
		return prerelease[0].Original()
	}
	return ""
}

// Cache layout under XDG cache dir:
//
//	~/.cache/nvcf-cli/validator-tag.json
//
// The file is keyed by baseImage so a stack pointing at a different
// repo doesn't share entries with the default repo.
type validatorTagCacheEntry struct {
	Image     string    `json:"image"`
	Tag       string    `json:"tag"`
	FetchedAt time.Time `json:"fetched_at"`
}

type validatorTagCache map[string]validatorTagCacheEntry

func validatorTagCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nvcf-cli", "validator-tag.json"), nil
}

func readValidatorTagCache(baseImage string) (string, bool) {
	path, err := validatorTagCachePath()
	if err != nil {
		return "", false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var cache validatorTagCache
	if err := json.Unmarshal(body, &cache); err != nil {
		return "", false
	}
	entry, ok := cache[baseImage]
	if !ok {
		return "", false
	}
	if time.Since(entry.FetchedAt) > validatorTagCacheTTL {
		return "", false
	}
	return entry.Tag, true
}

func writeValidatorTagCache(baseImage, tag string) error {
	path, err := validatorTagCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cache := validatorTagCache{}
	if body, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(body, &cache)
	}
	cache[baseImage] = validatorTagCacheEntry{
		Image:     baseImage,
		Tag:       tag,
		FetchedAt: time.Now().UTC(),
	}
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
