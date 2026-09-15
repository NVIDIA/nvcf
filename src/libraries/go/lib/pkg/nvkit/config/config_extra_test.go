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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitConfig_DefaultCfgPath(t *testing.T) {
	cmd := &cobra.Command{}
	v, err := InitConfig(cmd, "", "", "TESTNVCF")
	assert.NoError(t, err)
	assert.NotNil(t, v)
}

func TestInitConfig_WithCustomCfgPath(t *testing.T) {
	cmd := &cobra.Command{}
	v, err := InitConfig(cmd, "", "my-custom-path", "TESTNVCF")
	assert.NoError(t, err)
	assert.NotNil(t, v)
}

func TestInitConfig_WithValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte("mykey: myvalue\n"), 0600)
	require.NoError(t, err)

	cmd := &cobra.Command{}
	v, err := InitConfig(cmd, cfgFile, "", "TESTNVCF")
	assert.NoError(t, err)
	assert.NotNil(t, v)
	assert.Equal(t, "myvalue", v.GetString("mykey"))
}

func TestInitConfig_WithInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "bad.yaml")
	err := os.WriteFile(cfgFile, []byte("{invalid yaml [content\n"), 0600)
	require.NoError(t, err)

	cmd := &cobra.Command{}
	v, err := InitConfig(cmd, cfgFile, "", "TESTNVCF")
	assert.Error(t, err)
	assert.Nil(t, v)
}

func TestInitConfig_WithExtraStructConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte("myfield: hello-world\n"), 0600)
	require.NoError(t, err)

	type TestCfg struct {
		MyField string
	}
	extra := &TestCfg{}
	cmd := &cobra.Command{}
	v, err := InitConfig(cmd, cfgFile, "", "TESTNVCF", extra)
	assert.NoError(t, err)
	assert.NotNil(t, v)
	assert.Equal(t, "hello-world", extra.MyField)
}

func TestSetupConfig_SuccessIsNoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		SetupConfig("my-config", true)
	})
}
