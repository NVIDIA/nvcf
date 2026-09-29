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

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackWithEnvs creates a stack directory holding base.yaml and one file per
// named environment.
func stackWithEnvs(t *testing.T, envs ...string) string {
	t.Helper()
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	for _, name := range append([]string{"base"}, envs...) {
		require.NoError(t, os.WriteFile(filepath.Join(envDir, name+".yaml"), []byte("global: {}\n"), 0o644))
	}
	return dir
}

// withEnvFlag sets --env as if the operator passed it, and restores it.
func withEnvFlag(t *testing.T, value string, changed bool) {
	t.Helper()
	f := selfHostedCmd.PersistentFlags().Lookup("env")
	require.NotNil(t, f)
	prevValue, prevChanged := selfHostedEnv, f.Changed
	selfHostedEnv, f.Changed = value, changed
	t.Cleanup(func() { selfHostedEnv, f.Changed = prevValue, prevChanged })
}

// helmfile layers the environment file over base.yaml, so both are returned,
// base first. Returning only the first file that exists dropped whichever
// half the other one set.
func TestResolveStackValuesFiles_LayersEnvOverBase(t *testing.T) {
	stack := stackWithEnvs(t, "local")
	prev := selfHostedControlPlaneStack
	selfHostedControlPlaneStack = stack
	t.Cleanup(func() { selfHostedControlPlaneStack = prev })
	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "local", false)

	got := resolveStackValuesFiles()
	require.Len(t, got, 2)
	assert.Equal(t, "base.yaml", filepath.Base(got[0]))
	assert.Equal(t, "local.yaml", filepath.Base(got[1]))
}

// An operator who exports HELMFILE_ENV and runs helmfile directly gets that
// environment; an explicit --env still wins over it.
func TestResolveStackEnv_Precedence(t *testing.T) {
	t.Setenv("HELMFILE_ENV", "airgap")
	withEnvFlag(t, "local", false)
	assert.Equal(t, "airgap", resolveStackEnv(), "HELMFILE_ENV beats the --env default")

	withEnvFlag(t, "staging", true)
	assert.Equal(t, "staging", resolveStackEnv(), "an explicit --env beats HELMFILE_ENV")

	t.Setenv("HELMFILE_ENV", "")
	withEnvFlag(t, "local", false)
	assert.Equal(t, "local", resolveStackEnv())
}
