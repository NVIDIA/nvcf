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
	"net"
	"net/http"
	"net/url"
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

func fetchWithBearer(ctx context.Context, rawURL, registry, repo string) ([]byte, error) {
	client := &http.Client{Timeout: validatorTagFetchTimeout}

	// First attempt without auth so anonymous-pullable registries work.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
	// Capture the auth challenge before closing.
	wwwAuth := resp.Header.Get("Www-Authenticate")
	resp.Body.Close()

	// Generic OCI Bearer-token exchange: uses the realm/service/scope from
	// the WWW-Authenticate header so any OCI-compliant registry works, not
	// just NGC. Falls back to NGC's /proxy_auth when the header is absent.
	token, err := exchangeBearerToken(ctx, client, registry, repo, wwwAuth)
	if err != nil {
		return nil, err
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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

// exchangeBearerToken implements the OCI Distribution Spec Bearer token flow,
// parsing realm/service/scope from the WWW-Authenticate header. Falls back to
// the NGC /proxy_auth endpoint when the header is absent or unparseable.
func exchangeBearerToken(ctx context.Context, client *http.Client, registry, repo, wwwAuthenticate string) (string, error) {
	realm, service, scope := parseWWWAuthenticate(wwwAuthenticate)

	if realm == "" {
		// No parseable WWW-Authenticate — use NGC's /proxy_auth as fallback.
		return exchangeNGCBearerToken(ctx, client, registry, repo)
	}

	// Build the token endpoint URL with service and scope query parameters.
	u, err := url.Parse(realm)
	if err != nil {
		return exchangeNGCBearerToken(ctx, client, registry, repo)
	}
	// Reject non-HTTPS or relative realms before attaching credentials.
	// The realm comes from a registry-controlled response header and must be
	// an absolute HTTPS URL to prevent sending credentials over cleartext or
	// to an unrelated host.
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("refusing token exchange at insecure or relative realm %q for %s", realm, registry)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	// Use scope from the WWW-Authenticate header when present.
	// When scope is empty and a repo is provided, synthesize the standard
	// pull scope. When neither is present (credential probe, no specific
	// repo needed), omit scope entirely — most registries issue a valid
	// token and the absence of a resource scope avoids org-level 403s for
	// non-existent repositories.
	if scope == "" && repo != "" {
		scope = "repository:" + repo + ":pull"
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	// Add credentials when present. Docker config covers any registry;
	// NGC API key is only applicable to NGC-hosted registries.
	// Critically: do NOT apply NGC_API_KEY to non-NGC registries — quay.io,
	// GHCR, and Harbor will reject it, producing a misleading "credentials
	// rejected" error when the real situation is "no credentials configured."
	if user, pass, ok := credentialsForRegistry(registry); ok {
		req.SetBasicAuth(user, pass)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// WWW-Authenticate realm failed — try NGC's /proxy_auth as last resort
		// for registries that implement both endpoints (e.g. staging NGC envs).
		if isNGCRegistry(registry) {
			return exchangeNGCBearerToken(ctx, client, registry, repo)
		}
		return "", fmt.Errorf("token exchange at %s returned %s", realm, resp.Status)
	}

	// Both "token" (OCI spec) and "access_token" (Docker Hub variant) are valid.
	var doc struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	tok := doc.Token
	if tok == "" {
		tok = doc.AccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("empty token in response from %s", realm)
	}
	return tok, nil
}

// exchangeNGCBearerToken is the NGC-specific /proxy_auth token exchange,
// kept as a named fallback for when the standard OCI flow cannot be used.
// It always rejects non-NGC registries so NGC credentials are never sent
// to an unrelated /proxy_auth endpoint.
func exchangeNGCBearerToken(ctx context.Context, client *http.Client, registry, repo string) (string, error) {
	if !isNGCRegistry(registry) {
		return "", fmt.Errorf("NGC token fallback not applicable for non-NGC registry %s", registry)
	}
	user, pass, ok := ngcCredentials(registry)
	if !ok {
		return "", fmt.Errorf("no credentials for %s", registry)
	}
	// Build scope: use actual repo when provided; omit when empty so the NGC
	// /proxy_auth endpoint validates the key without org-scoped access checks.
	scope := ""
	if repo != "" {
		scope = "repository:" + repo + ":pull"
	}
	tokenURL := fmt.Sprintf("https://%s/proxy_auth?service=%s&scope=%s",
		registry, registry, scope)
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
		return "", fmt.Errorf("NGC token exchange returned %s", resp.Status)
	}
	var doc struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.Token == "" {
		return "", fmt.Errorf("empty token in NGC response")
	}
	return doc.Token, nil
}

// parseWWWAuthenticate extracts realm, service, and scope from a standard
// Bearer challenge header:
//
//	Bearer realm="https://auth.example.com/token",service="reg.example.com",scope="repository:lib:pull"
//
// Returns empty strings when the header is absent, not a Bearer challenge, or
// cannot be parsed. The parser handles quoted values that might contain commas.
func parseWWWAuthenticate(header string) (realm, service, scope string) {
	// Split scheme from parameters on the first whitespace. HTTP auth scheme
	// names are case-insensitive (RFC 7235 s2.1), so compare with EqualFold.
	idx := strings.IndexByte(header, ' ')
	if idx < 0 || !strings.EqualFold(header[:idx], "Bearer") {
		return
	}
	params := strings.TrimSpace(header[idx+1:])
	for len(params) > 0 {
		// Find key=
		eq := strings.IndexByte(params, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(params[:eq])
		params = params[eq+1:]

		// Read value (quoted or unquoted)
		var val string
		if strings.HasPrefix(params, `"`) {
			end := strings.IndexByte(params[1:], '"')
			if end < 0 {
				break
			}
			val = params[1 : end+1]
			params = strings.TrimPrefix(strings.TrimSpace(params[end+2:]), ",")
		} else {
			comma := strings.IndexByte(params, ',')
			if comma < 0 {
				val = strings.TrimSpace(params)
				params = ""
			} else {
				val = strings.TrimSpace(params[:comma])
				params = params[comma+1:]
			}
		}

		switch key {
		case "realm":
			realm = val
		case "service":
			service = val
		case "scope":
			scope = val
		}
	}
	return
}

// ngcApprovedHosts is the set of exact hostnames (without port) that are
// considered NGC-hosted. Dot-prefixed entries match any subdomain.
var ngcApprovedHosts = []string{
	"nvcr.io",
	".nvcr.io",
	"nvidia.com",
	".nvidia.com",
	"ngc.nvidia",
	".ngc.nvidia",
}

// isNGCRegistry returns true when the registry host belongs to an NVIDIA / NGC
// domain. The check strips any port from the registry string before matching
// so that nvcr.io:443 is handled correctly, and uses dot-boundary matching to
// reject deceptive suffixes such as evilnvcr.io or nvidia.com.invalid.
func isNGCRegistry(registry string) bool {
	host := registry
	// Strip port if present (e.g. nvcr.io:5000 -> nvcr.io).
	if h, _, err := net.SplitHostPort(registry); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	for _, approved := range ngcApprovedHosts {
		if strings.HasPrefix(approved, ".") {
			// Subdomain match: host must end with ".suffix" or equal "suffix".
			suffix := approved[1:] // strip the leading dot
			if host == suffix || strings.HasSuffix(host, approved) {
				return true
			}
		} else {
			if host == approved {
				return true
			}
		}
	}
	return false
}

// credentialsForRegistry resolves (username, password) for any registry.
// Checks ~/.docker/config.json first, then falls back to NGC_API_KEY only
// for NGC-domain registries to avoid sending NGC creds to unrelated registries.
func credentialsForRegistry(registry string) (string, string, bool) {
	if u, p, ok := credsFromDockerConfig(registry); ok {
		return u, p, true
	}
	if isNGCRegistry(registry) {
		if key := firstNonEmptyEnv(ngcAPIKeyEnvNames...); key != "" {
			return "$oauthtoken", key, true
		}
	}
	return "", "", false
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
