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
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A registry may send several WWW-Authenticate headers. The Bearer one is
// answered wherever it appears; reading only the first reported a registry
// answering "Negotiate" then "Bearer" as a clean skip.
func TestSelectAuthChallenge(t *testing.T) {
	bearer := `Bearer realm="https://auth.example/token",service="registry"`
	assert.Equal(t, bearer, selectAuthChallenge([]string{"Negotiate", bearer}))
	assert.Equal(t, `Basic realm="r"`, selectAuthChallenge([]string{`Basic realm="r"`}))
	assert.Equal(t, "", selectAuthChallenge(nil))
}

// dockerHome writes ~/.docker/config.json under a temporary HOME and puts a
// fake docker-credential-<name> for each helper on PATH, each answering with
// its own secret.
// inlineDockerConfig builds a config.json with one inline credential at run
// time. A literal auths blob trips secret scanners even with fake values.
func inlineDockerConfig(t *testing.T, registry, user, pass, credsStore string) string {
	t.Helper()
	cfg := map[string]any{"auths": map[string]any{
		registry: map[string]string{"username": user, "password": pass},
	}}
	if credsStore != "" {
		cfg["credsStore"] = credsStore
	}
	b, err := json.Marshal(cfg)
	require.NoError(t, err)
	return string(b)
}

func dockerHome(t *testing.T, config string, helpers ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script credential helpers")
	}
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".docker"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(config), 0o600))
	t.Setenv("HOME", home)
	bin := t.TempDir()
	for _, h := range helpers {
		script := "#!/bin/sh\n[ \"$1\" = get ] || exit 1\nread server\n" +
			"printf '{\"ServerURL\":\"%s\",\"Username\":\"u-" + h + "\",\"Secret\":\"s-" + h + "\"}' \"$server\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, "docker-credential-"+h), []byte(script), 0o755))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := credentialHelperTimeout
	credentialHelperTimeout = time.Minute
	t.Cleanup(func() { credentialHelperTimeout = prev })
}

// Docker Desktop leaves only an empty auths entry and keeps the login in
// credsStore, so a working `docker login` must be read through the helper.
func TestCredsFromDockerConfig_UsesCredsStore(t *testing.T) {
	dockerHome(t, `{"auths":{"nvcr.io":{}},"credsStore":"store"}`, "store")
	u, p, ok := credsFromDockerConfig("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "u-store", u)
	assert.Equal(t, "s-store", p)
}

// A per-registry credHelpers entry wins over the global store.
func TestCredsFromDockerConfig_CredHelpersBeatCredsStore(t *testing.T) {
	dockerHome(t, `{"credsStore":"store","credHelpers":{"nvcr.io":"ngc"}}`, "store", "ngc")
	u, _, ok := credsFromDockerConfig("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "u-ngc", u)
}

// Inline credentials are read when no store is configured.
func TestCredsFromDockerConfig_InlineAuthWithoutAStore(t *testing.T) {
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "x", "y", ""))
	u, p, ok := credsFromDockerConfig("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "x", u)
	assert.Equal(t, "y", p)
}

// With a store configured docker never reads an inline password, so a stale
// one left in the file must not win over the store.
func TestCredsFromDockerConfig_StoreBeatsStaleInlineAuth(t *testing.T) {
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "old", "stale", "store"), "store")
	u, p, ok := credsFromDockerConfig("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "u-store", u)
	assert.Equal(t, "s-store", p)
}

// Docker Hub logins live under the legacy index key, whatever host name the
// image uses.
func TestCredsFromDockerConfig_DockerHubKey(t *testing.T) {
	dockerHome(t, inlineDockerConfig(t, "https://index.docker.io/v1/", "hub", "pw", ""))
	for _, host := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
		u, _, ok := credsFromDockerConfig(host)
		require.True(t, ok, host)
		assert.Equal(t, "hub", u)
	}
}

// The timeout holds even when the helper leaves a child holding stdout open.
func TestCredsFromHelper_TimeoutIsEnforced(t *testing.T) {
	dockerHome(t, `{"credsStore":"hang"}`)
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 30 &\nsleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker-credential-hang"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	credentialHelperTimeout = 500 * time.Millisecond

	start := time.Now()
	_, _, ok := credsFromDockerConfig("nvcr.io")
	assert.False(t, ok)
	assert.Less(t, time.Since(start), 10*time.Second, "the helper call must not outlive its timeout by the child's lifetime")
}

// A helper that floods stdout is refused rather than buffered without bound.
func TestCredsFromHelper_OutputIsCapped(t *testing.T) {
	dockerHome(t, `{"credsStore":"flood"}`)
	bin := t.TempDir()
	// Valid JSON padded past the cap, so only the cap can refuse it.
	script := "#!/bin/sh\nprintf '{\"Username\":\"u\",\"Secret\":\"s\"}'\nhead -c 200000 /dev/zero | tr '\\0' ' '\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker-credential-flood"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, ok := credsFromDockerConfig("nvcr.io")
	assert.False(t, ok)
}

// The helper name comes from a file; anything that is not a plain helper
// suffix is refused rather than put on an exec path.
func TestCredsFromDockerConfig_RefusesUnsafeHelperName(t *testing.T) {
	// The helper exists on PATH, so only the name check can refuse it.
	dockerHome(t, `{"credsStore":"bad name"}`, "bad name")
	_, _, ok := credsFromDockerConfig("nvcr.io")
	assert.False(t, ok)
}

// Several challenges can share one header, comma-separated.
func TestSelectAuthChallenge_JoinedInOneHeader(t *testing.T) {
	got := selectAuthChallenge([]string{`Negotiate, Bearer realm="https://auth.example/token",service="r"`})
	assert.Equal(t, `Bearer realm="https://auth.example/token",service="r"`, got)
	assert.Equal(t, `Basic realm="bearer thing"`, selectAuthChallenge([]string{`Basic realm="bearer thing"`}),
		"a quoted word is not a challenge")
}

// For an NGC registry the check uses NGC_API_KEY when it is set, the key the
// install mints its pull secrets from, even when a credential store also has a
// login for it. Without the key, the store is used. Other registries never
// get the key.
func TestCredentialsForRegistry_NGCKeyMatchesWhatTheInstallUses(t *testing.T) {
	dockerHome(t, `{"auths":{"nvcr.io":{}},"credsStore":"store"}`, "store")
	t.Setenv("NGC_API_KEY", "env-key")
	u, p, ok := credentialsForRegistry("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "$oauthtoken", u)
	assert.Equal(t, "env-key", p)

	t.Setenv("NGC_API_KEY", "")
	u, _, ok = credentialsForRegistry("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "u-store", u, "without the key the store's login is used")

	t.Setenv("NGC_API_KEY", "env-key")
	_, p, _ = credentialsForRegistry("quay.io")
	assert.NotEqual(t, "env-key", p, "the NGC key is never sent to another registry")
}
