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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		reg    string
		repo   string
		tag    string
		wantOK bool
	}{
		{"full tag", "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26", "example.nvcr.io", "nvidia/nvcf-byoc/cluster-validator", "3.0.0-rc.26", true},
		{"digest", "nvcr.io/foo/bar@sha256:abc", "nvcr.io", "foo/bar", "sha256:abc", true},
		{"no tag", "example.nvcr.io/foo/bar", "example.nvcr.io", "foo/bar", "", true},
		{"localhost with port", "localhost:5000/foo:latest", "localhost:5000", "foo", "latest", true},
		{"docker hub shorthand", "foo/bar:latest", "registry-1.docker.io", "foo/bar", "latest", true},
		{"docker hub library image", "busybox:1.36", "registry-1.docker.io", "library/busybox", "1.36", true},
		{"tag and digest", "nvcr.io/nvidia/cv:1.2.0@sha256:abc", "nvcr.io", "nvidia/cv", "sha256:abc", true},
		{"port and tag and digest", "reg.example.com:5000/a/b:1@sha256:abc", "reg.example.com:5000", "a/b",
			"sha256:abc", true},
		{"empty", "", "", "", "", false},
		{"dotted single name is a Docker Hub name", "example.nvcr.io", "registry-1.docker.io", "library/example.nvcr.io", "", true},
		{"only a registry", "example.nvcr.io/", "", "", "", false},
		{"userinfo moves the host", "nvcr.io@attacker.example/nvidia/cv", "", "", "", false},
		{"fragment moves the host", "attacker.example#.nvcr.io/nvidia/cv", "", "", "", false},
		{"bad port", "nvcr.io:44x/nvidia/cv", "", "", "", false},
		{"query in the repository", "nvcr.io/nvidia/cv?x=1", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, repo, tag, ok := parseImageRef(c.in)
			assert.Equal(t, c.wantOK, ok)
			if !c.wantOK {
				return
			}
			assert.Equal(t, c.reg, reg)
			assert.Equal(t, c.repo, repo)
			assert.Equal(t, c.tag, tag)
		})
	}
}

