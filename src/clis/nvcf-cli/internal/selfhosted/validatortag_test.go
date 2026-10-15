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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		reg     string
		repo    string
		tag     string
		wantOK  bool
	}{
		{"full tag", "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26", "stg.nvcr.io", "nvidia/nvcf-byoc/cluster-validator", "3.0.0-rc.26", true},
		{"digest", "nvcr.io/foo/bar@sha256:abc", "nvcr.io", "foo/bar", "sha256:abc", true},
		{"no tag", "stg.nvcr.io/foo/bar", "stg.nvcr.io", "foo/bar", "", true},
		{"localhost with port", "localhost:5000/foo:latest", "localhost:5000", "foo", "latest", true},
		{"docker hub shorthand", "foo/bar:latest", "", "", "", false},
		{"empty", "", "", "", "", false},
		{"only host", "stg.nvcr.io", "", "", "", false},
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
		"3.0.0",        // stable; should win over any rc
		"3.1.0-rc.1",   // higher major but pre-release: must lose to 3.0.0
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
	const baseImage = "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.5"
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
	const baseImage = "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator"
	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	got, ok := ResolveLatestValidatorTag(context.Background(), baseImage)
	require.True(t, ok)
	assert.Equal(t, "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.99", got)
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

	const baseImage = "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"

	_, ok := readValidatorTagCache(baseImage)
	assert.False(t, ok, "empty cache must miss")

	require.NoError(t, writeValidatorTagCache(baseImage, "3.0.0-rc.99"))

	got, ok := readValidatorTagCache(baseImage)
	require.True(t, ok)
	assert.Equal(t, "3.0.0-rc.99", got)
}

func TestValidatorTagCache_TTLExpiry(t *testing.T) {
	withTempCacheDir(t)

	const baseImage = "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"
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

	const imgA = "stg.nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26"
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
			"stg.nvcr.io": map[string]any{
				// base64("$oauthtoken:fake-key")
				"auth": "JG9hdXRodG9rZW46ZmFrZS1rZXk=",
			},
		},
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dockerDir, "config.json"), body, 0o600))

	user, pass, ok := credsFromDockerConfig("stg.nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "$oauthtoken", user)
	assert.Equal(t, "fake-key", pass)

	_, _, ok = credsFromDockerConfig("does-not-exist.example.com")
	assert.False(t, ok, "unknown registry must miss")
}

func TestNGCCredentials_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // no docker config in this HOME
	for _, n := range ngcAPIKeyEnvNames {
		t.Setenv(n, "")
	}
	t.Setenv("NGC_API_KEY", "from-env")

	user, pass, ok := ngcCredentials("stg.nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "$oauthtoken", user)
	assert.Equal(t, "from-env", pass)
}
