// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/cmd/reval/cli"
)

func TestNewRootCommand_Structure(t *testing.T) {
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v1.2.3", "abc1234", cli.Options{})
	require.NotNil(t, cmd)

	assert.Equal(t, "reval", cmd.Use)
	assert.Contains(t, cmd.Version, "v1.2.3")
	assert.Contains(t, cmd.Version, "abc1234")
	assert.NotEmpty(t, cmd.Short)
	assert.True(t, cmd.SilenceUsage)
	assert.True(t, cmd.SilenceErrors)
}

func TestNewRootCommand_ConfigFlag(t *testing.T) {
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v0.1.0", "deadbeef", cli.Options{})
	require.NotNil(t, cmd)

	flag := cmd.Flags().Lookup("config")
	require.NotNil(t, flag, "expected --config flag to be registered")
	assert.Equal(t, "config", flag.Name)
}

func TestNewRootCommand_HasPersistentPreRunE(t *testing.T) {
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v0.1.0", "abc", cli.Options{})
	require.NotNil(t, cmd)
	// PersistentPreRunE should be set (handles config file loading).
	assert.NotNil(t, cmd.PersistentPreRunE)
}

func TestNewRootCommand_HasRunE(t *testing.T) {
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v0.1.0", "abc", cli.Options{})
	require.NotNil(t, cmd)
	assert.NotNil(t, cmd.RunE)
}

func TestNewRootCommand_PersistentPreRunE_EmptyConfig(t *testing.T) {
	// Call PersistentPreRunE directly with no --config flags set.
	// It should succeed with empty config file list (just logs a warning).
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v0.1.0", "abc", cli.Options{})
	require.NotNil(t, cmd.PersistentPreRunE)

	err := cmd.PersistentPreRunE(cmd, nil)
	require.NoError(t, err)
}

func TestNewRootCommand_NilFactory_StillCreates(t *testing.T) {
	logger := zap.NewNop()
	// With a nil AuthorizerFactory the default is used; command creation should succeed.
	cmd := cli.NewRootCommand(logger, "v1.0.0", "sha", cli.Options{AuthorizerFactory: nil})
	require.NotNil(t, cmd)
	assert.Equal(t, "reval", cmd.Use)
}

func TestNewRootCommand_RunE_BothAuthDisabled_CommandWellFormed(t *testing.T) {
	// With both auth modes disabled the server now starts with auth disabled (logs a warning).
	// Calling RunE would block on a live HTTP listener, so we only verify command structure here.
	// The auth-disabled path is exercised in authorizers.TestBuildChain and main_test.go.
	logger := zap.NewNop()
	cmd := cli.NewRootCommand(logger, "v1.0.0", "sha", cli.Options{AuthorizerFactory: nil})
	require.NotNil(t, cmd.RunE)
	assert.Equal(t, "reval", cmd.Use)
}