func TestPickBestValidatorTag_StablePreferred(t *testing.T) {
	tags := []string{
		"3.0.0-rc.11",
		"3.0.0-rc.26",
		"3.0.0",      // stable; should win over any rc
		"3.1.0-rc.1", // higher major but pre-release: must lose to 3.0.0
		"sha256-abc.sig",
		"3.0.0-v50ca53a0", // commit-SHA: filtered out by pattern
	}
	assert.Equal(t, "3.0.0", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_OnlyRcs(t *testing.T) {
	tags := []string{"3.0.0-rc.11", "3.0.0-rc.26", "3.0.0-rc.2"}
	assert.Equal(t, "3.0.0-rc.26", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_StableWinsAcrossMajors(t *testing.T) {
	// Once stable releases exist, prefer them even if a higher major rc is present.
	tags := []string{"3.0.0", "2.9.0", "3.1.0-rc.1"}
	assert.Equal(t, "3.0.0", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_HigherStableWins(t *testing.T) {
	tags := []string{"3.0.0", "3.1.0", "3.0.5"}
	assert.Equal(t, "3.1.0", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_FiltersSigstore(t *testing.T) {
	// All the sigstore artifacts; no real image tags.
	tags := []string{
		"sha256-a.sig",
		"sha256-b.sbom",
		"sha256-c.vex",
	}
	assert.Equal(t, "", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_FiltersCommitSHA(t *testing.T) {
	// 3.0.0-v<sha> commit-build tags must not be picked over rc.
	tags := []string{"3.0.0-rc.26", "3.0.0-v50ca53a0", "3.0.0-v78ddaee9"}
	assert.Equal(t, "3.0.0-rc.26", pickBestValidatorTag(tags))
}

func TestPickBestValidatorTag_EmptyInput(t *testing.T) {
	assert.Equal(t, "", pickBestValidatorTag(nil))
	assert.Equal(t, "", pickBestValidatorTag([]string{}))
}

// Pinned tags MUST NOT be overridden by registry discovery: the help
// text, config template, and resolver doc all promise this. The
// caller's fallback path returns baseImage unchanged on (\"\", false),
// so an early-return there is the right way to express "respect the pin".
func TestResolveLatestValidatorTag_HonorsPinnedTag(t *testing.T) {
	withTempCacheDir(t)
	// Pre-populate cache to prove discovery is short-circuited regardless.
	// If the function ever consulted the cache for a tagged input, this
	// test would fail by returning the cached-substituted value.
	const baseImage = "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.5"
	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	got, ok := ResolveLatestValidatorTag(context.Background(), baseImage)
	assert.False(t, ok,
		"pinned tag must short-circuit discovery; caller falls back to baseImage unchanged")
	assert.Equal(t, "", got)
}

func TestResolveLatestValidatorTag_TaglessTriggersDiscoveryFromCache(t *testing.T) {
	// Mirror of the test above for the tagless case: discovery (here
	// from cache) should run and substitute the resolved tag.
	withTempCacheDir(t)
	const baseImage = "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator"
	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	got, ok := ResolveLatestValidatorTag(context.Background(), baseImage)
	require.True(t, ok)
	assert.Equal(t, "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.99", got)
}

// withTempCacheDir redirects HOME so the cache lives in a temp dir for the
// duration of the test, leaving the real ~/.cache untouched.
func withTempCacheDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir) // fallback for os.UserCacheDir on systems without XDG
}

func TestValidatorTagCache_RoundTrip(t *testing.T) {
	withTempCacheDir(t)

	const baseImage = "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"

	_, ok := readValidatorTagCache(baseImage)
	assert.False(t, ok, "empty cache must miss")

	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	got, ok := readValidatorTagCache(baseImage)
	require.True(t, ok)
	assert.Equal(t, "3.0.0-rc.99", got)
}

func TestValidatorTagCache_TTLExpiry(t *testing.T) {
	withTempCacheDir(t)

	const baseImage = "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"
	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	// Rewrite cache file with a stale fetched_at timestamp.
	path, err := validatorTagCachePath()
	require.NoError(t, err)
	cache := validatorTagCache{
		baseImage: validatorTagCacheEntry{
			Image:     baseImage,
			Tag:       "3.0.0-rc.99",
			FetchedAt: time.Now().UTC().Add(-2 * validatorTagCacheTTL),
		},
	}
	body, _ := json.MarshalIndent(cache, "", "  ")
	require.NoError(t, os.WriteFile(path, body, 0o644))

	_, ok := readValidatorTagCache(baseImage)
	assert.False(t, ok, "expired entry must be ignored")
}

func TestValidatorTagCache_PerImageKeying(t *testing.T) {
	withTempCacheDir(t)

	const imgA = "example.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"
	const imgB = "nvcr.io/nvidia/other/cluster-validator:1.0.0"

	require.NoError(t, writeValidatorTagCache(imgA, "3.0.0-rc.99"))
	require.NoError(t, writeValidatorTagCache(imgB, "1.2.3"))

	gotA, okA := readValidatorTagCache(imgA)
	require.True(t, okA)
	assert.Equal(t, "3.0.0-rc.99", gotA)

	gotB, okB := readValidatorTagCache(imgB)
	require.True(t, okB)
	assert.Equal(t, "1.2.3", gotB, "different images must not share cache slots")
}

func TestValidatorTagCachePath_UnderUserCacheDir(t *testing.T) {
	withTempCacheDir(t)

	path, err := validatorTagCachePath()
	require.NoError(t, err)
	assert.Contains(t, path, "nvcf-cli")
	assert.True(t, filepath.Base(path) == "validator-tag.json")
}

func TestCredsFromDockerConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	dockerDir := filepath.Join(dir, ".docker")
	require.NoError(t, os.MkdirAll(dockerDir, 0o755))

	cfg := map[string]any{
		"auths": map[string]any{
			"example.nvcr.io": map[string]any{
				// base64("$oauthtoken:fake-key")
				"auth": "JG9hdXRodG9rZW46ZmFrZS1rZXk=",
			},
		},
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dockerDir, "config.json"), body, 0o600))

	user, pass, ok := credsFor(t, "example.nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "$oauthtoken", user)
	assert.Equal(t, "fake-key", pass)

	_, _, ok = credsFor(t, "does-not-exist.example.com")
	assert.False(t, ok, "unknown registry must miss")
}

func TestNGCCredentials_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // no docker config in this HOME
	t.Setenv("NGC_API_KEY", "from-env")

	for _, prefer := range []bool{true, false} {
		cred, ok, err := NewRegistryCredentials(prefer).lookup(context.Background(), "nvcr.io")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "$oauthtoken", cred.user)
		assert.Equal(t, "from-env", cred.pass)
	}
}

// NGC_API_KEY is for nvcr.io, the one registry up mints its pull secrets for.
// Other NVIDIA registries, staging NGC and internal ones, take their own
// docker login, and the key is never sent to them.
func TestCredentialsForRegistry_NGCKeyOnlyForNvcrIO(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("NGC_API_KEY", "ngc-key")
	login := base64.StdEncoding.EncodeToString([]byte("robot:own-login"))
	auths := map[string]any{}
	for _, host := range []string{"nvcr.io", "example.nvcr.io", "registry.example.nvidia.com:5000"} {
		auths[host] = map[string]string{"auth": login}
	}
	cfg, err := json.Marshal(map[string]any{"auths": auths})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".docker"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".docker", "config.json"), cfg, 0o600))

	for host, wantPass := range map[string]string{
		"nvcr.io":                          "ngc-key",
		"NVCR.IO:443":                      "ngc-key",
		"example.nvcr.io":                  "own-login",
		"registry.example.nvidia.com:5000": "own-login",
	} {
		cred, ok, _ := NewRegistryCredentials(true).lookup(context.Background(), host)
		require.True(t, ok, host)
		assert.Equal(t, wantPass, cred.pass, host)
	}
}

// -- parseWWWAuthenticate --

func TestParseWWWAuthenticate_Standard(t *testing.T) {
	header := `Bearer realm="https://auth.docker.io/token",service="registry-1.docker.io",scope="repository:library/ubuntu:pull"`
	realm, service, scope := parseWWWAuthenticate(header)
	assert.Equal(t, "https://auth.docker.io/token", realm)
	assert.Equal(t, "registry-1.docker.io", service)
	assert.Equal(t, "repository:library/ubuntu:pull", scope)
}

func TestParseWWWAuthenticate_GHCR(t *testing.T) {
	header := `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:owner/image:pull"`
	realm, service, scope := parseWWWAuthenticate(header)
	assert.Equal(t, "https://ghcr.io/token", realm)
	assert.Equal(t, "ghcr.io", service)
	assert.Equal(t, "repository:owner/image:pull", scope)
}

func TestParseWWWAuthenticate_NoBearer(t *testing.T) {
	// Basic auth challenge - should return empty strings.
	realm, service, scope := parseWWWAuthenticate(`Basic realm="My Registry"`)
	assert.Empty(t, realm)
	assert.Empty(t, service)
	assert.Empty(t, scope)
}

func TestParseWWWAuthenticate_Empty(t *testing.T) {
	realm, service, scope := parseWWWAuthenticate("")
	assert.Empty(t, realm)
	assert.Empty(t, service)
	assert.Empty(t, scope)
}

func TestParseWWWAuthenticate_RealmOnly(t *testing.T) {
	// Some registries omit service/scope in the initial challenge.
	realm, service, scope := parseWWWAuthenticate(`Bearer realm="https://example.com/auth"`)
	assert.Equal(t, "https://example.com/auth", realm)
	assert.Empty(t, service)
	assert.Empty(t, scope)
}

func TestParseWWWAuthenticate_MixedCaseParams(t *testing.T) {
	// Auth parameter names are parsed with mixed case in the wild.
	realm, service, scope := parseWWWAuthenticate(`Bearer Realm="https://auth.example.com/token",Service="reg.example.com",Scope="repository:foo:pull"`)
	assert.Equal(t, "https://auth.example.com/token", realm)
	assert.Equal(t, "reg.example.com", service)
	assert.Equal(t, "repository:foo:pull", scope)
}

func TestParseWWWAuthenticate_CaseInsensitiveBearer(t *testing.T) {
	// HTTP auth scheme names are case-insensitive (RFC 7235 s2.1).
	for _, header := range []string{
		`bearer realm="https://auth.example.com/token",service="reg.example.com"`,
		`BEARER realm="https://auth.example.com/token",service="reg.example.com"`,
		`Bearer realm="https://auth.example.com/token",service="reg.example.com"`,
	} {
		realm, service, _ := parseWWWAuthenticate(header)
		assert.Equal(t, "https://auth.example.com/token", realm, "header: %s", header)
		assert.Equal(t, "reg.example.com", service, "header: %s", header)
	}
}

// -- isNGCRegistry --

func TestIsNGCRegistry(t *testing.T) {
	// Valid NGC registries.
	assert.True(t, isNGCRegistry("nvcr.io"))
	assert.True(t, isNGCRegistry("example.nvcr.io"))
	assert.True(t, isNGCRegistry("registry.example.nvidia.com"))
	assert.True(t, isNGCRegistry("nvcr.io:443"), "port must be stripped before matching")

	// Non-NGC registries must be rejected.
	assert.False(t, isNGCRegistry("ghcr.io"))
	assert.False(t, isNGCRegistry("quay.io"))
	assert.False(t, isNGCRegistry("harbor.company.internal"))

	// Deceptive suffixes must be rejected.
	assert.False(t, isNGCRegistry("evilnvcr.io"), "suffix match without dot boundary must be rejected")
	assert.False(t, isNGCRegistry("nvidia.com.invalid"), "deceptive TLD must be rejected")
	assert.False(t, isNGCRegistry("fakenvidia.com"), "partial host match must be rejected")
}

// -- exchangeBearerToken realm host authorization --

// recordingTransport records every request it sees and reports whether any
// carried an Authorization header, so a test can assert that no credential
// escaped rather than only that an error was returned.
type recordingTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	withAuth []string
	inner    http.RoundTripper
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.requests = append(rt.requests, r)
	if r.Header.Get("Authorization") != "" {
		rt.withAuth = append(rt.withAuth, r.URL.String())
	}
	rt.mu.Unlock()
	if rt.inner == nil {
		return nil, fmt.Errorf("no inner transport")
	}
	return rt.inner.RoundTrip(r)
}

