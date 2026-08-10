/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package reloadableconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testConfig struct {
	Value string `yaml:"value"`
}

func useFastReloadTiming(t *testing.T) {
	t.Helper()
	oldConfigLoadTick := ConfigLoadTick
	oldPollInterval := PollInterval
	ConfigLoadTick = time.Millisecond
	PollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		ConfigLoadTick = oldConfigLoadTick
		PollInterval = oldPollInterval
	})
}

func writeTestConfig(t *testing.T, path, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("value: "+value+"\n"), 0600))
}

func requireConfigValue(t *testing.T, cfg ReloadableConfig[testConfig], value string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return cfg.Get().Value == value
	}, 2*time.Second, 10*time.Millisecond)
}

func TestSetupConfigReloadsValidFileUpdates(t *testing.T) {
	useFastReloadTiming(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestConfig(t, path, "one")

	cfg, err := SetupConfig[testConfig](path)
	require.NoError(t, err)
	require.Equal(t, "one", cfg.Get().Value)

	time.Sleep(20 * time.Millisecond)
	writeTestConfig(t, path, "two")

	requireConfigValue(t, cfg, "two")
}

func TestSetupConfigKeepsLastGoodConfigWhenReloadIsInvalid(t *testing.T) {
	useFastReloadTiming(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestConfig(t, path, "one")

	cfg, err := SetupConfig[testConfig](path, WithValidateFunc(func(c *testConfig) error {
		if c.Value == "bad" {
			return fmt.Errorf("bad value")
		}
		return nil
	}))
	require.NoError(t, err)
	require.Equal(t, "one", cfg.Get().Value)

	time.Sleep(20 * time.Millisecond)
	writeTestConfig(t, path, "bad")
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, "one", cfg.Get().Value)

	writeTestConfig(t, path, "two")
	requireConfigValue(t, cfg, "two")
}

func TestSetupConfigKeepsLastGoodConfigDuringZeroByteWrite(t *testing.T) {
	useFastReloadTiming(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestConfig(t, path, "one")

	cfg, err := SetupConfig[testConfig](path)
	require.NoError(t, err)
	require.Equal(t, "one", cfg.Get().Value)

	time.Sleep(20 * time.Millisecond)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, "one", cfg.Get().Value)

	writeTestConfig(t, path, "two")
	requireConfigValue(t, cfg, "two")
}

func TestSetupConfigReloadsConfigMapStyleSymlinkReplacement(t *testing.T) {
	useFastReloadTiming(t)
	root := t.TempDir()
	dataOne := filepath.Join(root, "data-1")
	require.NoError(t, os.Mkdir(dataOne, 0700))
	writeTestConfig(t, filepath.Join(dataOne, "config.yaml"), "one")
	require.NoError(t, os.Symlink("data-1", filepath.Join(root, "..data")))
	require.NoError(t, os.Symlink(filepath.Join("..data", "config.yaml"), filepath.Join(root, "config.yaml")))

	cfg, err := SetupConfig[testConfig](filepath.Join(root, "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, "one", cfg.Get().Value)

	time.Sleep(20 * time.Millisecond)
	dataTwo := filepath.Join(root, "data-2")
	require.NoError(t, os.Mkdir(dataTwo, 0700))
	writeTestConfig(t, filepath.Join(dataTwo, "config.yaml"), "two")
	require.NoError(t, os.Symlink("data-2", filepath.Join(root, "..data_tmp")))
	require.NoError(t, os.Rename(filepath.Join(root, "..data_tmp"), filepath.Join(root, "..data")))

	requireConfigValue(t, cfg, "two")
}
