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
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConfigFileName = "example-config.yaml"

func TestConfigOverrides(t *testing.T) {
	testDir, err := os.Getwd()
	require.NoError(t, err, "error getting the current working directory")
	defer os.Chdir(testDir)
	// Check if test config file exists
	cfgFilePath := filepath.Join(testDir, testConfigFileName)
	_, err = os.Stat(cfgFilePath)
	require.NoError(t, err, "missing test config file")

	// Run TEST_NESTED_VAR="nvar-from-env-var" ./test --config=./example-config.yaml --flag-var="fvar-from-flag"
	os.Setenv("TEST_NESTED_VAR", "nvar-from-env-var")
	defer os.Unsetenv("TEST_NESTED_VAR")

	cmd := NewTestCommand()
	output := &bytes.Buffer{}
	cmd.SetOut(output)
	cmd.SetArgs([]string{"--config", cfgFilePath, "--flag-var", "fvar-from-flag"})
	cmd.Execute()

	gotOutput := output.String()
	wantOutput := `cfg.defaultVar: default-var
cfg.flagVar: fvar-from-flag
cfg.nested.nVar: nvar-from-env-var
cfg.nested.deepNested.dnVar: dnvar-from-config-file
complexVar: &{[{cfg1 c1-val1 c1-val2} {cfg2 c2-va11 c2-va12}]}
`
	assert.Equal(t, wantOutput, gotOutput)
}