func TestExchangeBearerToken_RejectsAttackerRealm(t *testing.T) {
	// A malicious registry returns a realm on an attacker-controlled host. The
	// realm check must reject it, and crucially must do so before any request
	// carrying the operator's credentials leaves the process.
	//
	// Credentials have to be configured for the assertion to mean anything: if
	// none were present, no request could carry an Authorization header whether
	// the control works or not. NGC_API_KEY is not one: it only applies to NGC
	// registries, so it left this assertion vacuous.
	dockerHome(t, inlineDockerConfig(t, "harbor.company.internal", "u", "p", ""))

	rec := &recordingTransport{inner: http.DefaultTransport}
	client := &http.Client{Transport: rec}

	const wwwAuth = `Bearer realm="https://attacker.example.com/token",service="harbor.company.internal"`
	_, err := exchangeBearerToken(context.Background(), client,
		"harbor.company.internal", "myrepo/image", wwwAuth, &registryCredential{user: "u", pass: "p"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not authorized for registry")
	assert.Empty(t, rec.withAuth,
		"no request carrying an Authorization header may be issued once the realm is rejected")
	for _, r := range rec.requests {
		assert.NotContains(t, r.URL.Host, "attacker",
			"no request at all may reach the attacker host")
	}
}

// An empty realm host must fail closed. "https://:443/token" has a non-empty
// u.Host (":443") so it clears the relative-realm guard, but Hostname() is "",
// and an empty trustedRealmDelegations lookup would compare equal to it.
func TestExchangeBearerToken_RejectsEmptyRealmHost(t *testing.T) {
	dockerHome(t, inlineDockerConfig(t, "harbor.company.internal", "u", "p", ""))
	rec := &recordingTransport{inner: http.DefaultTransport}
	client := &http.Client{Transport: rec}

	_, err := exchangeBearerToken(context.Background(), client,
		"harbor.company.internal", "myrepo/image", `Bearer realm="https://:443/token"`,
		&registryCredential{user: "u", pass: "p"})

	require.Error(t, err)
	assert.Empty(t, rec.withAuth, "an empty realm host must not receive credentials")
}

// -- exchangeNGCBearerToken --

// spyTransport is an http.RoundTripper that fails the test if called.
type spyTransport struct{ t *testing.T }

func (s *spyTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	s.t.Fatal("HTTP request must not be issued for non-NGC registry")
	return nil, nil
}

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExchangeBearerToken_DockerHubDelegatedRealm(t *testing.T) {
	// Docker Hub uses registry-1.docker.io as the pull host and auth.docker.io
	// for token exchange. The realm check must authorize that delegation, so
	// drive exchangeBearerToken and assert the request actually reached the
	// auth host rather than comparing the delegation map against itself.
	t.Setenv("NGC_API_KEY", "test-key")

	rec := &recordingTransport{inner: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth.docker.io" {
			return nil, fmt.Errorf("unexpected host %s", r.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"token":"dockerhub-token"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	client := &http.Client{Transport: rec}

	const wwwAuth = `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",` +
		`scope="repository:library/ubuntu:pull"`
	// The credential comes from the run's lookup, which must not hand the NGC
	// key to Docker Hub.
	cred, ok, _ := NewRegistryCredentials(true).lookup(context.Background(), "registry-1.docker.io")
	var sent *registryCredential
	if ok {
		sent = &cred
	}
	tok, err := exchangeBearerToken(context.Background(), client,
		"registry-1.docker.io", "library/ubuntu", wwwAuth, sent)

	require.NoError(t, err, "the documented Docker Hub delegation must be authorized")
	assert.Equal(t, "dockerhub-token", tok)
	require.NotEmpty(t, rec.requests, "the token exchange must actually be issued")
	assert.Equal(t, "auth.docker.io", rec.requests[0].URL.Host)
	assert.Empty(t, rec.withAuth, "the NGC key must not reach Docker Hub's token service")
}

func TestExchangeNGCBearerToken_RejectsNonNGCRegistry(t *testing.T) {
	// A non-NGC registry must be rejected before any HTTP request is made,
	// even when NGC credentials are configured. The spy transport fails the
	// test immediately if RoundTrip is called, ensuring the isNGCRegistry
	// guard fires before any network activity.
	t.Setenv("NGC_API_KEY", "test-key") // configure a credential so a missing guard would reach the transport
	client := &http.Client{Transport: &spyTransport{t: t}}
	for _, reg := range []string{"harbor.company.internal", "example.nvcr.io", "nvcr.io:443@attacker.example"} {
		_, err := exchangeNGCBearerToken(context.Background(), client, reg, "myrepo/image",
			&registryCredential{user: "$oauthtoken", pass: "test-key", ngcKey: true})
		require.Error(t, err, "%s must be rejected without issuing a request", reg)
		assert.Contains(t, err.Error(), "not applicable")
	}
}

// The registry string is concatenated into "https://" + registry + "/...", so a
// suffix match alone is not enough to authorize forwarding the NGC API key:
// "evil.com/x.nvcr.io" ends with a trusted suffix but parses to host evil.com.
func TestIsNGCRegistry_RejectsHostConfusion(t *testing.T) {
	for _, tc := range []struct {
		registry string
		want     bool
		why      string
	}{
		{"nvcr.io", true, "the canonical host"},
		{"example.nvcr.io", true, "a real subdomain"},
		{"evil.com/x.nvcr.io", false, "path component: the request host is evil.com"},
		{"evil.com@nvcr.io", false, "userinfo: not a bare host"},
		{"nvcr.io?x=.nvcr.io", false, "query truncates the host"},
		{"nvcr.io#.nvcr.io", false, "fragment truncates the host"},
		{"nvcr.io.evil.com", false, "deceptive suffix"},
		{"nvcr.io evil.com", false, "whitespace"},
	} {
		assert.Equal(t, tc.want, isNGCRegistry(tc.registry),
			"isNGCRegistry(%q): %s", tc.registry, tc.why)
	}
}

// A non-bare registry must never reach the NGC token exchange, which attaches
// the API key with basic auth.
func TestExchangeNGCBearerToken_RejectsHostConfusion(t *testing.T) {
	t.Setenv("NGC_API_KEY", "test-key")
	rec := &recordingTransport{inner: http.DefaultTransport}
	client := &http.Client{Transport: rec}

	_, err := exchangeNGCBearerToken(context.Background(), client, "evil.com/x.nvcr.io", "repo/img",
		&registryCredential{user: "$oauthtoken", pass: "test-key", ngcKey: true})

	require.Error(t, err)
	assert.Empty(t, rec.withAuth, "the NGC API key must not be sent to a confused host")
	assert.Empty(t, rec.requests, "no request may be issued at all")
}

// A bracketed IPv6 registry on the default port must accept its own token
// server: u.Hostname() drops the brackets, so the registry side must too.
func TestExchangeBearerToken_AcceptsBracketedIPv6Registry(t *testing.T) {
	dockerHome(t, inlineDockerConfig(t, "[fd00::1]", "u", "p", ""))
	rec := &recordingTransport{inner: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"token":"t"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	tok, err := exchangeBearerToken(context.Background(), &http.Client{Transport: rec},
		"[fd00::1]", "repo", `Bearer realm="https://[fd00::1]/token",service="r"`,
		&registryCredential{user: "u", pass: "p"})
	require.NoError(t, err)
	assert.Equal(t, "t", tok)
}

// A redirect may not downgrade to cleartext: Go keeps the Authorization header
// across a same-host redirect whatever the scheme.
func TestRefuseInsecureRedirect(t *testing.T) {
	plain, err := http.NewRequest(http.MethodGet, "http://reg.example.com/token", nil)
	require.NoError(t, err)
	assert.Error(t, refuseInsecureRedirect(plain, nil))
	secure, err := http.NewRequest(http.MethodGet, "https://reg.example.com/token", nil)
	require.NoError(t, err)
	assert.NoError(t, refuseInsecureRedirect(secure, nil))
}

// nvcr.io issues its own tokens, so an NVIDIA-family realm that is neither the
// registry's host nor a subdomain of it is refused like any other: the NGC key
// and an internal login go nowhere but their own registry.
func TestExchangeBearerToken_NoNVIDIAWideRealmGrant(t *testing.T) {
	rec := &recordingTransport{inner: http.DefaultTransport}
	client := &http.Client{Transport: rec}
	for _, tc := range []struct{ registry, realm string }{
		{"nvcr.io", "https://authn.nvidia.com/token"},
		{"example.nvcr.io", "https://nvcr.io/proxy_auth"},
		{"nvcr.io", "https://x.ngc.nvidia/token"},
		{"registry.example.nvidia.com:5000", "https://nvcr.io/proxy_auth"},
	} {
		_, err := exchangeBearerToken(context.Background(), client, tc.registry, "repo",
			`Bearer realm="`+tc.realm+`"`, &registryCredential{user: "$oauthtoken", pass: "k"})
		var te *tokenExchangeError
		require.ErrorAs(t, err, &te, tc.realm)
		assert.True(t, te.refused, tc.realm)
	}
	assert.Empty(t, rec.requests, "no request may leave for a refused realm")
}

// Every registry client refuses a redirect to plain http, and an https realm
// that redirects there gets no request on the second hop, with or without
// the Authorization header.
func TestRegistryClient_RefusesCleartextRedirect(t *testing.T) {
	require.NotNil(t, newRegistryHTTPClient(time.Second).CheckRedirect)

	var plainHits []string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits = append(plainHits, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()
	realm := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/token", http.StatusFound)
	}))
	defer realm.Close()
	client := newRegistryHTTPClient(5 * time.Second)
	client.Transport = realm.Client().Transport

	host := strings.TrimPrefix(realm.URL, "https://")
	_, err := exchangeBearerToken(context.Background(), client, host, "repo",
		`Bearer realm="`+realm.URL+`/token"`, &registryCredential{user: "u", pass: "p"})
	var te *tokenExchangeError
	require.ErrorAs(t, err, &te)
	assert.True(t, te.refused, "a refused redirect is a refused realm, not a rejected credential")
	assert.Empty(t, plainHits, "the cleartext hop must not be requested")
}

// A realm on plain http is refused before anything is sent.
func TestExchangeBearerToken_RefusesHTTPRealm(t *testing.T) {
	rec := &recordingTransport{inner: http.DefaultTransport}
	_, err := exchangeBearerToken(context.Background(), &http.Client{Transport: rec}, "harbor.example.com", "repo",
		`Bearer realm="http://harbor.example.com/token"`, &registryCredential{user: "u", pass: "p"})
	var te *tokenExchangeError
	require.ErrorAs(t, err, &te)
	assert.True(t, te.refused)
	assert.Empty(t, rec.requests)
}

// isNGCKeyRegistry accepts nvcr.io alone, on any port, and nothing that
// moves the host.
func TestIsNGCKeyRegistry(t *testing.T) {
	for reg, want := range map[string]bool{
		"nvcr.io": true, "NVCR.io:443": true, "nvcr.io:5000": true,
		"example.nvcr.io": false, "nvcr.io:443@attacker.example": false, "attacker.example/nvcr.io": false,
		"nvcr.io#.attacker.example": false, "nvcr.io.attacker.example": false,
	} {
		assert.Equal(t, want, isNGCKeyRegistry(reg), reg)
	}
}

// Tag discovery never sends a request for an image whose registry is not a
// bare host, and so never writes the tag cache from such a reply.
func TestResolveLatestValidatorTag_NoRequestForAHostMovingRef(t *testing.T) {
	withTempCacheDir(t)
	rec := &recordingTransport{}
	prev := http.DefaultTransport
	http.DefaultTransport = rec
	t.Cleanup(func() { http.DefaultTransport = prev })
	for _, ref := range []string{"nvcr.io@attacker.example/nvidia/cv", "attacker.example#.nvcr.io/nvidia/cv"} {
		_, ok := ResolveLatestValidatorTag(context.Background(), ref)
		assert.False(t, ok, ref)
	}
	assert.Empty(t, rec.requests)
}

// Tag discovery moves to the NGC key when nvcr.io rejects the docker login,
// as the credential row does.
func TestResolveLatestValidatorTag_RejectedLoginGivesWayToTheNGCKey(t *testing.T) {
	withTempCacheDir(t)
	ngc := newFakeNGC(t, "good-key")
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "rotated-out", ""))
	t.Setenv("NGC_API_KEY", "good-key")
	ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))
	got, ok := ResolveLatestValidatorTag(ctx, "nvcr.io/nvidia/cv")
	require.True(t, ok)
	assert.Equal(t, "nvcr.io/nvidia/cv:1.2.0", got)
	assert.Equal(t, []string{"rotated-out", "good-key"}, ngc.passwords())
}

// A credential helper that outlasts the round trips' budget does not spend
// it: once the helper's own bound ends, the NGC key it gives way to still has
// the whole budget to reach the registry.
func TestResolveLatestValidatorTag_SlowHelperDoesNotSpendTheBudget(t *testing.T) {
	withTempCacheDir(t)
	ngc := newFakeNGC(t, "good-key")
	dockerHome(t, `{"credsStore":"hang"}`)
	bin := t.TempDir()
	hang := filepath.Join(bin, "docker-credential-hang")
	require.NoError(t, os.WriteFile(hang, []byte("#!/bin/sh\nsleep 30\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NGC_API_KEY", "good-key")
	credentialHelperTimeout = time.Second
	prev := validatorTagFetchTimeout
	validatorTagFetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { validatorTagFetchTimeout = prev })

	ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))
	got, ok := ResolveLatestValidatorTag(ctx, "nvcr.io/nvidia/cv")
	require.True(t, ok)
	assert.Equal(t, "nvcr.io/nvidia/cv:1.2.0", got)
	assert.Equal(t, []string{"good-key"}, ngc.passwords())
}
