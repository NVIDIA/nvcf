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

// Inline credentials are still read without running any helper.
func TestCredsFromDockerConfig_InlineAuthNeedsNoHelper(t *testing.T) {
	dockerHome(t, `{"auths":{"nvcr.io":{"username":"x","password":"y"}},"credsStore":"missing"}`)
	u, p, ok := credsFromDockerConfig("nvcr.io")
	require.True(t, ok)
	assert.Equal(t, "x", u)
	assert.Equal(t, "y", p)
}

// The helper name comes from a file; anything that is not a plain helper
// suffix is refused rather than put on an exec path.
func TestCredsFromDockerConfig_RefusesUnsafeHelperName(t *testing.T) {
	// The helper exists on PATH, so only the name check can refuse it.
	dockerHome(t, `{"credsStore":"bad name"}`, "bad name")
	_, _, ok := credsFromDockerConfig("nvcr.io")
	assert.False(t, ok)
}
