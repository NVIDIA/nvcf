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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	// Wall-clock budget for cache freshness. Preflight is rarely re-run more
	// often than this, and the cluster-validator's rc cadence is multi-day.
	validatorTagCacheTTL = 1 * time.Hour

	// Hard upper bound on the registry round trips of tag discovery, so a
	// slow registry cannot stall preflight. When it trips, an untagged image
	// is reported as unresolved rather than launched.
	validatorTagFetchTimeout = 5 * time.Second
)

// Restrict to X.Y.Z or X.Y.Z-rc.N tags; excludes sigstore metadata
// (sha256-*.sig/sbom/vex) and commit-SHA pre-releases (X.Y.Z-vSHA).
var validatorTagPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-rc\.\d+)?$`)

// ResolveLatestValidatorTag returns baseImage's repo with the latest tag
// substituted, sourced from the OCI registry hosting baseImage. Prefers
// stable releases over rc; falls back to the highest rc when no stable
// exists. Returns ("", false) on any failure (network, auth, parse, no
// matching tags), and the caller then reports an untagged image as
// unresolved.
//
// Discovery only runs when baseImage has no tag. A pinned tag
// (image:vX.Y.Z) is honored verbatim: operators who specify a tag
// must get exactly that tag, never a registry-side override.
//
// Reads and writes a 1h-TTL cache at ~/.cache/nvcf-cli/validator-tag.json
// so back-to-back preflight runs share the result without re-hitting the
// registry.
func ResolveLatestValidatorTag(ctx context.Context, baseImage string) (string, bool) {
	// parseImageRef refuses a registry that is not a bare host[:port], so no
	// URL below is built from one that moves the request elsewhere.
	registry, repo, tag, ok := parseImageRef(baseImage)
	if !ok {
		return "", false
	}
	// Operator pinned an explicit tag: respect it, no discovery. The
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

// ImageRefIsPinned reports whether ref names a tag or a digest. An unpinned
// reference is pulled as :latest, which a registry need not have.
func ImageRefIsPinned(ref string) bool {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "@") {
		return true
	}
	return strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":")
}

// parseImageRef splits an image reference into its registry, repository and
// tag or digest, the way the container runtime reads it. A first component
// with a '.' or ':', or "localhost", is the registry; anything else is a
// Docker Hub name. In "repo:tag@digest" the digest pins the image and the tag
// is dropped, so it never leaks into a token scope. ok is false for a
// registry that is not a bare host[:port] and for an empty repository: every
// caller builds a URL from the result.
func parseImageRef(image string) (registry, repo, ref string, ok bool) {
	image = strings.TrimSpace(image)
	registry, rest := dockerHubRegistry, image
	if head, tail, found := strings.Cut(image, "/"); found &&
		(strings.ContainsAny(head, ".:") || head == "localhost") {
		registry, rest = head, tail
	}
	if host, _ := parseRegistryHostPort(registry); host == "" {
		return "", "", "", false
	}
	if name, digest, found := strings.Cut(rest, "@"); found {
		rest, ref = name, digest
		if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
			rest = rest[:i]
		}
	} else if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest, ref = rest[:i], rest[i+1:]
	}
	if !isRepositoryPath(rest) {
		return "", "", "", false
	}
	if registry == dockerHubRegistry && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	return registry, rest, ref, true
}

// isRepositoryPath reports whether s can be a repository path: non-empty
// '/'-separated components with nothing that would change a URL built from it.
func isRepositoryPath(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || !isBareRegistryHost(part) || strings.Contains(part, ":") {
			return false
		}
	}
	return true
}

// fetchValidatorTags walks the OCI tag-list endpoint for a single repo.
// Handles the standard Bearer token exchange: try anonymous, on 401 re-auth
// with the run's local credential for the registry.
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
	client := newRegistryHTTPClient(validatorTagFetchTimeout)

	// First attempt without auth so anonymous-pullable registries work.
	resp, err := doRegistryRequest(ctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	})
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
	wwwAuth := selectAuthChallenge(resp.Header.Values("Www-Authenticate"))
	resp.Body.Close()

	cred, hasCred, _ := registryCredentialsFrom(ctx).lookup(ctx, registry)
	var credential *registryCredential
	if hasCred {
		credential = &cred
	}
	token, err := exchangeBearerToken(ctx, client, registry, repo, wwwAuth, credential)
	if err != nil {
		return nil, err
	}

	resp, err = doRegistryRequest(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s after bearer auth", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// newRegistryHTTPClient is the one client every registry call uses, so none
// can follow a redirect to plain http with the operator's credentials.
func newRegistryHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: refuseInsecureRedirect}
}

// errRefusedRedirect is a redirect the registry client would not follow.
type errRefusedRedirect struct{ msg string }

func (e errRefusedRedirect) Error() string { return e.msg }

// refuseInsecureRedirect rejects any redirect hop that is not https.
//
// Go strips the Authorization header only when a redirect changes host, and
// that comparison ignores the scheme. Without this, an https realm that 302s to
// http on the same host would re-send the operator's credentials in cleartext,
// defeating the https-only realm check in exchangeBearerToken.
func refuseInsecureRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return errRefusedRedirect{msg: fmt.Sprintf("refusing redirect to non-https URL %q", req.URL.Redacted())}
	}
	if len(via) >= 10 {
		return errRefusedRedirect{msg: fmt.Sprintf("stopped after %d redirects", len(via))}
	}
	return nil
}

// registryRetryAttempts and registryRetryBackoff bound the retries of a
// registry call that failed transiently: a connection error, a 5xx or a 429.
// Vars so tests need not wait out the backoff.
var (
	registryRetryAttempts = 3
	registryRetryBackoff  = 500 * time.Millisecond
)

// registryRetryAfterCap bounds how long a Retry-After header can hold a retry.
const registryRetryAfterCap = 5 * time.Second

// doRegistryRequest sends the request build returns, retrying a transient
// failure while ctx leaves time for another attempt. A refused redirect is
// not retried. The last response or error is returned as is.
func doRegistryRequest(
	ctx context.Context, client *http.Client, build func() (*http.Request, error),
) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		var refused errRefusedRedirect
		retry := err != nil && ctx.Err() == nil && !errors.As(err, &refused)
		wait := time.Duration(attempt) * registryRetryBackoff
		if err == nil && isTransientStatus(resp.StatusCode) {
			retry = true
			wait = max(wait, retryAfter(resp.Header.Get("Retry-After")))
		}
		if !retry || attempt >= registryRetryAttempts || !allowsRetryAfter(ctx, wait) {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// isTransientStatus reports a status a retry may clear.
func isTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// retryAfter reads a Retry-After header in seconds or as an HTTP date,
// capped at registryRetryAfterCap.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = time.Until(at)
	}
	return min(max(d, 0), registryRetryAfterCap)
}

// allowsRetryAfter reports whether ctx allows waiting d and then trying once more.
func allowsRetryAfter(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > d
}

// tokenExchangeError is a token exchange that produced no token.
type tokenExchangeError struct {
	// status is the token endpoint's HTTP status; 0 when none was read.
	status int
	// credentialed reports that a credential was sent.
	credentialed bool
	// refused marks an exchange the CLI declined before sending anything,
	// for a realm it will not send credentials to.
	refused bool
	msg     string
}

func (e *tokenExchangeError) Error() string { return e.msg }

// rejected reports that the registry refused a credential it was sent:
// a 401 or 403 from the token endpoint. Nothing else is evidence about the
// credential.
func (e *tokenExchangeError) rejected() bool {
	return e.credentialed && (e.status == http.StatusUnauthorized || e.status == http.StatusForbidden)
}

// exchangeBearerToken implements the OCI Distribution Spec Bearer token flow,
// parsing realm/service/scope from the WWW-Authenticate header. cred is the
// credential to send, or nil for an anonymous token. A challenge with no
// realm falls back to NGC's /proxy_auth, for nvcr.io only. Failures are a
// *tokenExchangeError.
func exchangeBearerToken(
	ctx context.Context, client *http.Client, registry, repo, wwwAuthenticate string, cred *registryCredential,
) (string, error) {
	realm, service, scope := parseWWWAuthenticate(wwwAuthenticate)

	if realm == "" {
		if isNGCKeyRegistry(registry) {
			return exchangeNGCBearerToken(ctx, client, registry, repo, cred)
		}
		return "", &tokenExchangeError{refused: true, msg: fmt.Sprintf("%s named no Bearer token realm", registry)}
	}

	u, err := url.Parse(realm)
	// Reject non-HTTPS or relative realms before attaching credentials.
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", &tokenExchangeError{refused: true,
			msg: fmt.Sprintf("refusing token exchange at insecure or relative realm %q for %s", realm, registry)}
	}
	if !realmAuthorized(registry, u) {
		return "", &tokenExchangeError{refused: true,
			msg: fmt.Sprintf("refusing to send credentials to realm host %q; not authorized for registry %s",
				u.Hostname(), registry)}
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	// Use scope from the WWW-Authenticate header when present.
	// When scope is empty and a repo is provided, synthesize the standard
	// pull scope. When neither is present (credential probe, no specific
	// repo needed), omit scope entirely: most registries issue a valid
	// token and the absence of a resource scope avoids org-level 403s for
	// non-existent repositories.
	if scope == "" && repo != "" {
		scope = "repository:" + repo + ":pull"
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	return requestToken(ctx, client, u.String(), realm, cred)
}

// realmAuthorized reports whether credentials for registry may go to the
// token realm u. The realm comes from a registry-controlled response header,
// so without this a registry could name realm="https://attacker.example/token"
// and receive the operator's credentials. Allowed: the registry's own host,
// a subdomain of it (auth.registry.example.com for registry.example.com), or
// a documented delegation in trustedRealmDelegations. NVIDIA hosts get no
// wider grant: nvcr.io issues its tokens itself.
func realmAuthorized(registry string, u *url.URL) bool {
	realmHost := strings.ToLower(u.Hostname())
	// Reject an empty realm host explicitly. "https://:443/token" has a
	// non-empty u.Host (":443"), but Hostname() is "", and an empty
	// trustedRealmDelegations lookup would then compare equal.
	if realmHost == "" {
		return false
	}
	// Brackets off, to match u.Hostname(): "[fd00::1]" with no port fails
	// SplitHostPort and would otherwise reject its own token server.
	regHost := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(registry, "["), "]"))
	if h, _, err := net.SplitHostPort(registry); err == nil {
		regHost = strings.ToLower(h)
	}
	return realmHost == regHost ||
		strings.HasSuffix(realmHost, "."+regHost) ||
		trustedRealmDelegations[regHost] == realmHost
}

// requestToken asks tokenURL for a Bearer token, sending cred as Basic auth
// when it is set. label names the endpoint in errors.
func requestToken(
	ctx context.Context, client *http.Client, tokenURL, label string, cred *registryCredential,
) (string, error) {
	resp, err := doRegistryRequest(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
		if err == nil && cred != nil {
			req.SetBasicAuth(cred.user, cred.pass)
		}
		return req, err
	})
	if err != nil {
		var refused errRefusedRedirect
		return "", &tokenExchangeError{
			credentialed: cred != nil, refused: errors.As(err, &refused),
			msg: fmt.Sprintf("token exchange at %s: %v", label, err),
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &tokenExchangeError{
			status: resp.StatusCode, credentialed: cred != nil,
			msg: fmt.Sprintf("token exchange at %s returned %s", label, resp.Status),
		}
	}
	// Both "token" (OCI spec) and "access_token" (Docker Hub variant) are valid.
	var doc struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", &tokenExchangeError{credentialed: cred != nil,
			msg: fmt.Sprintf("decode token response from %s: %v", label, err)}
	}
	if tok := cmp.Or(doc.Token, doc.AccessToken); tok != "" {
		return tok, nil
	}
	return "", &tokenExchangeError{credentialed: cred != nil, msg: fmt.Sprintf("empty token in response from %s", label)}
}

// exchangeNGCBearerToken is NGC's /proxy_auth token exchange, used when
// nvcr.io sends a challenge with no realm. It refuses every other registry,
// so the NGC key never reaches an unrelated /proxy_auth endpoint.
func exchangeNGCBearerToken(
	ctx context.Context, client *http.Client, registry, repo string, cred *registryCredential,
) (string, error) {
	if !isNGCKeyRegistry(registry) {
		return "", &tokenExchangeError{refused: true,
			msg: fmt.Sprintf("NGC token fallback not applicable for registry %s", registry)}
	}
	// url.Values omits scope entirely when repo is empty rather than sending
	// scope= with an empty value. An empty scope validates the API key
	// without org-scoped access checks.
	q := url.Values{"service": {registry}}
	if repo != "" {
		q.Set("scope", "repository:"+repo+":pull")
	}
	return requestToken(ctx, client, "https://"+registry+"/proxy_auth?"+q.Encode(), "NGC /proxy_auth", cred)
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

		switch strings.ToLower(key) {
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

// trustedRealmDelegations maps a registry host to its authorized token host
// when the registry uses a separate host for token exchange. Only add entries
// here for registries with publicly documented auth architectures; this list
// extends the fail-closed realm validation and must not grow without a clear
// trust basis.
var trustedRealmDelegations = map[string]string{
	// Docker Hub documents this split explicitly: the pull host and the auth
	// host are distinct (docs.docker.com/registry/spec/auth/token/).
	"registry-1.docker.io": "auth.docker.io",
	"docker.io":            "auth.docker.io",
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

// isBareRegistryHost reports whether s is a plain host[:port], with nothing in
// it that could move the request somewhere else once concatenated into a URL.
//
// This matters because the registry string is interpolated directly into
// "https://" + registry + "/...". A value like "evil.com/x.nvcr.io" ends with a
// trusted suffix but parses to host evil.com, so a suffix check alone would
// authorize sending credentials to an attacker. IPv6 literals keep their
// brackets and are allowed.
func isBareRegistryHost(s string) bool {
	if s == "" {
		return false
	}
	// "/" and "@" move the host; "?" and "#" truncate it; whitespace and
	// control characters have no place in a hostname.
	if strings.ContainsAny(s, "/@?#\\") {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// isNGCRegistry returns true when the registry host belongs to an NVIDIA / NGC
// domain. The check strips any port from the registry string before matching
// so that nvcr.io:443 is handled correctly, and uses dot-boundary matching to
// reject deceptive suffixes such as evilnvcr.io or nvidia.com.invalid.
func isNGCRegistry(registry string) bool {
	// Never treat a value we cannot safely place in a URL as an NGC host: the
	// caller uses this to decide whether to forward the NGC API key.
	if !isBareRegistryHost(registry) {
		return false
	}
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
